package view

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/BeCrafter/sail/internal/s3path"
)

// Source 是统一的渲染数据源:来自 S3 对象或本地文件,均暴露 io.ReadCloser。
type Source struct {
	Reader      io.ReadCloser
	Size        int64  // -1 表示未知(S3 无 Content-Length 时)
	ContentType string // S3 resp.ContentType / 嗅探结果
	Name        string // 本地 filepath.Base / S3 s3path.BaseName(key)
	IsS3        bool
}

// Close 关闭底层 reader。
func (s *Source) Close() error {
	if s.Reader != nil {
		return s.Reader.Close()
	}
	return nil
}

// OpenSource 按 scheme 分发:s3:// → S3 GetObject 直读(不用 manager.Downloader,
// 避免 checksum-trailer 与服务端不兼容);否则 → os.Open + os.Stat。
// s3c 在本地路径时可为 nil。defaultBucket 在 s3:///key(空桶段)时填充为配置默认桶。
func OpenSource(ctx context.Context, arg string, s3c *s3.Client, defaultBucket string) (*Source, error) {
	if strings.HasPrefix(arg, "s3://") {
		return openS3(ctx, arg, s3c, defaultBucket)
	}
	return openLocal(arg)
}

func openS3(ctx context.Context, arg string, s3c *s3.Client, defaultBucket string) (*Source, error) {
	if s3c == nil {
		return nil, fmt.Errorf("缺少 S3 客户端(本地路径无需 s3://)")
	}
	p, err := s3path.Parse(arg)
	if err != nil {
		return nil, err
	}
	if p.Bucket == "" {
		if defaultBucket == "" {
			return nil, fmt.Errorf("未指定 bucket,请用 s3://bucket/key 或 s3:///key(用配置默认 bucket)")
		}
		p.Bucket = defaultBucket
	}
	if p.Key == "" {
		return nil, fmt.Errorf("缺少 key,需指定 s3://bucket/key")
	}
	resp, err := s3c.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &p.Bucket,
		Key:    &p.Key,
	})
	if err != nil {
		return nil, fmt.Errorf("读取对象失败: %w", err)
	}
	s := &Source{
		Reader: resp.Body,
		Name:   s3path.BaseName(p.Key),
		IsS3:   true,
	}
	if resp.ContentLength != nil {
		s.Size = *resp.ContentLength
	} else {
		s.Size = -1
	}
	if resp.ContentType != nil {
		s.ContentType = *resp.ContentType
	}
	return s, nil
}

func openLocal(arg string) (*Source, error) {
	info, err := os.Stat(arg)
	if err != nil {
		return nil, fmt.Errorf("读取本地文件失败: %w", err)
	}
	f, err := os.Open(arg)
	if err != nil {
		return nil, fmt.Errorf("打开本地文件失败: %w", err)
	}
	return &Source{
		Reader: f,
		Size:   info.Size(),
		Name:   filepath.Base(arg),
		IsS3:   false,
	}, nil
}

// SniffContentType 读取前 512 字节,经 net/http.DetectContentType 判定 MIME,
// 再用 io.MultiReader 拼回,确保不丢数据。仅当 ContentType 为空时调用。
func SniffContentType(s *Source) error {
	if s.ContentType != "" {
		return nil
	}
	buf := make([]byte, 512)
	n, err := io.ReadFull(s.Reader, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return fmt.Errorf("嗅探内容类型失败: %w", err)
	}
	s.ContentType = http.DetectContentType(buf[:n])
	s.Reader = io.NopCloser(io.MultiReader(bytes.NewReader(buf[:n]), s.Reader))
	return nil
}
