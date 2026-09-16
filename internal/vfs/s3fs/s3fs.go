// Package s3fs 是 vfs.FileSystem 的 S3 实现。它把逻辑路径映射成
// bucket(可带共享根前缀)下的对象 key,并负责目录标记对象与共同前缀的合并。
package s3fs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/BeCrafter/sail/internal/s3del"
	"github.com/BeCrafter/sail/internal/s3path"
	"github.com/BeCrafter/sail/internal/vfs"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

const (
	// minPartSize 是 S3 multipart 的最小分片大小。
	minPartSize = 5 << 20
	// maxParts 是 S3 单次 multipart 上传的分片数上限。
	maxParts = 10000
	// defaultConcurrency 是显式并发度。内存预算 ≈ (Concurrency+1) × PartSize。
	defaultConcurrency = 4
	// deleteBatchSize 是 DeleteObjects 的单批上限,也是删除后列举分页的大小。
	deleteBatchSize = 1000
	// objectOpsConcurrency 是分片复制的并发上限(删除回退的并发度在 s3del 内)。
	objectOpsConcurrency = 10
	// maxChunkSize 是单片的硬上界:S3 PutObject 单次请求上限 5GiB。
	maxChunkSize = 5 << 30
	// metaConcurrency 是列目录补元信息时对 HEAD 的并发上限。
	metaConcurrency = 8
)

// Config 是 s3fs 的构造参数。
type Config struct {
	Client *s3.Client
	Bucket string
	// Prefix 是共享根前缀,映射为逻辑路径 "/";为空表示桶根。
	Prefix string
	// StagingDir 是写暂存目录;空则用系统临时目录。
	StagingDir string
	// MaxUploadSize 是单次请求体上限(字节);<= 0 表示不限制。
	MaxUploadSize int64
	// PartSize 是分片大小;0 表示按对象大小自适应(下限 minPartSize)。
	// 显式给定后不再随对象放大——语义上禁止依赖 SDK 自动放大。
	PartSize int64
	// Concurrency 是分片并发度;<= 0 取 defaultConcurrency。
	Concurrency int
	// ChunkedUpload 打开分片存储表示:超过 ChunkSize 的文件拆片 + manifest。
	// 默认关——关时桶内 1 文件 = 1 对象,既有桶与第三方 S3 工具零感知。
	ChunkedUpload bool
	// ChunkSize 是单个物理片的上限(字节),也是「多大算大文件」的阈值。
	// 仅 ChunkedUpload 为真时生效;越界在构造期拒绝。
	ChunkSize int64
}

// FS 实现 vfs.FileSystem。
type FS struct {
	client        *s3.Client
	bucket        string
	prefix        string
	stagingDir    string
	maxUploadSize int64
	partSize      int64
	concurrency   int
	chunkedUpload bool
	chunkSize     int64
	// deleter 统一处理批量端点探测与并发回退(见 internal/s3del)。
	deleter *s3del.Deleter
}

// New 校验配置并确保暂存目录可用。
func New(cfg Config) (*FS, error) {
	if cfg.Client == nil {
		return nil, errors.New("s3fs: 缺少 S3 client")
	}
	if cfg.Bucket == "" {
		return nil, errors.New("s3fs: 缺少 bucket")
	}
	dir := cfg.StagingDir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("s3fs: 创建暂存目录 %s 失败: %w", dir, err)
	}
	partSize := cfg.PartSize
	if partSize > 0 && partSize < minPartSize {
		partSize = minPartSize
	}
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = defaultConcurrency
	}
	if cfg.ChunkedUpload {
		if cfg.ChunkSize < minPartSize || cfg.ChunkSize > maxChunkSize {
			return nil, fmt.Errorf("s3fs: --chunk-size 必须在 %d 与 %d 字节之间(5MiB ~ 5GiB),当前 %d",
				int64(minPartSize), int64(maxChunkSize), cfg.ChunkSize)
		}
	}
	return &FS{
		client:        cfg.Client,
		bucket:        cfg.Bucket,
		prefix:        strings.Trim(cfg.Prefix, "/"),
		stagingDir:    dir,
		maxUploadSize: cfg.MaxUploadSize,
		partSize:      partSize,
		concurrency:   concurrency,
		chunkedUpload: cfg.ChunkedUpload,
		chunkSize:     cfg.ChunkSize,
		deleter:       s3del.New(cfg.Client, cfg.Bucket),
	}, nil
}

