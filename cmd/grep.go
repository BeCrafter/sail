package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/BeCrafter/sail/internal/i18n"
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
	Short: "Search object/file contents",
	Long: `Stream-search object/file contents line by line with a regex, without downloading to disk.
-i ignore case; -v invert (print non-matching lines); -l list only sources with matches; -c print match count; -n show line numbers.
A single source prints bare matching lines; multiple sources prefix each line with "source:line". Exit code 1 when no source matches (GNU grep convention).

Examples:
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
			return fmt.Errorf(i18n.T("invalid regex: %w"), err)
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
							fmt.Printf(i18n.T("%s: binary file matches\n"), arg)
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
			return matched, fmt.Errorf(i18n.T("read failed: %w"), rerr)
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
	grepCmd.Flags().BoolVarP(&grepIgnoreCase, "ignore-case", "i", false, "ignore case")
	grepCmd.Flags().BoolVarP(&grepInvert, "invert-match", "v", false, "invert match (print non-matching lines)")
	grepCmd.Flags().BoolVarP(&grepFilesOnly, "files-with-matches", "l", false, "list only sources with matches")
	grepCmd.Flags().BoolVar(&grepCount, "count", false, "print match count")
	grepCmd.Flags().BoolVarP(&grepLineNo, "line-number", "n", false, "show line numbers")
}
