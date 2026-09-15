// Package webdavfs 是 WebDAV 协议壳:把 vfs.FileSystem 翻译成
// golang.org/x/net/webdav 需要的 FileSystem / File,并提供 HTTP 外壳
// (Basic 认证、上传体长闸门、目录级 MOVE/COPY 退化)。
//
// 本包是唯一允许 import webdav 的地方;它不直接调用 S3。
package webdavfs

import (
	"context"
	"errors"
	"io"
	"mime"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/BeCrafter/sail/internal/vfs"
	"golang.org/x/net/webdav"
)

// sailReservedDir 是内核保留前缀,列目录时过滤掉(S3 上不存在真正的目录,
// 这里只按名字过滤一层,规则先立着)。
const sailReservedDir = ".sail"

// FileSystem 把 vfs.FileSystem 适配成 webdav.FileSystem。
type FileSystem struct {
	core vfs.FileSystem
	// dirs 是服务级目录列表缓存;nil 表示禁用。
	// Finder 反复对同一目录发 PROPFIND,每次都要向后端列一次(根目录分页、
	// 跨区域可达 ~0.9s;超大目录可达数十秒);缓存吸收这层重复读,
	// 配合 stale-while-revalidate 与并发合并,不与后端分页次数相乘。
	dirs *listingCache
}

// New 包装内核。内核与壳共享同一 bucket,壳不持有任何 S3 客户端。
// 不开目录列表缓存(单次请求内的重复 Stat 仍走请求级 readCache)。
func New(core vfs.FileSystem) *FileSystem {
	return &FileSystem{core: core}
}

// NewWithListingCache 同 New,但开启服务级目录列表缓存。
// ttl <= 0 等同 New(不缓存)。
// 预热通过 Server 的 Config.PrewarmDirs 驱动(见 Server.prewarmDirs)。
func NewWithListingCache(core vfs.FileSystem, ttl time.Duration) *FileSystem {
	fs := &FileSystem{core: core}
	if ttl > 0 {
		fs.dirs = newListingCache(ttl)
	}
	return fs
}

// Stat 实现 webdav.FileSystem。
func (fs *FileSystem) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	logical, err := davPath(name)
	if err != nil {
		return nil, err
	}
	fi, err := fs.stat(ctx, logical)
	if err != nil {
		return nil, err
	}
	return davFileInfo{fi}, nil
}

// Mkdir 写一个 0 字节、key 以 "/" 结尾的目录标记对象:否则 Finder/Explorer
// 里新建的文件夹刷新即消失(S3 没有目录,共同前缀无法自证存在)。
//
// 已存在时按 os.Mkdir 语义返回 ErrExist(webdav 映射成 405):既避免覆盖已有
// 目录,也避免在同名文件旁多出一个 "x/" 标记对象、让列表里出现两条同名项。
func (fs *FileSystem) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	logical, err := davPath(name)
	if err != nil {
		return err
	}
	if logical == "/" {
		return vfs.ErrExist
	}
	if _, err := fs.core.Stat(ctx, logical); err == nil {
		return vfs.ErrExist
	} else if !errors.Is(err, vfs.ErrNotExist) {
		return err
	}
	w, err := fs.core.OpenWrite(ctx, logical+"/")
	if err != nil {
		return err
	}
	if _, err := w.Commit(ctx); err != nil {
		w.Close()
		return err
	}
	invalidateCache(ctx, logical)
	fs.invalidateDir(logical)
	return w.Close()
}

// OpenFile 实现 webdav.FileSystem。读路径不做任何 S3 请求:
// 元信息来自请求级缓存,真正的 GetObject 推迟到第一次 Read/Seek。
func (fs *FileSystem) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	logical, err := davPath(name)
	if err != nil {
		return nil, err
	}
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC) != 0 {
		w, err := fs.core.OpenWrite(ctx, logical)
		if err != nil {
			return nil, err
		}
		if opt, ok := w.(vfs.WriteOptioner); ok {
			opt.SetWriteOptions(vfs.WriteOptions{
				ContentType:   requestContentType(ctx),
				ContentLength: requestContentLength(ctx),
			})
		}
		return &davFile{ctx: ctx, fs: fs, name: logical, w: w}, nil
	}

	fi, err := fs.stat(ctx, logical)
	if err != nil {
		return nil, err
	}
	if ct := fi.ContentType; ct != "" {
		registerContentType(ctx, ct)
	}
	return &davFile{ctx: ctx, fs: fs, name: logical, info: davFileInfo{fi}}, nil
}

