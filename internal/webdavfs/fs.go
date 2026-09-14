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
}

// New 包装内核。内核与壳共享同一 bucket,壳不持有任何 S3 客户端。
func New(core vfs.FileSystem) *FileSystem {
	return &FileSystem{core: core}
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
	return nil
}

// stat 先查请求级缓存,未命中才落到内核。
func (fs *FileSystem) stat(ctx context.Context, logical string) (vfs.FileInfo, error) {
	if c := cacheOf(ctx); c != nil {
		if fi, ok := c.get(logical); ok {
			return fi, nil
		}
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
		infos, err := f.fs.core.ReadDir(f.ctx, f.name)
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
func (f *davFile) ensureReader() error {
	if f.r != nil {
		return nil
	}
	if f.info.IsDir() {
		return vfs.ErrNotSupported
	}
	r, err := f.fs.core.OpenRead(f.ctx, f.name)
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
