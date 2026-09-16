package webdavfs_test

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/fakes3"
	"github.com/BeCrafter/sail/internal/vfs/s3fs"
	"github.com/BeCrafter/sail/internal/webdavfs"
)

// multiGateway 是多用户网关的测试外壳:fakes3 后端 + 每用户一个 s3fs 核 +
// 查表认证的 HTTP 网关,附并发安全的日志缓冲供审计断言。
type multiGateway struct {
	ts *httptest.Server
	s3 *fakes3.Server
	gw *webdavfs.Server
	mu sync.Mutex
	lg bytes.Buffer
}

func newMultiGateway(t *testing.T, prefix string, users map[string]webdavfs.UserEntry) *multiGateway {
	t.Helper()
	s3srv := fakes3.New()
	t.Cleanup(s3srv.Close)
	s3c, err := client.New(context.Background(), &config.Resolved{
		Endpoint: s3srv.URL(), AccessKey: "ak", SecretKey: "sk",
		Region: "us-east-1", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("构造 S3 client 失败: %v", err)
	}
	entries := make([]webdavfs.UserEntry, 0, len(users))
	for name, e := range users {
		if e.FileSystem == nil {
			core, err := s3fs.New(s3fs.Config{
				Client: s3c, Bucket: bucket,
				Prefix:     strings.TrimPrefix(prefix+"/"+name, "/"),
				StagingDir: t.TempDir(),
			})
			if err != nil {
				t.Fatalf("构造用户 %s 的 s3fs 失败: %v", name, err)
			}
			e.FileSystem = webdavfs.NewWithListingCache(core, time.Minute)
		}
		entries = append(entries, webdavfs.UserEntry{Name: name, Password: e.Password, FileSystem: e.FileSystem, PrewarmDirs: e.PrewarmDirs})
	}
	g := &multiGateway{s3: s3srv}
	gw, err := webdavfs.NewServer(webdavfs.Config{
		Users:  entries,
		Logger: log.New(&gwLogWriter{g: g}, "", 0),
	})
	if err != nil {
		t.Fatalf("构造多用户网关失败: %v", err)
	}
	g.ts = httptest.NewServer(gw)
	g.gw = gw
	t.Cleanup(g.ts.Close)
	return g
}

// swap 原子替换用户路由表(热加载 Swap 步的网关侧入口)。
func (g *multiGateway) swap(t *testing.T, entries []webdavfs.UserEntry) error {
	t.Helper()
	return g.gw.SwapUsers(entries)
}

// gwLogWriter 把网关日志写入并发安全的缓冲,供审计断言。
type gwLogWriter struct {
	g *multiGateway
}

func (w *gwLogWriter) Write(p []byte) (int, error) {
	w.g.mu.Lock()
	defer w.g.mu.Unlock()
	return w.g.lg.Write(p)
}

func (g *multiGateway) logs() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lg.String()
}

// doAs 发起带指定凭据的请求;user 为空则不带认证。
func (g *multiGateway) doAs(t *testing.T, user, pass, method, path, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, g.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := g.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s 失败: %v", method, path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func swapEntries(fsByName map[string]*webdavfs.FileSystem, specs map[string]string) []webdavfs.UserEntry {
	entries := make([]webdavfs.UserEntry, 0, len(specs))
	for name, pw := range specs {
		entries = append(entries, webdavfs.UserEntry{Name: name, Password: pw, FileSystem: fsByName[name]})
	}
	return entries
}

