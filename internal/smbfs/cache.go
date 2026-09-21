package smbfs

import (
	"context"
	"log"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/BeCrafter/sail/internal/vfs"
)

// 目录列表 + 条目元信息缓存。
//
// WebDAV 壳里缓存只是优化项,SMB 壳里它是必需品,原因在上游库的列举实现:
// github.com/go-filesystems/smb 的 listDir 对每个条目单独调一次
// Filesystem.Stat(dir.go:148),而它返回的 DirEntry 只有 inode/name/type,
// 不带元数据 —— 落到 S3 上就是「每条目一次往返」。实测同一个 20 对象的目录:
// 无缓存 25 次后端调用 / 4.66s,有缓存 3 次 / 1.18s,TTL 内再列 0 次 / 1ms;
// 1000 key 的目录不缓存就是每次列目录 1000 次往返。
//
// 缓存两样东西,缺一不可:
//   - 目录列表(按目录路径),吸收重复列举;
//   - 每个条目的 FileInfo(按完整路径),把「列完目录紧接着逐条 Stat」变成命中。
//     只做前者的话,那 24 次 Stat 一次不少。
//
// 失效靠写路径主动告知(backend 在落 S3 之后调 invalidate),不缓存负结果:
// 不存在的路径每次都问后端,免得「删掉后立刻重建」被一条陈旧的 not-found 挡住。
type cachedFS struct {
	core   vfs.FileSystem
	ttl    time.Duration
	logger *log.Logger

	mu     sync.Mutex
	dirs   map[string]cachedListing
	files  map[string]cachedFile
	flight map[string]*dirFlight
}

type cachedListing struct {
	at      time.Time
	entries []vfs.FileInfo
}

type cachedFile struct {
	at   time.Time
	info vfs.FileInfo
}

// dirFlight 合并同一目录的并发列举:客户端会为一个目录并发发多条请求,
// 不合并就会把后端的分页次数乘上并发数。
type dirFlight struct {
	done    chan struct{}
	entries []vfs.FileInfo
	err     error
}

// listingRefreshTimeout 是后台刷新的兜底时限,防止一次列举无限期占用连接。
const listingRefreshTimeout = 10 * time.Minute

func newCachedFS(core vfs.FileSystem, ttl time.Duration, logger *log.Logger) *cachedFS {
	if ttl <= 0 {
		return &cachedFS{core: core}
	}
	return &cachedFS{
		core:   core,
		ttl:    ttl,
		logger: logger,
		dirs:   map[string]cachedListing{},
		files:  map[string]cachedFile{},
		flight: map[string]*dirFlight{},
	}
}

// cacheKey 归一化缓存键:"/image/" 与 "/image" 是同一条 —— 内核列目录给出的
// 子项路径不带尾斜杠,而请求路径可能带,不归一化就永远命不中。
func cacheKey(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return path.Clean(p)
}

func (c *cachedFS) fresh(at time.Time) bool { return time.Since(at) < c.ttl }

func (c *cachedFS) Stat(ctx context.Context, p string) (vfs.FileInfo, error) {
	if c.ttl <= 0 {
		return c.core.Stat(ctx, p)
	}
	key := cacheKey(p)
	c.mu.Lock()
	hit, ok := c.files[key]
	fresh := ok && c.fresh(hit.at)
	c.mu.Unlock()
	if fresh {
		return hit.info, nil
	}
	fi, err := c.core.Stat(ctx, p)
	if err != nil {
		return vfs.FileInfo{}, err
	}
	c.mu.Lock()
	c.files[key] = cachedFile{at: time.Now(), info: fi}
	c.mu.Unlock()
	return fi, nil
}

func (c *cachedFS) ReadDir(ctx context.Context, p string) ([]vfs.FileInfo, error) {
	if c.ttl <= 0 {
		return c.core.ReadDir(ctx, p)
	}
	key := cacheKey(p)
	if fis, ok := c.listing(key, true); ok {
		return fis, nil
	}
	// 过期但仍有旧值:先给旧的,后台刷新(stale-while-revalidate)。
	// S3 上的整目录列举是几百毫秒到数十秒的真实延迟,不该让用户卡在进度条上;
	// 代价是「别人改了桶」在 TTL 内看不到,与 WebDAV 模式同一取舍。
	if fis, ok := c.listing(key, false); ok {
		c.refreshAsync(key)
		return fis, nil
	}
	return c.fetch(ctx, key)
}

// listing 取缓存里的目录列表;freshOnly 为真时只认未过期的。
func (c *cachedFS) listing(key string, freshOnly bool) ([]vfs.FileInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.dirs[key]
	if !ok || (freshOnly && !c.fresh(d.at)) {
		return nil, false
	}
	return d.entries, true
}