// Stat 用单次 HeadObject 定论;未命中时再用一次带 MaxKeys=1 的列举判断
// 该路径是否为「只有共同前缀、没有标记对象」的目录。
func (f *FS) Stat(ctx context.Context, p string) (vfs.FileInfo, error) {
	logical, err := normalize(p)
	if err != nil {
		return vfs.FileInfo{}, err
	}
	if logical == "/" {
		return vfs.FileInfo{Name: "/", Path: "/", IsDir: true}, nil
	}
	key := f.key(logical)
	h, err := f.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(f.bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		if _, fi, ok, derr := f.detectMeta(ctx, logical, key, h); derr != nil {
			return vfs.FileInfo{}, derr
		} else if ok {
			// 分片文件:对外汇报逻辑大小与逻辑类型,不是 manifest 的物理大小。
			return fi, nil
		}
		return f.infoFromHead(logical, key, h), nil
	}
	if !isNotFound(err) {
		return vfs.FileInfo{}, fmt.Errorf("s3fs: 读取 %s 元信息失败: %w", logical, err)
	}
	probe := key
	if !strings.HasSuffix(probe, "/") {
		probe += "/"
	}
	found, lerr := f.hasObjects(ctx, probe)
	if lerr != nil {
		return vfs.FileInfo{}, lerr
	}
	if found {
		return vfs.FileInfo{Name: baseName(logical), Path: logical, IsDir: true}, nil
	}
	return vfs.FileInfo{}, notExist(logical)
}

// ReadDir 用单次分页 ListObjectsV2(Delimiter="/")列举,不逐项 HEAD:
// 共同前缀与目录标记对象合并为同一条目录项。
func (f *FS) ReadDir(ctx context.Context, p string) ([]vfs.FileInfo, error) {
	logical, err := normalize(p)
	if err != nil {
		return nil, err
	}
	prefix := f.key(logical)
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	out := []vfs.FileInfo{}
	sawMarker := false
	// files 暂存文件条目,稍后统一补元信息(分片文件要换成逻辑大小)。
	var files []vfs.FileInfo
	paginator := s3.NewListObjectsV2Paginator(f.client, &s3.ListObjectsV2Input{
		Bucket:    aws.String(f.bucket),
		Prefix:    aws.String(prefix),
		Delimiter: aws.String("/"),
	})
	for paginator.HasMorePages() {
		page, perr := paginator.NextPage(ctx)
		if perr != nil {
			return nil, fmt.Errorf("s3fs: 列举 %s 失败: %w", logical, perr)
		}
		for _, cp := range page.CommonPrefixes {
			name := childName(aws.ToString(cp.Prefix), prefix)
			if name == "" || name == sailDir {
				// .sail/ 是内核保留前缀,不暴露给任何协议壳。
				continue
			}
			out = append(out, vfs.FileInfo{
				Name:  name,
				Path:  joinLogical(logical, name),
				IsDir: true,
			})
		}
		for _, o := range page.Contents {
			k := aws.ToString(o.Key)
			if k == prefix {
				// 目录标记对象自身:它让本目录存在,不是本目录的子项。
				sawMarker = true
				continue
			}
			name := childName(k, prefix)
			if name == "" || strings.HasPrefix(name, sailDir+"/") || name == sailDir {
				continue
			}
			files = append(files, vfs.FileInfo{
				Name:    name,
				Path:    joinLogical(logical, name),
				Size:    aws.ToInt64(o.Size),
				ModTime: aws.ToTime(o.LastModified),
				ETag:    unquote(aws.ToString(o.ETag)),
			})
		}
	}

	if err := f.decorateEntries(ctx, files); err != nil {
		return nil, err
	}
	out = append(out, files...)

	if len(out) == 0 && !sawMarker && logical != "/" {
		// 没有任何子项也没有标记对象:要么是空目录(父层有共同前缀),要么根本不存在。
		fi, serr := f.Stat(ctx, logical)
		if serr != nil {
			return nil, serr
		}
		if !fi.IsDir {
			return nil, notExist(logical)
		}
	}
	return out, nil
}

