package cmd

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/BeCrafter/sail/internal/client"
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
	Short: "按名称/大小/时间查找对象",
	Long: `按条件查找前缀下的对象,默认打印 s3://bucket/key 每行一个。

过滤条件可组合(AND 关系):
  --name    文件名通配符(可重复,多个之间 OR),如 '*.log' / 'data_*'
  --size    +1M 大于 / -500K 小于 / 1024 精确;单位 B/K/M/G 不区分大小写
  --newer   修改时间晚于指定时刻(2006-01-02 或 2006-01-02 15:04:05)
  --max-depth  最大层级深度,0 表示不限

示例:
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
				return fmt.Errorf("未指定 bucket,请用 s3://bucket/prefix 或在配置中设置默认 bucket")
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
			return fmt.Errorf("--max-depth 不能为负数")
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
		return nil, fmt.Errorf("无效的大小规格: %q", s)
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
		return nil, fmt.Errorf("无效的大小单位: %q", string(last))
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("无效的大小规格: %q", s)
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
	return time.Time{}, fmt.Errorf("无法解析时间 %q,格式为 2006-01-02 或 2006-01-02 15:04:05", s)
}

func init() {
	findCmd.Flags().StringSliceVar(&findNames, "name", nil, "文件名通配符(可重复,多个之间 OR)")
	findCmd.Flags().StringVar(&findSize, "size", "", "按大小过滤(+1M 大于 / -500K 小于 / 1024 精确)")
	findCmd.Flags().StringVar(&findNewer, "newer", "", "修改时间晚于指定时刻")
	findCmd.Flags().IntVar(&findMaxDepth, "max-depth", 0, "最大层级深度,0 表示不限")
	findCmd.Flags().BoolVarP(&findLong, "long", "l", false, "显示大小和修改时间")
}
