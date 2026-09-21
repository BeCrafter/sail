package smbfs

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path"
	"strings"
	"time"

	"github.com/BeCrafter/sail/internal/vfs"
	filesystem "github.com/go-filesystems/interface"
)

// opTimeout 是单次内核调用的时限。库的命令循环没有 context(它是 net.Conn 上的
// 状态机),而 S3 侧的调用可能长时间阻塞 —— 大文件提交、跨目录改名(在 S3 上是
// copy + delete)都可能跑上几分钟。给一个足够宽的上限,既容得下慢操作,又不让
// 卡死的后端永远占着共享锁。
const opTimeout = 10 * time.Minute

// writeAdmitter 由写句柄可选实现(quotafs 的配额会计):提交前做配额准入。
// 与 webdavfs 里同名接口是同一份约定,两边都不把它加进 vfs 的必需方法集。
type writeAdmitter interface {
	AdmitWrite(ctx context.Context, contentLength int64) error
}

// backend 把 vfs.FileSystem 适配成库要的 filesystem.Filesystem。
//
// 这里就是选型隔离层:换服务端库时只重写本文件与 file.go,内核、配额装饰器
// 与缓存层都不受影响。
type backend struct {
	core    *cachedFS
	staging string
	logger  *log.Logger
}

func newBackend(core *cachedFS, staging string, logger *log.Logger) *backend {
	return &backend{core: core, staging: staging, logger: logger}
}

// op 给一次内核调用配上时限。
func (b *backend) op() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), opTimeout)
}

// stagingDir 是壳层定位写的落盘位置;空 = 系统临时目录(只有写路径用它)。
func (b *backend) stagingDir() string {
	if b.staging != "" {
		return b.staging
	}
	return os.TempDir()
}

// logicalPath 把库给的路径归一成内核逻辑路径。库的 smbPathToFS 已经做过一次
// (反斜杠转正斜杠、Clean、拦住越界),这里是同一套规则的防御性复算:单元测试
// 与将来换库时都不必依赖上游行为。
func logicalPath(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return path.Clean(p)
}

// Close 实现 filesystem.Filesystem。共享的文件系统随进程存活,无需释放。
func (b *backend) Close() error { return nil }

// Stat 只映射库要的三样(mode / size / inode)。SMB 客户端要的其余元数据由库
// 自己补:时间戳一律取「现在」、AllocationSize 按块向上取整 —— 两者都不经过
// 驱动,故本层无需也无法表达它们。
func (b *backend) Stat(p string) (filesystem.Stat, error) {
	ctx, cancel := b.op()
	defer cancel()
	logical := logicalPath(p)
	fi, err := b.core.Stat(ctx, logical)
	if err != nil {
		return nil, err
	}
	size := fi.Size
	if size < 0 {
		size = 0
	}
	return filesystem.NewStat(modeOf(fi), uint64(size), inodeOf(logical)), nil
}

// ListDir 只报名字与类型,元数据由紧随其后的逐条 Stat 补齐 —— 那一次每个条目
// 的 Stat 正是缓存层要吃的开销。
func (b *backend) ListDir(p string) ([]filesystem.DirEntry, error) {
	ctx, cancel := b.op()
	defer cancel()
	entries, err := b.core.ReadDir(ctx, logicalPath(p))
	if err != nil {
		return nil, err
	}
	out := make([]filesystem.DirEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, filesystem.NewDirEntry(inodeOf(e.Path), e.Name, dirEntryType(e)))
	}
	return out, nil
}

