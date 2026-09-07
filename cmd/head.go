package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/BeCrafter/sail/internal/i18n"
	"github.com/spf13/cobra"
)

var (
	headLines int64 = 10
	headBytes int64
)

var headCmd = &cobra.Command{
	Use:   "head [-n N | --bytes N] <s3://bucket/key|LOCAL_PATH>",
	Short: "Output the first part of object/file",
	Long: `Stream-read the head of an object/file without downloading it to disk.
-n shows the first N lines (default 10); --bytes shows the first N bytes; the two are mutually exclusive.

Examples:
  sail head -n 20 s3://bucket/logs/app.log
  sail head --bytes 4096 s3://bucket/data.bin`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		nChanged := cmd.Flags().Changed("lines")
		cChanged := cmd.Flags().Changed("bytes")
		if nChanged && cChanged {
			return errors.New(i18n.T("-n and --bytes cannot be used together"))
		}
		if headLines < 0 || headBytes < 0 {
			return errors.New(i18n.T("-n and --bytes cannot be negative"))
		}
		ctx := context.Background()
		src, err := openSourceArg(ctx, args[0])
		if err != nil {
			return err
		}
		defer src.Close()
		if cChanged {
			_, err := io.CopyN(os.Stdout, src.Reader, headBytes)
			if err != nil && err != io.EOF {
				return fmt.Errorf(i18n.T("read failed: %w"), err)
			}
			return nil
		}
		return headLinesFromReader(src.Reader, headLines)
	},
}

// headLinesFromReader 打印前 n 行(与 GNU head 一致:最后一行无换行符也原样输出)。
func headLinesFromReader(r io.Reader, n int64) error {
	if n == 0 {
		return nil
	}
	br := bufio.NewReader(r)
	for i := int64(0); i < n; i++ {
		line, err := br.ReadString('\n')
		if line != "" {
			fmt.Print(line)
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf(i18n.T("read failed: %w"), err)
		}
	}
	return nil
}

func init() {
	headCmd.Flags().Int64VarP(&headLines, "lines", "n", 10, "show first N lines")
	headCmd.Flags().Int64Var(&headBytes, "bytes", 0, "show first N bytes")
}
