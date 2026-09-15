package webdavfs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/BeCrafter/sail/internal/vfs"
)

// fakeCore 是可控的 vfs.FileSystem:ReadDir 可被阻塞以观察调用方的行为。
type fakeCore struct {
	mu    sync.Mutex
	calls int
	block chan struct{} // 非 nil 时 ReadDir 会等待它
	fis   []vfs.FileInfo
}

func (f *fakeCore) ReadDir(ctx context.Context, p string) ([]vfs.FileInfo, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.block != nil {
		<-f.block
	}
	return f.fis, nil
}
func (f *fakeCore) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func (f *fakeCore) Stat(context.Context, string) (vfs.FileInfo, error) { return vfs.FileInfo{}, nil }
func (f *fakeCore) OpenRead(context.Context, string) (vfs.ReadSeekCloser, error) {
	return nil, errors.New("not used")
}
func (f *fakeCore) OpenWrite(context.Context, string) (vfs.WriteHandle, error) {
	return nil, errors.New("not used")
}
func (f *fakeCore) Remove(context.Context, string, bool) error   { return nil }
func (f *fakeCore) Rename(context.Context, string, string) error { return nil }

// 过期后 readDir 必须「先回旧值、后台刷新」,不能阻塞在重新列举上。
func TestReadDirServesStaleWhileRevalidating(t *testing.T) {
	core := &fakeCore{fis: []vfs.FileInfo{mkFi("/d/x")}}
	fs := NewWithListingCache(core, 30*time.Millisecond)

	// 首次列举:填充缓存。
	if _, err := fs.readDir(context.Background(), "/d"); err != nil {
		t.Fatal(err)
	}
	if core.callCount() != 1 {
		t.Fatalf("首次应列举 1 次,实际 %d", core.callCount())
	}
	// 等到过期。
	time.Sleep(60 * time.Millisecond)

	// 让后端卡住:过期后的 readDir 仍须立刻返回旧值。
	core.mu.Lock()
	core.block = make(chan struct{})
	core.mu.Unlock()

	done := make(chan struct{})
	go func() {
		fis, err := fs.readDir(context.Background(), "/d")
		if err != nil || len(fis) != 1 {
			t.Errorf("应返回旧值,实际 fis=%v err=%v", fis, err)
		}
		close(done)
	}()
	select {
	case <-done:
		// 立刻返回,正确。
	case <-time.After(2 * time.Second):
		t.Fatal("过期后 readDir 阻塞了,未走 stale-while-revalidate")
	}
	// 放行后台刷新,避免 goroutine 泄漏。
	core.mu.Lock()
	close(core.block)
	core.mu.Unlock()
}

// prewarm 必须在后台把目录列入缓存,使首次访问直接命中。
func TestPrewarmPopulatesCache(t *testing.T) {
	core := &fakeCore{fis: []vfs.FileInfo{mkFi("/big/x")}}
	fs := NewWithListingCache(core, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs.prewarm(ctx, []string{"/big"})

	// 等预热完成(轮询,不依赖固定 sleep)。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, hit := fs.dirs.get("/big"); hit {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	before := core.callCount()
	if _, hit := fs.dirs.get("/big"); !hit {
		t.Fatal("预热后目录应已入缓存")
	}
	// 再读一次应命中缓存,不再调用后端。
	if _, err := fs.readDir(context.Background(), "/big"); err != nil {
		t.Fatal(err)
	}
	if core.callCount() != before {
		t.Fatalf("预热后 readDir 不应再调后端,调用数 %d → %d", before, core.callCount())
	}
}

// 同一路径的并发 readDir(缓存冷)必须合并为一次后端列举。
func TestReadDirConcurrentFetchCoalesced(t *testing.T) {
	core := &fakeCore{fis: []vfs.FileInfo{mkFi("/d/x")}, block: make(chan struct{})}
	fs := NewWithListingCache(core, time.Minute)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); fs.readDir(context.Background(), "/d") }()
	}
	time.Sleep(100 * time.Millisecond)
	core.mu.Lock()
	close(core.block)
	core.mu.Unlock()
	wg.Wait()

	if got := core.callCount(); got != 1 {
		t.Fatalf("并发 readDir 应只列举 1 次,实际 %d", got)
	}
}
