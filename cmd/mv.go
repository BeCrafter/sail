package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/i18n"
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
	Short: "Move objects/files (copy then delete source)",
	Long: `Move objects/files: copy then delete the source. s3-to-s3 uses server-side CopyObject + Delete with zero bandwidth.

Examples:
  sail mv s3://bucket/a.txt s3://bucket/moved.txt     # single object, no confirm
  sail mv ./local.txt s3://bucket/uploaded.txt
  sail mv s3://bucket/file.txt ./retrieved.txt
  sail mv -r s3://bucket/src/ s3://bucket/dst/         # recursive, interactive confirm
  sail mv -r --yes s3://bucket/src/ s3://bucket/dst/   # skip confirm
  sail mv -r --yes ./dir s3://bucket/mirror/`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		srcIsS3 := strings.HasPrefix(args[0], "s3://")
		dstIsS3 := strings.HasPrefix(args[1], "s3://")
		if !srcIsS3 && !dstIsS3 {
			return errors.New(i18n.T("local-to-local move should use the system mv command"))
		}

		// 递归移动有破坏性:复制静默失败后再删源会丢数据,故递归需确认。
		// 单对象无确认(对齐 rm 单删,快、可恢复)。
		if mvRecursive && !mvDryRun && !mvYes {
			if !isTTY(os.Stdin) {
				return errors.New(i18n.T("recursive move is destructive, add --yes to confirm in non-interactive environments"))
			}
			fmt.Printf(i18n.T("would recursively move %s -> %s, confirm? [y/N] "), args[0], args[1])
			if !confirm() {
				fmt.Println(i18n.T("canceled"))
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
	mvCmd.Flags().BoolVarP(&mvRecursive, "recursive", "r", false, "move recursively")
	mvCmd.Flags().BoolVar(&mvYes, "yes", false, "skip the confirmation prompt")
	mvCmd.Flags().BoolVar(&mvDryRun, "dry-run", false, "show what would be done without actually moving")
}
