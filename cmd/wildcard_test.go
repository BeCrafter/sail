package cmd

import (
	"testing"
)

func TestHasWildcard(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"a/b*.txt", true},
		{"*.txt", true},
		{"a?b", true},
		{"a/b.txt", false},
		{"a*b/c.txt", true},
	}
	for _, c := range cases {
		if got := hasWildcard(c.in); got != c.want {
			t.Errorf("hasWildcard(%q) = %v,期望 %v", c.in, got, c.want)
		}
	}
}

func TestStaticPrefix(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"a/b*.txt", "a/"},
		{"*.txt", ""},
		{"a/b/c*", "a/b/"},
		{"a/b.txt", "a/b.txt"}, // 无通配符:返回自身
		{"a*b/c.txt", ""},      // 首个通配符前无 /:静态目录前缀为空(全桶列举,客户端过滤)
		{"a/b*c/d.txt", "a/"},
	}
	for _, c := range cases {
		if got := staticPrefix(c.in); got != c.want {
			t.Errorf("staticPrefix(%q) = %q,期望 %q", c.in, got, c.want)
		}
	}
}

func TestGlobToRegexMatches(t *testing.T) {
	cases := []struct {
		pattern string
		key     string
		want    bool
	}{
		{"*.txt", "a/b/c.txt", true}, // * 跨 /,s5cmd 语义
		{"*.txt", "a/b/c.zip", false},
		{"logs/*.log", "logs/app.log", true},
		{"logs/*.log", "logs/sub/app.log", true}, // * 跨 /
		{"a/b?.txt", "a/b1.txt", true},
		{"a/b?.txt", "a/bb.txt", true}, // ? 匹配恰好一个任意字符
		{"a/b?.txt", "a/b.txt", false}, // ? 不能匹配零字符
		{"a[.txt", "a[.txt", true},     // 正则特殊字符按字面
	}
	for _, c := range cases {
		re := globToRegex(c.pattern)
		if got := re.MatchString(c.key); got != c.want {
			t.Errorf("glob %q 匹配 %q = %v,期望 %v", c.pattern, c.key, got, c.want)
		}
	}
}

func TestRelKeyOf(t *testing.T) {
	cases := []struct {
		key, base string
		want      string
	}{
		{"a/b/c.txt", "a/b", "c.txt"},  // pattern a/b/*.txt 展开
		{"a/b/c.txt", "a", "b/c.txt"},  // pattern a/b*.txt 展开
		{"a/b/c.txt", "", "a/b/c.txt"}, // 桶根通配:保留全部层级
		{"a/b1.txt", "a", "b1.txt"},
	}
	for _, c := range cases {
		if got := relKeyOf(c.key, c.base); got != c.want {
			t.Errorf("relKeyOf(%q, %q) = %q,期望 %q", c.key, c.base, got, c.want)
		}
	}
}
