// Package main 是 CRAF-1 阶段 0 的选型 spike:把 sail 的 vfs 内核接到两个
// 候选 SMB 服务端库上,用同一套验收脚本比接口契合度与真实客户端行为。
//
// 这是**一次性**代码,不进正式交付:定标后按方案重写 internal/smbfs/。
package main

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/BeCrafter/sail/internal/vfs"
)

// coreOps 把协议无关的 vfs.FileSystem 收敛成两个候选库都要的那几个原语。
type coreOps struct{ fs vfs.FileSystem }

// smbPath 把 SMB 的 "\dir\file" 归一成内核逻辑路径 "/dir/file"。
func smbPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if p == "" || p == "." {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return path.Clean(p)
}

func (c *coreOps) mkdir(ctx context.Context, p string) error {
	if p == "/" {
		return vfs.ErrExist
	}
	if _, err := c.fs.Stat(ctx, p); err == nil {
		return vfs.ErrExist
	} else if !errors.Is(err, vfs.ErrNotExist) {
		return err
	}
	// 与 webdavfs.Mkdir 同款:0 字节、key 以 "/" 结尾的目录标记对象。
	w, err := c.fs.OpenWrite(ctx, p+"/")
	if err != nil {
		return err
	}
	if _, err := w.Commit(ctx); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

func (c *coreOps) remove(ctx context.Context, p string, recursive bool) error {
	if p == "/" {
		return os.ErrInvalid
	}
	return c.fs.Remove(ctx, p, recursive)
}

func (c *coreOps) rename(ctx context.Context, oldPath, newPath string) error {
	if oldPath == "/" || newPath == "/" {
		return os.ErrInvalid
	}
	return c.fs.Rename(ctx, oldPath, newPath)
}

// writeAdmitter 由写句柄可选实现(quotafs 的配额会计):提交前做配额准入。
type writeAdmitter interface {
	AdmitWrite(ctx context.Context, contentLength int64) error
}

// logFS 是 spike 期的诊断装饰器:把每次内核调用的耗时打进日志,
// 用来区分「客户端在等什么」与「后端有多慢」。
type logFS struct {
	fs  vfs.FileSystem
	log *log.Logger
}

func (l *logFS) trace(op, p string, start time.Time, err error) {
	l.log.Printf("%-10s %-40s %8.1fms err=%v", op, p, float64(time.Since(start).Microseconds())/1000, err)
}

func (l *logFS) Stat(ctx context.Context, p string) (vfs.FileInfo, error) {
	t := time.Now()
	fi, err := l.fs.Stat(ctx, p)
	l.trace("Stat", p, t, err)
	return fi, err
}

func (l *logFS) ReadDir(ctx context.Context, p string) ([]vfs.FileInfo, error) {
	t := time.Now()
	fi, err := l.fs.ReadDir(ctx, p)
	l.trace("ReadDir", p, t, err)
	return fi, err
}

func (l *logFS) OpenRead(ctx context.Context, p string) (vfs.ReadSeekCloser, error) {
	t := time.Now()
	r, err := l.fs.OpenRead(ctx, p)
	l.trace("OpenRead", p, t, err)
	return r, err
}

func (l *logFS) OpenWrite(ctx context.Context, p string) (vfs.WriteHandle, error) {
	t := time.Now()
	w, err := l.fs.OpenWrite(ctx, p)
	l.trace("OpenWrite", p, t, err)
	return w, err
}

func (l *logFS) Remove(ctx context.Context, p string, recursive bool) error {
	t := time.Now()
	err := l.fs.Remove(ctx, p, recursive)
	l.trace("Remove", p, t, err)
	return err
}

func (l *logFS) Rename(ctx context.Context, oldPath, newPath string) error {
	t := time.Now()
	err := l.fs.Rename(ctx, oldPath, newPath)
	l.log.Printf("%-10s %-40s %8.1fms -> %s err=%v", "Rename", oldPath,
		float64(time.Since(t).Microseconds())/1000, newPath, err)
	return err
}

// cachedFS 验证「目录列表缓存能不能把逐条 Stat 吃掉」:ReadDir 一次就把
// 每条目的 FileInfo 记下来,后续 Stat 命中缓存(0 次后端调用)。写/删/改名
// 一律粗粒度失效。
type cachedFS struct {
	fs  vfs.FileSystem
	ttl time.Duration

	mu    sync.Mutex
	dirs  map[string]cachedDir
	files map[string]cachedFile
}

type cachedDir struct {
	at      time.Time
	entries []vfs.FileInfo
}

type cachedFile struct {
	at   time.Time
	info vfs.FileInfo
}

func newCachedFS(fs vfs.FileSystem, ttl time.Duration) *cachedFS {
	return &cachedFS{fs: fs, ttl: ttl, dirs: map[string]cachedDir{}, files: map[string]cachedFile{}}
}

func (c *cachedFS) fresh(t time.Time) bool { return time.Since(t) < c.ttl }

func (c *cachedFS) Stat(ctx context.Context, p string) (vfs.FileInfo, error) {
	if c.ttl > 0 {
		c.mu.Lock()
		if f, ok := c.files[p]; ok && c.fresh(f.at) {
			c.mu.Unlock()
			return f.info, nil
		}
		c.mu.Unlock()
	}
	fi, err := c.fs.Stat(ctx, p)
	if err == nil && c.ttl > 0 {
		c.mu.Lock()
		c.files[p] = cachedFile{at: time.Now(), info: fi}
		c.mu.Unlock()
	}
	return fi, err
}

func (c *cachedFS) ReadDir(ctx context.Context, p string) ([]vfs.FileInfo, error) {
	if c.ttl > 0 {
		c.mu.Lock()
		if d, ok := c.dirs[p]; ok && c.fresh(d.at) {
			c.mu.Unlock()
			return d.entries, nil
		}
		c.mu.Unlock()
	}
	entries, err := c.fs.ReadDir(ctx, p)
	if err != nil {
		return nil, err
	}
	if c.ttl > 0 {
		c.mu.Lock()
		now := time.Now()
		c.dirs[p] = cachedDir{at: now, entries: entries}
		for _, e := range entries {
			c.files[e.Path] = cachedFile{at: now, info: e}
		}
		c.mu.Unlock()
	}
	return entries, nil
}

func (c *cachedFS) invalidate(p string) {
	if c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	delete(c.files, p)
	delete(c.dirs, p)
	// 目录内容变了,父目录的列表也失效
	if i := strings.LastIndex(p, "/"); i >= 0 {
		parent := p[:i]
		if parent == "" {
			parent = "/"
		}
		delete(c.dirs, parent)
	}
	c.mu.Unlock()
}

func (c *cachedFS) OpenRead(ctx context.Context, p string) (vfs.ReadSeekCloser, error) {
	return c.fs.OpenRead(ctx, p)
}

func (c *cachedFS) OpenWrite(ctx context.Context, p string) (vfs.WriteHandle, error) {
	c.invalidate(p)
	return c.fs.OpenWrite(ctx, p)
}

func (c *cachedFS) Remove(ctx context.Context, p string, recursive bool) error {
	c.invalidate(strings.TrimSuffix(p, "/"))
	return c.fs.Remove(ctx, p, recursive)
}

func (c *cachedFS) Rename(ctx context.Context, oldPath, newPath string) error {
	c.invalidate(oldPath)
	c.invalidate(newPath)
	return c.fs.Rename(ctx, oldPath, newPath)
}

// spikeFile 是两库共用的文件句柄。读直通内核;一旦发生写,落到本地暂存
// 文件(SMB2 WRITE 带 64 位 offset,而 vfs.WriteHandle 只有顺序 io.Writer),
// Sync/Close 时把暂存顺序灌进 vfs.OpenWrite 提交。顺带满足 read-your-writes。
type spikeFile struct {
	ctx  context.Context
	ops  *coreOps
	path string

	// noLoad 为真表示这次打开要求「空文件起步」(CREATE/SUPERSEDE 等),
	// 首次建暂存时不预载既有对象内容。
	noLoad bool

	mu     sync.Mutex
	ro     vfs.ReadSeekCloser
	st     *os.File
	loaded bool
	dirty  bool
	closed bool
}

func newSpikeFile(ctx context.Context, ops *coreOps, p string, fresh bool) (*spikeFile, error) {
	f := &spikeFile{ctx: ctx, ops: ops, path: p, noLoad: fresh}
	if fresh {
		f.mu.Lock()
		err := f.ensureStaging()
		f.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	return f, nil
}

// ensureStaging 建暂存文件;非 fresh 打开时先把既有对象灌进来,让后续
// 定位写表现为「读改写」而不是「整文件替换」。调用方须持 f.mu。
func (f *spikeFile) ensureStaging() error {
	if f.st != nil {
		return nil
	}
	tmp, err := os.CreateTemp("", "sail-smb-spike-*")
	if err != nil {
		return err
	}
	f.st = tmp
	if f.noLoad {
		f.loaded = true
		return nil
	}
	return f.loadFromBackend()
}

// loadFromBackend 把既有对象内容读进暂存。调用方须持 f.mu。
func (f *spikeFile) loadFromBackend() error {
	f.loaded = true
	r, err := f.ops.fs.OpenRead(f.ctx, f.path)
	if errors.Is(err, vfs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer r.Close()
	_, err = io.Copy(f.st, r)
	return err
}

func (f *spikeFile) readAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, os.ErrClosed
	}
	if f.st != nil {
		if !f.loaded {
			if err := f.loadFromBackend(); err != nil {
				return 0, err
			}
		}
		return f.st.ReadAt(p, off)
	}
	return f.readBackend(p, off)
}

// readBackend 按 ReaderAt 契约读内核(SMB 侧大量随机读走这条路)。
func (f *spikeFile) readBackend(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, os.ErrInvalid
	}
	if f.ro == nil {
		r, err := f.ops.fs.OpenRead(f.ctx, f.path)
		if err != nil {
			return 0, err
		}
		f.ro = r
	}
	if _, err := f.ro.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}
	n, err := io.ReadFull(f.ro, p)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		err = io.EOF
	}
	return n, err
}

