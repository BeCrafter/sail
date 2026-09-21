package main

import (
	"context"
	"errors"
	"iter"
	"log"
	"os"
	"path"
	"time"

	"github.com/BeCrafter/sail/internal/vfs"
	svfs "github.com/sonroyaalmerol/go-smb-server/smb/vfs"
)

// 候选 A:github.com/sonroyaalmerol/go-smb-server(MIT)
// 接口契合度最高:Backend.Open → Handle(定位读/写、Stat、Enumerate)与
// sail 的 vfs 六方法近乎 1:1,选型差异全部关在这一个文件里。

const (
	sonAttrReadonly  uint32 = 0x00000001
	sonAttrHidden    uint32 = 0x00000002
	sonAttrDirectory uint32 = 0x00000010
	sonAttrArchive   uint32 = 0x00000020
)

type sonBackend struct{ ops *coreOps }

func sonAttrs(fi vfs.FileInfo) uint32 {
	if fi.IsDir {
		return sonAttrDirectory
	}
	return sonAttrArchive
}

func sonInfo(fi vfs.FileInfo) svfs.FileInfo {
	return svfs.FileInfo{
		Name:         fi.Name,
		Size:         fi.Size,
		IsDir:        fi.IsDir,
		Attributes:   sonAttrs(fi),
		CreationTime: fi.ModTime,
		LastAccess:   fi.ModTime,
		LastWrite:    fi.ModTime,
		ChangeTime:   fi.ModTime,
	}
}

func (b *sonBackend) Open(ctx context.Context, o svfs.OpenOptions) (svfs.Handle, error) {
	log.Printf("[open] path=%q disposition=0x%08x createDir=%v append=%v", o.Path, o.Disposition, o.CreateDir, o.Append)
	p := smbPath(o.Path)
	if o.CreateDir {
		err := b.ops.mkdir(ctx, p)
		existed := errors.Is(err, vfs.ErrExist)
		if err != nil && !existed {
			return nil, err
		}
		// 目录已就绪。FILE_CREATE 的「已存在」判定由 mkdir 承担,
		// 不能再用下面那条 exists 检查(那时目录刚被自己建出来)。
		if existed && o.Disposition == svfs.DispositionCreate {
			return nil, os.ErrExist
		}
		return &sonHandle{ctx: ctx, ops: b.ops, path: p, dir: true}, nil
	}
	fi, err := b.ops.fs.Stat(ctx, p)
	exists := err == nil
	if err != nil && !errors.Is(err, vfs.ErrNotExist) {
		return nil, err
	}

	h := &sonHandle{ctx: ctx, ops: b.ops, path: p, dir: exists && fi.IsDir}

	// discipline → (必须存在, 必须不存在, 空文件起步)
	mustExist, mustNotExist, fresh := false, false, false
	switch o.Disposition {
	case svfs.DispositionOpen:
		mustExist = true
	case svfs.DispositionCreate:
		mustNotExist, fresh = true, true
	case svfs.DispositionOverwrite:
		mustExist, fresh = true, true
	case svfs.DispositionSupersede, svfs.DispositionOverwriteIf:
		fresh = true
	case svfs.DispositionOpenIf:
		fresh = !exists
	}
	if mustExist && !exists {
		return nil, os.ErrNotExist
	}
	if mustNotExist && exists {
		return nil, os.ErrExist
	}
	if h.dir {
		return h, nil
	}
	f, err := newSpikeFile(ctx, b.ops, p, fresh)
	if err != nil {
		return nil, err
	}
	h.f = f
	return h, nil
}

// Remove 走 Backend 侧的可选接口(库侧探测 tr.share.Backend().(vfs.Remover))。
func (b *sonBackend) Remove(ctx context.Context, p string) error {
	return b.ops.remove(ctx, smbPath(p), false)
}

type sonHandle struct {
	ctx  context.Context
	ops  *coreOps
	path string
	dir  bool
	f    *spikeFile
}

func (h *sonHandle) Read(_ context.Context, offset int64, p []byte) (int, error) {
	if h.dir {
		return 0, os.ErrInvalid
	}
	return h.f.readAt(p, offset)
}

func (h *sonHandle) Write(_ context.Context, offset int64, p []byte) (int, error) {
	if h.dir {
		return 0, os.ErrInvalid
	}
	return h.f.writeAt(p, offset)
}

func (h *sonHandle) Close(_ context.Context) error {
	if h.dir {
		return nil
	}
	return h.f.close()
}

func (h *sonHandle) Stat(ctx context.Context) (svfs.FileInfo, error) {
	if h.f != nil {
		fi, err := h.f.info()
		if err != nil {
			return svfs.FileInfo{}, err
		}
		return sonInfo(fi), nil
	}
	fi, err := h.ops.fs.Stat(ctx, h.path)
	if err != nil {
		return svfs.FileInfo{}, err
	}
	return sonInfo(fi), nil
}

func (h *sonHandle) Enumerate(ctx context.Context, pattern string) iter.Seq2[svfs.FileInfo, error] {
	return func(yield func(svfs.FileInfo, error) bool) {
		entries, err := h.ops.fs.ReadDir(ctx, h.path)
		if err != nil {
			yield(svfs.FileInfo{}, err)
			return
		}
		for _, e := range entries {
			if pattern != "" && pattern != "*" {
				if ok, err := path.Match(pattern, e.Name); err != nil || !ok {
					continue
				}
			}
			if !yield(sonInfo(e), nil) {
				return
			}
		}
	}
}

// Rename 挂在句柄上(queryinfo.go: oh.h.(vfs.Renamer))。
func (h *sonHandle) Rename(ctx context.Context, newPath string, replaceIfExists bool) error {
	return h.ops.rename(ctx, h.path, smbPath(newPath))
}

// SetInfo 挂在句柄上:EndOfFile 走截断,时间戳/属性接受但不持久化
// (S3 表达不了,返回错误会让客户端反复重试)。
func (h *sonHandle) SetInfo(ctx context.Context, req *svfs.SetInfoRequest) error {
	if h.dir || h.f == nil {
		return nil
	}
	if req.EndOfFile != nil {
		return h.f.truncate(*req.EndOfFile)
	}
	return nil
}

// sonServerName 供 NTLM 挑战的 target name(客户端显示用)。
var _ = time.Now