// store 记下一份目录列表,并把每个条目的元信息一并记进文件缓存 —— 后半句
// 才是吃掉「逐条 Stat」的那一半。
func (c *cachedFS) store(key string, entries []vfs.FileInfo) {
	now := time.Now()
	snapshot := make([]vfs.FileInfo, len(entries))
	copy(snapshot, entries)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dirs[key] = cachedListing{at: now, entries: snapshot}
	for _, e := range entries {
		c.files[cacheKey(e.Path)] = cachedFile{at: now, info: e}
	}
}

// fetch 阻塞地列举一次,并发调用合并成一次后端请求。
func (c *cachedFS) fetch(ctx context.Context, key string) ([]vfs.FileInfo, error) {
	c.mu.Lock()
	if f, joined := c.flight[key]; joined {
		c.mu.Unlock()
		select {
		case <-f.done:
			return f.entries, f.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f := &dirFlight{done: make(chan struct{})}
	c.flight[key] = f
	c.mu.Unlock()

	f.entries, f.err = c.core.ReadDir(ctx, key)
	if f.err == nil {
		c.store(key, f.entries)
	}
	close(f.done)
	c.mu.Lock()
	delete(c.flight, key)
	c.mu.Unlock()
	return f.entries, f.err
}

// refreshAsync 在后台把一份过期的列表换成新的。已有在途列举时不重复发起。
func (c *cachedFS) refreshAsync(key string) {
	c.mu.Lock()
	_, busy := c.flight[key]
	c.mu.Unlock()
	if busy {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), listingRefreshTimeout)
		defer cancel()
		if _, err := c.fetch(ctx, key); err != nil && c.logger != nil {
			c.logger.Printf("smbfs: refreshing the listing of %s failed: %v", key, err)
		}
	}()
}

// prewarm 后台保热若干目录:立刻列一次并入缓存,此后周期刷新,使它们在客户端
// 访问之前就是热的。用于个别超大目录 —— 它们的首次列举可能长达数十秒,
// 预热把这份代价挪到用户点击之前。
func (c *cachedFS) prewarm(ctx context.Context, dirs []string) {
	if c.ttl <= 0 || len(dirs) == 0 {
		return
	}
	for _, d := range dirs {
		go c.prewarmLoop(ctx, cacheKey(d))
	}
}

func (c *cachedFS) prewarmLoop(ctx context.Context, key string) {
	for {
		// 走 singleflight 路径:与用户请求触发的刷新互斥,同一目录不会同时跑
		// 两份全量列举;顺带拿到本轮耗时用于安排下一轮。
		start := time.Now()
		if _, err := c.fetch(ctx, key); err != nil && c.logger != nil {
			c.logger.Printf("smbfs: prewarm of %s failed (will retry): %v", key, err)
		}
		last := time.Since(start)

		// 刷新间隔:默认 TTL 的一半(过期前就有新值),但绝不快于上一轮耗时的
		// 两倍 —— 列举本身要数十秒的大目录,不参考耗时就会背靠背打后端。
		interval := c.ttl / 2
		if min := 2 * last; min > interval {
			interval = min
		}
		if interval < time.Second {
			interval = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// invalidate 让一个路径的缓存失效。tree 为真时连同整棵子树一起失效
// (目录被整棵删除或改名)。
//
// 父目录只失效一级:条目的增删改改变的是直接父目录看到的内容。再往上的祖先
// 看到的是父目录这一层,内容没变 —— 而根目录的列举恰好是整棵树里最贵的一次,
// 每写一个文件就把它作废等于让缓存白做。
func (c *cachedFS) invalidate(p string, tree bool) {
	if c.ttl <= 0 {
		return
	}
	key := cacheKey(p)
	prefix := key + "/"
	if key == "/" {
		prefix = "/"
	}
	parent := path.Dir(key)
	if parent == key {
		parent = "" // 根自己,没有父目录
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.files {
		if k == key || (tree && strings.HasPrefix(k, prefix)) {
			delete(c.files, k)
		}
	}
	for k := range c.dirs {
		if k == key || (tree && strings.HasPrefix(k, prefix)) {
			delete(c.dirs, k)
		}
	}
	if parent != "" {
		delete(c.dirs, parent)
		delete(c.files, parent)
	}
}

func (c *cachedFS) OpenRead(ctx context.Context, p string) (vfs.ReadSeekCloser, error) {
	return c.core.OpenRead(ctx, p)
}

func (c *cachedFS) OpenWrite(ctx context.Context, p string) (vfs.WriteHandle, error) {
	return c.core.OpenWrite(ctx, p)
}

func (c *cachedFS) Remove(ctx context.Context, p string, recursive bool) error {
	if err := c.core.Remove(ctx, p, recursive); err != nil {
		return err
	}
	c.invalidate(p, true)
	return nil
}

func (c *cachedFS) Rename(ctx context.Context, oldPath, newPath string) error {
	if err := c.core.Rename(ctx, oldPath, newPath); err != nil {
		return err
	}
	c.invalidate(oldPath, true)
	c.invalidate(newPath, true)
	return nil
}