// ReadFile 是库在没有 Opener 时的兜底路径(我们实现了 Opener,正常不会走到)。
// 整文件进内存,只为接口完整性保留。
func (b *backend) ReadFile(p string) ([]byte, error) {
	ctx, cancel := b.op()
	defer cancel()
	r, err := b.core.OpenRead(ctx, logicalPath(p))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// WriteFile 在库里有两个用途,本层都要正确对待:
//   - CREATE / SUPERSEDE 时以 WriteFile(path, nil) 表达「创建或截断为空文件」——
//     必须真的落一个 0 字节对象,否则客户端建完文件在列表里看不到;
//   - 没有 WritableFile 能力时的「读-改-写」兜底。我们返回的句柄实现了
//     WritableFile,故不走这条。
func (b *backend) WriteFile(p string, data []byte, _ os.FileMode) error {
	ctx, cancel := b.op()
	defer cancel()
	logical := logicalPath(p)
	w, err := b.core.OpenWrite(ctx, logical)
	if err != nil {
		return err
	}
	if opt, ok := w.(vfs.WriteOptioner); ok {
		// 声明长度让内核做暂存盘预留;0 在这里是「声明 0 字节」的正确含义。
		opt.SetWriteOptions(vfs.WriteOptions{ContentLength: int64(len(data))})
	}
	if len(data) > 0 {
		if adm, ok := w.(writeAdmitter); ok {
			if err := adm.AdmitWrite(ctx, int64(len(data))); err != nil {
				w.Close()
				return err
			}
		}
		if _, err := w.Write(data); err != nil {
			w.Abort()
			w.Close()
			return err
		}
	}
	if _, err := w.Commit(ctx); err != nil {
		w.Close()
		return err
	}
	b.core.invalidate(logical, false)
	return w.Close()
}

// MkDir 写 0 字节、key 以 "/" 结尾的目录标记对象 —— 与 webdavfs.Mkdir 同一套
// 目录语义:S3 没有目录,共同前缀无法自证存在,不落标记对象的话客户端新建的
// 文件夹刷新一次就没了。已存在返回 ErrExist(库映射成 OBJECT_NAME_COLLISION),
// 既不覆盖同名对象,也不在同名文件旁多出一个 "x/" 标记。
func (b *backend) MkDir(p string, _ os.FileMode) error {
	ctx, cancel := b.op()
	defer cancel()
	logical := logicalPath(p)
	if logical == "/" {
		return vfs.ErrExist
	}
	if _, err := b.core.Stat(ctx, logical); err == nil {
		return vfs.ErrExist
	} else if !errors.Is(err, vfs.ErrNotExist) {
		return err
	}
	w, err := b.core.OpenWrite(ctx, logical+"/")
	if err != nil {
		return err
	}
	if opt, ok := w.(vfs.WriteOptioner); ok {
		opt.SetWriteOptions(vfs.WriteOptions{ContentLength: 0})
	}
	if _, err := w.Commit(ctx); err != nil {
		w.Close()
		return err
	}
	b.core.invalidate(logical, false)
	return w.Close()
}

func (b *backend) DeleteFile(p string) error {
	ctx, cancel := b.op()
	defer cancel()
	return b.core.Remove(ctx, logicalPath(p), false)
}

func (b *backend) DeleteDir(p string) error {
	ctx, cancel := b.op()
	defer cancel()
	return b.core.Remove(ctx, logicalPath(p), true)
}

func (b *backend) Rename(oldPath, newPath string) error {
	ctx, cancel := b.op()
	defer cancel()
	return b.core.Rename(ctx, logicalPath(oldPath), logicalPath(newPath))
}

// ReadLink 恒不支持:S3 对象没有链接概念。库不会据此把文件当成重解析点,
// 故这里返回契约里的 ErrNotSupported 而不是 nil。
func (b *backend) ReadLink(string) (string, error) { return "", vfs.ErrNotSupported }

// OpenFile 实现可选接口 filesystem.Opener:库一旦发现它,读走定位读、写走
// WritableFile,不再整文件读写。返回的句柄见 file.go。
func (b *backend) OpenFile(p string) (filesystem.File, error) {
	logical := logicalPath(p)
	ctx, cancel := b.op()
	defer cancel()
	fi, err := b.core.Stat(ctx, logical)
	if err != nil {
		return nil, err
	}
	if fi.IsDir {
		return nil, vfs.ErrNotSupported
	}
	return newFileHandle(b, logical, fi.Size), nil
}

var _ filesystem.Filesystem = (*backend)(nil)
var _ filesystem.Opener = (*backend)(nil)
