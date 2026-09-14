package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeCrafter/sail/internal/i18n"
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
	origBucket, origOpts, origPath, origProfile := cfgBucket, serveWebdavOpts, cfgPath, profile
	t.Cleanup(func() {
		cfgBucket, serveWebdavOpts, cfgPath, profile = origBucket, origOpts, origPath, origProfile
	})

	// 缺桶分支:三条来源(--bucket / SAIL_BUCKET / profile.bucket)都没有桶才拒绝启动。
	// 配置文件解析在 flag 校验之后,故这里必须注入一份合法配置,否则报错会变成
	// 「读不到配置」而不是「没有桶」;凭据也要给全,否则先撞上 --user/--password 校验。
	writeServeConfig(t, "")
	t.Setenv("SAIL_BUCKET", "")
	serveWebdavOpts = serveWebdavFlags{backendMaxSize: "5TiB", listen: ":8080", user: "alice", password: "s3cret"}
	cfgBucket = ""
	err := runServeWebdav(serveWebdavCmd, nil)
	if err == nil {
		t.Fatal("缺少 bucket 应报错")
	}
	for _, want := range []string{"--bucket", "SAIL_BUCKET", "bucket"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("缺桶错误未提到 %q:%v", want, err)
		}
	}

	// 以下 flag 校验一律先于配置解析:配置刻意指向不存在的路径,若顺序被改动,
	// 报错会退化成「读配置失败」而被这些断言抓住。
	cfgPath = filepath.Join(t.TempDir(), "no-such-config.yaml")
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

// writeServeConfig 写一份最小可用配置(含凭据与所需 bucket),并把全局 flag 指向它。
// bucket 传空串即写出「profile 未配置 bucket」的合法配置 —— config.Resolve 不校验 Bucket 非空。
func writeServeConfig(t *testing.T, bucket string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "default-profile: prod\nprofiles:\n  prod:\n    endpoint: http://127.0.0.1:9000\n" +
		"    access-key: ak\n    secret-key: sk\n    bucket: " + bucket + "\n" +
		"    region: us-east-1\n    path-style: true\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	origPath, origProfile, origBucket := cfgPath, profile, cfgBucket
	t.Cleanup(func() { cfgPath, profile, cfgBucket = origPath, origProfile, origBucket })
	cfgPath, profile, cfgBucket = path, "prod", ""
}

// 默认桶的唯一解析出口是 loadResolved() 的 r.Bucket,优先级 --bucket > SAIL_BUCKET > profile.bucket。
func TestLoadResolvedBucketPriorityChain(t *testing.T) {
	cases := []struct {
		name        string
		profileBkt  string
		envBucket   string
		flagBucket  string
		wantBucket  string
		wantProfile string
	}{
		{"仅 profile 提供桶", "tizzy", "", "", "tizzy", "prod"},
		{"SAIL_BUCKET 覆盖 profile", "tizzy", "env-bucket", "", "env-bucket", "prod"},
		{"--bucket 覆盖 env 与 profile", "tizzy", "env-bucket", "other", "other", "prod"},
		{"profile 无桶且无 flag/env", "", "", "", "", "prod"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeServeConfig(t, c.profileBkt)
			t.Setenv("SAIL_BUCKET", c.envBucket)
			cfgBucket = c.flagBucket

			r, _, err := loadResolved()
			if err != nil {
				t.Fatalf("loadResolved 失败: %v", err)
			}
			if r.Bucket != c.wantBucket {
				t.Errorf("bucket = %q,期望 %q", r.Bucket, c.wantBucket)
			}
			if r.ProfileName != c.wantProfile {
				t.Errorf("profile = %q,期望 %q", r.ProfileName, c.wantProfile)
			}
		})
	}
}

// 启动横幅是绑定后唯一的「我到底暴露了什么」断言面:bucket / profile / prefix 三件事都必须在。
func TestServeBannerExposesBucketProfilePrefix(t *testing.T) {
	line := i18n.Tf(
		"sail webdav started: %s://%s  bucket=%s profile=%s%s user=%s max-object-size=%s staging=%s chunked=%s\n",
		"https", ":8443", "tizzy", "prod", exposePrefix("tenant-a"), "alice", humanSize(5<<40), stagingDirOf(""), chunkedText(false, 0))
	for _, want := range []string{"bucket=tizzy", "profile=prod", "prefix=tenant-a"} {
		if !strings.Contains(line, want) {
			t.Errorf("启动横幅缺少 %q:%s", want, line)
		}
	}
	// 未设前缀时整段省略,不出现具有误导性的 `prefix=""`。
	if line := exposePrefix(""); line != "" {
		t.Errorf("空前缀应省略整段,实际 %q", line)
	}
	if line := exposePrefix("/tenant-a/"); line != " prefix=tenant-a" {
		t.Errorf("前缀应归一化渲染,实际 %q", line)
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
