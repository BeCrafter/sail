package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

var (
	wcLines bool
	wcWords bool
	wcBytes bool
)

var wcCmd = &cobra.Command{
	Use:   "wc <src>...",
	Short: "统计行数/单词数/字节数",
	Long: `流式统计对象/文件的行、词、字节数。
未给选项时输出三列(行 词 字节,GNU wc 顺序);给选项则只输出所选列。

示例:
  sail wc -l s3://bucket/logs/app.log
  sail wc s3://bucket/a.json ./b.txt`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		showL := wcLines || !(wcLines || wcWords || wcBytes)
		showW := wcWords || !(wcLines || wcWords || wcBytes)
		showC := wcBytes || !(wcLines || wcWords || wcBytes)

		ctx := context.Background()
		var totalL, totalW, totalC int64
		for _, arg := range args {
			l, w, c, err := wcOne(ctx, arg)
			if err != nil {
				return err
			}
			totalL += l
			totalW += w
			totalC += c
			printWCLine(showL, showW, showC, arg, l, w, c)
		}
		if len(args) > 1 {
			printWCLine(showL, showW, showC, "total", totalL, totalW, totalC)
		}
		return nil
	},
}

// wcOne 统计单个源的行/词/字节。
func wcOne(ctx context.Context, arg string) (lines, words, bytes int64, err error) {
	src, err := openSourceArg(ctx, arg)
	if err != nil {
		return 0, 0, 0, err
	}
	defer src.Close()
	br := bufio.NewReader(src.Reader)
	inWord := false
	buf := make([]byte, 64*1024)
	for {
		n, rerr := br.Read(buf)
		if n > 0 {
			bytes += int64(n)
			for _, ch := range buf[:n] {
				if ch == '\n' {
					lines++
				}
				if isSpace(ch) {
					inWord = false
				} else if !inWord {
					words++
					inWord = true
				}
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			return 0, 0, 0, fmt.Errorf("读取失败: %w", rerr)
		}
	}
	return lines, words, bytes, nil
}

func isSpace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '\v' || ch == '\f'
}

func printWCLine(showL, showW, showC bool, name string, l, w, c int64) {
	switch {
	case showL && showW && showC:
		fmt.Printf("%8d%8d%8d  %s\n", l, w, c, name)
	case showL && showW:
		fmt.Printf("%8d%8d  %s\n", l, w, name)
	case showL && showC:
		fmt.Printf("%8d%8d  %s\n", l, c, name)
	case showW && showC:
		fmt.Printf("%8d%8d  %s\n", w, c, name)
	case showL:
		fmt.Printf("%8d  %s\n", l, name)
	case showW:
		fmt.Printf("%8d  %s\n", w, name)
	default:
		fmt.Printf("%8d  %s\n", c, name)
	}
}

func init() {
	wcCmd.Flags().BoolVarP(&wcLines, "lines", "l", false, "统计行数")
	wcCmd.Flags().BoolVarP(&wcWords, "words", "w", false, "统计单词数")
	wcCmd.Flags().BoolVar(&wcBytes, "bytes", false, "统计字节数")
}
