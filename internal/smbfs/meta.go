package smbfs

import (
	"hash/fnv"
	"io"

	"github.com/BeCrafter/sail/internal/vfs"
)

// 库要的元数据只有 mode / size / inode 三样(go-filesystems/interface 的
// Stat 接口);SMB 客户端要的其余属性由库自己补,不经过驱动 —— 时间戳一律取
// 「现在」(file.go 的 writeTimes)、AllocationSize 按块向上取整(attrs.go)。
// 也就是说「S3 表达不了的属性只读」这条落在库里,本层不需要另做取舍。

// mode 位(POSIX 风格的文件类型,库按 sIFMT/sIFDIR 判定目录)。
const (
	modeDirectory = 0o040000
	modeRegular   = 0o100000
)

// dirEntryType 取 dirent.h 的 DT_* 编号。注意与上面的 mode 位不是同一套取值域,
// 库对两者分别断言,别混用。
const (
	dtDirectory = 4
	dtRegular   = 8
)

func modeOf(fi vfs.FileInfo) uint16 {
	if fi.IsDir {
		return modeDirectory | 0o755
	}
	return modeRegular | 0o644
}

func dirEntryType(fi vfs.FileInfo) uint8 {
	if fi.IsDir {
		return dtDirectory
	}
	return dtRegular
}

// inodeOf 用路径哈希当 FileId:客户端拿它判断「是不是同一个文件」,只要在一次
// 会话里稳定即可,不需要与任何真实 inode 对应(S3 也没有 inode 可言)。
// 与 webdavfs 的 ETag 取舍同理 —— 这里连 ETag 都用不上,库的 FileId 是 16 字节
// 句柄标识,与我们给的值无关,所以只求稳定。
func inodeOf(p string) uint64 {
	h := fnv.New64a()
	io.WriteString(h, p)
	return h.Sum64()
}
