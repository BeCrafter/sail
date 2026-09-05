package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

var (
	grepIgnoreCase bool
	grepInvert     bool
	grepFilesOnly  bool
	grepCount      bool
	grepLineNo     bool
)

var grepCmd = &cobra.Command{
	Use:   "grep [options] <pattern> <src>...",
	Short: "在对象/文件内容中搜索",
	Long: `流式按行正则搜索对象/文件内容,不落盘。
-i 忽略大小写;-v 反向(输出不匹配的行);-l 只列出有匹配的源;-c 输出匹配行数;-n 显示行号。
单源输出裸匹配行;多源带 "源:行" 前缀。全部源无匹配时退出码为 1(GNU grep 惯例)。

示例:
  sail grep -n "ERROR" s3://bucket/logs/app.log
  sail grep -ic "timeout" s3://bucket/a.json ./b.txt`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		pattern := args[0]
		sources := args[1:]
		if grepIgnoreCase {
			pattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("无效的正则: %w", err)
		}
		ctx := context.Background()
		multi := len(sources) > 1
		anyMatched := false
		for _, arg := range sources {
			matched, err := grepOne(ctx, arg, re, multi)
			if err != nil {
				return err
			}
			if matched {
				anyMatched = true
			}
		}
		if !anyMatched {
			os.Exit(1) // 无匹配:静默退出码 1(Unix grep 惯例)
		}
		return nil
	},
}

// grepOne 在单个源中按行搜索,返回是否有匹配。
func grepOne(ctx context.Context, arg string, re *regexp.Regexp, multi bool) (bool, error) {
	src, err := openSourceArg(ctx, arg)
	if err != nil {
		return false, err
	}
	defer src.Close()
	br := bufio.NewReader(src.Reader)
	lineNo := int64(0)
	count := int64(0)
	matched := false
	binary := false
	binaryReported := false
	for {
		line, rerr := br.ReadString('\n')
		if line != "" {
			lineNo++
			if !binary && strings.Contains(line, "\x00") {
				binary = true
			}
			content := strings.TrimSuffix(line, "\n")
			hit := re.MatchString(content)
			if grepInvert {
				hit = !hit
			}
			if hit {
				matched = true
				count++
				switch {
				case grepFilesOnly:
					fmt.Println(arg)
					return true, nil
				case grepCount:
				default:
					if binary {
						// 二进制内容:只提示一次,不再输出原文
						if !binaryReported {
							fmt.Printf("%s: 二进制文件匹配\n", arg)
							binaryReported = true
						}
						continue
					}
					prefix := ""
					if multi {
						prefix = arg + ":"
					}
					if grepLineNo {
						prefix += fmt.Sprintf("%d:", lineNo)
					}
					fmt.Print(prefix, content, "\n")
				}
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			return matched, fmt.Errorf("读取失败: %w", rerr)
		}
	}
	if grepFilesOnly && matched {
		fmt.Println(arg)
	} else if grepCount && matched {
		if multi {
			fmt.Printf("%s:%d\n", arg, count)
		} else {
			fmt.Printf("%d\n", count)
		}
	}
	return matched, nil
}

func init() {
	grepCmd.Flags().BoolVarP(&grepIgnoreCase, "ignore-case", "i", false, "忽略大小写")
	grepCmd.Flags().BoolVarP(&grepInvert, "invert-match", "v", false, "反向匹配(输出不匹配的行)")
	grepCmd.Flags().BoolVarP(&grepFilesOnly, "files-with-matches", "l", false, "只列出有匹配的源")
	grepCmd.Flags().BoolVar(&grepCount, "count", false, "输出匹配行数")
	grepCmd.Flags().BoolVarP(&grepLineNo, "line-number", "n", false, "显示行号")
}
