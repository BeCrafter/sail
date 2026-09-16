package webdavfs

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BeCrafter/sail/internal/vfs"
)

func mkFi(path string) vfs.FileInfo { return vfs.FileInfo{Path: path, Name: path} }

func TestListingCacheHitAndExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	c := newListingCache(5 * time.Second)
	c.now = func() time.Time { return now }

	c.put("/a", []vfs.FileInfo{mkFi("/a/x")})
	fis, ok := c.get("/a")
	if !ok {
		t.Fatal("未过期应命中")
	}
	// 必须断言内容:put 若漏了深拷贝,缓存里会是零值(曾有此 bug,仅查 ok 抓不住)。
	if len(fis) != 1 || fis[0].Path != "/a/x" || fis[0].Name != "/a/x" {
		t.Fatalf("缓存内容被破坏: %+v", fis)
	}
	now = now.Add(4 * time.Second)
	if _, ok := c.get("/a"); !ok {
		t.Fatal("TTL 内应命中")
	}
	now = now.Add(2 * time.Second) // 超过 5s
	if _, ok := c.get("/a"); ok {
		t.Fatal("过期后应未命中")
	}
}

func TestListingCacheInvalidatePathCoversAncestorsAndSubtree(t *testing.T) {
	c := newListingCache(time.Minute)
	c.put("/", []vfs.FileInfo{mkFi("/")})
	c.put("/a", []vfs.FileInfo{mkFi("/a")})
	c.put("/a/b", []vfs.FileInfo{mkFi("/a/b")})
	c.put("/a/b/c", []vfs.FileInfo{mkFi("/a/b/c")})
	c.put("/other", []vfs.FileInfo{mkFi("/other")})

	// 在 /a/b 下变更:失效 /a/b 自身、子树 /a/b/c 与直接父 /a;
	// 根 / 保持不变(一次深层写入不该砸掉整个根缓存),/other 也不受影响。
	c.invalidatePath("/a/b")

	for _, gone := range []string{"/a/b", "/a", "/a/b/c"} {
		if _, ok := c.get(gone); ok {
			t.Errorf("%s 应被失效", gone)
		}
	}
	for _, kept := range []string{"/", "/other"} {
		if _, ok := c.get(kept); !ok {
			t.Errorf("%s 不应被失效(深层写不应砸掉上层列表)", kept)
		}
	}
}

// 中间目录此前从未被列举过(可能是这次写入新建的):必须继续上溯,直到遇到
// 已登记的祖先为止 —— 否则父目录的列表会漏掉新建的目录项。
func TestListingCacheInvalidatePathWalksUpUnknownParents(t *testing.T) {
	c := newListingCache(time.Minute)
	c.put("/", []vfs.FileInfo{mkFi("/")})
	c.put("/a", []vfs.FileInfo{mkFi("/a")})
	// /a/new/deep 从未被列举过。

	c.invalidatePath("/a/new/deep/file.txt")

	for _, gone := range []string{"/a"} {
		if _, ok := c.get(gone); ok {
			t.Errorf("%s 应被失效(其下可能出现新目录)", gone)
		}
	}
	if _, ok := c.get("/"); !ok {
		t.Error("根仍应保留:第一级目录 a 在写入前就存在")
	}
}

func TestListingCacheDisabledWhenZeroTTL(t *testing.T) {
	fs := NewWithListingCache(nil, 0)
	if fs.dirs != nil {
		t.Fatal("ttl=0 不应创建缓存")
	}
}

// getStale 必须返回已过期但仍在缓存里的旧值(供 stale-while-revalidate)。
func TestListingCacheGetStale(t *testing.T) {
	now := time.Unix(2000, 0)
	c := newListingCache(5 * time.Second)
	c.now = func() time.Time { return now }

	c.put("/a", []vfs.FileInfo{mkFi("/a/x")})
	now = now.Add(10 * time.Second) // 已过期

	if _, ok := c.get("/a"); ok {
		t.Fatal("过期后 get 不应命中")
	}
	if fis, ok := c.getStale("/a"); !ok || len(fis) != 1 || fis[0].Path != "/a/x" {
		t.Fatalf("过期后 getStale 应返回旧值且内容正确,实际 ok=%v fis=%+v", ok, fis)
	}
}

