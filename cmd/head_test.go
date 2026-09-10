package cmd

import (
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout 在 fn 执行期间重定向 os.Stdout,返回其输出。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestHeadLinesFromReader(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int64
		want string
	}{
		{name: "n=0 无输出", in: "a\nb\n", n: 0, want: ""},
		{name: "前 2 行", in: "1\n2\n3\n", n: 2, want: "1\n2\n"},
		{name: "n 超实际行数", in: "1\n2\n", n: 10, want: "1\n2\n"},
		{name: "末行无换行也输出", in: "1\n2", n: 10, want: "1\n2"},
		{name: "空输入", in: "", n: 10, want: ""},
		{name: "恰一行无换行 n=1", in: "solo", n: 1, want: "solo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := captureStdout(t, func() {
				if err := headLinesFromReader(strings.NewReader(c.in), c.n); err != nil {
					t.Errorf("headLinesFromReader 报错: %v", err)
				}
			})
			if got != c.want {
				t.Errorf("headLinesFromReader(%q,%d) = %q,期望 %q", c.in, c.n, got, c.want)
			}
		})
	}
}
