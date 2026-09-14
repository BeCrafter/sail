package s3fs

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"os"
	"path"
	"syscall"
	"time"

	"github.com/BeCrafter/sail/internal/vfs"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// OpenWrite 建本地暂存文件。此时不产生任何对象:只有 Commit 才落 S3。
func (f *FS) OpenWrite(ctx context.Context, p string) (vfs.WriteHandle, error) {
	logical, err := normalize(p)
	if err != nil {
		return nil, err
	}
	if logical == "/" {
		return nil, vfs.ErrNotSupported
	}
	tmp, err := os.CreateTemp(f.stagingDir, "sail-serve-*")
	if err != nil {
		if errors.Is(err, syscall.ENOSPC) {
			return nil, fmt.Errorf("s3fs: 暂存目录 %s 空间不足: %w", f.stagingDir, vfs.ErrInsufficientStorage)
		}
		return nil, fmt.Errorf("s3fs: 创建暂存文件失败: %w", err)
	}
	return &writeFile{
		fs:      f,
		logical: logical,
		key:     f.key(logical),
		tmp:     tmp,
		tmpPath: tmp.Name(),
		// 未声明长度时必须是 -1(未知),不能是 0——0 会被当成"声明了 0 字节",
		// 让 Commit 把正常写入判成长度不符。
		opts: vfs.WriteOptions{ContentLength: -1},
	}, nil
}

// writeFile 是本地暂存 + 显式分片参数的写入句柄。
//
// 数据先落 --staging-dir,Commit 时才切块上传;暂存盘峰值 ≈ 单文件大小 ×
// 并发 PUT 数,因此 OpenWrite 后立即按声明的 Content-Length 做一次空间预留。
type writeFile struct {
	fs      *FS
	logical string
	key     string

	tmp     *os.File
	tmpPath string

	opts         vfs.WriteOptions
	size         int64
	spaceChecked bool
	committed    bool
	aborted      bool
	info         vfs.FileInfo

	// writeErr 一旦非 nil,句柄就被"毒化":Commit 必须拒绝提交。
	// webdav 的 PUT 路径是 Write → Stat → Close,写失败后仍会调 Stat(),
	// 不拦住就会把截断的暂存当成完整对象传上去。
	writeErr error
}

func (w *writeFile) SetWriteOptions(opts vfs.WriteOptions) {
	w.opts = opts
}

func (w *writeFile) Write(p []byte) (int, error) {
	if w.aborted {
		return 0, fmt.Errorf("s3fs: 写句柄已放弃")
	}
	if w.committed {
		return 0, fmt.Errorf("s3fs: 写句柄已提交")
	}
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	if !w.spaceChecked {
		w.spaceChecked = true
		if err := w.reserve(); err != nil {
			w.writeErr = err
			return 0, err
		}
	}
	if w.fs.maxUploadSize > 0 && w.size+int64(len(p)) > w.fs.maxUploadSize {
		w.writeErr = fmt.Errorf("s3fs: %s: %w", w.logical, vfs.ErrTooLarge)
		return 0, w.writeErr
	}
	n, err := w.tmp.Write(p)
	w.size += int64(n)
	if err != nil {
		if errors.Is(err, syscall.ENOSPC) {
			w.writeErr = fmt.Errorf("s3fs: 暂存目录空间不足: %w", vfs.ErrInsufficientStorage)
		} else {
			w.writeErr = fmt.Errorf("s3fs: 写入暂存文件失败: %w", err)
		}
		return n, w.writeErr
	}
	return n, nil
}

// reserve 用声明的 Content-Length 做暂存盘预留:装不下就在写第一个字节之前
// 判成 ErrInsufficientStorage(壳据此回 507)。长度未知时不做预留,
// 交给写入时的 ENOSPC 判定。
func (w *writeFile) reserve() error {
	avail, ok := availableBytes(w.fs.stagingDir)
	if !ok {
		return nil
	}
	need := w.opts.ContentLength
	if need < 0 {
		need = 0
	}
	if avail < need {
		return fmt.Errorf("s3fs: 暂存目录 %s 可用 %d 字节,不足 %d 字节: %w",
			w.fs.stagingDir, avail, need, vfs.ErrInsufficientStorage)
	}
	return nil
}

