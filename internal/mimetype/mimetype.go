// Package mimetype 判定对象应存的内容类型:名字扩展名优先,未命中再探测内容,
// 最后兜底 application/octet-stream。
//
// 为什么需要它:S3 对象不带类型信息时,通过链接(CDN/presign/WebDAV GET)访问
// 只能下载、浏览器不渲染;而 SDK 与各客户端的默认值(application/octet-stream)
// 会把这个"没意见"的默认值顶到落库元数据里。CLI 上传路径、s3fs 写路径与
// WebDAV 壳需要同一套判定规则,故收口在这里。
//
// 名字一层留在本包:探测库只提供 MIME→扩展名,没有扩展名→MIME 的入口,且不认
// markdown 等类型。内置表补 mime.TypeByExtension 在各平台命不中或不稳定的条目
// (它在 Unix 读系统 mime.types、Windows 读注册表,同一次上传在不同机器上结果
// 可能不同),其余回退标准库,不重复维护。
//
// 内容一层交给 github.com/gabriel-vasile/mimetype:魔数签名识别,能认出 JSON、
// CSV、HTML 等 net/http.DetectContentType 只能判成 text/plain 的格式。
package mimetype

import (
	"io"
	"mime"
	"path"
	"path/filepath"
	"strings"

	"github.com/gabriel-vasile/mimetype"
)

// OctetStream 是"不知道/二进制"的通用类型,也是最终兜底值。
const OctetStream = "application/octet-stream"

// headLen 是内容探测窗口:探测库的默认读取上限(超过 3072 字节的部分不参与判定)。
const headLen = 3072

// Lookup 按名字的扩展名判定 MIME,两个候选名(name 优先)都不中时返回空串。
// name 通常是目标 key(用户改名的意图),altName 是本地文件名(内容来源)。
func Lookup(name, altName string) string {
	for _, n := range []string{name, altName} {
		if n == "" {
			continue
		}
		// 按 / 取最后一段再判扩展名:key 的目录名里带点不应参与判定。
		ext := strings.ToLower(path.Ext(filepath.ToSlash(n)))
		if ct, ok := builtin[ext]; ok {
			return ct
		}
		if ct := mime.TypeByExtension(ext); ct != "" {
			return ct
		}
	}
	return ""
}

// IsGenericBinary 判断 ct 是否只是"通用二进制"占位值(application/octet-stream
// 或 binary/octet-stream,含大小写与 charset 等参数变体)。它不携带类型信息:
// SDK 与部分客户端拿它当"我不知道类型"的默认值,协议壳据此决定是否交回本包
// 推断。非法值返回 false(按原样透传处理)。
func IsGenericBinary(ct string) bool {
	if ct == "" {
		return false
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	switch strings.ToLower(mt) {
	case OctetStream, "binary/octet-stream":
		return true
	}
	return false
}

// Detect 判定对象最终落库的 MIME:名字扩展名 → 内容探测 → OctetStream。
// head 是内容前若干字节(调用方能拿到时提供)。head 为空表示 0 字节对象或
// 调用方读不到内容,此时跳过探测——探测库对空输入返回 text/plain,反过来会
// 把空对象标错。
func Detect(name, altName string, head []byte) string {
	if ct := Lookup(name, altName); ct != "" {
		return ct
	}
	if len(head) > 0 {
		return mimetype.Detect(head).String()
	}
	return OctetStream
}

// ReadHead 从可随机读的源取内容探测窗口(headLen 字节)供 Detect 使用。
// 读不到或内容为空时返回 nil。用 ReadAt 语义,不移动调用方的读取偏移。
func ReadHead(r io.ReaderAt) []byte {
	buf := make([]byte, headLen)
	n, _ := r.ReadAt(buf, 0) // 数据不足时 n 仍是已读到的字节数
	if n == 0 {
		return nil
	}
	return buf[:n]
}
