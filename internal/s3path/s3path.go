package s3path

import (
	"fmt"
	"strings"
)

// S3Path 表示一个 s3://bucket/key 路径
type S3Path struct {
	Bucket string
	Key    string
}

// Parse 解析 s3://bucket/key 形式的路径。
// 允许只有 s3://bucket (Key 为空,用于 ls 桶根)。
func Parse(s string) (*S3Path, error) {
	if s == "" {
		return nil, fmt.Errorf("路径为空")
	}
	if !strings.HasPrefix(s, "s3://") {
		return nil, fmt.Errorf("路径 %q 必须以 s3:// 开头", s)
	}
	rest := strings.TrimPrefix(s, "s3://")
	if rest == "" {
		return nil, fmt.Errorf("缺少 bucket")
	}
	idx := strings.Index(rest, "/")
	if idx < 0 {
		return &S3Path{Bucket: rest, Key: ""}, nil
	}
	bucket := rest[:idx]
	key := rest[idx+1:]
	return &S3Path{Bucket: bucket, Key: key}, nil
}

// Format 拼回 s3://bucket/key
func (p *S3Path) Format() string {
	if p.Key == "" {
		return "s3://" + p.Bucket
	}
	return "s3://" + p.Bucket + "/" + p.Key
}

// BaseName 返回 key 末段(最后一个 / 之后的部分),用于从 S3 key 派生文件名。
func BaseName(key string) string {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '/' {
			return key[i+1:]
		}
	}
	return key
}

// JoinKey 在已有 key 基础上追加子路径
func JoinKey(base, name string) string {
	if base == "" {
		return name
	}
	if strings.HasSuffix(base, "/") {
		return base + name
	}
	return base + "/" + name
}
