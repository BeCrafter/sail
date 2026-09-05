package cmd

import (
	"testing"
)

func TestMatchPatterns(t *testing.T) {
	cases := []struct {
		patterns []string
		rel      string
		want     bool
	}{
		{nil, "a/b.log", true},                 // 空 = 全部命中
		{[]string{"*.tmp"}, "a/b.tmp", true},   // basename 命中
		{[]string{"*.tmp"}, "a/b.log", false},  // 不命中
		{[]string{"cache/"}, "cache/x", true},  // 目录模式命中
		{[]string{"cache/"}, "cache", false},   // 目录模式只排目录下的条目
		{[]string{"a/*.log"}, "a/b.log", true}, // 相对路径命中
		{[]string{"a/*.log"}, "a/b.log", true},
	}
	for _, c := range cases {
		if got := matchPatterns(c.patterns, c.rel); got != c.want {
			t.Errorf("matchPatterns(%v, %q) = %v,期望 %v", c.patterns, c.rel, got, c.want)
		}
	}
}

func TestIsVisible(t *testing.T) {
	// 双向无过滤:全部可见
	syncInclude, syncExclude = nil, nil
	if !isVisible("a.txt") {
		t.Errorf("无过滤时 a.txt 应可见")
	}
	syncInclude = []string{"*.txt"}
	syncExclude = []string{"*.tmp"}
	defer func() {
		syncInclude, syncExclude = nil, nil
	}()
	cases := []struct {
		rel  string
		want bool
	}{
		{"a.txt", true},      // include 命中且未 exclude
		{"a.json", false},    // include 未命中
		{"b.txt.tmp", false}, // exclude 命中(exclude 优先)
		{"sub/a.txt", true},  // include 按 basename 命中
	}
	for _, c := range cases {
		if got := isVisible(c.rel); got != c.want {
			t.Errorf("isVisible(%q) = %v,期望 %v", c.rel, got, c.want)
		}
	}
}

func TestIsPlainETag(t *testing.T) {
	cases := []struct {
		etag string
		want bool
	}{
		{"df31ab9d4881a1a91ab8be84e6186d6a", true}, // 单分片 md5
		{"DF31AB9D4881A1A91AB8BE84E6186D6A", true}, // 大写也合法
		{"abc-123", false},                         // 分片复合 ETag
		{"", false},
		{"nothex", false},
	}
	for _, c := range cases {
		if got := isPlainETag(c.etag); got != c.want {
			t.Errorf("isPlainETag(%q) = %v,期望 %v", c.etag, got, c.want)
		}
	}
}

func TestPrefixOverlap(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"a", "a", true},
		{"", "", true},
		{"", "x", true},
		{"a", "a/b", true}, // a 是 a/b 的父
		{"a/b", "a", true}, // 反向同样重叠
		{"a/b", "a/c", false},
		{"a/b", "ab", false},
	}
	for _, c := range cases {
		if got := prefixOverlap(c.a, c.b); got != c.want {
			t.Errorf("prefixOverlap(%q,%q) = %v,期望 %v", c.a, c.b, got, c.want)
		}
	}
}
