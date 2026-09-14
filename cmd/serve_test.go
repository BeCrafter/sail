package cmd

import (
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"5TiB", 5 << 40, false},
		{"1GiB", 1 << 30, false},
		{"500MB", 500 * 1000 * 1000, false},
		{"1024", 1024, false},
		{"0", 0, false},
		{"5tib", 5 << 40, false},
		{" 2 GiB ", 2 << 30, false},
		{"", 0, true},
		{"GiB", 0, true},
		{"5XB", 0, true},
	}
	for _, c := range cases {
		got, err := parseSize(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseSize(%q) 期望报错,实际 %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSize(%q) 失败: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseSize(%q) = %d,期望 %d", c.in, got, c.want)
		}
	}
}

func TestHumanSize(t *testing.T) {
	if got := humanSize(5 << 40); got != "5.0 TiB" {
		t.Errorf("humanSize(5TiB) = %q", got)
	}
	if got := humanSize(512); got != "512 B" {
		t.Errorf("humanSize(512) = %q", got)
	}
}

func TestServeWebdavRequiresBucketAndCredentials(t *testing.T) {
	origBucket, origOpts := cfgBucket, serveWebdavOpts
	t.Cleanup(func() {
		cfgBucket, serveWebdavOpts = origBucket, origOpts
	})

	cfgBucket = ""
	serveWebdavOpts = serveWebdavFlags{backendMaxSize: "5TiB", listen: ":8080"}
	if err := runServeWebdav(serveWebdavCmd, nil); err == nil || !strings.Contains(err.Error(), "--bucket") {
		t.Fatalf("缺少 bucket 应报错,实际 %v", err)
	}

	cfgBucket = "mybucket"
	serveWebdavOpts.user = "alice"
	serveWebdavOpts.password = ""
	if err := runServeWebdav(serveWebdavCmd, nil); err == nil || !strings.Contains(err.Error(), "--user") {
		t.Fatalf("缺少密码应报错,实际 %v", err)
	}

	serveWebdavOpts.password = "s3cret"
	serveWebdavOpts.tlsCert = "c.pem"
	serveWebdavOpts.tlsKey = ""
	if err := runServeWebdav(serveWebdavCmd, nil); err == nil || !strings.Contains(err.Error(), "--tls-key") {
		t.Fatalf("只给证书应报错,实际 %v", err)
	}

	serveWebdavOpts.tlsCert = ""
	serveWebdavOpts.backendMaxSize = "不是大小"
	if err := runServeWebdav(serveWebdavCmd, nil); err == nil || !strings.Contains(err.Error(), "backend-max-object-size") {
		t.Fatalf("非法上限应报错,实际 %v", err)
	}
}

func TestWindowsSetupText(t *testing.T) {
	text := windowsSetupText(":8443")
	for _, want := range []string{
		"FileSizeLimitInBytes",
		"BasicAuthLevel",
		"Restart-Service WebClient",
		"@SSL@8443",
		"DavWWWRoot",
		"net use Z:",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("客户端配置文案缺少 %q", want)
		}
	}
	// 绝不提供"看着能抬高客户端 50MB 闸门"的服务端参数说明。
	if strings.Contains(text, "--max-file-size") {
		t.Error("文案不应出现 --max-file-size 这类假旋钮")
	}
}

func TestParseChunkSize(t *testing.T) {
	const mib = 1 << 20
	const gib = 1 << 30
	cases := []struct {
		name       string
		enabled    bool
		raw        string
		backendMax int64
		want       int64
		wantErr    bool
	}{
		{"分片关闭时不校验", false, "1", 5 << 40, 0, false},
		{"默认 4GiB", true, "4GiB", 5 << 40, 4 * gib, false},
		{"合法下限", true, "5MiB", 5 << 40, 5 * mib, false},
		{"低于 5MiB 拒绝", true, "1MiB", 5 << 40, 0, true},
		{"超过 5GiB 拒绝", true, "6GiB", 5 << 40, 0, true},
		{"超过声明后端上限拒绝", true, "4GiB", 1 * gib, 0, true},
		{"非法字面量拒绝", true, "不是大小", 5 << 40, 0, true},
	}
	for _, c := range cases {
		got, err := parseChunkSize(c.enabled, c.raw, c.backendMax)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: 期望报错,实际 %d", c.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: 意外报错 %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}

func TestServeWebdavRejectsBadChunkSize(t *testing.T) {
	origBucket, origOpts := cfgBucket, serveWebdavOpts
	t.Cleanup(func() {
		cfgBucket, serveWebdavOpts = origBucket, origOpts
	})
	cfgBucket = "mybucket"
	serveWebdavOpts = serveWebdavFlags{
		backendMaxSize: "5TiB",
		listen:         ":8080",
		user:           "alice",
		password:       "s3cret",
		chunkedUpload:  true,
		chunkSize:      "1MiB", // 低于 S3 multipart 下限
	}
	if err := runServeWebdav(serveWebdavCmd, nil); err == nil || !strings.Contains(err.Error(), "--chunk-size") {
		t.Fatalf("非法 --chunk-size 应拒绝启动,实际 %v", err)
	}
}

// 命名纪律:绝不提供看着能抬高 Windows 客户端 50MB 闸门的假旋钮,
// 也不在功能落地前先暴露 P2 之外的参数。
func TestServeWebdavFlagSurface(t *testing.T) {
	for _, name := range []string{"chunked-upload", "chunk-size"} {
		if serveWebdavCmd.Flags().Lookup(name) == nil {
			t.Errorf("缺少参数 --%s", name)
		}
	}
	if f := serveWebdavCmd.Flags().Lookup("max-file-size"); f != nil {
		t.Error("不应提供 --max-file-size(服务端假旋钮)")
	}
}
