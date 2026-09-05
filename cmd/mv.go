package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	mvRecursive bool
	mvYes       bool
	mvDryRun    bool
)

var mvCmd = &cobra.Command{
	Use:   "mv <src> <dst>",
	Short: "移动对象/文件(复制后删除源)",
	Long: `移动对象/文件,等于复制后删除源。s3↔s3 走服务端 CopyObject+Delete,零带宽。

示例:
  sail mv s3://bucket/a.txt s3://bucket/moved.txt     # 单对象,无确认
  sail mv ./local.txt s3://bucket/uploaded.txt
  sail mv s3://bucket/file.txt ./retrieved.txt
  sail mv -r s3://bucket/src/ s3://bucket/dst/         # 递归,交互确认
  sail mv -r --yes s3://bucket/src/ s3://bucket/dst/   # 跳过确认
  sail mv -r --yes ./dir s3://bucket/mirror/`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		srcIsS3 := strings.HasPrefix(args[0], "s3://")
		dstIsS3 := strings.HasPrefix(args[1], "s3://")
		if !srcIsS3 && !dstIsS3 {
			return fmt.Errorf("本地到本地的移动请使用系统 mv 命令")
		}

		// 递归移动有破坏性:复制静默失败后再删源会丢数据,故递归需确认。
		// 单对象无确认(对齐 rm 单删,快、可恢复)。
		if mvRecursive && !mvDryRun && !mvYes {
			if !isTTY(os.Stdin) {
				return fmt.Errorf("递归移动有破坏性,非交互环境请加 --yes 确认")
			}
			fmt.Printf("将递归移动 %s -> %s,确认? [y/N] ", args[0], args[1])
			if !confirm() {
				fmt.Println("已取消")
				return nil
			}
		}

		ctx := context.Background()
		// dry-run 只是预览,不需要凭据;各 cpXxx 在 dryRun 分支返回前不会用 s3c
		var s3c *s3.Client
		var r *config.Resolved
		if !mvDryRun {
			var err error
			r, _, err = loadResolved()
			if err != nil {
				return err
			}
			s3c, err = client.New(ctx, r)
			if err != nil {
				return err
			}
		} else {
			r, _, _ = loadResolved()
		}
		switch {
		case !srcIsS3 && dstIsS3:
			dst, err := parseS3(args[1], r)
			if err != nil {
				return err
			}
			return cpLocalToS3(ctx, s3c, args[0], dst, mvRecursive, true, mvDryRun)
		case srcIsS3 && !dstIsS3:
			src, err := parseS3(args[0], r)
			if err != nil {
				return err
			}
			return cpS3ToLocal(ctx, s3c, src, args[1], mvRecursive, true, mvDryRun)
		default:
			src, err := parseS3(args[0], r)
			if err != nil {
				return err
			}
			dst, err := parseS3(args[1], r)
			if err != nil {
				return err
			}
			return cpS3ToS3(ctx, s3c, src, dst, mvRecursive, true, mvDryRun)
		}
	},
}

func isTTY(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

func init() {
	mvCmd.Flags().BoolVarP(&mvRecursive, "recursive", "r", false, "递归移动")
	mvCmd.Flags().BoolVar(&mvYes, "yes", false, "跳过确认提示")
	mvCmd.Flags().BoolVar(&mvDryRun, "dry-run", false, "只显示将执行的操作,不实际移动")
}
