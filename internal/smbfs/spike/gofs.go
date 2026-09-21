package main

import (
	"context"
	"errors"
	"hash/fnv"
	"io"
	"os"
	"path"
	"strings"

	"github.com/BeCrafter/sail/internal/vfs"
	filesystem "github.com/go-filesystems/interface"
)

// 候选 B:github.com/go-filesystems/smb(BSD-3)
// 基础接口是整文件语义(ReadFile/WriteFile),定位读写要走可选 Opener/
// WritableFile;错误映射是硬编码的 statusFor。

const (
	gofsIFMT  = 0xF000
	gofsIFDIR = 0x4000
	gofsIFREG = 0x8000
)

type gofsAdapter struct{ ops *coreOps }

func inodeOf(p string) uint64 {
	h := fnv.New64a()
	io.WriteString(h, p)
	return h.Sum64()
}

func gofsMode(fi vfs.FileInfo) uint16 {
	if fi.IsDir {
		return gofsIFDIR | 0o755
	}
	return gofsIFREG | 0o644
}

func (a *gofsAdapter) Close() error { return nil }

func (a *gofsAdapter) ReadFile(p string) ([]byte, error) {
	r, err := a.ops.fs.OpenRead(context.Background(), smbPath(p))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func (a *gofsAdapter) WriteFile(p string, data []byte, _ os.FileMode) error {
	ctx := context.Background()
	logical := smbPath(p)
	w, err := a.ops.fs.OpenWrite(ctx, logical)
	if err != nil {
		return err
	}
	if opt, ok := w.(vfs.WriteOptioner); ok {
		opt.SetWriteOptions(vfs.WriteOptions{ContentLength: int64(len(data))})
	}
	if _, err := w.Write(data); err != nil {
		w.Abort()
		w.Close()
		return err
	}
	if _, err := w.Commit(ctx); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

func (a *gofsAdapter) ListDir(p string) ([]filesystem.DirEntry, error) {
	entries, err := a.ops.fs.ReadDir(context.Background(), smbPath(p))
	if err != nil {
		return nil, err
	}
	out := make([]filesystem.DirEntry, 0, len(entries))
	for _, e := range entries {
		var ft uint8 = 8 // DT_REG
		if e.IsDir {
			ft = 4 // DT_DIR
		}
		out = append(out, filesystem.NewDirEntry(inodeOf(e.Path), e.Name, ft))
	}
	return out, nil
}

func (a *gofsAdapter) Stat(p string) (filesystem.Stat, error) {
	fi, err := a.ops.fs.Stat(context.Background(), smbPath(p))
	if err != nil {
		return nil, err
	}
	return filesystem.NewStat(gofsMode(fi), uint64(fi.Size), inodeOf(fi.Path)), nil
}

func (a *gofsAdapter) ReadLink(string) (string, error) {
	return "", vfs.ErrNotSupported
}

func (a *gofsAdapter) MkDir(p string, _ os.FileMode) error {
	return a.ops.mkdir(context.Background(), smbPath(p))
}

func (a *gofsAdapter) DeleteFile(p string) error {
	return a.ops.remove(context.Background(), smbPath(p), false)
}

func (a *gofsAdapter) DeleteDir(p string) error {
	return a.ops.remove(context.Background(), smbPath(p), true)
}

func (a *gofsAdapter) Rename(oldPath, newPath string) error {
	return a.ops.rename(context.Background(), smbPath(oldPath), smbPath(newPath))
}

// OpenFile 实现可选接口 filesystem.Opener:返回的 File 同时实现
// WritableFile,库才会走定位读写而不是「整文件读改写」。
func (a *gofsAdapter) OpenFile(p string) (filesystem.File, error) {
	logical := smbPath(p)
	fi, err := a.ops.fs.Stat(context.Background(), logical)
	if err != nil {
		return nil, err
	}
	if fi.IsDir {
		return nil, vfs.ErrNotSupported
	}
	f, err := newSpikeFile(context.Background(), a.ops, logical, false)
	if err != nil {
		return nil, err
	}
	return &gofsFile{spikeFile: f, path: logical}, nil
}

// gofsFile 把 spikeFile 适配成 filesystem.File + filesystem.WritableFile。
type gofsFile struct {
	*spikeFile
	path string
}

func (f *gofsFile) ReadAt(p []byte, off int64) (int, error) { return f.readAt(p, off) }

func (f *gofsFile) WriteAt(p []byte, off int64) (int, error) { return f.writeAt(p, off) }

func (f *gofsFile) Size() int64 {
	n, err := f.size()
	if err != nil {
		return 0
	}
	return n
}

func (f *gofsFile) Truncate(size int64) error { return f.truncate(size) }

func (f *gofsFile) Sync() error { return f.sync() }

func (f *gofsFile) Close() error { return f.close() }

// gofsErrNotSupported 让「库不认的错误」至少不误报成别的语义。
var _ = errors.Is
var _ = path.Join
var _ = strings.ToUpper
