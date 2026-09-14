// Package vfs 定义协议无关的对象存储虚拟文件系统契约(CoreVFS)。
//
// 依赖方向:协议壳 → vfs → S3。本包不得引入 net/http、webdav 或 S3 SDK 类型;
// 协议壳也不得绕过本包直连 S3。新增协议壳(SMB 等)时只复用本包,不改本包语义。
package vfs

import (
	"context"
	"errors"
	"io"
	"os"
	"time"
)

// FileInfo 是内核视角的资源元信息。协议壳按各自需要翻译成 os.FileInfo /
// webdav.FileInfo 等宿主类型。
type FileInfo struct {
	Name    string // 路径最后一段
	Path    string // 完整逻辑路径,以 "/" 开头
	Size    int64  // 逻辑大小(字节);PROPFIND 的 getcontentlength 取它
	ModTime time.Time
	IsDir   bool
	ETag    string // 已提交对象的 ETag(已去引号);目录为空

	// ContentType 是对象存储中记录的 Content-Type。列目录(ListObjectsV2)
	// 不返回该字段,故列举得到的条目此处为空,由协议壳按扩展名兜底推断。
	ContentType string
}

// FileSystem 是协议无关的核心契约。
//
// 注意它与 webdav.FileSystem 不是一回事:方法集不同,且本接口不知道
// HTTP/XML 的存在。目录语义由 S3 的 Delimiter 共同前缀 + 目录标记对象
// (key 以 "/" 结尾的 0 字节对象)共同推断。
type FileSystem interface {
	Stat(ctx context.Context, path string) (FileInfo, error)
	ReadDir(ctx context.Context, path string) ([]FileInfo, error)
	OpenRead(ctx context.Context, path string) (ReadSeekCloser, error)
	OpenWrite(ctx context.Context, path string) (WriteHandle, error)
	Remove(ctx context.Context, path string, recursive bool) error
	Rename(ctx context.Context, oldPath, newPath string) error
}

// ReadSeekCloser 是读句柄。io.Seeker 是 http.ServeContent 实现 Range 的全部依据。
type ReadSeekCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}

// WriteHandle 是写句柄。
//
// 提交点由协议壳挂在宿主语言的对象上(WebDAV 壳挂在 File.Stat()),
// 因为 webdav 的 PUT 路径是 Write → Stat → Close:Close 返回非 nil 会被
// 判成 405,而数据其实已经写成功。
type WriteHandle interface {
	io.Writer
	// Commit 落 S3,幂等,失败可重试。未 Commit 前不得产生任何对象。
	Commit(ctx context.Context) (FileInfo, error)
	// Abort 丢弃暂存,幂等。对象存储中不留残留。
	Abort() error
	// Close 只清理暂存,幂等;Commit 成功后不得返回错误。
	Close() error
}

// WriteOptions 是壳给核心的可选写侧提示。内容来自 HTTP 请求本身
// (Content-Type / Content-Length),不属于 CoreVFS 的必需方法集。
type WriteOptions struct {
	ContentType string
	// ContentLength 是声明的请求体长度;未知时必须是 -1(不是 0,
	// 0 表示"声明了 0 字节",会被当成截断)。
	ContentLength int64
}

// WriteOptioner 由写句柄可选实现;协议壳在 OpenWrite 成功后立即调用。
// 实现方可据此做暂存盘预留;不实现时壳按未知长度处理。
type WriteOptioner interface {
	SetWriteOptions(WriteOptions)
}

// 错误语义:必须 errors.Is 可判定,协议壳依赖它做状态码映射。
var (
	// ErrNotExist → 404
	ErrNotExist = os.ErrNotExist
	// ErrExist → 405
	ErrExist = os.ErrExist
	// ErrNotSupported → 501
	ErrNotSupported = errors.New("vfs: not supported")
	// ErrTooLarge → 413
	ErrTooLarge = errors.New("vfs: exceeds backend limit")
	// ErrInsufficientStorage → 507
	ErrInsufficientStorage = errors.New("vfs: insufficient storage")
)
