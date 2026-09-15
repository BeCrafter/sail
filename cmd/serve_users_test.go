package cmd

import (
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/fakes3"
)

// --- mergeServe:用户表合并与 I4 互斥 ---

func usersFrom(names ...string) []config.UserConfig {
	out := make([]config.UserConfig, 0, len(names))
	for _, n := range names {
		out = append(out, config.UserConfig{Name: n, Password: "pw-" + n, Prefix: n + "-space/"})
	}
	return out
}

var noChanged = func(string) bool { return false }

// defaultFlags 复刻 init() 里 serve webdav 各 flag 的默认值。
func defaultFlags() serveWebdavFlags {
	return serveWebdavFlags{
		listen:         ":8080",
		backendMaxSize: "5TiB",
		chunkSize:      "4GiB",
		dirCacheTTL:    "60s",
	}
}

// I4:users 表与单 user/password 同设拒绝启动(含跨来源:flag vs 配置)。
func TestMergeServeUsersAndPasswordAreMutuallyExclusive(t *testing.T) {
	cases := []struct {
		name    string
		users   []config.UserConfig
		cfgUser string
		flagChg string // 显式设置的 flag 名
		wantErr string
	}{
		{"仅 users 表", usersFrom("alice", "bob"), "", "", ""},
		{"users + 配置单用户", usersFrom("alice"), "alice", "", "mutually exclusive"},
		{"users + flag 单用户", usersFrom("alice"), "", "user", "mutually exclusive"},
		{"users + flag 密码", usersFrom("alice"), "", "password", "mutually exclusive"},
		{"仅配置单用户", nil, "alice", "", ""},
		{"两者皆无", nil, "", "", "--user and --password are required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &config.Resolved{ProfileName: "prod", Bucket: "b"}
			r.Serve.User = c.cfgUser
			if c.cfgUser != "" {
				r.Serve.Password = "pw"
			}
			r.Serve.Users = c.users
			o := defaultFlags()
			chg := noChanged
			if c.flagChg != "" {
				chg = func(f string) bool { return f == c.flagChg }
				if c.flagChg == "user" {
					o.user = "alice"
				} else {
					o.password = "pw"
				}
			}
			s, err := mergeServe(o, r, chg)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("期望通过,实际报错: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("期望被拒绝(含 %q),实际通过: %+v", c.wantErr, s)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("错误 %q 不含 %q", err.Error(), c.wantErr)
			}
		})
	}
}

// 用户表校验错误在 mergeServe 处 fail-loud(I6 嵌套、quota 语法)。
func TestMergeServeRejectsInvalidUserTable(t *testing.T) {
	r := &config.Resolved{ProfileName: "prod", Bucket: "b"}
	r.Serve.Users = []config.UserConfig{
		{Name: "alice", Password: "p", Prefix: "a/"},
		{Name: "bob", Password: "p", Prefix: "a/b/"},
	}
	if _, err := mergeServe(defaultFlags(), r, noChanged); err == nil || !strings.Contains(err.Error(), "nests inside") {
		t.Errorf("嵌套前缀应拒绝启动,实际: %v", err)
	}
	r.Serve.Users = []config.UserConfig{{Name: "alice", Password: "p", Quota: "10XB"}}
	if _, err := mergeServe(defaultFlags(), r, noChanged); err == nil || !strings.Contains(err.Error(), "invalid quota") {
		t.Errorf("非法 quota 应拒绝启动,实际: %v", err)
	}
}

// --- serveRuntime:多用户端到端(隔离/路径自定义/目录自动创建) ---

type bufLogger struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *bufLogger) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *bufLogger) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

type runtimeFixture struct {
	rt   *serveRuntime
	ts   *httptest.Server
	s3   *fakes3.Server
	logs *bufLogger
}

func newRuntimeFixture(t *testing.T, users []config.UserConfig) *runtimeFixture {
	t.Helper()
	s3srv := fakes3.New()
	t.Cleanup(s3srv.Close)
	f := &runtimeFixture{s3: s3srv, logs: &bufLogger{}}

	r := &config.Resolved{
		ProfileName: "prod", Bucket: "b",
		Endpoint: s3srv.URL(), AccessKey: "ak", SecretKey: "sk",
		Region: "us-east-1", PathStyle: true,
	}
	r.Serve.Prefix = "team"
	r.Serve.Users = users
	// 走生产链路:mergeServe 校验 → newServeRuntime 建栈/网关。
	s, err := mergeServe(defaultFlags(), r, noChanged)
	if err != nil {
		t.Fatalf("mergeServe 失败: %v", err)
	}
	rt, err := newServeRuntime(defaultFlags(), r, s, noChanged, log.New(f.logs, "", 0))
	if err != nil {
		t.Fatalf("newServeRuntime 失败: %v", err)
	}
	f.rt = rt
	f.ts = httptest.NewServer(rt.gw)
	t.Cleanup(f.ts.Close)
	return f
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("条件在 %v 内未满足", d)
}

