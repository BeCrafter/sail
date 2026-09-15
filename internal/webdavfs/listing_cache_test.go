package webdavfs

import (
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

	// 在 /a/b 下变更:应失效 /a/b 自身、祖先 /a 与 /、子树 /a/b/c,但不动 /other。
	c.invalidatePath("/a/b")

	for _, gone := range []string{"/a/b", "/a", "/", "/a/b/c"} {
		if _, ok := c.get(gone); ok {
			t.Errorf("%s 应被失效", gone)
		}
	}
	if _, ok := c.get("/other"); !ok {
		t.Error("/other 不应被失效")
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
