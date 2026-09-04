package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/spf13/cobra"
)

var mbCmd = &cobra.Command{
	Use:   "mb <s3://bucket>...",
	Short: "创建桶",
	Long: `创建桶(CreateBucket)。桶名需符合所接入 S3 服务的命名规则。
重复创建同一桶可能被服务拒绝(取决于服务实现),失败时提示改用已存在的桶。

示例:
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
				return fmt.Errorf("mb 只接受桶级路径(不带 key): %s", arg)
			}
			_, err = s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &p.Bucket})
			if err != nil {
				return fmt.Errorf("创建桶失败: %w", err)
			}
			fmt.Printf("已创建桶 s3://%s\n", p.Bucket)
		}
		return nil
	},
}

var rbCmd = &cobra.Command{
	Use:   "rb <s3://bucket>...",
	Short: "删除空桶",
	Long: `删除空桶(DeleteBucket)。桶内仍有对象或占位对象时服务会拒绝,
需先清空(如 sail rm -r s3://bucket/)。删除后再次创建通常有延迟,请稍候重试。

示例:
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
				return fmt.Errorf("rb 只接受桶级路径(不带 key): %s", arg)
			}
			_, err = s3c.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: &p.Bucket})
			if err != nil {
				if isBucketNotEmpty(err) {
					return fmt.Errorf("桶非空,请先用 sail rm -r s3://%s/ 清空: %w", p.Bucket, err)
				}
				return fmt.Errorf("删除桶失败: %w", err)
			}
			fmt.Printf("已删除桶 s3://%s\n", p.Bucket)
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
		return fmt.Errorf("列举桶失败: %w", err)
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