// decorateEntries 为列目录得到的文件条目补元信息:分片文件要换成逻辑大小
// 与逻辑 Content-Type,否则 Finder 里会显示成 manifest JSON 的字节数。
//
// 代价是有界的:只在开启分片、且条目还没有 Content-Type(ListObjectsV2 不
// 返回该字段)时,才按 metaConcurrency 并发发一次 HEAD。分片关闭时本函数
// 一次请求都不发,与 P1 完全一致。
func (f *FS) decorateEntries(ctx context.Context, entries []vfs.FileInfo) error {
	if !f.chunkedUpload {
		return nil
	}
	sem := make(chan struct{}, metaConcurrency)
	errCh := make(chan error, len(entries))
	var wg sync.WaitGroup
	for i := range entries {
		e := &entries[i]
		if e.IsDir || e.ContentType != "" {
			continue
		}
		wg.Add(1)
		go func(e *vfs.FileInfo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			key := f.key(e.Path)
			h, err := f.client.HeadObject(ctx, &s3.HeadObjectInput{
				Bucket: aws.String(f.bucket),
				Key:    aws.String(key),
			})
			if err != nil {
				if isNotFound(err) {
					return
				}
				errCh <- fmt.Errorf("s3fs: 读取 %s 元信息失败: %w", e.Path, err)
				return
			}
			if _, fi, ok, derr := f.detectMeta(ctx, e.Path, key, h); derr != nil {
				errCh <- derr
			} else if ok {
				*e = fi
			} else {
				// 普通文件:补上 HEAD 才知道的 Content-Type。
				e.ContentType = aws.ToString(h.ContentType)
			}
		}(e)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

// OpenRead 返回以 Range 实现的 io.ReadSeeker:Seek 只重定位偏移,
// 真正读取时才发 GetObject,且不落盘、不整文件入内存。
func (f *FS) OpenRead(ctx context.Context, p string) (vfs.ReadSeekCloser, error) {
	logical, err := normalize(p)
	if err != nil {
		return nil, err
	}
	if logical == "/" {
		return nil, vfs.ErrNotSupported
	}
	key := f.key(logical)
	h, err := f.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(f.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, notExist(logical)
		}
		return nil, fmt.Errorf("s3fs: 打开 %s 失败: %w", logical, err)
	}
	fi := f.infoFromHead(logical, key, h)
	if fi.IsDir {
		return nil, vfs.ErrNotSupported
	}
	// 分片文件:交给跨片聚合的 io.ReadSeeker(Seek 同样只改偏移、不发请求)。
	if m, cfi, ok, cerr := f.chunked(ctx, logical, key, h); cerr != nil {
		return nil, cerr
	} else if ok {
		return newChunkedReader(ctx, f.client, f.bucket, f.partsPath(logical), m, cfi), nil
	}
	return &readFile{ctx: ctx, client: f.client, bucket: f.bucket, key: key, info: fi, size: fi.Size}, nil
}

// OpenReadWithInfo 是 OpenRead 的快路径:调用方已通过 Stat 拿到 fi,
// 普通文件据此可直接构造读句柄,省掉 OpenRead 内部重复的一次 HEAD。
// 分片文件需要 manifest 里的版本号与片表,而 FileInfo 不含这些,故退回
// OpenRead 的完整路径(HEAD + 读 manifest),不牺牲正确性。
func (f *FS) OpenReadWithInfo(ctx context.Context, p string, fi vfs.FileInfo) (vfs.ReadSeekCloser, error) {
	if fi.Chunked {
		return f.OpenRead(ctx, p)
	}
	logical, err := normalize(p)
	if err != nil {
		return nil, err
	}
	if logical == "/" || fi.IsDir {
		return nil, vfs.ErrNotSupported
	}
	return &readFile{
		ctx:    ctx,
		client: f.client,
		bucket: f.bucket,
		key:    f.key(logical),
		info:   fi,
		size:   fi.Size,
	}, nil
}

// Remove 删除单个对象;recursive 为真时删除该前缀下的全部对象(
// 含目录标记对象),批量 DeleteObjects 每批 1000 个。
//
// 分片文件/目录的片命名空间不落在逻辑 key 的前缀下(见 partsPath),
// 因此两种 recursive 取值都必须显式回收本路径名下的片 —— 回收是纯前缀删除,
// 不需要读 manifest,也不可能碰到其它逻辑路径的数据。
func (f *FS) Remove(ctx context.Context, p string, recursive bool) error {
	logical, err := normalize(p)
	if err != nil {
		return err
	}
	if logical == "/" {
		return vfs.ErrNotSupported
	}
	key := f.key(logical)
	if !recursive {
		// 分片文件:先删逻辑 key(提交点),再清片目录。
		handled, exists, err := f.removeChunkedPath(ctx, logical, key)
		if err != nil {
			return err
		}
		if handled {
			return nil
		}
		// 对象不存在就不发那次注定空转的 DELETE:单次删除的延迟可能高达数十
		// 秒(实测某网关固定 ~28s),而删除本身幂等,不删与删了的结果一致。
		if !exists {
			return nil
		}
		return f.deleteObjects(ctx, []string{key})
	}
	// 递归删除:按前缀整体清。先回收该路径名下的片(此时逻辑 key 仍可见,
	// 失败只留可判定孤儿,不会把文件删成半截),再删逻辑前缀。
	// 末尾把「同名对象本身」(逻辑路径是文件、同时存在同前缀时)并入同一批,
	// 而不是再发一次删除:单次删除的延迟可能高达数十秒,多一次调用就是多一份
	// 串行等待,而删除本身幂等,合并无副作用。
	if f.chunkedUpload {
		f.cleanupPrefix(ctx, f.partsPath(logical), "分片")
	}
	prefix := key
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	paginator := s3.NewListObjectsV2Paginator(f.client, &s3.ListObjectsV2Input{
		Bucket:  aws.String(f.bucket),
		Prefix:  aws.String(prefix),
		MaxKeys: aws.Int32(deleteBatchSize),
	})
	var doomed []string
	for paginator.HasMorePages() {
		page, perr := paginator.NextPage(ctx)
		if perr != nil {
			return fmt.Errorf("s3fs: 列举待删除对象 %s 失败: %w", logical, perr)
		}
		for _, o := range page.Contents {
			doomed = append(doomed, aws.ToString(o.Key))
		}
	}
	// 「同名对象本身」放在队首:并发删除按队列取任务,若它排在末尾而队列恰好
	// 是并发度的整数倍,它就要等整轮跑完才启动——单次删除延迟数十秒时,这一等
	// 就是白加的几十秒。放队首可确保它落在第一轮里。
	doomed = append([]string{key}, doomed...)
	return f.deleteObjects(ctx, doomed)
}

// Rename 是对象级重命名(CopyObject + DeleteObject)。目录级在 P1 不支持,
// 由协议壳提前返回 501 让客户端退化为「复制 + 删除」。
func (f *FS) Rename(ctx context.Context, oldPath, newPath string) error {
	oldLogical, err := normalize(oldPath)
	if err != nil {
		return err
	}
	newLogical, err := normalize(newPath)
	if err != nil {
		return err
	}
	if oldLogical == "/" || newLogical == "/" {
		return vfs.ErrNotSupported
	}
	oldKey := f.key(oldLogical)
	newKey := f.key(newLogical)

	oldHead, err := f.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(f.bucket),
		Key:    aws.String(oldKey),
	})
	if err != nil {
		if isNotFound(err) {
			// 没有同名对象:可能是只有共同前缀的目录(或根本不存在)。
			probe := oldKey
			if !strings.HasSuffix(probe, "/") {
				probe += "/"
			}
			found, lerr := f.hasObjects(ctx, probe)
			if lerr != nil {
				return lerr
			}
			if found {
				return vfs.ErrNotSupported
			}
			return notExist(oldLogical)
		}
		return fmt.Errorf("s3fs: 移动 %s 失败: %w", oldLogical, err)
	}
	if strings.HasSuffix(oldKey, "/") {
		// 目录标记对象本身不能当文件搬走。
		return vfs.ErrNotSupported
	}

	// 分片文件:片目录按逻辑路径归属,只搬 manifest 会让新路径读不出内容。
	// 先把片复制到新路径自己的版本目录,再写新 manifest,最后删旧件旧片。
	if m, ok, cerr := f.detectManifest(ctx, oldKey, oldHead); cerr != nil {
		return cerr
	} else if ok {
		return f.renameChunked(ctx, oldLogical, newLogical, oldKey, newKey, m)
	}

	if _, err := f.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(f.bucket),
		Key:        aws.String(newKey),
		CopySource: aws.String(f.bucket + "/" + oldKey),
	}); err != nil {
		return fmt.Errorf("s3fs: 复制 %s -> %s 失败: %w", oldLogical, newLogical, err)
	}
	if _, err := f.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(f.bucket),
		Key:    aws.String(oldKey),
	}); err != nil {
		return fmt.Errorf("s3fs: 删除源对象 %s 失败: %w", oldLogical, err)
	}
	return nil
}

