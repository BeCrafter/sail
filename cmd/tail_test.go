package cmd

import (
	"testing"
)

func TestSplitLines(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"a\nb\n", 2},
		{"a\nb", 2},
		{"a\n\n", 2},
		{"", 0},
		{"\n", 1}, // 一个空行
	}
	for _, c := range cases {
		got := splitLines([]byte(c.in))
		if len(got) != c.want {
			t.Errorf("splitLines(%q) = %d 行,期望 %d", c.in, len(got), c.want)
		}
	}
}

func TestTailLinesFromBuffer(t *testing.T) {
	buf := []byte("1\n2\n3\n4\n5\n")
	if got := splitLines(buf); len(got) != 5 {
		t.Fatalf("splitLines = %d 行,期望 5", len(got))
	}
}
