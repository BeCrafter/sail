package cmd

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/i18n"
	"github.com/BeCrafter/sail/internal/s3path"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/spf13/cobra"
)

var (
	findNames    []string
	findSize     string
	findNewer    string
	findMaxDepth int
	findLong     bool
)

var findCmd = &cobra.Command{
	Use:   "find [s3://bucket/prefix]",
	Short: "Find objects by name/size/time",
	Long: `Find objects under a prefix by criteria, printing s3://bucket/key one per line by default.

Filter criteria are combinable (AND):
  --name    filename glob (repeatable, OR-ed together), e.g. '*.log' / 'data_*'
  --size    +1M greater than / -500K less than / 1024 exact; unit B/K/M/G is case-insensitive
  --newer   last-modified time after the given time (2006-01-02 or 2006-01-02 15:04:05)
  --max-depth  maximum depth, 0 means unlimited

Examples:
  sail find s3://bucket/logs --name '*.log' --size +1M -l
  sail find s3://bucket --newer 2026-01-01`,
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

		sizeSpec, err := parseSizeSpec(findSize)
		if err != nil {
			return err
		}
		var newer time.Time
		if findNewer != "" {
			newer, err = parseTimeArg(findNewer)
			if err != nil {
				return err
			}
		}
		if findMaxDepth < 0 {
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
		for _, obj := range objs {
			key := *obj.Key
			if !matchFind(obj, key, base, sizeSpec, newer) {
				continue
			}
			if findLong && obj.Size != nil && obj.LastModified != nil {
				fmt.Printf("%12d  %s  s3://%s/%s\n", *obj.Size, obj.LastModified.Format("2006-01-02 15:04:05"), bucket, key)
			} else {
				fmt.Printf("s3://%s/%s\n", bucket, key)
			}
		}
		return nil
	},
}

// matchFind 依次应用 max-depth/name/size/newer 过滤,全部命中才保留。
func matchFind(obj types.Object, key, base string, sizeSpec []int64, newer time.Time) bool {
	if findMaxDepth > 0 {
		rel := strings.TrimPrefix(strings.TrimPrefix(key, base), "/")
		if strings.Count(rel, "/") >= findMaxDepth {
			return false
		}
	}
	if len(findNames) > 0 {
		name := s3path.BaseName(key)
		matched := false
		for _, pattern := range findNames {
			if ok, _ := path.Match(pattern, name); ok {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if sizeSpec != nil {
		size := int64(0)
		if obj.Size != nil {
			size = *obj.Size
		}
		op, n := sizeSpec[0], sizeSpec[1]
		switch op {
		case '+':
			if !(size > n) {
				return false
			}
		case '-':
			if !(size < n) {
				return false
			}
		default:
			if size != n {
				return false
			}
		}
	}
	if !newer.IsZero() {
		if obj.LastModified == nil || !obj.LastModified.After(newer) {
			return false
		}
	}
	return true
}

// parseSizeSpec 解析 +N/-N/N 大小规格(单位 B/K/M/G 不区分大小写,裸数字=字节)。
// op 编码:'+' '>'、'-' '<'、'=' 精确。空串返回 nil,1 表示未启用。
func parseSizeSpec(s string) ([]int64, error) {
	if s == "" {
		return nil, nil
	}
	op := int64('=')
	if s[0] == '+' || s[0] == '-' {
		op = int64(s[0])
		s = s[1:]
	}
	if len(s) == 0 {
		return nil, fmt.Errorf(i18n.T("invalid size spec: %q"), s)
	}
	mult := int64(1)
	last := s[len(s)-1]
	switch {
	case last >= '0' && last <= '9':
	case last == 'B' || last == 'b':
		s = s[:len(s)-1]
	case last == 'K' || last == 'k':
		mult = 1024
		s = s[:len(s)-1]
	case last == 'M' || last == 'm':
		mult = 1024 * 1024
		s = s[:len(s)-1]
	case last == 'G' || last == 'g':
		mult = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	default:
		return nil, fmt.Errorf(i18n.T("invalid size unit: %q"), string(last))
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("invalid size spec: %q"), s)
	}
	return []int64{op, n * mult}, nil
}

// parseTimeArg 解析 2006-01-02 或 2006-01-02 15:04:05(本地时区)。
func parseTimeArg(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf(i18n.T("cannot parse time %q, expected 2006-01-02 or 2006-01-02 15:04:05"), s)
}

func init() {
	findCmd.Flags().StringSliceVar(&findNames, "name", nil, "filename glob (repeatable, OR-ed together)")
	findCmd.Flags().StringVar(&findSize, "size", "", "filter by size (+1M greater / -500K less / 1024 exact)")
	findCmd.Flags().StringVar(&findNewer, "newer", "", "last-modified time after the given time")
	findCmd.Flags().IntVar(&findMaxDepth, "max-depth", 0, "maximum depth, 0 means unlimited")
	findCmd.Flags().BoolVarP(&findLong, "long", "l", false, "show size and last-modified time")
}
