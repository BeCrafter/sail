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
	// deleteBatchSize 是 DeleteObjects 的单批上限。
	deleteBatchSize = 1000
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
	return &FS{
		client:        cfg.Client,
		bucket:        cfg.Bucket,
		prefix:        strings.Trim(cfg.Prefix, "/"),
		stagingDir:    dir,
		maxUploadSize: cfg.MaxUploadSize,
		partSize:      partSize,
		concurrency:   concurrency,
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
			if name == "" {
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
			if name == "" {
				continue
			}
			out = append(out, vfs.FileInfo{
				Name:    name,
				Path:    joinLogical(logical, name),
				Size:    aws.ToInt64(o.Size),
				ModTime: aws.ToTime(o.LastModified),
				ETag:    unquote(aws.ToString(o.ETag)),
			})
		}
	}

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
	return &readFile{ctx: ctx, client: f.client, bucket: f.bucket, key: key, info: fi, size: fi.Size}, nil
}

// Remove 删除单个对象;recursive 为真时删除该前缀下的全部对象(
// 含目录标记对象),批量 DeleteObjects 每批 1000 个。
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
		return f.deleteObjects(ctx, []string{key})
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
	for paginator.HasMorePages() {
		page, perr := paginator.NextPage(ctx)
		if perr != nil {
			return fmt.Errorf("s3fs: 列举待删除对象 %s 失败: %w", logical, perr)
		}
		keys := make([]string, 0, len(page.Contents))
		for _, o := range page.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
		if err := f.deleteObjects(ctx, keys); err != nil {
			return err
		}
	}
	// 同名对象本身(逻辑路径是文件、同时存在同前缀时)。
	return f.deleteObjects(ctx, []string{key})
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

	if _, err := f.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(f.bucket),
		Key:    aws.String(oldKey),
	}); err != nil {
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
	for i := 0; i < len(keys); i += deleteBatchSize {
		end := i + deleteBatchSize
		if end > len(keys) {
			end = len(keys)
		}
		ids := make([]types.ObjectIdentifier, 0, end-i)
		for _, k := range keys[i:end] {
			ids = append(ids, types.ObjectIdentifier{Key: aws.String(k)})
		}
		out, err := f.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(f.bucket),
			Delete: &types.Delete{Objects: ids},
		})
		if err != nil || len(out.Errors) > 0 {
			// 部分 S3 兼容服务不支持批量删除,回退逐个删除(对已删除对象幂等)。
			for _, k := range keys[i:end] {
				if _, derr := f.client.DeleteObject(ctx, &s3.DeleteObjectInput{
					Bucket: aws.String(f.bucket),
					Key:    aws.String(k),
				}); derr != nil {
					return fmt.Errorf("s3fs: 删除 %s 失败: %w", k, derr)
				}
			}
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
