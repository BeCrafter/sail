package cmd

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/BeCrafter/sail/internal/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// hasWildcard 判断 s3 key 是否含通配符(* 或 ?)。
func hasWildcard(s string) bool {
	return strings.ContainsAny(s, "*?")
}

// staticPrefix 取 pattern 中不含通配符的最长前缀(截止到最后一个 /,含该斜杠)。
// 用作 ListObjectsV2 的 Prefix,缩小列举范围。如 "a/b*.txt" → "a/"、"*.txt" → ""。
func staticPrefix(pattern string) string {
	idx := strings.IndexAny(pattern, "*?")
	if idx < 0 {
		return pattern
	}
	last := strings.LastIndex(pattern[:idx], "/")
	if last < 0 {
		return ""
	}
	return pattern[:last+1]
}

// globToRegex 把通配符 pattern 转正则:* 匹配任意字符(跨 /,与 s5cmd 语义一致),? 匹配单字符。
func globToRegex(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for _, ch := range pattern {
		switch ch {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// expandWildcards 展开含通配符的 s3:// 路径为匹配对象列表。
// 基于基础命令组合:ListObjectsV2 静态前缀列举 + 客户端模式匹配。
// 返回(对象列表, 静态前缀去尾斜杠, bucket),供调用方推导对象相对路径。
func expandWildcards(ctx context.Context, s3c *s3.Client, r *config.Resolved, arg string) ([]types.Object, string, string, error) {
	p, err := parseS3(arg, r)
	if err != nil {
		return nil, "", "", err
	}
	if p.Key == "" {
		return nil, "", "", fmt.Errorf("缺少 key,需指定 s3://bucket/prefix")
	}
	objs, err := collectAllObjects(ctx, s3c, p.Bucket, staticPrefix(p.Key))
	if err != nil {
		return nil, "", "", err
	}
	re := globToRegex(p.Key)
	var matched []types.Object
	for _, o := range objs {
		if key := o.Key; key != nil && re.MatchString(*key) {
			matched = append(matched, o)
		}
	}
	if len(matched) == 0 {
		return nil, "", "", fmt.Errorf("无匹配对象: %s", arg)
	}
	return matched, strings.TrimSuffix(staticPrefix(p.Key), "/"), p.Bucket, nil
}

// relKeyOf 计算对象 key 相对展开静态前缀的相对路径(用于保留源层级)。
func relKeyOf(key, staticBase string) string {
	if staticBase == "" {
		return key
	}
	return strings.TrimPrefix(key, staticBase+"/")
}