func (f *runtimeFixture) doAs(t *testing.T, user, pass, method, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, f.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := f.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s 失败: %v", method, path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (f *runtimeFixture) waitForLog(t *testing.T, frag string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(f.logs.String(), frag) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("日志 5s 内未出现 %q,实际: %s", frag, f.logs.String())
}

// 场景:路径自定义 + 权限隔离 + 新增用户目录自动创建。
func TestServeRuntimeMultiUserEndToEnd(t *testing.T) {
	f := newRuntimeFixture(t, usersFrom("alice", "bob"))

	// alice 上传落在 team/alice-space/ 下(路径自定义 + I1 前缀隔离)。
	if resp := f.doAs(t, "alice", "pw-alice", "PUT", "/doc.txt", "hello"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice PUT 期望 201,实际 %d", resp.StatusCode)
	}
	if _, ok := f.s3.Get("b", "team/alice-space/doc.txt"); !ok {
		t.Error("alice 的对象应落在 team/alice-space/ 前缀下")
	}
	// bob 看不到 alice 的对象。
	resp := f.doAs(t, "bob", "pw-bob", "GET", "/doc.txt", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("bob 访问 alice 对象期望 404,实际 %d", resp.StatusCode)
	}

	// 目录自动创建:用户根 marker(0 字节、key 以 "/" 结尾)落桶(I10)。
	waitFor(t, 5*time.Second, func() bool {
		_, okA := f.s3.Get("b", "team/alice-space/")
		_, okB := f.s3.Get("b", "team/bob-space/")
		return okA && okB
	})
	if obj, ok := f.s3.Get("b", "team/alice-space/"); !ok || len(obj.Data) != 0 {
		t.Errorf("用户根 marker 应为 0 字节对象,实际存在=%v", ok)
	}
	f.waitForLog(t, "directory created for user space: team/alice-space/")
}

// 场景:热加载(经 rt.applyUserTable)新增/改密/删除用户,新前缀目录自动创建。
func TestServeRuntimeHotReloadSwap(t *testing.T) {
	f := newRuntimeFixture(t, usersFrom("alice"))

	// 新增 carol + 改 alice 密码:换表后生效。
	table := usersFrom("alice", "carol")
	table[0].Password = "pw-alice-2"
	if err := f.rt.applyUserTable(table); err != nil {
		t.Fatalf("applyUserTable 失败: %v", err)
	}
	if resp := f.doAs(t, "alice", "pw-alice", "HEAD", "/x", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("改密后旧密码期望 401,实际 %d", resp.StatusCode)
	}
	if resp := f.doAs(t, "carol", "pw-carol", "PUT", "/c.txt", "c"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("新增用户 carol PUT 期望 201,实际 %d: %s", resp.StatusCode, mustBody(t, resp))
	}
	if _, ok := f.s3.Get("b", "team/carol-space/c.txt"); !ok {
		t.Error("carol 的对象应落在 team/carol-space/ 前缀下")
	}
	// 新前缀的目录 marker 随 reload 自动创建。
	waitFor(t, 5*time.Second, func() bool {
		_, ok := f.s3.Get("b", "team/carol-space/")
		return ok
	})

	// 删除 alice:401;carol 不受影响。
	if err := f.rt.applyUserTable(usersFrom("carol")); err != nil {
		t.Fatalf("applyUserTable 失败: %v", err)
	}
	if resp := f.doAs(t, "alice", "pw-alice-2", "HEAD", "/x", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("被删除用户期望 401,实际 %d", resp.StatusCode)
	}
	if resp := f.doAs(t, "carol", "pw-carol", "HEAD", "/c.txt", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("carol 应不受换表影响,实际 %d", resp.StatusCode)
	}
}

