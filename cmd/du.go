package cmd

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/i18n"
	"github.com/spf13/cobra"
)

var (
	duSummarize bool
	duHuman     bool
	duMaxDepth  int
)

var duCmd = &cobra.Command{
	Use:   "du [s3://bucket/prefix]",
	Short: "Summarize object size under a prefix",
	Long: `Sum the size of objects under a prefix, broken down by directory level
(each level is cumulative; the root is the grand total).
With no arguments, sums the default bucket; --max-depth limits the printed levels;
-s prints only the total.

Examples:
  sail du -h s3://bucket/logs
  sail du -h --max-depth 1 s3://bucket`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		r, _, err := loadResolved()
		if err != nil {
			return err
		}
		var bucket, prefix string
		if len(args) == 0 {
			if r.Bucket == "" {
				return errors.New(i18n.T("no bucket specified; use s3://bucket/prefix or set a default bucket in config"))
			}
			bucket = r.Bucket
		} else {
			p, err := parseS3(args[0], r)
			if err != nil {
				return err
			}
			bucket, prefix = p.Bucket, p.Key
		}
		if duMaxDepth < 0 {
			return errors.New(i18n.T("--max-depth cannot be negative"))
		}

		ctx := context.Background()
		s3c, err := client.New(ctx, r)
		if err != nil {
			return err
		}
		objs, err := collectAllObjects(ctx, s3c, bucket, prefix)
		if err != nil {
			return err
		}

		base := strings.TrimSuffix(prefix, "/")
		sizes := map[string]int64{"": 0}
		for _, obj := range objs {
			key := *obj.Key
			// 跳过目录占位对象(0 字节、尾 /),避免虚增占用
			if strings.HasSuffix(key, "/") && (obj.Size == nil || *obj.Size == 0) {
				continue
			}
			size := int64(0)
			if obj.Size != nil {
				size = *obj.Size
			}
			rel := strings.TrimPrefix(strings.TrimPrefix(key, base), "/")
			for i := 0; i < len(rel); i++ {
				if rel[i] == '/' {
					sizes[rel[:i+1]] += size
				}
			}
			sizes[""] += size
		}

		total := displayRoot(bucket, base)
		if duSummarize {
			printDULine(sizes[""], total)
			return nil
		}
		// 各层级按字典序,根总计行最后
		var prefixes []string
		for pfx := range sizes {
			if pfx == "" {
				continue
			}
			if duMaxDepth > 0 && strings.Count(pfx, "/") > duMaxDepth {
				continue
			}
			prefixes = append(prefixes, pfx)
		}
		sort.Strings(prefixes)
		for _, pfx := range prefixes {
			display := pfx
			if base != "" {
				display = base + "/" + pfx
			}
			printDULine(sizes[pfx], display)
		}
		printDULine(sizes[""], total)
		return nil
	},
}

// displayRoot 根总计行的展示路径。
func displayRoot(bucket, base string) string {
	if base == "" {
		return "s3://" + bucket
	}
	return "s3://" + bucket + "/" + base
}

func printDULine(size int64, display string) {
	if duHuman {
		fmt.Printf("%s\t%s\n", humanBytes(size), display)
	} else {
		fmt.Printf("%d\t%s\n", size, display)
	}
}

func init() {
	duCmd.Flags().BoolVarP(&duSummarize, "summarize", "s", false, "print only the total")
	duCmd.Flags().BoolVar(&duHuman, "human", false, "human-readable sizes")
	duCmd.Flags().IntVar(&duMaxDepth, "max-depth", 0, "maximum print depth, 0 means unlimited")
}
