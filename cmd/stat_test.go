package cmd

import "testing"

// TestHumanBytes 覆盖 stat/ls/du/tree 共用的字节格式化(注意与 internal 包内
// 同名函数是各自独立实现;此处针对 cmd/humanBytes)。
func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{5 * 1024 * 1024, "5.0 MiB"},
		{2 << 30, "2.0 GiB"},
		{1024 * 1024 * 1024 * 1024, "1.0 TiB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d) = %q,期望 %q", c.in, got, c.want)
		}
	}
}