// 并发 fetch 同一路径必须合并为一次后端调用(singleflight):
// 否则 Finder 的并发 PROPFIND 会把超大目录的分页次数成倍放大。
func TestListingCacheFetchCoalesces(t *testing.T) {
	c := newListingCache(time.Minute)
	var calls int32
	started := make(chan struct{})
	release := make(chan struct{})

	fetchFn := func() ([]vfs.FileInfo, error) {
		atomic.AddInt32(&calls, 1)
		close(started)
		<-release // 卡住,让其余 goroutine 进入等待
		return []vfs.FileInfo{mkFi("/a/x")}, nil
	}

	const n = 8
	results := make(chan int, n)
	go func() {
		c.fetch("/a", fetchFn)
	}()
	<-started
	for i := 0; i < n; i++ {
		go func() {
			fis, _ := c.fetch("/a", fetchFn)
			results <- len(fis)
		}()
	}
	// 给其余 goroutine 时间进入 inflight 等待,再放行。
	time.Sleep(50 * time.Millisecond)
	close(release)

	for i := 0; i < n; i++ {
		if got := <-results; got != 1 {
			t.Fatalf("等待者应拿到同一结果(1 条),实际 %d", got)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("并发 fetch 应只触发 1 次后端列举,实际 %d", got)
	}
}

// 超出 maxDirs 时淘汰最久未使用的条目,避免超大目录列表把内存吃光。
func TestListingCacheEvictsLRU(t *testing.T) {
	now := time.Unix(3000, 0)
	c := newListingCache(time.Minute)
	c.now = func() time.Time { return now }
	c.maxDirs = 3

	for _, p := range []string{"/a", "/b", "/c"} {
		c.put(p, []vfs.FileInfo{mkFi(p)})
		now = now.Add(time.Second)
	}
	// 触碰 /a 使其最近使用,再插入 /d → 应淘汰最久未用的 /b。
	c.get("/a")
	c.put("/d", []vfs.FileInfo{mkFi("/d")})

	if _, ok := c.get("/b"); ok {
		t.Error("/b 最久未用,应被淘汰")
	}
	for _, p := range []string{"/a", "/c", "/d"} {
		if _, ok := c.get(p); !ok {
			t.Errorf("%s 应仍在缓存", p)
		}
	}
}

// 缓存键必须归一化:`/image` 与 `/image/` 是同一目录。
// 否则预热用的键(带/不带斜杠)与请求的键对不上,预热形同虚设。
func TestListingCacheNormalizesTrailingSlash(t *testing.T) {
	c := newListingCache(time.Minute)
	c.put("/image/", []vfs.FileInfo{mkFi("/image/x")})

	if fis, ok := c.get("/image"); !ok || len(fis) != 1 || fis[0].Path != "/image/x" {
		t.Fatalf("/image 应命中 /image/ 写入的条目且内容正确,实际 ok=%v fis=%+v", ok, fis)
	}
	// 反向:不带斜杠写入,带斜杠读取。
	c.put("/other", []vfs.FileInfo{mkFi("/other/y")})
	if _, ok := c.get("/other/"); !ok {
		t.Fatal("/other/ 应命中 /other 写入的条目")
	}
	// 根。
	c.put("/", []vfs.FileInfo{mkFi("/z")})
	if _, ok := c.get("/"); !ok {
		t.Fatal("根目录应可命中")
	}
}

// --- 批 2:缓存语义(负缓存 / 过期目录判定 / 后台刷新去重告警) ---

// negCore 的 Stat 恒报不存在并计数,用于验证负缓存。
type negCore struct {
	statCalls int32
}

func (c *negCore) Stat(context.Context, string) (vfs.FileInfo, error) {
	atomic.AddInt32(&c.statCalls, 1)
	return vfs.FileInfo{}, vfs.ErrNotExist
}
func (c *negCore) ReadDir(context.Context, string) ([]vfs.FileInfo, error) { return nil, nil }
func (c *negCore) OpenRead(context.Context, string) (vfs.ReadSeekCloser, error) {
	return nil, vfs.ErrNotExist
}
func (c *negCore) OpenWrite(context.Context, string) (vfs.WriteHandle, error) {
	return nil, vfs.ErrNotExist
}
func (c *negCore) Remove(context.Context, string, bool) error   { return nil }
func (c *negCore) Rename(context.Context, string, string) error { return nil }

// 负缓存:同一请求内对同一路径的重复 Stat 只探一次后端 —— MOVE/COPY 会先由
// 壳探测目标、webdav 随后再 Stat 一次,没有负缓存就是两次后端探测。
func TestStatNegativeCacheAvoidsRepeatProbe(t *testing.T) {
	core := &negCore{}
	fs := New(core)
	ctx := context.WithValue(context.Background(), stateCtxKey{}, &requestState{cache: newReadCache()})

	if _, err := fs.stat(ctx, "/missing"); !errors.Is(err, vfs.ErrNotExist) {
		t.Fatalf("首次 Stat 应报不存在: %v", err)
	}
	if _, err := fs.stat(ctx, "/missing"); !errors.Is(err, vfs.ErrNotExist) {
		t.Fatalf("第二次 Stat 应命中负缓存并报不存在: %v", err)
	}
	if got := atomic.LoadInt32(&core.statCalls); got != 1 {
		t.Errorf("负缓存应让第二次探测不打后端,实际探测 %d 次", got)
	}
}

// isDir 不看过期:条目过期只说明列表要刷新,不改变「这是个目录」的事实;
// 用 get 会让 Stat 在 SWR 窗口内回落一次后端探测,与 readDir 的 stale 行为不一致。
func TestListingCacheIsDirToleratesExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	c := newListingCache(time.Second)
	c.now = func() time.Time { return now }
	c.put("/d", []vfs.FileInfo{mkFi("/d/x")})

	if !c.isDir("/d") {
		t.Fatal("未过期应判定为目录")
	}
	now = now.Add(time.Hour)
	if _, ok := c.get("/d"); ok {
		t.Fatal("前提:该条目应已过期")
	}
	if !c.isDir("/d") {
		t.Error("过期条目的 isDir 仍应为 true")
	}
}

