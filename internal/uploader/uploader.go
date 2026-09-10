package uploader

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/BeCrafter/sail/internal/i18n"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const partSize = 5 * 1024 * 1024 // 5MB,S3 multipart 最小分片大小

// Uploader 包装 s3manager,提供单文件/目录上传。
// s3 字段声明为 manager.UploadAPIClient 接口(而非具体 *s3.Client),
// 使上传路径可在测试中用 mock 覆盖;真实客户端天然满足该接口。
type Uploader struct {
	s3       manager.UploadAPIClient
	uploader *manager.Uploader
}

func New(s3c *s3.Client) *Uploader {
	u := manager.NewUploader(s3c, func(o *manager.Uploader) {
		o.PartSize = partSize
		o.Concurrency = 16
		// manager.NewUploader 默认设为 WhenSupported,会对每个分片
		// 使用 aws-chunked + CRC32 trailing checksum。部分 S3 兼容服务端不
		// 解码 aws-chunked,导致存储的数据被 trailer 污染。设为
		// WhenRequired 使 SDK 不主动添加 checksum。
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})
	return &Uploader{s3: s3c, uploader: u}
}

// UploadFile 上传单个本地文件到 bucket/key。
func (u *Uploader) UploadFile(ctx context.Context, localPath, bucket, key string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf(i18n.T("failed to open file: %w"), err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf(i18n.T("failed to read file info: %w"), err)
	}

	pr := newProgressReader(f, info.Size())
	pr.start()
	defer pr.stop()

	_, err = u.uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: &bucket,
		Key:    &key,
		Body:   pr,
	})
	if err != nil {
		return fmt.Errorf(i18n.T("upload failed: %w"), err)
	}
	return nil
}

// UploadStream 从 reader 读取全部内容上传为单个 object。
// s3manager 要求可 seek 的 reader,管道/网络流不可 seek,
// 因此先读入内存 buffer 再上传。
func (u *Uploader) UploadStream(ctx context.Context, r io.Reader, bucket, key string) error {
	buf, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf(i18n.T("failed to read input: %w"), err)
	}
	_, err = u.uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: &bucket,
		Key:    &key,
		Body:   bytes.NewReader(buf),
	})
	if err != nil {
		return fmt.Errorf(i18n.T("upload failed: %w"), err)
	}
	return nil
}

// UploadDir 递归上传本地目录到 bucket 下的 prefix。
func (u *Uploader) UploadDir(ctx context.Context, localDir, bucket, prefix string) error {
	err := filepath.Walk(localDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(localDir, path)
		if err != nil {
			return err
		}
		key := buildKey(prefix, filepath.ToSlash(rel))
		fmt.Printf(i18n.T("uploading %s -> s3://%s/%s\n"), path, bucket, key)
		return u.UploadFile(ctx, path, bucket, key)
	})
	return err
}

// buildKey 在 S3 key 的 base prefix 之上追加子路径 rel。
// prefix 首尾的 / 会被裁掉(空 prefix 直接返回 rel),避免拼出形如
// "prefix//rel" 或 "/rel" 的脏 key。
func buildKey(prefix, rel string) string {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return rel
	}
	return prefix + "/" + rel
}

// progressReader 跟踪读取字节数并周期性打印进度。
type progressReader struct {
	r      io.Reader
	total  int64
	read   int64
	mu     sync.Mutex
	stopCh chan struct{}
	done   bool
}

func newProgressReader(r io.Reader, total int64) *progressReader {
	return &progressReader{r: r, total: total, stopCh: make(chan struct{})}
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	p.mu.Lock()
	p.read += int64(n)
	p.mu.Unlock()
	return n, err
}

func (p *progressReader) start() {
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-p.stopCh:
				p.print(true)
				return
			case <-ticker.C:
				p.print(false)
			}
		}
	}()
}

func (p *progressReader) stop() {
	if p.done {
		return
	}
	p.done = true
	close(p.stopCh)
}

func (p *progressReader) print(final bool) {
	p.mu.Lock()
	read := p.read
	total := p.total
	p.mu.Unlock()
	pct := 0.0
	if total > 0 {
		pct = float64(read) / float64(total) * 100
	}
	if final {
		fmt.Printf(i18n.T("\r\033[K%s / %s  %.1f%% done\n"), humanBytes(read), humanBytes(total), pct)
	} else {
		fmt.Printf("\r\033[K%s / %s  %.1f%%", humanBytes(read), humanBytes(total), pct)
	}
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
