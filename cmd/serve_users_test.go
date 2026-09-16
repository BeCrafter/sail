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
	f.rt.ensuredMu.Lock()
	ensured := f.rt.ensured["team/alice-space/"]
	f.rt.ensuredMu.Unlock()
	if ensured {
		t.Error("失败的 ensure 不得登记为已完成")
	}
}

// 场景:经 reload() 的热加载不得死锁 —— reload 持 reloadMu 调用
// applyUserTable,后者在启用多用户时会调 ensureDirAsync;若它再取
// reloadMu 就会自锁(不可重入)。这条覆盖整条 reload 链路(既有测试
// 都只直调 applyUserTable,漏掉了这条路径)。
func TestReloadDoesNotDeadlock(t *testing.T) {
	f := newRuntimeFixture(t, usersFrom("alice"))

	serveBody := "      users:\n" +
		"        - name: alice\n          password: pw-alice\n          prefix: alice-space/\n" +
		"        - name: bob\n          password: pw-bob\n          prefix: bob-space/\n"
	writeServeConfigWithServe(t, "b", serveBody)

	runReload := func() {
		t.Helper()
		done := make(chan struct{})
		go func() {
			f.rt.reload()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("reload 死锁:3s 内未返回")
		}
	}

	runReload()
	// 新用户 bob 生效。
	if resp := f.doAs(t, "bob", "pw-bob", "PUT", "/b.txt", "b"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("reload 后 bob PUT 期望 201,实际 %d", resp.StatusCode)
	}
	// 再改一次(换 alice 密码,并保留 bob):reloadMu 未被上次 reload 卡住。
	serveBody2 := "      users:\n" +
		"        - name: alice\n          password: pw-alice-2\n          prefix: alice-space/\n" +
		"        - name: bob\n          password: pw-bob\n          prefix: bob-space/\n"
	writeServeConfigWithServe(t, "b", serveBody2)
	runReload()
	if resp := f.doAs(t, "alice", "pw-alice-2", "HEAD", "/", ""); resp.StatusCode == http.StatusUnauthorized {
		t.Error("第二次 reload 未生效(reloadMu 可能仍被占用)")
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
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watchConfig(ctx, cfg, 80*time.Millisecond, watchRebuildBackoff, onChange, log.New(os.Stderr, "", 0))

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
	users[0].Quota = "1024"
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
	table2[0].Quota = "2048"
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

// --- mergeServe:dir-cache-ttl / prewarm 的配置落点(与同名 flag 一一对应) ---

func TestMergeServeDirCacheTTLAndPrewarm(t *testing.T) {
	cases := []struct {
		name        string
		cfgTTL      string
		cfgPrewarm  []string
		flagTTL     string
		flagPrewarm []string
		chgTTL      bool
		chgPrewarm  bool
		wantTTL     time.Duration
		wantPrewarm []string
		wantErr     string
	}{
		{name: "配置为空落回 flag 默认", wantTTL: 60 * time.Second},
		{name: "配置生效", cfgTTL: "5m", cfgPrewarm: []string{"/a", "/b"}, wantTTL: 5 * time.Minute, wantPrewarm: []string{"/a", "/b"}},
		{name: "flag 显式覆盖配置", cfgTTL: "5m", cfgPrewarm: []string{"/a"}, chgTTL: true, flagTTL: "30s", chgPrewarm: true, flagPrewarm: []string{"/x"}, wantTTL: 30 * time.Second, wantPrewarm: []string{"/x"}},
		{name: "0 表示关闭缓存", cfgTTL: "0", wantTTL: 0},
		{name: "非法 TTL fail-loud", cfgTTL: "nope", wantErr: "invalid --dir-cache-ttl"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &config.Resolved{ProfileName: "prod", Bucket: "b"}
			r.Serve.User, r.Serve.Password = "alice", "pw"
			r.Serve.DirCacheTTL = c.cfgTTL
			r.Serve.Prewarm = c.cfgPrewarm
			o := defaultFlags()
			if c.chgTTL {
				o.dirCacheTTL = c.flagTTL
			}
			if c.chgPrewarm {
				o.prewarm = c.flagPrewarm
			}
			chg := func(f string) bool {
				return (c.chgTTL && f == "dir-cache-ttl") || (c.chgPrewarm && f == "prewarm")
			}
			s, err := mergeServe(o, r, chg)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("期望错误含 %q,实际 %v", c.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("期望通过,实际报错: %v", err)
			}
			if s.dirCacheTTL != c.wantTTL {
				t.Errorf("dirCacheTTL = %v,期望 %v", s.dirCacheTTL, c.wantTTL)
			}
			if len(s.prewarm) != len(c.wantPrewarm) {
				t.Errorf("prewarm = %v,期望 %v", s.prewarm, c.wantPrewarm)
			}
		})
	}
}

// --- 批 2:冷区告警 / 退役清理 / watcher 重建 ---

// dir-cache-ttl 与 prewarm 只在建栈时被读取(挂在栈上),改了不会生效;
// 它们必须进冷区告警,否则运维改完配置毫无反应还以为生效了。
func TestWarnColdZoneIncludesCacheAndPrewarm(t *testing.T) {
	f := newRuntimeFixture(t, usersFrom("alice"))

	r2 := &config.Resolved{ProfileName: "prod", Bucket: "b"}
	s2 := f.rt.settings
	s2.dirCacheTTL = f.rt.settings.dirCacheTTL + time.Minute
	s2.prewarm = []string{"/hot"}

	f.rt.warnColdZone(r2, s2)
	out := f.logs.String()
	if !strings.Contains(out, "dir-cache-ttl") || !strings.Contains(out, "prewarm") {
		t.Errorf("冷区告警应包含 dir-cache-ttl 与 prewarm,实际日志:\n%s", out)
	}
}

// 退役用户的 ensured 必须同步清理:同前缀用户被重建时要重新补建 marker(I10),
// 否则会被误判为「已建」而永远跳过。
func TestRetireClearsEnsuredForRecreatedPrefix(t *testing.T) {
	f := newRuntimeFixture(t, usersFrom("alice"))
	eff := "team/alice-space" // ensured 的键是生效前缀本身(无尾斜杠)

	waitFor(t, 5*time.Second, func() bool {
		f.rt.ensuredMu.Lock()
		defer f.rt.ensuredMu.Unlock()
		return f.rt.ensured[eff]
	})

	// 删除 alice(退役):ensured 应被清掉。
	if err := f.rt.applyUserTable(usersFrom("bob")); err != nil {
		t.Fatalf("applyUserTable 失败: %v", err)
	}
	f.rt.ensuredMu.Lock()
	left := f.rt.ensured[eff]
	f.rt.ensuredMu.Unlock()
	if left {
		t.Error("退役后 ensured 未清理,同前缀用户重建时 marker 会被跳过")
	}

	// 同前缀用户重建:marker 重新补建(再次发 PUT)。
	before := f.s3.Counts.Put.Load()
	if err := f.rt.applyUserTable(usersFrom("alice")); err != nil {
		t.Fatalf("applyUserTable 失败: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return f.s3.Counts.Put.Load() > before })
}

// watcher 失效(监听目录被整体替换/通道关闭)必须重建,而不是静默变哑。
// 这里用「目录一开始不存在、稍后出现」来驱动重建路径(跨平台稳定,不依赖
// kqueue 对目录自删除的事件语义)。
func TestWatchConfigRearmsUntilDirExists(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "conf") // 先不存在:第一次 add 必失败
	cfg := filepath.Join(dir, "config.yaml")

	fired := make(chan struct{}, 1)
	onChange := func() {
		select {
		case fired <- struct{}{}:
		default:
		}
	}
	logs := &bufLogger{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watchConfig(ctx, cfg, 50*time.Millisecond, 50*time.Millisecond, onChange, log.New(logs, "", 0))

	time.Sleep(120 * time.Millisecond) // 让第一轮失败并进入退避
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = os.WriteFile(cfg, []byte("v: 1\n"), 0o600)
		select {
		case <-fired:
			return // 重建后的 watcher 收到了事件
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("目录出现后热加载未自愈,日志:\n%s", logs.String())
}
