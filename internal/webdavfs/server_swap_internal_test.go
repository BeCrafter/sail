package webdavfs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/fakes3"
	"github.com/BeCrafter/sail/internal/vfs"
	"github.com/BeCrafter/sail/internal/vfs/s3fs"
)

// countingCore 统计 ReadDir 次数,用于断言预热 goroutine 的启停。
type countingCore struct {
	core    vfs.FileSystem
	readdir atomic.Int64
}

func (c *countingCore) Stat(ctx context.Context, p string) (vfs.FileInfo, error) {
	return c.core.Stat(ctx, p)
}
func (c *countingCore) ReadDir(ctx context.Context, p string) ([]vfs.FileInfo, error) {
	c.readdir.Add(1)
	return c.core.ReadDir(ctx, p)
}
func (c *countingCore) OpenRead(ctx context.Context, p string) (vfs.ReadSeekCloser, error) {
	return c.core.OpenRead(ctx, p)
}
func (c *countingCore) OpenWrite(ctx context.Context, p string) (vfs.WriteHandle, error) {
	return c.core.OpenWrite(ctx, p)
}
func (c *countingCore) Remove(ctx context.Context, p string, recursive bool) error {
	return c.core.Remove(ctx, p, recursive)
}
func (c *countingCore) Rename(ctx context.Context, oldPath, newPath string) error {
	return c.core.Rename(ctx, oldPath, newPath)
}

// I9:同一 FileSystem 指针 = 同一栈复用(不重建 handler/MemLS,凭据热替换);
// 退役栈的预热 goroutine 必须随换表停止,不允许泄漏。
func TestSwapUsersReusesStackAndStopsRetiredPrewarm(t *testing.T) {
	s3srv := fakes3.New()
	t.Cleanup(s3srv.Close)
	s3c, err := client.New(context.Background(), &config.Resolved{
		Endpoint: s3srv.URL(), AccessKey: "ak", SecretKey: "sk",
		Region: "us-east-1", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("构造 S3 client 失败: %v", err)
	}
	newFS := func(prefix string) *FileSystem {
		t.Helper()
		core, err := s3fs.New(s3fs.Config{Client: s3c, Bucket: "b", Prefix: prefix, StagingDir: t.TempDir()})
		if err != nil {
			t.Fatalf("构造 s3fs 失败: %v", err)
		}
		return NewWithListingCache(core, time.Minute)
	}
	fsAlice := newFS("alice")

	coreBob := &countingCore{core: newFS("bob").core}
	fsBob := NewWithListingCache(coreBob, time.Second)

	gw, err := NewServer(Config{Users: []UserEntry{{Name: "alice", Password: "pa", FileSystem: fsAlice}}})
	if err != nil {
		t.Fatalf("构造网关失败: %v", err)
	}
	ts := httptest.NewServer(gw)
	t.Cleanup(ts.Close)
	doAs := func(user, pass, method, path string) *http.Response {
		req, err := http.NewRequest(method, ts.URL+path, nil)
		if err != nil {
			t.Fatalf("构造请求失败: %v", err)
		}
		if user != "" {
			req.SetBasicAuth(user, pass)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s 失败: %v", method, path, err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}

	// bob 的栈带预热目录;预热挂在可取消 ctx 上,首次列举即刻发生。
	if err := gw.SwapUsers([]UserEntry{
		{Name: "alice", Password: "pa", FileSystem: fsAlice},
		{Name: "bob", Password: "pb", FileSystem: fsBob, PrewarmDirs: []string{"/d"}},
	}); err != nil {
		t.Fatalf("SwapUsers 失败: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool { return coreBob.readdir.Load() >= 1 })

	// 同一 FileSystem 再次换表 = 同栈复用,只换凭据。
	stackBefore := gw.current()["bob"].stack
	if err := gw.SwapUsers([]UserEntry{
		{Name: "alice", Password: "pa", FileSystem: fsAlice},
		{Name: "bob", Password: "pb2", FileSystem: fsBob, PrewarmDirs: []string{"/d"}},
	}); err != nil {
		t.Fatalf("SwapUsers 失败: %v", err)
	}
	if got := gw.current()["bob"].stack; got != stackBefore {
		t.Fatal("同一 FileSystem 换表必须复用同一栈,不得重建")
	}
	if resp := doAs("bob", "pb", "HEAD", "/x"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("复用栈换凭据后旧密码期望 401,实际 %d", resp.StatusCode)
	}
	if resp := doAs("bob", "pb2", "HEAD", "/x"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("复用栈新密码应通过认证(404=对象不存在),实际 %d", resp.StatusCode)
	}

	// 退役 bob 的栈:预热不再刷新(预热周期 = max(ttl/2, 1s),取消后计数稳定)。
	if err := gw.SwapUsers([]UserEntry{{Name: "alice", Password: "pa", FileSystem: fsAlice}}); err != nil {
		t.Fatalf("SwapUsers 失败: %v", err)
	}
	n := coreBob.readdir.Load()
	time.Sleep(1600 * time.Millisecond)
	if got := coreBob.readdir.Load(); got != n {
		t.Errorf("退役后预热应停止: count %d → %d", n, got)
	}
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
