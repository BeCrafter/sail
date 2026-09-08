package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/i18n"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/spf13/cobra"
)

var mbCmd = &cobra.Command{
	Use:   "mb <s3://bucket>...",
	Short: "Create buckets",
	Long: `Create buckets (CreateBucket). Bucket names must follow the naming rules of the S3 service you connect to.
Recreating the same bucket may be rejected by the service (implementation-dependent); on failure you are prompted to use the existing bucket instead.

Examples:
  sail mb s3://my-new-bucket`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		r, _, err := loadResolved()
		if err != nil {
			return err
		}
		ctx := context.Background()
		s3c, err := client.New(ctx, r)
		if err != nil {
			return err
		}
		for _, arg := range args {
			p, err := parseS3(arg, r)
			if err != nil {
				return err
			}
			if p.Key != "" {
				return fmt.Errorf(i18n.T("mb only accepts bucket-level paths (without key): %s"), arg)
			}
			_, err = s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &p.Bucket})
			if err != nil {
				return fmt.Errorf(i18n.T("failed to create bucket: %w"), err)
			}
			fmt.Printf(i18n.T("created bucket s3://%s\n"), p.Bucket)
		}
		return nil
	},
}

var rbCmd = &cobra.Command{
	Use:   "rb <s3://bucket>...",
	Short: "Delete empty buckets",
	Long: `Delete empty buckets (DeleteBucket). The service rejects the call when the bucket still holds objects or placeholder objects;
empty it first (e.g. sail rm -r s3://bucket/). Recreation after deletion usually has a delay — wait and retry.

Examples:
  sail rb s3://my-old-bucket`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		r, _, err := loadResolved()
		if err != nil {
			return err
		}
		ctx := context.Background()
		s3c, err := client.New(ctx, r)
		if err != nil {
			return err
		}
		for _, arg := range args {
			p, err := parseS3(arg, r)
			if err != nil {
				return err
			}
			if p.Key != "" {
				return fmt.Errorf(i18n.T("rb only accepts bucket-level paths (without key): %s"), arg)
			}
			_, err = s3c.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: &p.Bucket})
			if err != nil {
				if isBucketNotEmpty(err) {
					return fmt.Errorf(i18n.T("bucket not empty; empty it first with sail rm -r s3://%s/: %w"), p.Bucket, err)
				}
				return fmt.Errorf(i18n.T("failed to delete bucket: %w"), err)
			}
			fmt.Printf(i18n.T("deleted bucket s3://%s\n"), p.Bucket)
		}
		return nil
	},
}

// isBucketNotEmpty 判断错误是否为"桶非空"(不同服务错误码不同)。
func isBucketNotEmpty(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "BucketNotEmpty", "NotEmpty":
			return true
		}
		msg := strings.ToLower(apiErr.ErrorMessage())
		return strings.Contains(msg, "not empty") || strings.Contains(msg, "非空")
	}
	return false
}

// listBuckets 列出全部桶(ListBuckets)。
// 部分 S3 兼容服务的 Bucket.CreationDate 格式不合标准(空格分隔而非 ISO8601),
// AWS SDK 反序列化会整体失败。internal/client 已通过 deserialize 中间件
// 在解析前规范化时间格式(见 client.go fixBrokenXMLTime),这里走标准 SDK 调用。
func listBuckets(ctx context.Context, s3c *s3.Client) error {
	resp, err := s3c.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return fmt.Errorf(i18n.T("failed to list buckets: %w"), err)
	}
	for _, b := range resp.Buckets {
		name := "<unknown>"
		if b.Name != nil {
			name = *b.Name
		}
		created := ""
		if b.CreationDate != nil {
			created = "  " + b.CreationDate.Format("2006-01-02")
		}
		fmt.Printf("s3://%s%s\n", name, created)
	}
	return nil
}
