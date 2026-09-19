package cmd

import (
	"testing"

	"github.com/BeCrafter/sail/internal/s3path"
	"github.com/spf13/pflag"
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

// --content-type 是本地→S3 上传的显式覆盖旋钮:cp 与 sync 都提供,默认空
// (自动判定),且共用同一段帮助文本以免在 i18n 表里漂移成两个 key。
func TestContentTypeFlagSurface(t *testing.T) {
	for _, c := range []struct {
		name string
		flag *pflag.Flag
	}{
		{"cp", cpCmd.Flags().Lookup("content-type")},
		{"sync", syncCmd.Flags().Lookup("content-type")},
	} {
		if c.flag == nil {
			t.Errorf("%s 缺少参数 --content-type", c.name)
			continue
		}
		if c.flag.DefValue != "" {
			t.Errorf("%s --content-type 默认值应为空(自动判定),实际 %q", c.name, c.flag.DefValue)
		}
		if c.flag.Usage != contentTypeFlagUsage {
			t.Errorf("%s 的帮助文本 = %q,期望与 contentTypeFlagUsage 一致", c.name, c.flag.Usage)
		}
	}
}