// 场景:权限隔离 —— alice 只见 alice 前缀,bob 的对象结构性不可见。
func TestMultiUserIsolation(t *testing.T) {
	g := newMultiGateway(t, "", map[string]webdavfs.UserEntry{
		"alice": {Password: "pa"},
		"bob":   {Password: "pb"},
	})
	g.s3.Put(bucket, "alice/alice-file.txt", []byte("a"), "")
	g.s3.Put(bucket, "bob/bob-file.txt", []byte("b"), "")

	resp := g.doAs(t, "alice", "pa", "PROPFIND", "/", propfindAll, map[string]string{"Depth": "1"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("期望 207,实际 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	ms := parseMulti(t, resp)
	hrefs := make([]string, 0, len(ms.Responses))
	for _, r := range ms.Responses {
		hrefs = append(hrefs, r.Href)
	}
	joined := strings.Join(hrefs, "\n")
	if !strings.Contains(joined, "alice-file.txt") {
		t.Errorf("alice 应看到自己的对象,实际: %s", joined)
	}
	if strings.Contains(joined, "bob-file.txt") {
		t.Errorf("alice 不应看到 bob 的对象,实际: %s", joined)
	}

	// 越界路径被 normalize 拒绝,返回 404。
	resp = g.doAs(t, "alice", "pa", "GET", "/../bob/bob-file.txt", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("越界 GET 期望 404,实际 %d", resp.StatusCode)
	}
}

// 场景:审计归因 —— 已认证请求的日志行带 user=<名字>,未认证放行的 OPTIONS 记 "-"。
func TestAuditLogAttribution(t *testing.T) {
	g := newMultiGateway(t, "", map[string]webdavfs.UserEntry{
		"alice": {Password: "pa"},
	})
	g.doAs(t, "alice", "pa", "PUT", "/audit.txt", "x", nil)
	g.doAs(t, "", "", "OPTIONS", "/", "", nil)

	logs := g.logs()
	if !strings.Contains(logs, "PUT /audit.txt 201") || !strings.Contains(logs, "user=alice") {
		t.Errorf("日志应含 user=alice 的 PUT 记录,实际: %s", logs)
	}
	if !strings.Contains(logs, "user=-") {
		t.Errorf("未认证 OPTIONS 应记 user=-,实际: %s", logs)
	}
}

// 场景:认证查表 —— 表内用户凭据生效,错密码/未知用户/无凭据一律 401。
func TestMultiUserAuthTable(t *testing.T) {
	g := newMultiGateway(t, "", map[string]webdavfs.UserEntry{
		"alice": {Password: "pa"},
		"bob":   {Password: "pb"},
	})
	if resp := g.doAs(t, "alice", "pa", "HEAD", "/x", "", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("合法用户应通过认证(404=对象不存在),实际 %d", resp.StatusCode)
	}
	if resp := g.doAs(t, "alice", "wrong", "HEAD", "/x", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("错密码期望 401,实际 %d", resp.StatusCode)
	}
	if resp := g.doAs(t, "mallory", "pm", "HEAD", "/x", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("未知用户期望 401,实际 %d", resp.StatusCode)
	}
	if resp := g.doAs(t, "", "", "PROPFIND", "/", propfindAll, map[string]string{"Depth": "1"}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("无凭据期望 401,实际 %d", resp.StatusCode)
	}
}

// 场景:热加载新增/改密/删除用户(经 SwapUsers 原子换表)。
func TestSwapUsersHotReload(t *testing.T) {
	g := newMultiGateway(t, "", map[string]webdavfs.UserEntry{
		"alice": {Password: "pa"},
	})
	fsAlice := mustFS(t, g, "", "alice")
	fsBob := mustFS(t, g, "", "bob")

	// 改密 + 新增 bob:alice 旧密码 401、新密码可用,bob 上传落在自己的前缀。
	if err := g.swap(t, swapEntries(map[string]*webdavfs.FileSystem{"alice": fsAlice, "bob": fsBob}, map[string]string{"alice": "pa2", "bob": "pb"})); err != nil {
		t.Fatalf("SwapUsers 失败: %v", err)
	}
	if resp := g.doAs(t, "alice", "pa", "HEAD", "/x", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("旧密码期望 401,实际 %d", resp.StatusCode)
	}
	if resp := g.doAs(t, "bob", "pb", "PUT", "/bob-file.txt", "b", nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("新用户 bob PUT 期望 201,实际 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	if _, ok := g.s3.Get(bucket, "bob/bob-file.txt"); !ok {
		t.Error("bob 上传应落在 bob/ 前缀(路径自定义)")
	}
	if resp := g.doAs(t, "alice", "pa2", "PUT", "/a.txt", "a", nil); resp.StatusCode != http.StatusCreated {
		t.Errorf("改密后的 alice PUT 期望 201,实际 %d", resp.StatusCode)
	}

	// 删除 alice:后续请求 401;bob 不受影响。
	if err := g.swap(t, swapEntries(map[string]*webdavfs.FileSystem{"bob": fsBob}, map[string]string{"bob": "pb"})); err != nil {
		t.Fatalf("SwapUsers 失败: %v", err)
	}
	if resp := g.doAs(t, "alice", "pa2", "HEAD", "/x", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("被删除用户期望 401,实际 %d", resp.StatusCode)
	}
	if resp := g.doAs(t, "bob", "pb", "HEAD", "/bob-file.txt", "", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("bob 应不受换表影响,实际 %d", resp.StatusCode)
	}
}

// mustFS 为一名用户构建带独立前缀的文件系统(测试内新建栈用)。
func mustFS(t *testing.T, g *multiGateway, prefix, name string) *webdavfs.FileSystem {
	t.Helper()
	s3c, err := client.New(context.Background(), &config.Resolved{
		Endpoint: g.s3.URL(), AccessKey: "ak", SecretKey: "sk",
		Region: "us-east-1", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("构造 S3 client 失败: %v", err)
	}
	core, err := s3fs.New(s3fs.Config{Client: s3c, Bucket: bucket, Prefix: strings.TrimPrefix(prefix+"/"+name, "/"), StagingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("构造 s3fs 失败: %v", err)
	}
	return webdavfs.NewWithListingCache(core, time.Minute)
}
