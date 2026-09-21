package smbfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"

	"github.com/BeCrafter/sail/internal/vfs"
)

// fileHandle 是库眼中的一个打开文件,同时实现 filesystem.File 与
// filesystem.WritableFile。两条实现要点:
//
//  1. 读走内核的定位读:ReadAt 直通 vfs 的 ReadSeekCloser,不把文件读进内存。
//     库的 READ 就是 ReadAt(带 64 位 offset),这条路径必须成立。
//  2. 写落本地暂存文件: SMB2 的 WRITE 带 64 位 offset,而 vfs.WriteHandle 只有
//     顺序 io.Writer。句柄关闭时才把暂存顺序灌进 vfs.OpenWrite 提交,内核、
//     分片、配额逻辑一行不用改。
//
// 暂存顺带解决了两件 vfs 本身没有语义的事:
//   - read-your-writes:客户端写完立刻回读(Office 存盘、SQLite、部分拷贝
//     路径)看到的是新内容,而不是 S3 里尚未更新的旧对象;
//   - 建完即 Stat:客户端 CREATE 之后马上查长度,那时对象在 S3 里还是旧的。
type fileHandle struct {
	b    *backend
	path string
	size int64 // 打开时的对象大小;没有暂存时 Size() 报它

	// ctx 是句柄的生命周期上下文,只给「传内容」的操作用(读正文、灌暂存、
	// 提交上传)。这些操作的时长由文件大小与链路决定,套一个固定上限就会在
	// 大文件上凭空失败;它们在句柄关闭时被取消。元数据类调用(Stat/ReadDir/
	// Remove/Rename)才走 backend 的 10 分钟上限 —— 那些不该跑上几分钟,
	// 卡死的后端也不该一直占着共享锁。
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	ro     vfs.ReadSeekCloser
	st     *os.File
	loaded bool
	dirty  bool
	closed bool
}

func newFileHandle(b *backend, logical string, size int64) *fileHandle {
	ctx, cancel := context.WithCancel(context.Background())
	return &fileHandle{b: b, path: logical, size: size, ctx: ctx, cancel: cancel}
}

// ReadAt 实现 io.ReaderAt。暂存存在时以暂存为准(见文件头注释);
// 否则按 offset 直读内核 —— SMB 的读几乎都是随机读,整文件进内存不可接受。
func (f *fileHandle) ReadAt(p []byte, off int64) (int, error) {
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

// readBackend 按 io.ReaderAt 的契约读内核:只在确实读不满时返回 io.EOF,
// 且不把「读到文件尾」当成错误以外的任何东西。
func (f *fileHandle) readBackend(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, os.ErrInvalid
	}
	if f.ro == nil {
		// 句柄级 ctx:读句柄把 ctx 收进请求里,用完即取消会让后续每次
		// GetObject 都带一个已取消的上下文(现象是「读到 0 字节」)。
		r, err := f.b.core.OpenRead(f.ctx, f.path)
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

// WriteAt 实现 io.WriterAt:定位写落暂存。首次写之前先把既有对象灌进暂存,
// 否则「改一行」会变成「整文件只剩那一行」。
func (f *fileHandle) WriteAt(p []byte, off int64) (int, error) {
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

// Truncate 实现 filesystem.WritableFile。库在 SET_INFO 的 EndOfFile 上优先走
// 句柄的 Truncate(info.go:349),所以这条路径是客户端改大小的实际入口。
func (f *fileHandle) Truncate(size int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return os.ErrClosed
	}
	if size < 0 {
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
	if err := f.st.Truncate(size); err != nil {
		return err
	}
	f.dirty = true
	return nil
}

// Size 实现 filesystem.File。不产生 I/O:有暂存时以暂存的长度为句柄视角的长度
// (契约要求 WritableFile 的 Size 反映本句柄自己的写),否则用打开时的快照 ——
// 后者符合「只读 File 是快照」的约定。
func (f *fileHandle) Size() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.st != nil {
		if fi, err := f.st.Stat(); err == nil {
			return fi.Size()
		}
	}
	return f.size
}

// Sync 只把暂存 fsync 到本地盘,不提交 S3。这是本壳最容易写错的一处:
//
// 库在每一次 WRITE 之后都会调一次 Sync(file.go:377)。若在这里提交,一个
// 100MiB 的文件按 64KiB 分块写就是 1600 次全量上传 —— 正是上游文档里那个
// 「2MiB 写 23 秒」的平方级退化。
//
// 对驱动而言「写已经落到自己的后备存储」为真:落的是本地暂存,提交点在句柄
// 关闭。这里仍然 fsync,是为了让暂存盘写满这类错误在 WRITE 的响应里就报给
// 客户端(Sync 的错误库会回给客户端),而不是等到关闭时才无声失败。
func (f *fileHandle) Sync() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.st == nil {
		return nil
	}
	return f.st.Sync()
}

// Close 是提交点:把暂存顺序灌进 vfs.OpenWrite 落 S3,再清理本地暂存。
// 幂等。
//
// 提交失败无法回传给客户端:库关闭句柄时忽略返回值(file.go:241 只调
// of.f.Close()),CLOSE 一律回成功。所以这里必须自己把失败喊进服务端日志 ——
// 静默丢数据比报错严重得多(README 的 SMB 限制一节写明了这条)。
func (f *fileHandle) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	err := f.commitLocked()
	// 提交之后才取消:提交本身走的是这个 ctx。
	f.cancel()
	if f.st != nil {
		name := f.st.Name()
		f.st.Close()
		f.st = nil
		if rmErr := os.Remove(name); rmErr != nil && !os.IsNotExist(rmErr) && f.b.logger != nil {
			f.b.logger.Printf("smbfs: removing staging file %s failed: %v", name, rmErr)
		}
	}
	if f.ro != nil {
		f.ro.Close()
		f.ro = nil
	}
	if err != nil && f.b.logger != nil {
		f.b.logger.Printf("smbfs: committing %s failed (the client was told the write succeeded; the object is NOT updated): %v", f.path, err)
	}
	return err
}

