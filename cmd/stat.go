package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/spf13/cobra"
	"github.com/BeCrafter/sail/internal/client"
)

var statCmd = &cobra.Command{
	Use:   "stat <s3://bucket/key | 本地路径>",
	Short: "查看对象/文件元信息",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		arg := args[0]
		ctx := context.Background()
		if strings.HasPrefix(arg, "s3://") {
			return statS3(ctx, arg)
		}
		return statLocal(arg)
	},
}

func statS3(ctx context.Context, arg string) error {
	r, _, err := loadResolved()
	if err != nil {
		return err
	}
	p, err := parseS3(arg, r)
	if err != nil {
		return err
	}
	if p.Key == "" {
		return fmt.Errorf("缺少 key,需指定 s3://bucket/key")
	}
	s3c, err := client.New(ctx, r)
	if err != nil {
		return err
	}
	resp, err := s3c.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &p.Bucket,
		Key:    &p.Key,
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchKey") {
			return fmt.Errorf("对象不存在: s3://%s/%s", p.Bucket, p.Key)
		}
		return fmt.Errorf("查询失败: %w", err)
	}
	// 部分服务 quirk:HEAD 可能返回 200 但元信息全空(对象其实不存在)
	if resp.ContentLength == nil && resp.ETag == nil && resp.LastModified == nil {
		fmt.Fprintf(os.Stderr, "警告: HeadObject 返回 200 但元信息为空,对象可能不存在: s3://%s/%s\n", p.Bucket, p.Key)
		return fmt.Errorf("对象不存在: s3://%s/%s", p.Bucket, p.Key)
	}
	fmt.Printf("key: s3://%s/%s\n", p.Bucket, p.Key)
	if resp.ContentLength != nil {
		fmt.Printf("size: %s (%d bytes)\n", humanBytes(*resp.ContentLength), *resp.ContentLength)
	}
	if resp.ContentType != nil {
		fmt.Printf("content-type: %s\n", *resp.ContentType)
	}
	if resp.LastModified != nil {
		fmt.Printf("last-modified: %s\n", resp.LastModified.Format("2006-01-02 15:04:05"))
	}
	if resp.ETag != nil {
		fmt.Printf("etag: %s\n", *resp.ETag)
	}
	if resp.StorageClass != "" {
		fmt.Printf("storage-class: %s\n", resp.StorageClass)
	}
	if resp.VersionId != nil {
		fmt.Printf("version-id: %s\n", *resp.VersionId)
	}
	if len(resp.Metadata) > 0 {
		fmt.Println("metadata:")
		for k, v := range resp.Metadata {
			fmt.Printf("  %s: %s\n", k, v)
		}
	}
	return nil
}

func statLocal(arg string) error {
	info, err := os.Stat(arg)
	if err != nil {
		return fmt.Errorf("读取本地文件失败: %w", err)
	}
	fmt.Printf("name: %s\n", filepath.Base(arg))
	fmt.Printf("size: %s (%d bytes)\n", humanBytes(info.Size()), info.Size())
	fmt.Printf("mode: %s\n", info.Mode().String())
	fmt.Printf("modtime: %s\n", info.ModTime().Format("2006-01-02 15:04:05"))
	fmt.Printf("is-dir: %v\n", info.IsDir())
	return nil
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

func init() {
}
