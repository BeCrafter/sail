package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/i18n"
	"github.com/BeCrafter/sail/internal/view"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
)

var (
	viewAs    string
	viewRaw   bool
	viewForce bool
	viewWidth int
)

var viewCmd = &cobra.Command{
	Use:     "view <s3://bucket/key | LOCAL_FILE_PATH>",
	Aliases: []string{"cat"},
	Short:   "View object/file contents",
	Long: `Smart-render an S3 object or local file based on its format:
  text/code -> output as-is
  JSON      -> pretty-print
  YAML      -> reformat
  CSV       -> aligned table
  XML       -> pretty-print
  image     -> terminal ASCII art (half-block characters, visible in any terminal, no graphics protocol needed)
  binary    -> metadata + first 256 bytes hex dump

Examples:
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
				return fmt.Errorf(i18n.T("view failed: %w"), err)
			}
			return nil
		}

		f, ok := view.ParseFormat(viewAs)
		if !ok {
			return fmt.Errorf(i18n.T("unsupported format --as %q"), viewAs)
		}
		opts := &view.Options{
			Force:         viewForce,
			MaxImageBytes: 10 << 20,
			Width:         viewWidth,
		}
		if err := view.Render(src, f, opts); err != nil {
			return fmt.Errorf(i18n.T("view failed: %w"), err)
		}
		return nil
	},
}

func init() {
	viewCmd.Flags().StringVar(&viewAs, "as", "", "force format: text|json|yaml|csv|xml|image|binary")
	viewCmd.Flags().BoolVar(&viewRaw, "raw", false, "raw output (skip formatting and ASCII art; good for piping)")
	viewCmd.Flags().BoolVar(&viewForce, "force", false, "skip size limit")
	viewCmd.Flags().IntVar(&viewWidth, "width", 0, "ASCII art column width (0=auto-detect)")
}