func (f *FS) deleteObjects(ctx context.Context, keys []string) error {
	return f.deleter.DeleteKeys(ctx, keys)
}

// runConcurrently 以 objectOpsConcurrency 为上限并发对每个元素执行 fn,
// 返回首个错误。等全部跑完再汇总:部分完成的中间态与批量端点「部分成功」
// 的语义一致,早退只会让调用方看到一个不完整的结果。
func runConcurrently[T any](items []T, fn func(T) error) error {
	if len(items) == 0 {
		return nil
	}
	sem := make(chan struct{}, objectOpsConcurrency)
	errCh := make(chan error, len(items))
	var wg sync.WaitGroup

	for _, item := range items {
		wg.Add(1)
		go func(item T) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := fn(item); err != nil {
				errCh <- err
			}
		}(item)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

// hasObjects 用一次 MaxKeys=1 的列举判断前缀下是否有对象。
func (f *FS) hasObjects(ctx context.Context, prefix string) (bool, error) {
	out, err := f.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(f.bucket),
		Prefix:  aws.String(prefix),
		MaxKeys: aws.Int32(1),
	})
	if err != nil {
		return false, fmt.Errorf("s3fs: 探测前缀 %s 失败: %w", prefix, err)
	}
	return len(out.Contents) > 0 || len(out.CommonPrefixes) > 0, nil
}

