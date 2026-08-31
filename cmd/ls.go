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

var (
	lsLong     bool
	lsDirsOnly bool
)

var lsCmd = &cobra.Command{
	Use:   "ls [s3://bucket/prefix]",
	Short: "列举对象或桶",
	Args:  cobra.MaximumNArgs(1),
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

		var bucket, prefix string
		if len(args) == 0 {
			if r.Bucket == "" {
				return fmt.Errorf("未指定 bucket,请用 s3://bucket/prefix 或在配置中设置默认 bucket")
			}
			bucket = r.Bucket
		} else {
			p, err := parseS3(args[0], r)
			if err != nil {
				return err
			}
			bucket = p.Bucket
			prefix = p.Key
		}

		if lsDirsOnly {
			return listDirs(ctx, s3c, bucket, prefix)
		}
		return listObjects(ctx, s3c, bucket, prefix, lsLong)
	},
}

func listObjects(ctx context.Context, s3c *s3.Client, bucket, prefix string, long bool) error {
	if bucket == "" {
		return fmt.Errorf("未指定 bucket")
	}
	paginator := s3.NewListObjectsV2Paginator(s3c, &s3.ListObjectsV2Input{
		Bucket: &bucket,
		Prefix: &prefix,
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("列举失败: %w", err)
		}
		for _, obj := range page.Contents {
			if long {
				fmt.Printf("%12d  %s  s3://%s/%s\n", obj.Size, obj.LastModified.Format("2006-01-02 15:04:05"), bucket, *obj.Key)
			} else {
				fmt.Println(*obj.Key)
			}
		}
	}
	return nil
}

func listDirs(ctx context.Context, s3c *s3.Client, bucket, prefix string) error {
	if bucket == "" {
		return fmt.Errorf("未指定 bucket")
	}
	// delimiter 模式下,prefix 必须以 "/" 结尾。否则 S3 会把本层所有 key 汇总成
	// 单个 "prefix/" 的 CommonPrefix(如 "pdf/"),去前缀后变成空串,导致无输出。
	// 这里统一补上尾斜杠;空串(列桶根目录)保持不变。
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	paginator := s3.NewListObjectsV2Paginator(s3c, &s3.ListObjectsV2Input{
		Bucket:    &bucket,
		Prefix:    &prefix,
		Delimiter: aws.String("/"),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("列举失败: %w", err)
		}
		for _, cp := range page.CommonPrefixes {
			if cp.Prefix == nil {
				continue
			}
			name := strings.TrimPrefix(*cp.Prefix, prefix) // 去掉查询前缀
			name = strings.TrimPrefix(name, "/")           // 去可能的残斜杠
			fmt.Println(name)                              // 形如 "subdir/"
		}
	}
	return nil
}

func init() {
	lsCmd.Flags().BoolVarP(&lsLong, "long", "l", false, "显示大小和修改时间")
	lsCmd.Flags().BoolVarP(&lsDirsOnly, "dir", "d", false, "只列目录(子前缀,不含文件)")
}
