package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
	"github.com/BeCrafter/sail/internal/client"
)

var (
	treeDepth    int
	treeDirsOnly bool
	treeSize     bool
	treeHuman    bool
)

type tentry struct {
	path  string // 相对路径,/ 分隔,无前导 /
	size  int64
	isDir bool
}

type tnode struct {
	name     string
	children map[string]*tnode
	size     int64
	isDir    bool
}

var treeCmd = &cobra.Command{
	Use:   "tree [s3://bucket/prefix/ | 本地路径]",
	Short: "树形查看对象/文件树",
	Long: `树形查看 S3 对象或本地文件树。

Flags:
  -L N            最大深度(0=不限)
  -d              只显目录(不含文件叶子)
  -s              显文件大小(字节数)
      --human     人类可读大小(隐含 -s)

示例:
  sail tree s3://bucket/prefix/
  sail tree -L 2 s3://bucket/prefix/
  sail tree -d -s --human s3://bucket/prefix/
  sail tree ./cmd`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var arg string
		if len(args) > 0 {
			arg = args[0]
		}
		ctx := context.Background()

		var entries []tentry
		var rootLabel string

		switch {
		case arg == "" || strings.HasPrefix(arg, "s3://"):
			r, _, err := loadResolved()
			if err != nil {
				return err
			}
			s3c, err := client.New(ctx, r)
			if err != nil {
				return err
			}
			var bucket, prefix string
			if arg == "" {
				if r.Bucket == "" {
					return fmt.Errorf("未指定 bucket,请用 s3://bucket/prefix 或在配置中设置默认 bucket")
				}
				bucket = r.Bucket
				rootLabel = "s3://" + bucket
			} else {
				p, err := parseS3(arg, r)
				if err != nil {
					return err
				}
				bucket = p.Bucket
				prefix = p.Key
				rootLabel = arg
			}
			if prefix != "" && !strings.HasSuffix(prefix, "/") {
				prefix += "/" // 目录语义:只列该"目录"下
			}
			entries, err = collectS3(ctx, s3c, bucket, prefix)
			if err != nil {
				return err
			}
		default: // 本地目录
			info, err := os.Stat(arg)
			if err != nil {
				return fmt.Errorf("读取本地路径失败: %w", err)
			}
			if !info.IsDir() {
				return fmt.Errorf("%s 不是目录", arg)
			}
			entries, err = collectLocal(arg)
			if err != nil {
				return err
			}
			rootLabel = arg
		}

		printTree(entries, rootLabel)
		return nil
	},
}

// collectS3 递归列举 S3 前缀下全部对象(无 Delimiter),返回相对路径 + 大小。
func collectS3(ctx context.Context, s3c *s3.Client, bucket, prefix string) ([]tentry, error) {
	var entries []tentry
	paginator := s3.NewListObjectsV2Paginator(s3c, &s3.ListObjectsV2Input{
		Bucket: &bucket,
		Prefix: &prefix,
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("列举失败: %w", err)
		}
		for _, obj := range page.Contents {
			key := *obj.Key
			if strings.HasSuffix(key, "/") {
				continue // 跳过目录标记对象
			}
			rel := strings.TrimPrefix(key, prefix)
			rel = strings.TrimPrefix(rel, "/")
			var sz int64
			if obj.Size != nil {
				sz = *obj.Size
			}
			entries = append(entries, tentry{path: rel, size: sz, isDir: false})
		}
	}
	return entries, nil
}

// collectLocal 遍历本地目录,返回相对路径 + 大小 + isDir。
func collectLocal(root string) ([]tentry, error) {
	var entries []tentry
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil // 跳过根本身
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, tentry{path: filepath.ToSlash(rel), size: info.Size(), isDir: info.IsDir()})
		return nil
	})
	return entries, err
}

func buildTree(entries []tentry) *tnode {
	root := &tnode{children: map[string]*tnode{}}
	for _, e := range entries {
		segs := strings.Split(e.path, "/")
		// 过滤空段(尾斜杠)
		var s []string
		for _, x := range segs {
			if x != "" {
				s = append(s, x)
			}
		}
		cur := root
		for i, seg := range s {
			last := i == len(s)-1
			child, ok := cur.children[seg]
			if !ok {
				child = &tnode{name: seg, children: map[string]*tnode{}}
				cur.children[seg] = child
			}
			if last {
				child.isDir = e.isDir
				child.size = e.size
			} else {
				child.isDir = true // 中间段为隐式目录
			}
			cur = child
		}
	}
	return root
}

func printTree(entries []tentry, rootLabel string) {
	fmt.Println(rootLabel)
	root := buildTree(entries)
	kids := visibleKids(root)
	for i, c := range kids {
		printNode(c, "", i == len(kids)-1, 1)
	}
}

func printNode(n *tnode, indent string, isLast bool, depth int) {
	if treeDepth > 0 && depth > treeDepth {
		return
	}
	conn := "├── "
	if isLast {
		conn = "└── "
	}
	line := indent + conn + n.name
	if n.isDir {
		line += "/"
	}
	if !n.isDir && (treeSize || treeHuman) {
		if treeHuman {
			line += "  " + humanBytes(n.size)
		} else {
			line += fmt.Sprintf("  %d", n.size)
		}
	}
	fmt.Println(line)

	childIndent := indent
	if isLast {
		childIndent += "    "
	} else {
		childIndent += "│   "
	}
	kids := visibleKids(n)
	for i, c := range kids {
		printNode(c, childIndent, i == len(kids)-1, depth+1)
	}
}

// visibleKids 返回排序后、按 -d 过滤的可见子节点。
func visibleKids(n *tnode) []*tnode {
	names := make([]string, 0, len(n.children))
	for k := range n.children {
		names = append(names, k)
	}
	sort.Strings(names)
	var kids []*tnode
	for _, name := range names {
		c := n.children[name]
		if treeDirsOnly && !c.isDir {
			continue
		}
		kids = append(kids, c)
	}
	return kids
}

func init() {
	treeCmd.Flags().IntVarP(&treeDepth, "level", "L", 0, "最大深度(0=不限)")
	treeCmd.Flags().BoolVarP(&treeDirsOnly, "dir", "d", false, "只显目录")
	treeCmd.Flags().BoolVarP(&treeSize, "size", "s", false, "显文件大小(字节数)")
	treeCmd.Flags().BoolVar(&treeHuman, "human", false, "人类可读大小(隐含 -s)")
}
