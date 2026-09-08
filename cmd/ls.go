package cmd

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/i18n"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/spf13/cobra"
)

var (
	lsLong     bool
	lsDirsOnly bool
	lsTime     bool
	lsBySize   bool
	lsReverse  bool
	lsHuman    bool
	lsBuckets  bool
)

var lsCmd = &cobra.Command{
	Use:   "ls [s3://bucket/prefix]",
	Short: "List objects or buckets",
	Long: `List objects or buckets. With no arguments, lists the default bucket
(s3://bucket is accepted but pointless);
-l long format (size + last-modified time), combinable with -t to sort by time,
-S by size, -r to reverse, and --human for human-readable sizes;
-d lists sub-directories only (comma-separated prefixes, no files), mirroring ls -d;
--buckets lists all buckets.
Without sorting flags, output is streamed (low memory on large buckets);
with sorting flags, all objects are collected first and then printed.

Examples:
  sail ls s3://bucket/prefix/
  sail ls -l -t --human s3://bucket/
  sail ls -d s3://bucket/prefix/
  sail ls --buckets`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		r, _, err := loadResolved()
		if err != nil {
			return err
		}
		ctx := context.Background()
		s3c, err := client.New(ctx, r)
		if err != nil {
			return err
		}

		if lsBuckets {
			return listBuckets(ctx, s3c)
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
			bucket = p.Bucket
			prefix = p.Key
		}

		if lsDirsOnly {
			return listDirs(ctx, s3c, bucket, prefix)
		}
		if lsTime || lsBySize || lsReverse {
			return listObjectsSorted(ctx, s3c, bucket, prefix, lsLong)
		}
		return listObjects(ctx, s3c, bucket, prefix, lsLong)
	},
}

func listObjects(ctx context.Context, s3c *s3.Client, bucket, prefix string, long bool) error {
	if bucket == "" {
		return errors.New(i18n.T("no bucket specified"))
	}
	paginator := s3.NewListObjectsV2Paginator(s3c, &s3.ListObjectsV2Input{
		Bucket: &bucket,
		Prefix: &prefix,
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf(i18n.T("list failed: %w"), err)
		}
		for _, obj := range page.Contents {
			printObject(obj, bucket, long)
		}
	}
	return nil
}

// listObjectsSorted 全量收集后排序打印。CLI 场景全量进内存可接受;
// 未开启排序 flag 时 listObjects 保持流式路径,避免大桶无谓内存。
func listObjectsSorted(ctx context.Context, s3c *s3.Client, bucket, prefix string, long bool) error {
	if bucket == "" {
		return errors.New(i18n.T("no bucket specified"))
	}
	objs, err := collectAllObjects(ctx, s3c, bucket, prefix)
	if err != nil {
		return err
	}
	sort.Slice(objs, func(i, j int) bool {
		switch {
		case lsTime:
			ti, tj := objTime(objs[i]), objTime(objs[j])
			if !ti.Equal(tj) {
				return ti.After(tj) // 新 → 旧
			}
		case lsBySize:
			si, sj := objSize(objs[i]), objSize(objs[j])
			if si != sj {
				return si > sj // 大 → 小
			}
		default:
			return *objs[i].Key < *objs[j].Key
		}
		return *objs[i].Key < *objs[j].Key
	})
	if lsReverse {
		for i, j := 0, len(objs)-1; i < j; i, j = i+1, j-1 {
			objs[i], objs[j] = objs[j], objs[i]
		}
	}
	for _, obj := range objs {
		printObject(obj, bucket, long)
	}
	return nil
}

// printObject 打印单个对象;long 时带大小与修改时间(-h 人类可读)。
func printObject(obj types.Object, bucket string, long bool) {
	if !long {
		fmt.Println(*obj.Key)
		return
	}
	size := objSize(obj)
	modTime := "-"
	if obj.LastModified != nil {
		modTime = obj.LastModified.Format("2006-01-02 15:04:05")
	}
	if lsHuman {
		fmt.Printf("%12s  %s  s3://%s/%s\n", humanBytes(size), modTime, bucket, *obj.Key)
	} else {
		fmt.Printf("%12d  %s  s3://%s/%s\n", size, modTime, bucket, *obj.Key)
	}
}

func objSize(o types.Object) int64 {
	if o.Size == nil {
		return 0
	}
	return *o.Size
}

func objTime(o types.Object) time.Time {
	if o.LastModified != nil {
		return *o.LastModified
	}
	return time.Time{}
}

func listDirs(ctx context.Context, s3c *s3.Client, bucket, prefix string) error {
	if bucket == "" {
		return errors.New(i18n.T("no bucket specified"))
	}
	// delimiter 模式下,prefix 必须以 "/" 结尾。否则 S3 会把本层所有 key 汇总成
	// 单个 "prefix/" 的 CommonPrefix(如 "pdf/"),去前缀后变成空串,导致无输出。
	// 这里统一补上尾斜杠;空串(列桶根目录)保持不变。
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	paginator := s3.NewListObjectsV2Paginator(s3c, &s3.ListObjectsV2Input{
		Bucket:    &bucket,
		Prefix:    &prefix,
		Delimiter: aws.String("/"),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf(i18n.T("list failed: %w"), err)
		}
		for _, cp := range page.CommonPrefixes {
			if cp.Prefix == nil {
				continue
			}
			name := strings.TrimPrefix(*cp.Prefix, prefix) // 去掉查询前缀
			name = strings.TrimPrefix(name, "/")           // 去可能的残斜杠
			fmt.Println(name)                              // 形如 "subdir/"
		}
	}
	return nil
}

func init() {
	lsCmd.Flags().BoolVarP(&lsLong, "long", "l", false, "show size and last-modified time")
	lsCmd.Flags().BoolVarP(&lsDirsOnly, "dir", "d", false, "list sub-directories only (no files)")
	lsCmd.Flags().BoolVarP(&lsTime, "time", "t", false, "sort by last-modified time (newest first)")
	lsCmd.Flags().BoolVarP(&lsBySize, "size", "S", false, "sort by size (largest first)")
	lsCmd.Flags().BoolVarP(&lsReverse, "reverse", "r", false, "reverse output order")
	lsCmd.Flags().BoolVar(&lsHuman, "human", false, "human-readable sizes (with -l)")
	lsCmd.Flags().BoolVar(&lsBuckets, "buckets", false, "list all buckets (ListBuckets)")
}