// RemoveAll 实现 webdav.FileSystem。webdav 的 DELETE 一律走这里,
// 目录删除等价于删掉该前缀下的全部对象(含目录标记对象)。
func (fs *FileSystem) RemoveAll(ctx context.Context, name string) error {
	logical, err := davPath(name)
	if err != nil {
		return err
	}
	if logical == "/" {
		return os.ErrInvalid
	}
	if err := fs.core.Remove(ctx, logical, true); err != nil {
		return err
	}
	invalidateCache(ctx, logical)
	fs.invalidateDir(logical)
	return nil
}

// Rename 实现 webdav.FileSystem。目录级由 HTTP 外壳提前拦成 501,
// 内核的 ErrNotSupported 只是兜底。
func (fs *FileSystem) Rename(ctx context.Context, oldName, newName string) error {
	oldLogical, err := davPath(oldName)
	if err != nil {
		return err
	}
	newLogical, err := davPath(newName)
	if err != nil {
		return err
	}
	if oldLogical == "/" || newLogical == "/" {
		return os.ErrInvalid
	}
	if err := fs.core.Rename(ctx, oldLogical, newLogical); err != nil {
		return err
	}
	invalidateCache(ctx, oldLogical)
	invalidateCache(ctx, newLogical)
	fs.invalidateDir(oldLogical)
	fs.invalidateDir(newLogical)
	return nil
}

// stat 先查请求级缓存,未命中才落到内核。
// 服务级目录列表缓存里已有的目录,直接按目录返回——省掉内核为「只有共同前缀、
// 没有标记对象」的目录发的那次存在性探测 List。
func (fs *FileSystem) stat(ctx context.Context, logical string) (vfs.FileInfo, error) {
	if c := cacheOf(ctx); c != nil {
		if fi, ok := c.get(logical); ok {
			return fi, nil
		}
	}
	if fs.dirs != nil && fs.dirs.isDir(logical) {
		fi := vfs.FileInfo{Name: davBaseName(logical), Path: logical, IsDir: true}
		if c := cacheOf(ctx); c != nil {
			c.put(fi)
		}
		return fi, nil
	}
	fi, err := fs.core.Stat(ctx, logical)
	if err != nil {
		return vfs.FileInfo{}, err
	}
	if c := cacheOf(ctx); c != nil {
		c.put(fi)
	}
	return fi, nil
}

// readDir 先查服务级目录列表缓存,未命中才落到内核。
//
// 三级策略:
//  1. 命中且未过期 → 直接返回(亚毫秒);
//  2. 命中但已过期 → 先返回旧值,后台异步刷新(stale-while-revalidate),
//     于是「浏览→刷新→再进」不再阻塞;
//  3. 完全没有 → 阻塞取一次,但同一路径的并发请求会被合并成一次
//     (singleflight),避免 Finder 并发 PROPFIND 把后端分页放大数倍。
//
// 缓存键在 listingCache 内部归一化(`/image` 与 `/image/` 命中同一条):
// 否则预热的键与请求的键对不上,预热形同虚设。
func (fs *FileSystem) readDir(ctx context.Context, logical string) ([]vfs.FileInfo, error) {
	if fs.dirs == nil {
		return fs.core.ReadDir(ctx, logical)
	}
	if fis, ok := fs.dirs.get(logical); ok {
		return fis, nil
	}
	// 已过期但仍有旧值:先给旧的,后台刷新。
	if fis, ok := fs.dirs.getStale(logical); ok {
		fs.dirs.refreshAsync(logical, func() ([]vfs.FileInfo, error) {
			return fs.fetchListing(ctx, logical)
		})
		return fis, nil
	}
	return fs.dirs.fetch(logical, func() ([]vfs.FileInfo, error) {
		return fs.fetchListing(ctx, logical)
	})
}