func (f *spikeFile) writeAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, os.ErrClosed
	}
	if off < 0 {
		return 0, os.ErrInvalid
	}
	if err := f.ensureStaging(); err != nil {
		return 0, err
	}
	if !f.loaded {
		if err := f.loadFromBackend(); err != nil {
			return 0, err
		}
	}
	n, err := f.st.WriteAt(p, off)
	if n > 0 {
		f.dirty = true
	}
	return n, err
}

// info 返回句柄视角的元信息:有暂存就以暂存为准(SMB 客户端在
// CREATE 之后立刻 Stat 一次,那时对象在 S3 里还不存在),否则落内核。
func (f *spikeFile) info() (vfs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.st != nil {
		fi, err := f.st.Stat()
		if err != nil {
			return vfs.FileInfo{}, err
		}
		return vfs.FileInfo{
			Name:    path.Base(f.path),
			Path:    f.path,
			Size:    fi.Size(),
			ModTime: time.Now(),
		}, nil
	}
	return f.ops.fs.Stat(f.ctx, f.path)
}

// staged 表示这个句柄已有本地暂存(Size/Stat 需要改看暂存)。
func (f *spikeFile) staged() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st != nil
}

func (f *spikeFile) size() (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.st != nil {
		fi, err := f.st.Stat()
		if err != nil {
			return 0, err
		}
		return fi.Size(), nil
	}
	fi, err := f.ops.fs.Stat(f.ctx, f.path)
	if err != nil {
		return 0, err
	}
	return fi.Size, nil
}

