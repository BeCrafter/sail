package quotafs

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BeCrafter/sail/internal/vfs"
)

// --- I5 内部测试:注入时钟与故障计数器,验证快照刷新失败沿用旧值 ---

type fakeCore struct {
	mu    sync.Mutex
	sizes map[string]int64
}

func (c *fakeCore) Stat(_ context.Context, p string) (vfs.FileInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.sizes[p]
	if !ok {
		return vfs.FileInfo{}, vfs.ErrNotExist
	}
	return vfs.FileInfo{Name: p, Path: p, Size: s}, nil
}
func (c *fakeCore) ReadDir(context.Context, string) ([]vfs.FileInfo, error) {
	return nil, vfs.ErrNotSupported
}
func (c *fakeCore) OpenRead(context.Context, string) (vfs.ReadSeekCloser, error) {
	return nil, vfs.ErrNotSupported
}
func (c *fakeCore) Remove(context.Context, string, bool) error { return vfs.ErrNotSupported }
func (c *fakeCore) Rename(context.Context, string, string) error {
	return vfs.ErrNotSupported
}

// fakeWriter 是 no-op 写句柄:配额会计测试只关心准入/预留,不关心落盘。
type fakeWriter struct{ core *fakeCore }

func (w *fakeWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *fakeWriter) Commit(context.Context) (vfs.FileInfo, error) {
	return vfs.FileInfo{}, nil
}
func (w *fakeWriter) Abort() error { return nil }
func (w *fakeWriter) Close() error { return nil }

func (c *fakeCore) OpenWrite(context.Context, string) (vfs.WriteHandle, error) {
	return &fakeWriter{core: c}, nil
}

type fakeCounter struct {
	mu     sync.Mutex
	value  int64
	fail   bool
	called int
}

func (c *fakeCounter) Usage(context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.called++
	if c.fail {
		return 0, errors.New("backend unavailable")
	}
	return c.value, nil
}

// 快照刷新失败:沿用旧快照、产生告警日志,准入继续可用且不过量放行。
func TestSnapshotRefreshFailureKeepsOldValueAndWarns(t *testing.T) {
	core := &fakeCore{}
	counter := &fakeCounter{value: 80}
	buf := &syncBuf{}
	fs := New(core, counter, 100, time.Minute, log.New(buf, "", 0))
	now := time.Now()
	fs.now = func() time.Time { return now }

	// 首次刷新成功:80/100,放行 10。
	w, err := fs.OpenWrite(context.Background(), "/a")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.(*quotaWriter).AdmitWrite(context.Background(), 10); err != nil {
		t.Fatalf("首次准入应放行: %v", err)
	}
	w.Abort()
	w.Close()

	// TTL 过期 + 计数器故障:沿用旧值 80,准入继续可用,且日志告警。
	now = now.Add(2 * time.Minute)
	counter.mu.Lock()
	counter.fail = true
	counter.mu.Unlock()
	w2, err := fs.OpenWrite(context.Background(), "/b")
	if err != nil {
		t.Fatal(err)
	}
	if err := w2.(*quotaWriter).AdmitWrite(context.Background(), 10); err != nil {
		t.Fatalf("刷新失败应沿用旧快照: %v", err)
	}
	w2.Abort()
	w2.Close()
	if !strings.Contains(buf.String(), "keeping previous value") {
		t.Errorf("应出现沿用旧值告警,实际: %s", buf.String())
	}

	// 恢复后拿到新值:计数器报 0 → 限额内大幅放行。
	now = now.Add(2 * time.Minute)
	counter.mu.Lock()
	counter.fail = false
	counter.value = 0
	counter.mu.Unlock()
	w3, err := fs.OpenWrite(context.Background(), "/c")
	if err != nil {
		t.Fatal(err)
	}
	if err := w3.(*quotaWriter).AdmitWrite(context.Background(), 90); err != nil {
		t.Fatalf("恢复后应按新快照放行: %v", err)
	}
	w3.Abort()
	w3.Close()
}

type syncBuf struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