// listingFetchTimeout 是单次目录列举的上限,防止后台刷新无限期占用连接。
// 大目录(十几万条、上百页)在后端慢时可能接近这个量级,故给得较宽。
const listingFetchTimeout = 10 * time.Minute

// fetchListing 用「不随请求取消」的上下文列举目录。
//
// 关键:客户端(PROPFIND)可能因超时而断开,但不该因此中断这次列举 ——
// 否则永远热不了缓存,下次仍要从零开始。detach 后让它跑完并入缓存,
// 外部用一个兜底超时封顶。
func (fs *FileSystem) fetchListing(ctx context.Context, logical string) ([]vfs.FileInfo, error) {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), listingFetchTimeout)
	defer cancel()
	return fs.core.ReadDir(fctx, logical)
}

// invalidateDir 让某路径及其祖先目录的列表缓存立即失效。
// 写操作(建/删/改名)会影响父目录的列表,故按父路径链逐级失效。
func (fs *FileSystem) invalidateDir(logical string) {
	if fs.dirs == nil {
		return
	}
	fs.dirs.invalidatePath(logical)
}

// prewarm 后台预热若干目录:立即列一次并入缓存,此后按 TTL 周期刷新,
// 使其在 Finder 访问前就处于「热」状态。用于个别超大目录——它们的首次
// 列举可能长达数十秒,预热把这份代价挪到用户点击之前。
func (fs *FileSystem) prewarm(ctx context.Context, dirs []string) {
	if fs.dirs == nil || len(dirs) == 0 {
		return
	}
	for _, d := range dirs {
		logical, err := davPath(d)
		if err != nil {
			continue
		}
		go fs.prewarmLoop(ctx, logical)
	}
}

