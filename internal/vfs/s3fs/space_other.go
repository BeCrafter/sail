//go:build !unix

package s3fs

// availableBytes 在非 unix 平台不做预留探测,退化到写入时报 ENOSPC。
func availableBytes(string) (int64, bool) {
	return 0, false
}
