package cmd

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
)

var mkdirCmd = &cobra.Command{
	Use:   "mkdir <s3://bucket/prefix/>...",
	Short: "创建目录占位对象",
	Long: `创建目录占位对象(0 字节、key 以 / 结尾)。
S3 没有真实目录,占位对象是约定俗成的目录标记;mkdir 天然幂等,重复执行只是覆盖占位对象。

示例:
  sail mkdir s3://bucket/videos/2026/
  sail mkdir s3://bucket/a s3://bucket/b`,
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
			key := strings.TrimSuffix(p.Key, "/") + "/"
			if key == "/" {
				return fmt.Errorf("缺少目录名,需指定 s3://bucket/prefix/")
			}
			_, err = s3c.PutObject(ctx, &s3.PutObjectInput{
				Bucket:        &p.Bucket,
				Key:           &key,
				Body:          bytes.NewReader(nil),
				ContentLength: aws.Int64(0),
				ContentType:   aws.String("application/x-directory"),
			})
			if err != nil {
				return fmt.Errorf("创建 s3://%s/%s 失败: %w", p.Bucket, key, err)
			}
			fmt.Printf("已创建 s3://%s/%s\n", p.Bucket, key)
		}
		return nil
	},
}
