package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BeCrafter/sail/internal/config"
)

// --- 纯函数:输入归一化 ---

func TestNormalizeURL(t *testing.T) {
	cases := map[string]string{
		"s3.example.com":         "https://s3.example.com",
		"s3.example.com:9000":    "https://s3.example.com:9000",
		"https://s3.example.com": "https://s3.example.com",
		"http://127.0.0.1:9000":  "http://127.0.0.1:9000",
		"":                       "",
	}
	for in, want := range cases {
		if got := normalizeURL(in); got != want {
			t.Errorf("normalizeURL(%q) = %q,期望 %q", in, got, want)
		}
	}
}

func TestParseBoolInput(t *testing.T) {
	yes := []string{"y", "Y", "yes", "YES", "true", "True", "1", "on", "是", "对", "好"}
	for _, in := range yes {
		if v, ok := parseBoolInput(in); !ok || !v {
			t.Errorf("parseBoolInput(%q) = %v,%v;期望 true,true", in, v, ok)
		}
	}
	no := []string{"n", "N", "no", "false", "FALSE", "0", "off", "否", "不", "不是"}
	for _, in := range no {
		if v, ok := parseBoolInput(in); !ok || v {
			t.Errorf("parseBoolInput(%q) = %v,%v;期望 false,true", in, v, ok)
		}
	}
	for _, in := range []string{"", "maybe", "2", "y/n"} {
		if _, ok := parseBoolInput(in); ok {
			t.Errorf("parseBoolInput(%q) 应无法识别", in)
		}
	}
}

func TestNormalizeListen(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		changed bool
		wantErr bool
	}{
		{"8443", ":8443", true, false},    // 纯数字补冒号
		{" :8080 ", ":8080", true, false}, // 容忍空格
		{":8443", ":8443", false, false},  // 已规范
		{"127.0.0.1:8443", "127.0.0.1:8443", false, false},
		{"0.0.0.0:8080", "0.0.0.0:8080", false, false},
		{"http://0.0.0.0:8080", "0.0.0.0:8080", true, false}, // 误写 scheme
		{"", "", false, false},
		{"localhost", "", false, true}, // 缺端口 → 报错重问
		{"99999", "", false, true},     // 端口越界
		{"host:0", "", false, true},    // 端口 0
		{"host:abc", "", false, true},  // 端口非数字
	}
	for _, c := range cases {
		got, changed, err := normalizeListen(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("normalizeListen(%q) 期望报错,得到 %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeListen(%q) 意外报错: %v", c.in, err)
			continue
		}
		if got != c.want || changed != c.changed {
			t.Errorf("normalizeListen(%q) = %q,%v;期望 %q,%v", c.in, got, changed, c.want, c.changed)
		}
	}
}

func TestNormalizeUserPrefix(t *testing.T) {
	cases := map[string]string{
		"alice/":  "alice/",
		"alice":   "alice/",
		"/alice":  "alice/",
		"/alice/": "alice/",
		"a/b":     "a/b/",
		"":        "",
		"/":       "",
	}
	for in, want := range cases {
		if got := normalizeUserPrefix(in); got != want {
			t.Errorf("normalizeUserPrefix(%q) = %q,期望 %q", in, got, want)
		}
	}
}

func TestNormalizeQuotaShortUnit(t *testing.T) {
	cases := map[string]string{
		"10G":   "10GB",
		"10g":   "10GB",
		"1.5t":  "1.5TB",
		"500m":  "500MB",
		" 10G ": "10GB",
		"10GB":  "10GB",  // 已规范:不变
		"10GiB": "10GiB", // 不猜二进制单位,交给 ParseQuota 报错
		"10XB":  "10XB",
		"1024":  "1024",
		"":      "",
	}
	for in, want := range cases {
		if got := normalizeQuotaShortUnit(in); got != want {
			t.Errorf("normalizeQuotaShortUnit(%q) = %q,期望 %q", in, got, want)
		}
	}
}

func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	if got := expandTilde("~/certs/c.pem"); got != filepath.Join(home, "certs/c.pem") {
		t.Errorf("expandTilde(~/certs/c.pem) = %q", got)
	}
	if got := expandTilde("~"); got != home {
		t.Errorf("expandTilde(~) = %q", got)
	}
	for _, in := range []string{"", "/abs/path", "rel/path"} {
		if got := expandTilde(in); got != in {
			t.Errorf("expandTilde(%q) = %q,应保持不变", in, got)
		}
	}
}

// --- 向导级:归一化与重问 ---

// listen 输入纯数字与非法端口:前者被补全,后者重问。
func TestCollectServeListenTolerance(t *testing.T) {
	in := []string{
		"y",           // 配置 serve 块
		"8443",        // 纯数字 → :8443
		"",            // prefix
		"n",           // 单用户
		"alice", "pw", // 凭据
		"", "", "", // tls/chunked/staging
	}
	got, err := collectServeConfig(newReader(in...), config.ServeConfig{})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if got.Listen != ":8443" {
		t.Errorf("纯数字端口应补冒号,得到 %q", got.Listen)
	}
}

// 非法 listen(缺端口)重问后接受合法值。
func TestCollectServeListenReprompt(t *testing.T) {
	in := []string{
		"y",
		"localhost", // 缺端口 → 重问
		"9090",      // 纯数字 → :9090
		"",
		"n",
		"alice", "pw",
		"", "", "",
	}
	got, err := collectServeConfig(newReader(in...), config.ServeConfig{})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if got.Listen != ":9090" {
		t.Errorf("重问后应取合法值,得到 %q", got.Listen)
	}
}

// 用户前缀不规范(带首尾斜杠)时被纠正而非报错。
func TestCollectServeUserPrefixNormalized(t *testing.T) {
	in := []string{
		"y", "", "", "y", // 门/多用户
		"alice", "pw", "/alice", "", // 前缀带前导斜杠 → 纠正为 alice/
		"",
		"", "", "",
	}
	got, err := collectServeConfig(newReader(in...), config.ServeConfig{})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(got.Users) != 1 || got.Users[0].Prefix != "alice/" {
		t.Errorf("前缀应被归一化为 alice/: %+v", got.Users)
	}
}

// quota 单字母单位自动补全。
func TestCollectServeQuotaShortUnit(t *testing.T) {
	in := []string{
		"y", "", "", "y",
		"alice", "pw", "alice/", "10G", // 单字母 → 10GB
		"",
		"", "", "",
	}
	got, err := collectServeConfig(newReader(in...), config.ServeConfig{})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(got.Users) != 1 || got.Users[0].Quota != "10GB" {
		t.Errorf("单字母单位应补全为 10GB: %+v", got.Users)
	}
}
