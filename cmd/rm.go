package cmd

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
	"github.com/BeCrafter/sail/internal/client"
)

var rmRecursive bool

var rmCmd = &cobra.Command{
	Use:   "rm s3://bucket/key",
	Short: "删除对象",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		r, _, err := loadResolved()
		if err != nil {
			return err
		}
		p, err := parseS3(args[0], r)
		if err != nil {
			return err
		}
		if p.Key == "" {
			return fmt.Errorf("缺少 key,需指定 s3://bucket/key")
		}
		ctx := context.Background()
		s3c, err := client.New(ctx, r)
		if err != nil {
			return err
		}

		if rmRecursive {
			return deleteRecursive(ctx, s3c, p.Bucket, p.Key)
		}
		_, err = s3c.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: &p.Bucket,
			Key:    &p.Key,
		})
		if err != nil {
			return fmt.Errorf("删除失败: %w", err)
		}
		fmt.Printf("已删除 s3://%s/%s\n", p.Bucket, p.Key)
		return nil
	},
}

func deleteRecursive(ctx context.Context, s3c *s3.Client, bucket, prefix string) error {
	paginator := s3.NewListObjectsV2Paginator(s3c, &s3.ListObjectsV2Input{
		Bucket: &bucket,
		Prefix: &prefix,
	})
	count := 0
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("列举失败: %w", err)
		}
		for _, obj := range page.Contents {
			_, err := s3c.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: &bucket,
				Key:    obj.Key,
			})
			if err != nil {
				return fmt.Errorf("删除 s3://%s/%s 失败: %w", bucket, *obj.Key, err)
			}
			fmt.Printf("已删除 s3://%s/%s\n", bucket, *obj.Key)
			count++
		}
	}
	fmt.Printf("共删除 %d 个对象\n", count)
	return nil
}

func init() {
	rmCmd.Flags().BoolVarP(&rmRecursive, "recursive", "r", false, "递归删除前缀下所有对象")
}
