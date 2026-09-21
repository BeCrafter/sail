package smbfs

import (
	"container/list"
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
//
// 内存有上界(见下方预算常量与 evictLocked):缓存是必需品就意味着它一定会被
// 喂满,长时间跑在大桶上不能让「列过什么」一路涨上去。
type cachedFS struct {
	core   vfs.FileSystem
	ttl    time.Duration
	logger *log.Logger

	maxDirs  int
	maxBytes int64
	bytes    int64 // dirs + files 的估算占用

	mu sync.Mutex
	// dirs 与 files 共用一条 LRU(见 lru 字段),淘汰时从两端取最久未用者。
	dirs   map[string]cachedListing
	files  map[string]cachedFile
	flight map[string]*dirFlight
	// lru 的元素值是缓存键(队尾最新)。两张表各自持有自己那个 *list.Element,
	// 同一个键在两张表里可以各有一条 —— 淘汰时按元素摘除,不必反查是哪张表。
	lru *list.List
}

type cachedListing struct {
	at      time.Time
	entries []vfs.FileInfo
	bytes   int64
	elem    *list.Element
}

type cachedFile struct {
	at    time.Time
	info  vfs.FileInfo
	bytes int64
	elem  *list.Element
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

// 缓存的内存预算,口径与 internal/webdavfs 的 listingCache 一致(那一侧的理由
// 在这里同样成立:目录数上限在大目录上仍可能吃掉几百 MB,故再按字节兜一层)。
// 两处数值保持同步是有意的 —— 同一个 bucket 换个协议壳共享,缓存该不该把
// 内存吃穿不该跟着变。
const (
	defaultMaxCachedDirs  = 512
	defaultMaxCachedBytes = 64 << 20
	// fileInfoCost 是单条 FileInfo 的估算占用(切片头 + 各字段字符串)。
	fileInfoCost = 96
	// listingCost 是一条目录列表自身的固定开销(除条目以外的那点)。
	listingCost = 64
)

func newCachedFS(core vfs.FileSystem, ttl time.Duration, logger *log.Logger) *cachedFS {
	if ttl <= 0 {
		return &cachedFS{core: core}
	}
	return &cachedFS{
		core:     core,
		ttl:      ttl,
		logger:   logger,
		maxDirs:  defaultMaxCachedDirs,
		maxBytes: defaultMaxCachedBytes,
		dirs:     map[string]cachedListing{},
		files:    map[string]cachedFile{},
		flight:   map[string]*dirFlight{},
		lru:      list.New(),
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
	if ok && c.fresh(hit.at) {
		c.lru.MoveToBack(hit.elem)
		c.mu.Unlock()
		return hit.info, nil
	}
	c.mu.Unlock()
	fi, err := c.core.Stat(ctx, p)
	if err != nil {
		return vfs.FileInfo{}, err
	}
	c.mu.Lock()
	c.putFileLocked(key, cachedFile{at: time.Now(), info: fi, bytes: fileInfoCost})
	c.evictLocked()
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
	c.lru.MoveToBack(d.elem)
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
	c.putDirLocked(key, cachedListing{
		at:      now,
		entries: snapshot,
		bytes:   int64(len(snapshot))*fileInfoCost + listingCost,
	})
	for _, e := range entries {
		c.putFileLocked(cacheKey(e.Path), cachedFile{at: now, info: e, bytes: fileInfoCost})
	}
	c.evictLocked()
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
		// 每轮单独封顶:预热 goroutine 活得和进程一样久,不能让它抱着一个
		// 永不超时的调用卡死在一台不响应的后端上。
		fctx, cancel := context.WithTimeout(ctx, listingRefreshTimeout)
		_, err := c.fetch(fctx, key)
		cancel()
		if err != nil && c.logger != nil {
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
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.files {
		if k == key || (tree && strings.HasPrefix(k, prefix)) {
			c.dropFileLocked(k)
		}
	}
	for k := range c.dirs {
		if k == key || (tree && strings.HasPrefix(k, prefix)) {
			c.dropDirLocked(k)
		}
	}
	if parent := path.Dir(key); parent != key {
		c.dropDirLocked(parent)
		c.dropFileLocked(parent)
	}
}

// putDirLocked 记下一条目录列表并接管其 LRU 位置。调用方需持锁。
func (c *cachedFS) putDirLocked(key string, d cachedListing) {
	c.dropDirLocked(key)
	d.elem = c.lru.PushBack(key)
	c.dirs[key] = d
	c.bytes += d.bytes
}

// putFileLocked 记下一条条目元信息并接管其 LRU 位置。调用方需持锁。
func (c *cachedFS) putFileLocked(key string, f cachedFile) {
	c.dropFileLocked(key)
	f.elem = c.lru.PushBack(key)
	c.files[key] = f
	c.bytes += f.bytes
}

// dropDirLocked / dropFileLocked 摘掉一条缓存并同步预算与 LRU。
// 两张表分开摘:同一个键可能既是目录(列过它),又是父目录列表里的一个条目。
// 调用方需持锁。
func (c *cachedFS) dropDirLocked(key string) {
	if d, ok := c.dirs[key]; ok {
		c.bytes -= d.bytes
		delete(c.dirs, key)
		c.lru.Remove(d.elem)
	}
}

func (c *cachedFS) dropFileLocked(key string) {
	if f, ok := c.files[key]; ok {
		c.bytes -= f.bytes
		delete(c.files, key)
		c.lru.Remove(f.elem)
	}
}

// evictLocked 在超出预算(目录数或估算字节)时淘汰最久未用的缓存,直到回到
// 预算内。调用方需持锁。
//
// 用 LRU 链表而不是「扫一遍找 used 最小者」:files 表是**按条目**写入的,预算
// 内可以驻留几十万条,每次淘汰都做一次全表扫描会让一次大目录列举退化成 O(n²)。
// 链表让每次淘汰都是 O(1),代价是每个条目多一个元素指针。
//
// 单条列表本身就超预算时(百万级目录)会被写进去又立刻淘汰 —— 于它而言缓存
// 等于不存在,每次都重新列举。这是有意的:内存上界比那一个目录的命中率重要,
// 而它已经大到不该常驻。
func (c *cachedFS) evictLocked() {
	for len(c.dirs) > c.maxDirs || c.bytes > c.maxBytes {
		front := c.lru.Front()
		if front == nil {
			return
		}
		key, _ := front.Value.(string)
		// 一个键在两张表里各有一条,一次摘掉这一对:淘汰只求把内存降下来,
		// 少留一条不影响正确性。
		c.dropDirLocked(key)
		c.dropFileLocked(key)
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