func (f *spikeFile) truncate(n int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return os.ErrClosed
	}
	if n < 0 {
		return os.ErrInvalid
	}
	if err := f.ensureStaging(); err != nil {
		return err
	}
	if !f.loaded {
		if err := f.loadFromBackend(); err != nil {
			return err
		}
	}
	f.dirty = true
	return f.st.Truncate(n)
}

// sync 就是提交点:把暂存灌进内核并落 S3。之后还能继续写(暂存保留)。
func (f *spikeFile) sync() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.dirty {
		return nil
	}
	return f.commitLocked()
}

func (f *spikeFile) close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	var err error
	if f.dirty {
		err = f.commitLocked()
	}
	if f.st != nil {
		name := f.st.Name()
		f.st.Close()
		os.Remove(name)
		f.st = nil
	}
	if f.ro != nil {
		f.ro.Close()
		f.ro = nil
	}
	return err
}

// commitLocked 把暂存内容顺序灌进 vfs.OpenWrite 并提交。调用方须持 f.mu。
func (f *spikeFile) commitLocked() error {
	fi, err := f.st.Stat()
	if err != nil {
		return err
	}
	w, err := f.ops.fs.OpenWrite(f.ctx, f.path)
	if err != nil {
		return err
	}
	if opt, ok := w.(vfs.WriteOptioner); ok {
		opt.SetWriteOptions(vfs.WriteOptions{ContentLength: fi.Size()})
	}
	if adm, ok := w.(writeAdmitter); ok {
		if err := adm.AdmitWrite(f.ctx, fi.Size()); err != nil {
			w.Close()
			return err
		}
	}
	if _, err := f.st.Seek(0, io.SeekStart); err != nil {
		w.Close()
		return err
	}
	if _, err := io.Copy(w, f.st); err != nil {
		w.Abort()
		w.Close()
		return err
	}
	if _, err := w.Commit(f.ctx); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	f.dirty = false
	return nil
}

// entryTimes 是两库共用的时间戳来源:S3 只给 LastModified,其余一律取它。
func entryTimes(fi vfs.FileInfo) (created, accessed, written, changed int64) {
	t := fi.ModTime.Unix()
	return t, t, t, t
}