func (f *FS) infoFromHead(logical, key string, h *s3.HeadObjectOutput) vfs.FileInfo {
	isDir := strings.HasSuffix(key, "/")
	fi := vfs.FileInfo{
		Name:        baseName(logical),
		Path:        logical,
		Size:        aws.ToInt64(h.ContentLength),
		ModTime:     aws.ToTime(h.LastModified),
		IsDir:       isDir,
		ContentType: aws.ToString(h.ContentType),
	}
	if !isDir {
		fi.ETag = unquote(aws.ToString(h.ETag))
	}
	return fi
}

// key 把逻辑路径映射为对象 key;逻辑路径以 "/" 结尾表示目录标记对象。
func (f *FS) key(logical string) string {
	if logical == "/" {
		return f.prefix
	}
	return s3path.JoinKey(f.prefix, strings.TrimPrefix(logical, "/"))
}

// normalize 规范化逻辑路径:强制以 "/" 开头,拒绝任何 ".." 段(越界),
// 保留尾随 "/"(目录标记对象),根固定为 "/"。
func normalize(p string) (string, error) {
	if p == "" {
		return "", notExist(p)
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", notExist(p)
		}
	}
	trailing := len(p) > 1 && strings.HasSuffix(p, "/")
	clean := path.Clean(p)
	if clean == "/" {
		return "/", nil
	}
	if trailing {
		clean += "/"
	}
	return clean, nil
}

// joinLogical 拼出子项的逻辑路径。
func joinLogical(dir, name string) string {
	if dir == "/" {
		return "/" + name
	}
	return strings.TrimSuffix(dir, "/") + "/" + name
}

// baseName 取逻辑路径最后一段(忽略尾随 "/")。
func baseName(logical string) string {
	s := strings.TrimSuffix(logical, "/")
	if s == "" {
		return "/"
	}
	return s[strings.LastIndex(s, "/")+1:]
}

// childName 从列举结果 key/前缀里取出相对 prefix 的末段名。
func childName(k, prefix string) string {
	name := strings.TrimPrefix(k, prefix)
	name = strings.TrimSuffix(name, "/")
	if name == "" || strings.Contains(name, "/") {
		return ""
	}
	return name
}

func unquote(etag string) string {
	return strings.Trim(etag, `"`)
}

func notExist(logical string) error {
	// 必须用 *os.PathError:errors.Is 认 fmt.Errorf 的 %w 包装,而
	// x/net/webdav 用的是 os.IsNotExist,它只解包 PathError/LinkError/SyscallError,
	// 对 fmt.Errorf 的包装判定为 false(会把「目标不存在」误判成 403)。
	return &os.PathError{Op: "stat", Path: logical, Err: vfs.ErrNotExist}
}

// isNotFound 判定对象不存在。不同 S3 兼容服务给出的错误码不统一,
// 这里同时覆盖 SDK 类型化错误、API 错误码与 HTTP 404。
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return true
	}
	var re *smithyhttp.ResponseError
	if errors.As(err, &re) && re.HTTPStatusCode() == 404 {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NotFound", "NoSuchKey", "NoSuchBucket", "404":
			return true
		}
	}
	return false
}

// partSizeFor 按对象大小显式推导分片大小:保证分片数不超过 maxParts,
// 且不小于 minPartSize。显式推导而非交给 SDK 自动放大。
func partSizeFor(size int64) int64 {
	const mib = 1 << 20
	ps := int64(minPartSize)
	if need := (size + maxParts - 1) / maxParts; need > ps {
		ps = need
	}
	if rem := ps % mib; rem != 0 {
		ps += mib - rem
	}
	return ps
}