func (fs *FileSystem) prewarmLoop(ctx context.Context, logical string) {
	for {
		fis, err := fs.fetchListing(ctx, logical)
		if err == nil {
			fs.dirs.put(logical, fis)
		}
		// 按 TTL 的一半刷新,保证过期前就有新值,读路径几乎总能命中新鲜值。
		interval := fs.dirs.ttl / 2
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

// listingCache 是服务级目录列表缓存,带 TTL。用于吸收 Finder 反复 PROPFIND
// 同一目录的重复读;任意写操作都会让受影响的路径立即失效,TTL 只是兜底上界。
//
// 已知取舍:外部(非本网关)对桶的改动最长 TTL 后才可见。
// 内存:每条目录列表驻留内存,超大目录(十万级)单条可达数十 MB;
// maxDirs 按目录数封顶,超出时淘汰最久未更新的条目。
type listingCache struct {
	ttl     time.Duration
	maxDirs int

	mu       sync.Mutex
	items    map[string]listingEntry
	inflight map[string]*inflightCall
	// now 可注入以便测试;默认 time.Now。
	now func() time.Time
}

type listingEntry struct {
	fis     []vfs.FileInfo
	expires time.Time
	// used 是该条目最近一次被读取的序号,用于淘汰最久未用者。
	used int64
}

// inflightCall 是一次进行中的列举,供同一路径的并发请求等待复用。
type inflightCall struct {
	done chan struct{}
	fis  []vfs.FileInfo
	err  error
}

const defaultMaxCachedDirs = 512

func newListingCache(ttl time.Duration) *listingCache {
	return &listingCache{
		ttl:      ttl,
		maxDirs:  defaultMaxCachedDirs,
		items:    map[string]listingEntry{},
		inflight: map[string]*inflightCall{},
		now:      time.Now,
	}
}

// norm 归一化键:去掉尾随 "/"(根保持 "/")。所有读写入口统一用归一化键,
// 使 "/image" 与 "/image/" 命中同一条,调用方无需关心尾随斜杠。
func norm(logical string) string {
	k := strings.TrimSuffix(logical, "/")
	if k == "" {
		return "/"
	}
	return k
}

// get 返回未过期的条目。
func (c *listingCache) get(logical string) ([]vfs.FileInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[norm(logical)]
	if !ok {
		return nil, false
	}
	if c.now().After(e.expires) {
		return nil, false
	}
	e.used = c.now().UnixNano()
	c.items[norm(logical)] = e
	return e.fis, true
}

// getStale 返回已过期但仍在缓存里的条目(供 stale-while-revalidate)。
func (c *listingCache) getStale(logical string) ([]vfs.FileInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := norm(logical)
	e, ok := c.items[k]
	if !ok {
		return nil, false
	}
	e.used = c.now().UnixNano()
	c.items[k] = e
	return e.fis, true
}

func (c *listingCache) put(logical string, fis []vfs.FileInfo) {
	// 必须深拷贝:缓存的切片不能与调用方的共享底层数组(调用方可能复用/改写)。
	copyFis := make([]vfs.FileInfo, len(fis))
	copy(copyFis, fis)
	c.mu.Lock()
	defer c.mu.Unlock()
	k := norm(logical)
	c.items[k] = listingEntry{fis: copyFis, expires: c.now().Add(c.ttl), used: c.now().UnixNano()}
	c.evictLocked()
}

// evictLocked 在超出 maxDirs 时淘汰最久未使用的条目。调用方需持有锁。
func (c *listingCache) evictLocked() {
	for len(c.items) > c.maxDirs {
		oldestKey, oldestUsed := "", int64(0)
		first := true
		for k, e := range c.items {
			if first || e.used < oldestUsed {
				oldestKey, oldestUsed, first = k, e.used, false
			}
		}
		if oldestKey == "" {
			return
		}
		delete(c.items, oldestKey)
	}
}

// fetch 合并同一路径的并发列举:首个调用者真正执行 fetchFn,其余等待其结果。
func (c *listingCache) fetch(logical string, fetchFn func() ([]vfs.FileInfo, error)) ([]vfs.FileInfo, error) {
	k := norm(logical)
	c.mu.Lock()
	if call, ok := c.inflight[k]; ok {
		c.mu.Unlock()
		<-call.done
		return call.fis, call.err
	}
	call := &inflightCall{done: make(chan struct{})}
	c.inflight[k] = call
	c.mu.Unlock()

	call.fis, call.err = fetchFn()
	if call.err == nil {
		c.put(k, call.fis)
	}
	c.mu.Lock()
	delete(c.inflight, k)
	c.mu.Unlock()
	close(call.done)
	return call.fis, call.err
}

// refreshAsync 在后台刷新一条已过期的缓存;同一路径已有在途刷新时不重复触发。
func (c *listingCache) refreshAsync(logical string, fetchFn func() ([]vfs.FileInfo, error)) {
	k := norm(logical)
	c.mu.Lock()
	if _, ok := c.inflight[k]; ok {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	go c.fetch(k, fetchFn)
}

// isDir 判定某逻辑路径是否已在缓存中登记为一个「已列举过的目录」(即确认存在)。
// 用于让 Stat 直接命中,省掉内核的存在性探测请求。
func (c *listingCache) isDir(logical string) bool {
	_, ok := c.get(logical)
	return ok
}

// invalidatePath 失效 path 自身、其父目录,以及其子树的列表缓存。
// 语义与请求级 readCache.invalidate 一致,但额外删除「父目录」条目:
// 在 /a/b 下新建文件会改变 /a 的列表,只失效 /a/b 不够。
// 键已归一化(无尾随斜杠),这里按同一规范比较。
func (c *listingCache) invalidatePath(logical string) {
	trimmed := norm(logical)
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.items {
		// 自身、子树,或祖先(k 是 trimmed 的祖先:根,或 trimmed 以 k+"/" 开头)。
		// 归一化后键无尾随斜杠、根为 "/",故祖先判定必须特判根。
		if k == trimmed || strings.HasPrefix(k, trimmed+"/") || isAncestorKey(k, trimmed) {
			delete(c.items, k)
		}
	}
}

// isAncestorKey 判定 k 是否为 trimmed 的祖先目录(含根)。
// 两者均为归一化键(无尾随斜杠,根为 "/")。
func isAncestorKey(k, trimmed string) bool {
	if k == "/" {
		return trimmed != "/"
	}
	return strings.HasPrefix(trimmed, k+"/")
}

// davBaseName 取逻辑路径最后一段(忽略尾随 "/");根返回 ""。
func davBaseName(logical string) string {
	s := strings.TrimSuffix(logical, "/")
	if s == "" || s == "/" {
		return ""
	}
	return s[strings.LastIndex(s, "/")+1:]
}

// davPath 规范 WebDAV 请求路径:强制以 "/" 开头,拒绝任何 ".." 段,
// 与内核路径语义保持一致(越界请求不产生任何跨前缀访问)。
func davPath(name string) (string, error) {
	if name == "" {
		return "/", nil
	}
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return "", os.ErrNotExist
		}
	}
	trailing := len(name) > 1 && strings.HasSuffix(name, "/")
	clean := path.Clean(name)
	if clean == "/" {
		return "/", nil
	}
	if trailing {
		clean += "/"
	}
	return clean, nil
}

// davFileInfo 是 os.FileInfo 的 WebDAV 侧实现,并额外实现 webdav.ETager 与
// webdav.ContentTyper 两个可选接口。
type davFileInfo struct {
	vfs.FileInfo
}

func (f davFileInfo) Name() string       { return f.FileInfo.Name }
func (f davFileInfo) Size() int64        { return f.FileInfo.Size }
func (f davFileInfo) ModTime() time.Time { return f.FileInfo.ModTime }
func (f davFileInfo) IsDir() bool        { return f.FileInfo.IsDir }
func (f davFileInfo) Sys() any           { return nil }

func (f davFileInfo) Mode() os.FileMode {
	if f.FileInfo.IsDir {
		return os.ModeDir | 0o755
	}
	return 0o644
}

// ETag 取自已提交对象。没有 ETag 时必须显式返回 webdav.ErrNotImplemented,
// 让库退化为启发式——返回空字符串会让客户端拿空 ETag 做缓存校验。
func (f davFileInfo) ETag(context.Context) (string, error) {
	if f.FileInfo.ETag == "" {
		return "", webdav.ErrNotImplemented
	}
	return `"` + f.FileInfo.ETag + `"`, nil
}

// ContentType 优先用对象里存的 Content-Type;列目录拿不到时按扩展名推断,
// 仍无结果则给 application/octet-stream。绝不返回 ErrNotImplemented——
// 那会让库为每个条目发一次 GetObject 去嗅探首字节。
func (f davFileInfo) ContentType(context.Context) (string, error) {
	if f.FileInfo.ContentType != "" {
		return f.FileInfo.ContentType, nil
	}
	if f.FileInfo.IsDir {
		return "", webdav.ErrNotImplemented
	}
	if ct := mime.TypeByExtension(path.Ext(f.FileInfo.Name)); ct != "" {
		return ct, nil
	}
	return "application/octet-stream", nil
}

// davFile 是 webdav.File 的实现。同一类型承担读、写、目录三种角色:
// 只有对应的字段被设置,其他方法报错。
type davFile struct {
	ctx  context.Context
	fs   *FileSystem
	name string
	info davFileInfo

	r        vfs.ReadSeekCloser
	w        vfs.WriteHandle
	statDone bool

	children []os.FileInfo
	childIdx int
}

func (f *davFile) Stat() (os.FileInfo, error) {
	if f.w != nil {
		// 提交点:webdav 的 PUT 是 Write → Stat → Close,Close 返回非 nil
		// 会被判 405,所以提交必须挂在这里而不是 Close。
		f.statDone = true
		fi, err := f.w.Commit(f.ctx)
		if err != nil {
			return nil, err
		}
		invalidateCache(f.ctx, f.name)
		f.fs.invalidateDir(f.name)
		return davFileInfo{fi}, nil
	}
	return f.info, nil
}

func (f *davFile) Read(p []byte) (int, error) {
	if err := f.ensureReader(); err != nil {
		return 0, err
	}
	return f.r.Read(p)
}

func (f *davFile) Seek(offset int64, whence int) (int64, error) {
	if err := f.ensureReader(); err != nil {
		return 0, err
	}
	return f.r.Seek(offset, whence)
}

func (f *davFile) Write(p []byte) (int, error) {
	if f.w == nil {
		return 0, vfs.ErrNotSupported
	}
	n, err := f.w.Write(p)
	if err != nil {
		// 只登记可映射成 413/507 的错误;其余交给 webdav 的默认状态码。
		registerFatal(f.ctx, err)
	}
	return n, err
}

func (f *davFile) Readdir(count int) ([]os.FileInfo, error) {
	if f.children == nil {
		infos, err := f.fs.readDir(f.ctx, f.name)
		if err != nil {
			return nil, err
		}
		out := make([]os.FileInfo, 0, len(infos))
		for _, fi := range infos {
			if fi.Name == sailReservedDir {
				continue
			}
			out = append(out, davFileInfo{fi})
		}
		if c := cacheOf(f.ctx); c != nil {
			c.putAll(infos)
		}
		f.children = out
	}
	if count <= 0 {
		out := f.children
		f.children = nil
		f.childIdx = 0
		return out, nil
	}
	if f.childIdx >= len(f.children) {
		return nil, io.EOF
	}
	end := f.childIdx + count
	if end > len(f.children) {
		end = len(f.children)
	}
	out := f.children[f.childIdx:end]
	f.childIdx = end
	return out, nil
}

func (f *davFile) Close() error {
	if f.r != nil {
		f.r.Close()
		f.r = nil
	}
	if f.w != nil {
		w := f.w
		f.w = nil
		if !f.statDone {
			// webdav 的 COPY 路径只 Write + Close、不调 Stat(见 x/net/webdav
			// copyFiles):不在此补一次提交,暂存就会被清掉、目标对象静默消失。
			if _, err := w.Commit(f.ctx); err != nil {
				w.Close()
				return err
			}
			invalidateCache(f.ctx, f.name)
		}
		return w.Close()
	}
	return nil
}

// ensureReader 把 GetObject 推迟到真正读的时候:PROPFIND 只调 Stat/Readdir,
// 因此列目录不会为每个条目建立读取句柄。
//
// 打开时优先走 InfoOpener:OpenFile 已经 Stat 过,把这份 FileInfo 传下去可省掉
// OpenRead 内部重复的一次 HEAD(慢后端上少一个 RTT)。内核未实现该接口时退回 OpenRead。
func (f *davFile) ensureReader() error {
	if f.r != nil {
		return nil
	}
	if f.info.IsDir() {
		return vfs.ErrNotSupported
	}
	core := f.fs.core
	if opener, ok := core.(vfs.InfoOpener); ok {
		r, err := opener.OpenReadWithInfo(f.ctx, f.name, f.info.FileInfo)
		if err != nil {
			return err
		}
		f.r = r
		return nil
	}
	r, err := core.OpenRead(f.ctx, f.name)
	if err != nil {
		return err
	}
	f.r = r
	return nil
}

// readCache 是请求级元信息缓存。x/net/webdav 的 PROPFIND Depth:1 会先列目录、
// 再对每个子项调 Stat 与 OpenFile(只为拿 Stat),没有缓存就会退化成
// 每项两次 HeadObject。缓存只活在一个请求内,不跨请求,不存在陈旧问题。
type readCache struct {
	mu sync.RWMutex
	m  map[string]vfs.FileInfo
}

func newReadCache() *readCache {
	return &readCache{m: map[string]vfs.FileInfo{}}
}

func (c *readCache) get(logical string) (vfs.FileInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	fi, ok := c.m[logical]
	return fi, ok
}

func (c *readCache) put(fi vfs.FileInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[fi.Path] = fi
}

func (c *readCache) putAll(fis []vfs.FileInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, fi := range fis {
		c.m[fi.Path] = fi
	}
}

func (c *readCache) invalidate(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	trimmed := strings.TrimSuffix(prefix, "/")
	for k := range c.m {
		if k == prefix || k == trimmed || strings.HasPrefix(k, trimmed+"/") {
			delete(c.m, k)
		}
	}
}
