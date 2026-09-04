package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
)

var rmdirCmd = &cobra.Command{
	Use:   "rmdir <s3://bucket/prefix/>...",
	Short: "删除空目录占位对象",
	Long: `删除空目录占位对象(不做递归删除)。目录下有其他对象时报错,请改用 sail rm -r。
目录不存在占位对象时视为已删除(幂等)。

示例:
  sail rmdir s3://bucket/videos/2026/`,
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
			dirKey := strings.TrimSuffix(p.Key, "/") + "/"
			if dirKey == "/" {
				return fmt.Errorf("缺少目录名,需指定 s3://bucket/prefix/")
			}
			if err := rmdirOne(ctx, s3c, p.Bucket, dirKey); err != nil {
				return err
			}
			fmt.Printf("已删除 s3://%s/%s\n", p.Bucket, dirKey)
		}
		return nil
	},
}

// rmdirOne 删除单个空目录占位对象。判定逻辑:按字典序占位对象 key(尾 /)排最前,
// 取前 2 个即可覆盖全部情况:
//   - 仅占位对象且 0 字节:删除;
//   - 占位对象非 0 字节:占位对象被上传了真实内容,报错;
//   - 含其他对象:目录非空,报错(请用 rm -r);
//   - 什么都没有:幂等成功。
func rmdirOne(ctx context.Context, s3c *s3.Client, bucket, dirKey string) error {
	resp, err := s3c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  &bucket,
		Prefix:  &dirKey,
		MaxKeys: aws.Int32(2),
	})
	if err != nil {
		return fmt.Errorf("列举失败: %w", err)
	}
	if len(resp.Contents) == 0 {
		return nil // 无占位对象也无子对象:幂等成功
	}
	first := resp.Contents[0]
	if *first.Key != dirKey {
		return fmt.Errorf("目录非空: s3://%s/%s (用 sail rm -r 递归删除)", bucket, strings.TrimSuffix(dirKey, "/"))
	}
	if len(resp.Contents) > 1 {
		return fmt.Errorf("目录非空: s3://%s/%s (用 sail rm -r 递归删除)", bucket, strings.TrimSuffix(dirKey, "/"))
	}
	// 占位对象可能是 0 字节标记,也可能有人往该 key 写了真实内容(上传到 key "a/" 本身)。
	if first.Size != nil && *first.Size != 0 {
		return fmt.Errorf("占位对象非空(%d 字节),请用 sail rm 删除该对象: s3://%s/%s", *first.Size, bucket, dirKey)
	}
	_, err = s3c.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &bucket,
		Key:    &dirKey,
	})
	if err != nil {
		return fmt.Errorf("删除失败: %w", err)
	}
	return nil
}
