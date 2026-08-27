package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/view"
)

var (
	viewAs    string
	viewRaw   bool
	viewForce bool
	viewWidth int
)

var viewCmd = &cobra.Command{
	Use:     "view <s3://bucket/key | 本地文件路径>",
	Aliases: []string{"cat"},
	Short:   "查看对象/文件内容",
	Long: `智能查看 S3 对象或本地文件,按格式渲染:
  文本/代码 → 原文输出
  JSON → 缩进美化
  YAML → 重新格式化
  CSV → 表格对齐
  XML → 缩进美化
  图片 → 终端字符画(半块字符,任何终端可见,不依赖终端图形协议)
  二进制 → 元信息 + 前 256 字节 hex dump

示例:
  sail view s3://bucket/config.json
  sail view ./local.log
  sail view s3://bucket/data.json --raw | jq .
  sail view s3://bucket/photo.png --width 60`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// cat 是 view --raw 的别名
		if cmd.CalledAs() == "cat" {
			viewRaw = true
		}
		arg := args[0]
		ctx := context.Background()

		var s3c *s3.Client
		var defaultBucket string
		if strings.HasPrefix(arg, "s3://") {
			r, _, err := loadResolved()
			if err != nil {
				return err
			}
			s3c, err = client.New(ctx, r)
			if err != nil {
				return err
			}
			defaultBucket = r.Bucket
		}
		src, err := view.OpenSource(ctx, arg, s3c, defaultBucket)
		if err != nil {
			return err
		}
		defer src.Close()

		if viewRaw {
			if _, err := io.Copy(os.Stdout, src.Reader); err != nil {
				return fmt.Errorf("查看失败: %w", err)
			}
			return nil
		}

		f, ok := view.ParseFormat(viewAs)
		if !ok {
			return fmt.Errorf("不支持的格式 --as %q", viewAs)
		}
		opts := &view.Options{
			Force:         viewForce,
			MaxImageBytes: 10 << 20,
			Width:         viewWidth,
		}
		if err := view.Render(src, f, opts); err != nil {
			return fmt.Errorf("查看失败: %w", err)
		}
		return nil
	},
}

func init() {
	viewCmd.Flags().StringVar(&viewAs, "as", "", "强制格式: text|json|yaml|csv|xml|image|binary")
	viewCmd.Flags().BoolVar(&viewRaw, "raw", false, "原样输出(跳过格式化和字符画,适合管道)")
	viewCmd.Flags().BoolVar(&viewForce, "force", false, "跳过大小限制")
	viewCmd.Flags().IntVar(&viewWidth, "width", 0, "字符画列宽(0=自动探测)")
}
