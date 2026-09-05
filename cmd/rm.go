package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/s3path"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
)

var rmRecursive bool

var rmCmd = &cobra.Command{
	Use:   "rm <s3://bucket/key>...",
	Short: "删除对象",
	Long: `删除对象,支持多个参数;-r 递归删除前缀下所有对象(批量删除,每次最多 1000 个)。
参数含通配符(*、?)时按模式匹配删除(基于列举 + 客户端匹配),如 s3://bucket/logs/*.log。

参数为 "-" 时从 stdin 逐行读取 key(每行一个,支持 s3://bucket/key 或裸 key,
裸 key 使用默认桶,也支持通配符),可与 -r 组合成管道式批量操作:
  sail ls s3://bucket/prefix/ | sail rm -r -
  sail ls s3://bucket | sail rm 's3://bucket/*.tmp' -`,
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

		total := 0
		for _, a := range args {
			if a == "-" {
				n, err := rmFromStdin(ctx, s3c, r, rmRecursive)
				if err != nil {
					return err
				}
				total += n
				continue
			}
			p, err := parseS3(a, r)
			if err != nil {
				return err
			}
			// 递归模式允许桶根(s3://bucket/),通配符分支在下方独立处理
			if p.Key == "" && !rmRecursive {
				return fmt.Errorf("缺少 key,需指定 s3://bucket/key")
			}
			// 通配符路径:展开为匹配对象后删除(独立于 -r)
			if hasWildcard(p.Key) {
				n, err := rmWildcard(ctx, s3c, r, a)
				if err != nil {
					return err
				}
				total += n
				continue
			}
			if rmRecursive {
				n, err := deleteRecursive(ctx, s3c, p.Bucket, p.Key)
				if err != nil {
					return err
				}
				total += n
				continue
			}
			if err := deleteOne(ctx, s3c, p.Bucket, p.Key); err != nil {
				return err
			}
			total++
		}
		fmt.Printf("共删除 %d 个对象\n", total)
		return nil
	},
}

// rmWildcard 按通配符展开并批量删除,返回删除数量。
func rmWildcard(ctx context.Context, s3c *s3.Client, r *config.Resolved, pattern string) (int, error) {
	objs, _, bucket, err := expandWildcards(ctx, s3c, r, pattern)
	if err != nil {
		return 0, err
	}
	keys := make([]string, len(objs))
	for i, o := range objs {
		keys[i] = *o.Key
	}
	return deleteObjectsBatch(ctx, s3c, bucket, keys)
}

// rmFromStdin 从 stdin 逐行读取 key 并删除(每行一个,支持通配符)。
func rmFromStdin(ctx context.Context, s3c *s3.Client, r *config.Resolved, recursive bool) (int, error) {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	total := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var p *s3path.S3Path
		if strings.HasPrefix(line, "s3://") {
			parsed, err := parseS3(line, r)
			if err != nil {
				return total, err
			}
			p = parsed
		} else {
			if r == nil || r.Bucket == "" {
				return total, fmt.Errorf("stdin 行 %q 不是 s3:// 路径且未配置默认 bucket", line)
			}
			p = &s3path.S3Path{Bucket: r.Bucket, Key: line}
		}
		if p.Key == "" && !recursive {
			return total, fmt.Errorf("缺少 key,需指定 s3://bucket/key")
		}
		if hasWildcard(p.Key) {
			n, err := rmWildcard(ctx, s3c, r, p.Format())
			if err != nil {
				return total, err
			}
			total += n
			continue
		}
		if recursive {
			n, err := deleteRecursive(ctx, s3c, p.Bucket, p.Key)
			if err != nil {
				return total, err
			}
			total += n
			continue
		}
		if err := deleteOne(ctx, s3c, p.Bucket, p.Key); err != nil {
			return total, err
		}
		total++
	}
	return total, scanner.Err()
}

// deleteOne 删除单个对象并打印结果。
func deleteOne(ctx context.Context, s3c *s3.Client, bucket, key string) error {
	_, err := s3c.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &bucket,
		Key:    &key,
	})
	if err != nil {
		return fmt.Errorf("删除失败: %w", err)
	}
	fmt.Printf("已删除 s3://%s/%s\n", bucket, key)
	return nil
}

// deleteRecursive 递归删除前缀下所有对象(批量 DeleteObjects),返回删除数量。
func deleteRecursive(ctx context.Context, s3c *s3.Client, bucket, prefix string) (int, error) {
	objs, err := collectAllObjects(ctx, s3c, bucket, prefix)
	if err != nil {
		return 0, err
	}
	keys := make([]string, len(objs))
	for i, o := range objs {
		keys[i] = *o.Key
	}
	return deleteObjectsBatch(ctx, s3c, bucket, keys)
}

func init() {
	rmCmd.Flags().BoolVarP(&rmRecursive, "recursive", "r", false, "递归删除前缀下所有对象")
}
