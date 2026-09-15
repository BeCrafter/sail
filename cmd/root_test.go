package cmd

import (
	"testing"

	"github.com/BeCrafter/sail/internal/config"
)

func TestParseS3DefaultBucket(t *testing.T) {
	r := &config.Resolved{Bucket: "defbucket"}

	// 空桶段填充默认桶
	p, err := parseS3("s3:///logs/a.txt", r)
	if err != nil {
		t.Fatalf("parseS3 报错: %v", err)
	}
	if p.Bucket != "defbucket" || p.Key != "logs/a.txt" {
		t.Errorf("空桶段应填默认桶,got %+v", p)
	}

	// 显式桶不覆盖
	p, err = parseS3("s3://real/a.txt", r)
	if err != nil {
		t.Fatalf("parseS3 报错: %v", err)
	}
	if p.Bucket != "real" {
		t.Errorf("显式桶被覆盖,got %q", p.Bucket)
	}

	// r 为 nil 且空桶段 -> 报错
	if _, err := parseS3("s3:///a.txt", nil); err == nil {
		t.Errorf("无默认桶时应报错")
	}

	// r.Bucket 为空且空桶段 -> 报错
	if _, err := parseS3("s3:///a.txt", &config.Resolved{}); err == nil {
		t.Errorf("默认桶为空时应报错")
	}
}

func TestLangFlagFromArgs(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--lang", "zh", "ls"}, "zh"},
		{[]string{"--lang=zh"}, "zh"},
		{[]string{"ls", "--lang", "en"}, "en"},
		{[]string{"ls"}, ""},
		{[]string{"--lang"}, ""}, // 缺值
		{[]string{"x", "--lang", "zh", "--help"}, "zh"},
	}
	for _, c := range cases {
		if got := langFlagFromArgs(c.args); got != c.want {
			t.Errorf("langFlagFromArgs(%v) = %q,期望 %q", c.args, got, c.want)
		}
	}
}

func TestConfigFlagFromArgs(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"-c", "/tmp/x.yaml", "ls"}, "/tmp/x.yaml"},
		{[]string{"--config", "/a/b.yaml"}, "/a/b.yaml"},
		{[]string{"--config=/a/b.yaml"}, "/a/b.yaml"},
		{[]string{"ls"}, ""},
		{[]string{"--config"}, ""},
	}
	for _, c := range cases {
		if got := configFlagFromArgs(c.args); got != c.want {
			t.Errorf("configFlagFromArgs(%v) = %q,期望 %q", c.args, got, c.want)
		}
	}
}

// 每个顶层命令都必须显式声明 GroupID:分组在命令定义处声明(见 cmd/*.go),
// 漏声明会掉进 help 的 "Additional Commands" 区。本测试兜住这类回归。
func TestEveryTopLevelCommandHasGroup(t *testing.T) {
	valid := map[string]bool{
		"transfer": true, "list": true, "content": true,
		"verify": true, "server": true, "config": true,
	}
	for _, c := range rootCmd.Commands() {
		if c.Name() == "help" {
			continue
		}
		if c.GroupID == "" {
			t.Errorf("命令 %q 未声明 GroupID", c.Name())
			continue
		}
		if !valid[c.GroupID] {
			t.Errorf("命令 %q 的 GroupID %q 不是已注册分组", c.Name(), c.GroupID)
		}
	}
}

// serve 属于「Server」组,不应混入对象传输组。
func TestServeGroupedUnderServer(t *testing.T) {
	if serveCmd.GroupID != "server" {
		t.Errorf("serve 的 GroupID = %q,期望 server", serveCmd.GroupID)
	}
}
