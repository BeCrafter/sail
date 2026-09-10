package cmd

import (
	"testing"

	"github.com/BeCrafter/sail/internal/s3path"
)

// TestDeriveDstKey 校验 cp/mv 目标 S3 key 推导:
// dst.Key 为空或尾 / 表示"进目录",追加 srcBase;否则用 dst.Key。
func TestDeriveDstKey(t *testing.T) {
	cases := []struct {
		name    string
		srcBase string
		dst     s3path.S3Path
		want    string
	}{
		{name: "dst 空 key 进目录", srcBase: "a.txt", dst: s3path.S3Path{Bucket: "b"}, want: "a.txt"},
		{name: "dst 尾斜杠进目录", srcBase: "a.txt", dst: s3path.S3Path{Bucket: "b", Key: "dir/"}, want: "dir/a.txt"},
		{name: "dst 指定完整 key", srcBase: "a.txt", dst: s3path.S3Path{Bucket: "b", Key: "x/y.txt"}, want: "x/y.txt"},
		{name: "dst 尾斜杠进子目录", srcBase: "sub/b.log", dst: s3path.S3Path{Bucket: "b", Key: "logs/"}, want: "logs/sub/b.log"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deriveDstKey(c.srcBase, &c.dst); got != c.want {
				t.Errorf("deriveDstKey(%q,%v) = %q,期望 %q", c.srcBase, c.dst, got, c.want)
			}
		})
	}
}
