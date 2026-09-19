package cmd

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/fakes3"
	"github.com/BeCrafter/sail/internal/i18n"
	"github.com/BeCrafter/sail/internal/vfs/s3fs"
	"github.com/BeCrafter/sail/internal/webdavfs"
)

// newServeHTTPServer 必须设 DisableGeneralOptionsHandler:否则 net/http 会用内置的
// globalOptionsHandler 截胡 `OPTIONS *`,不回 DAV 头,macOS Finder 判定为非 WebDAV
// 而卡在「连接中」。本测试起真实 http.Server(httptest 不走此分支),发 `OPTIONS *`,
// 断言响应带 DAV 头 —— 这正是修复前会失败的点。
func TestServeHTTPServerAnswersOptionsStarWithDAVHeader(t *testing.T) {
	s3srv := fakes3.New()
	t.Cleanup(s3srv.Close)
	s3c, err := client.New(context.Background(), &config.Resolved{
		Endpoint: s3srv.URL(), AccessKey: "ak", SecretKey: "sk", Region: "us-east-1", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("构造 S3 client 失败: %v", err)
	}
	core, err := s3fs.New(s3fs.Config{Client: s3c, Bucket: "b", StagingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("构造 s3fs 失败: %v", err)
	}
	gw, err := webdavfs.NewServer(webdavfs.Config{
		FileSystem: webdavfs.New(core),
		User:       "admin",
		Password:   "123",
	})
	if err != nil {
		t.Fatalf("构造网关失败: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	srv := newServeHTTPServer(ln.Addr().String(), gw)
	go srv.Serve(ln)
	t.Cleanup(func() { _ = srv.Close() })

	// 用底层 TCP 发 `OPTIONS *`:net/http 的 client 不允许直接设 RequestURI,
	// 只有原始握手才能确保请求行确实是 `OPTIONS * HTTP/1.1`。
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("OPTIONS * HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatalf("写请求失败: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "OPTIONS"})
	if err != nil {
		t.Fatalf("读响应失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("OPTIONS * 期望 200,实际 %d", resp.StatusCode)
	}
	if dav := resp.Header.Get("DAV"); dav == "" {
		t.Fatal("OPTIONS * 必须带 DAV 头(Finder 靠它判定 WebDAV);" +
			"缺少说明 http.Server 的 globalOptionsHandler 未被禁用")
	}
}

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
	// 配置文件解析先于 flag 校验,故这里注入一份合法配置(含凭据但无 bucket),否则先撞上
	// 「读不到配置」或「缺 user/password」。
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

	// user/password/tls 校验依赖配置合并结果:注入合法配置(不提供 user/password),
	// 断言报错同时提及 flag 与 serve 配置两条来源。
	writeServeConfig(t, "mybucket")
	serveWebdavOpts = serveWebdavFlags{backendMaxSize: "5TiB", listen: ":8080", user: "", password: ""}
	credErr := runServeWebdav(serveWebdavCmd, nil)
	if credErr == nil || !strings.Contains(credErr.Error(), "--user") {
		t.Fatalf("缺少密码应报错,实际 %v", credErr)
	}
	if !strings.Contains(credErr.Error(), "serve") {
		t.Errorf("缺 user/password 报错应提及 serve 配置来源,实际 %v", credErr)
	}

	serveWebdavOpts.user = "alice"
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

func TestPick(t *testing.T) {
	cases := []struct {
		name    string
		changed bool
		flagVal string
		cfgVal  string
		want    string
	}{
		{"flag 显式设置覆盖配置", true, "flag", "cfg", "flag"},
		{"未设置 flag 取配置", false, ":8080", ":8443", ":8443"},
		{"配置为空落回 flag 默认", false, ":8080", "", ":8080"},
		{"flag 显式设置且配置为空取 flag", true, "x", "", "x"},
	}
	for _, c := range cases {
		if got := pick(c.changed, c.flagVal, c.cfgVal); got != c.want {
			t.Errorf("%s: pick(%v,%q,%q) = %q,期望 %q", c.name, c.changed, c.flagVal, c.cfgVal, got, c.want)
		}
	}
}

// TestPickList 覆盖 prewarm 的「flag > 配置 > flag 默认」合并。
func TestPickList(t *testing.T) {
	cases := []struct {
		name    string
		changed bool
		flagVal []string
		cfgVal  []string
		want    []string
	}{
		{"flag 显式设置覆盖配置", true, []string{"/flag"}, []string{"/cfg"}, []string{"/flag"}},
		{"未设置 flag 取配置", false, nil, []string{"/cfg"}, []string{"/cfg"}},
		{"配置为空落回 flag 默认(nil)", false, nil, nil, nil},
		{"flag 显式设置且配置为空取 flag", true, []string{"/x"}, nil, []string{"/x"}},
	}
	for _, c := range cases {
		got := pickList(c.changed, c.flagVal, c.cfgVal)
		if len(got) != len(c.want) {
			t.Errorf("%s: pickList(%v,%v,%v) = %v,期望 %v", c.name, c.changed, c.flagVal, c.cfgVal, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: pickList(%v,%v,%v) = %v,期望 %v", c.name, c.changed, c.flagVal, c.cfgVal, got, c.want)
				break
			}
		}
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

// writeServeConfigWithServe 写一份含 serve 块(serveBody 为缩进后的键值行)的配置,
// 并把全局 flag 指向它。用于验证 serve 参数能从配置读取。
func writeServeConfigWithServe(t *testing.T, bucket, serveBody string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "default-profile: prod\nprofiles:\n  prod:\n    endpoint: http://127.0.0.1:9000\n" +
		"    access-key: ak\n    secret-key: sk\n    bucket: " + bucket + "\n" +
		"    region: us-east-1\n    path-style: true\n" +
		"    serve:\n" + serveBody
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	origPath, origProfile, origBucket := cfgPath, profile, cfgBucket
	t.Cleanup(func() { cfgPath, profile, cfgBucket = origPath, origProfile, origBucket })
	cfgPath, profile, cfgBucket = path, "prod", ""
}

// TestServeWebdavConfigProvidesCredentials 校验 user/password 可由配置提供:
// 配置给全凭据但不给桶时,应通过凭据校验、卡在缺桶(证明配置来源的凭据被接受)。
func TestServeWebdavConfigProvidesCredentials(t *testing.T) {
	origBucket, origOpts := cfgBucket, serveWebdavOpts
	t.Cleanup(func() { cfgBucket, serveWebdavOpts = origBucket, origOpts })
	cfgBucket = ""

	writeServeConfigWithServe(t, "",
		"      user: alice\n      password: s3cret\n")
	serveWebdavOpts = serveWebdavFlags{backendMaxSize: "5TiB", listen: ":8080"}
	if err := runServeWebdav(serveWebdavCmd, nil); err == nil || !strings.Contains(err.Error(), "bucket") {
		t.Fatalf("配置提供凭据但无桶,应卡在缺桶,实际 %v", err)
	}

	// 配置提供非法 backend-max-object-size:应被拒绝(证明大小解析走合并值)。
	writeServeConfigWithServe(t, "mybucket",
		"      user: alice\n      password: s3cret\n      backend-max-object-size: 不是大小\n")
	serveWebdavOpts = serveWebdavFlags{backendMaxSize: "5TiB", listen: ":8080"}
	if err := runServeWebdav(serveWebdavCmd, nil); err == nil || !strings.Contains(err.Error(), "backend-max-object-size") {
		t.Fatalf("配置提供非法上限应被拒绝,实际 %v", err)
	}
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
		{"仅 profile 提供桶", "mybucket", "", "", "mybucket", "prod"},
		{"SAIL_BUCKET 覆盖 profile", "mybucket", "env-bucket", "", "env-bucket", "prod"},
		{"--bucket 覆盖 env 与 profile", "mybucket", "env-bucket", "other", "other", "prod"},
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
		"https", ":8443", "mybucket", "prod", exposePrefix("tenant-a"), "alice", humanSize(5<<40), stagingDirOf(""), chunkedText(false, 0))
	for _, want := range []string{"bucket=mybucket", "profile=prod", "prefix=tenant-a"} {
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

// serveURLs 必须把通配绑定展开成可挂载的地址:localhost(本机)+ 局域网 IP(其它设备),
// 而不是原样吐出 ":8080" / "0.0.0.0:8080"。
func TestServeURLs(t *testing.T) {
	// 通配绑定:应包含 localhost,且不含 0.0.0.0/:: 这类不可连地址。
	for _, listen := range []string{":8080", "0.0.0.0:8443"} {
		urls := serveURLs("https", listen)
		if len(urls) == 0 {
			t.Fatalf("serveURLs(%q) 不应为空", listen)
		}
		if urls[0] != "https://localhost:"+strings.SplitN(listen, ":", 2)[1]+"/" {
			t.Errorf("serveURLs(%q) 首条应为 localhost,实际 %q", listen, urls[0])
		}
		for _, u := range urls {
			if strings.Contains(u, "0.0.0.0") || strings.Contains(u, "[::]") {
				t.Errorf("serveURLs(%q) 含不可连的通配地址: %q", listen, u)
			}
			if !strings.HasSuffix(u, "/") {
				t.Errorf("serveURLs(%q) 生成的 %q 不是以 / 结尾的挂载 URL", listen, u)
			}
		}
	}

	// 显式绑定具体主机:只暴露它,不额外猜测其它地址。
	urls := serveURLs("http", "127.0.0.1:8080")
	if len(urls) != 1 || urls[0] != "http://127.0.0.1:8080/" {
		t.Errorf("显式 host 应只给该地址,实际 %v", urls)
	}
	// IPv6 字面量要加方括号。
	urls6 := serveURLs("http", "[::1]:8080")
	if len(urls6) != 1 || urls6[0] != "http://[::1]:8080/" {
		t.Errorf("IPv6 应加方括号,实际 %v", urls6)
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
	r := &config.Resolved{ProfileName: "prod"}
	// changed 显式置位:模拟用户显式给了 --chunked-upload 与 --chunk-size。
	changed := map[string]bool{"chunked-upload": true, "chunk-size": true}
	o := serveWebdavFlags{
		backendMaxSize: "5TiB",
		listen:         ":8080",
		user:           "alice",
		password:       "s3cret",
		chunkedUpload:  true,
		chunkSize:      "1MiB", // 低于 S3 multipart 下限
	}
	if _, err := mergeServe(o, r, func(f string) bool { return changed[f] }); err == nil || !strings.Contains(err.Error(), "--chunk-size") {
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