// 场景:marker 创建失败不阻塞生效 —— S3 不可达时仅告警、不登记 ensured,
// 用户照常使用(这里以 marker 请求失败 + 日志告警验证降级路径)。
func TestServeRuntimeEnsureFailureOnlyWarns(t *testing.T) {
	f := newRuntimeFixture(t, usersFrom("alice"))
	f.s3.Close() // 后端不可达:marker PUT 必然失败

	f.rt.ensureDirAsync("team/alice-space/")
	f.waitForLog(t, "WARN: creating directory for user space")
	f.rt.reloadMu.Lock()
	ensured := f.rt.ensured["team/alice-space/"]
	f.rt.reloadMu.Unlock()
	if ensured {
		t.Error("失败的 ensure 不得登记为已完成")
	}
}

// 场景:坏配置/非法表被 reload 拒绝,旧表继续生效(I8)。
func TestServeRuntimeReloadKeepsOldTableOnInvalidConfig(t *testing.T) {
	f := newRuntimeFixture(t, usersFrom("alice"))

	// 嵌套前缀的新表:swapUsers 前置校验拒绝,旧表不受影响。
	bad := usersFrom("alice", "bob")
	bad[1].Prefix = "alice-space/logs/"
	if err := f.rt.applyUserTable(bad); err == nil {
		t.Fatal("嵌套前缀应被拒绝")
	}
	if resp := f.doAs(t, "alice", "pw-alice", "HEAD", "/x", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("旧表应继续生效(404=对象不存在),实际 %d", resp.StatusCode)
	}
	if resp := f.doAs(t, "bob", "pw-bob", "HEAD", "/x", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("被拒绝的 bob 不应生效,实际 %d", resp.StatusCode)
	}
}

// --- watchConfig:防抖、Remove 不退出、重建自愈 ---

func TestWatchConfigDebounceRemoveAndSelfHeal(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("v: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	fired := 0
	once := make(chan struct{})
	onChange := func() {
		mu.Lock()
		fired++
		n := fired
		mu.Unlock()
		if n == 1 {
			close(once)
		}
	}
	watchConfig(context.Background(), cfg, 80*time.Millisecond, onChange, log.New(os.Stderr, "", 0))

	// 写入 → 防抖后触发一次。
	if err := os.WriteFile(cfg, []byte("v: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-once:
	case <-time.After(5 * time.Second):
		t.Fatal("保存后 5s 内未触发 reload")
	}

	// 删除 → 不触发、watch 不退出。
	mu.Lock()
	n0 := fired
	mu.Unlock()
	if err := os.Remove(cfg); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)

	// 重建 + 写入 → 自愈,再次触发。
	if err := os.WriteFile(cfg, []byte("v: 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := fired
		mu.Unlock()
		if n > n0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("文件重建后 watch 未自愈")
}

func mustBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b := make([]byte, 4096)
	n, _ := resp.Body.Read(b)
	return string(b[:n])
}

// 场景(P2 热加载修改配额):改 quota 不重建栈,quotafs 原子改参数,
// 既有对象与在途连接不受影响,后续准入立即按新口径计算。
func TestServeRuntimeQuotaHotUpdate(t *testing.T) {
	users := usersFrom("alice")
	users[0].Quota = "1KiB"
	f := newRuntimeFixture(t, users)

	if resp := f.doAs(t, "alice", "pw-alice", "PUT", "/a.txt", strings.Repeat("x", 800)); resp.StatusCode != http.StatusCreated {
		t.Fatalf("1KiB 内写 800B 期望 201,实际 %d: %s", resp.StatusCode, mustBody(t, resp))
	}
	// 800(已用)+ 500 > 1024 → 507。
	if resp := f.doAs(t, "alice", "pw-alice", "PUT", "/b.txt", strings.Repeat("y", 500)); resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("旧配额 800+500>1024 期望 507,实际 %d", resp.StatusCode)
	}
	// 热更新配额为 2KiB:同一用户栈,无需重建。
	table2 := usersFrom("alice")
	table2[0].Quota = "2KiB"
	before := f.rt.stacks["team/alice-space"].fs
	if err := f.rt.applyUserTable(table2); err != nil {
		t.Fatalf("applyUserTable 失败: %v", err)
	}
	if after := f.rt.stacks["team/alice-space"].fs; after != before {
		t.Fatal("改 quota 不得重建栈")
	}
	if resp := f.doAs(t, "alice", "pw-alice", "PUT", "/b.txt", strings.Repeat("y", 500)); resp.StatusCode != http.StatusCreated {
		t.Fatalf("2KiB 口径下 800+500≤2048 应放行,实际 %d", resp.StatusCode)
	}
	// 既有对象不受影响。
	if obj, ok := f.s3.Get("b", "team/alice-space/a.txt"); !ok || string(obj.Data) != strings.Repeat("x", 800) {
		t.Error("热更新不得影响既有对象")
	}
}
