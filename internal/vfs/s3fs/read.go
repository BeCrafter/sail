package s3fs

import (
	"context"
	"fmt"
	"io"

	"github.com/BeCrafter/sail/internal/vfs"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// readFile 是 Range 驱动的读取句柄。
//
// 与「下载到本地再交差」不同,它一次只持有一段服务端响应体:
// Seek 只改偏移,后续 Read 才发一次带 Range 的 GetObject。因此
// http.ServeContent 的 Seek(0, SeekEnd)+Seek(start, SeekStart) 组合
// 最多只产生一次 GetObject,不会整文件入内存。
type readFile struct {
	// ctx 是发起请求的上下文(WebDAV 请求的 ctx),随请求结束而取消。
	ctx    context.Context
	client *s3.Client
	bucket string
	key    string
	info   vfs.FileInfo
	size   int64

	off  int64
	body io.ReadCloser
}

func (r *readFile) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.off >= r.size {
		return 0, io.EOF
	}
	if r.body == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	n, err := r.body.Read(p)
	r.off += int64(n)
	if err == io.EOF {
		r.closeBody()
		if n > 0 {
			return n, nil
		}
		return 0, io.EOF
	}
	return n, err
}

func (r *readFile) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.off + offset
	case io.SeekEnd:
		abs = r.size + offset
	default:
		return 0, fmt.Errorf("s3fs: 非法的 Seek whence %d", whence)
	}
	if abs < 0 {
		return 0, fmt.Errorf("s3fs: 非法的 Seek 位置 %d", abs)
	}
	if abs != r.off {
		r.closeBody()
		r.off = abs
	}
	return abs, nil
}

func (r *readFile) Close() error {
	r.closeBody()
	return nil
}

func (r *readFile) open() error {
	req := &s3.GetObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(r.key),
	}
	if r.off > 0 {
		req.Range = aws.String(fmt.Sprintf("bytes=%d-", r.off))
	}
	out, err := r.client.GetObject(r.ctx, req)
	if err != nil {
		return fmt.Errorf("s3fs: 读取 %s 失败: %w", r.info.Path, err)
	}
	r.body = out.Body
	return nil
}

func (r *readFile) closeBody() {
	if r.body != nil {
		r.body.Close()
		r.body = nil
	}
}
