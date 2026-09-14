//go:build unix

package s3fs

import "golang.org/x/sys/unix"

// availableBytes 返回目录所在文件系统的可用字节数;探测失败时 ok 为 false,
// 调用方按「未知」处理(不做预留,退化到写入时报 ENOSPC)。
func availableBytes(dir string) (int64, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}