// Commit 关闭暂存,按显式 PartSize/Concurrency 切块上传,再回读一次
// HeadObject 拿权威 ETag——PUT/GET/PROPFIND 三处 ETag 必须一致。
func (w *writeFile) Commit(ctx context.Context) (vfs.FileInfo, error) {
	if w.committed {
		return w.info, nil
	}
	if w.aborted {
		return vfs.FileInfo{}, fmt.Errorf("s3fs: 写句柄已放弃")
	}
	if w.writeErr != nil {
		// 写过程出过错(超限、暂存盘满、连接中断):绝不提交截断的数据。
		return vfs.FileInfo{}, w.writeErr
	}
	if w.opts.ContentLength >= 0 && w.size != w.opts.ContentLength {
		// 声明长度与实际写入量不符:客户端中途断了或请求体本身破损。
		// webdav 的 PUT 流程在 io.Copy 读侧报错时仍会调 Stat(),不拦住
		// 就会把截断的暂存当成完整对象传上去,覆盖掉桶里的好数据。
		return vfs.FileInfo{}, fmt.Errorf("s3fs: %s: 实际写入 %d 字节,与声明的 %d 字节不符,拒绝提交",
			w.logical, w.size, w.opts.ContentLength)
	}
	if w.tmp != nil {
		if err := w.tmp.Close(); err != nil {
			return vfs.FileInfo{}, fmt.Errorf("s3fs: 关闭暂存文件失败: %w", err)
		}
		w.tmp = nil
	}
	body, err := os.Open(w.tmpPath)
	if err != nil {
		return vfs.FileInfo{}, fmt.Errorf("s3fs: 读取暂存文件失败: %w", err)
	}
	defer body.Close()

	partSize := w.fs.partSize
	if partSize <= 0 {
		partSize = partSizeFor(w.size)
	}
	uploader := manager.NewUploader(w.fs.client, func(o *manager.Uploader) {
		o.PartSize = partSize
		o.Concurrency = w.fs.concurrency
		// 部分 S3 兼容服务不解码 aws-chunked + trailing checksum,
		// 会导致存储内容被 trailer 污染(与 internal/uploader 同因)。
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})
	contentType := w.opts.ContentType
	if contentType == "" {
		contentType = mime.TypeByExtension(path.Ext(w.logical))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if _, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(w.fs.bucket),
		Key:         aws.String(w.key),
		Body:        body,
		ContentType: aws.String(contentType),
	}); err != nil {
		return vfs.FileInfo{}, fmt.Errorf("s3fs: 上传 %s 失败: %w", w.logical, err)
	}

	info := vfs.FileInfo{
		Name:        baseName(w.logical),
		Path:        w.logical,
		Size:        w.size,
		ModTime:     time.Now(),
		ContentType: contentType,
	}
	h, err := w.fs.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(w.fs.bucket),
		Key:    aws.String(w.key),
	})
	if err == nil {
		info = w.fs.infoFromHead(w.logical, w.key, h)
	} else {
		// 对象已写入成功,不把回读失败升级成上传失败;ETag 交由协议壳
		// 退化为启发式(客户端至多多校验一次)。
		fmt.Fprintf(os.Stderr, "警告: 上传成功但回读 %s 元信息失败: %v\n", w.logical, err)
	}
	w.committed = true
	w.info = info
	return info, nil
}

// Abort 丢弃暂存,幂等。
func (w *writeFile) Abort() error {
	if w.aborted {
		return nil
	}
	w.aborted = true
	if w.tmp != nil {
		w.tmp.Close()
		w.tmp = nil
	}
	if err := os.Remove(w.tmpPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("s3fs: 清理暂存文件失败: %w", err)
	}
	return nil
}

// Close 只清理暂存,幂等,且永不返回错误:webdav 的 PUT 路径在 Close 返回
// 非 nil 时会判 405,而数据其实已经写成功。
func (w *writeFile) Close() error {
	if w.tmp != nil {
		w.tmp.Close()
		w.tmp = nil
	}
	if err := os.Remove(w.tmpPath); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "警告: 清理暂存文件 %s 失败: %v\n", w.tmpPath, err)
	}
	return nil
}