// 后台刷新必须去重(并发触发只跑一份全量列举),且失败要有告警。
func TestListingCacheRefreshAsyncCoalescesAndLogsFailure(t *testing.T) {
	c := newListingCache(time.Second)
	buf := &syncLogBuf{}
	c.logger = log.New(buf, "", 0)

	var calls int32
	release := make(chan struct{})
	fetch := func() ([]vfs.FileInfo, error) {
		atomic.AddInt32(&calls, 1)
		<-release
		return nil, errors.New("backend down")
	}
	for i := 0; i < 5; i++ {
		c.refreshAsync("/d", fetch)
	}
	time.Sleep(50 * time.Millisecond) // 让第一份刷新进入 fetch
	close(release)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		inflight := len(c.inflight)
		c.mu.Unlock()
		if inflight == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("并发触发的后台刷新应合并为一次,实际 %d 次", got)
	}
	if !strings.Contains(buf.String(), "background refresh") {
		t.Errorf("刷新失败应告警,实际日志: %q", buf.String())
	}
}

// syncLogBuf 是并发安全的日志缓冲。
type syncLogBuf struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncLogBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncLogBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// 字节预算:多用户下每个用户一份缓存,条目数上限在大目录上仍可能吃掉几百 MB;
// 超预算时按 LRU 淘汰,内存不随目录规模失控。
func TestListingCacheEvictsByByteBudget(t *testing.T) {
	c := newListingCache(time.Minute)
	c.maxBytes = 10 * fileInfoCost // 约 10 条

	big := make([]vfs.FileInfo, 8)
	for i := range big {
		big[i] = mkFi(fmt.Sprintf("/big/%d", i))
	}
	c.put("/big", big)
	c.put("/small", []vfs.FileInfo{mkFi("/small/a"), mkFi("/small/b")})

	if _, ok := c.get("/small"); !ok {
		t.Error("最近写入的 /small 应保留")
	}
	if _, ok := c.get("/big"); ok {
		t.Error("超预算时最久未用的 /big 应被淘汰")
	}
	if c.bytes > c.maxBytes {
		t.Errorf("淘汰后占用仍超预算: %d > %d", c.bytes, c.maxBytes)
	}
}

// 预热与用户请求共用同一份 singleflight:同一目录不会同时跑两份全量列举
// (大目录下那是成百上千次分页请求)。
func TestPrewarmSharesInflightWithReadDir(t *testing.T) {
	core := &fakeCore{fis: []vfs.FileInfo{mkFi("/d/x")}, block: make(chan struct{})}
	fs := NewWithListingCache(core, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	fs.prewarm(ctx, []string{"/d"})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && core.callCount() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if core.callCount() != 1 {
		t.Fatalf("预热应发出一次列举,实际 %d 次", core.callCount())
	}

	done := make(chan struct{})
	go func() {
		_, _ = fs.readDir(ctx, "/d") // 缓存未命中(预热尚未写回):应并入在途列举
		close(done)
	}()
	time.Sleep(80 * time.Millisecond)
	if got := core.callCount(); got != 1 {
		t.Errorf("读请求应共用预热的在途列举,实际后端列举 %d 次", got)
	}
	close(core.block)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("读请求应随在途列举完成后返回")
	}
}

// 请求级缓存有上限:十万级目录的一次 PROPFIND 不会把每个子项都登记进 map。
func TestReadCacheBounded(t *testing.T) {
	c := newReadCache()
	for i := 0; i < maxReadCacheEntries+100; i++ {
		c.put(mkFi(fmt.Sprintf("/f%d", i)))
	}
	c.mu.RLock()
	n := len(c.m)
	c.mu.RUnlock()
	if n > maxReadCacheEntries {
		t.Errorf("请求级缓存应有上限 %d,实际 %d 条", maxReadCacheEntries, n)
	}
}
