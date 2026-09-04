package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

var (
	headLines int64 = 10
	headBytes int64
)

var headCmd = &cobra.Command{
	Use:   "head [-n N | --bytes N] <s3://bucket/key|本地路径>",
	Short: "查看对象/文件开头内容",
	Long: `流式读取对象/文件开头内容,不落盘。
-n 显示前 N 行(默认 10);--bytes 显示前 N 字节;两者互斥。

示例:
  sail head -n 20 s3://bucket/logs/app.log
  sail head --bytes 4096 s3://bucket/data.bin`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		nChanged := cmd.Flags().Changed("lines")
		cChanged := cmd.Flags().Changed("bytes")
		if nChanged && cChanged {
			return fmt.Errorf("-n 与 --bytes 不能同时使用")
		}
		if headLines < 0 || headBytes < 0 {
			return fmt.Errorf("-n 与 --bytes 不能为负数")
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
				return fmt.Errorf("读取失败: %w", err)
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
			return fmt.Errorf("读取失败: %w", err)
		}
	}
	return nil
}

func init() {
	headCmd.Flags().Int64VarP(&headLines, "lines", "n", 10, "显示前 N 行")
	headCmd.Flags().Int64Var(&headBytes, "bytes", 0, "显示前 N 字节")
}