// ensureStaging 建暂存文件。调用方须持 f.mu。
func (f *fileHandle) ensureStaging() error {
	if f.st != nil {
		return nil
	}
	tmp, err := os.CreateTemp(f.b.staging, "sail-smb-*")
	if err != nil {
		if errors.Is(err, syscall.ENOSPC) {
			return fmt.Errorf("smbfs: staging dir %s is out of space: %w", f.b.stagingDir(), vfs.ErrInsufficientStorage)
		}
		return fmt.Errorf("smbfs: creating staging file: %w", err)
	}
	f.st = tmp
	return nil
}

// loadFromBackend 把既有对象内容灌进暂存,让定位写表现为「读改写」。
// 对象不存在是正常情况(CREATE 之后紧跟第一次写),不是错误。
// 调用方须持 f.mu。
func (f *fileHandle) loadFromBackend() error {
	f.loaded = true
	r, err := f.b.core.OpenRead(f.ctx, f.path)
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

// commitLocked 把暂存灌进 vfs.OpenWrite 提交。提交成功后暂存保留:客户端可能
// 还在同一个句柄上继续写,再提交一次就是把新内容整体覆盖上去。
// 调用方须持 f.mu。
func (f *fileHandle) commitLocked() error {
	if !f.dirty || f.st == nil {
		return nil
	}
	fi, err := f.st.Stat()
	if err != nil {
		return err
	}
	ctx := f.ctx
	w, err := f.b.core.OpenWrite(ctx, f.path)
	if err != nil {
		return err
	}
	if opt, ok := w.(vfs.WriteOptioner); ok {
		opt.SetWriteOptions(vfs.WriteOptions{ContentLength: fi.Size()})
	}
	// 配额准入在提交点做:到这里才知道真实长度,而 webdav 那条路径之所以能
	// 「读请求体之前」判,是因为 HTTP 给了 Content-Length。超限返回
	// vfs.ErrInsufficientStorage,库把它降级成 ACCESS_DENIED(见 README)。
	if adm, ok := w.(writeAdmitter); ok {
		if err := adm.AdmitWrite(ctx, fi.Size()); err != nil {
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
	if _, err := w.Commit(ctx); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	f.dirty = false
	f.b.core.invalidate(f.path, false)
	return nil
}
