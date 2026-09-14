package s3fs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/BeCrafter/sail/internal/vfs"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// 分片文件的判定与编码。逻辑 key 上放的是一个小 JSON(manifest),
// 片放在保留前缀 .sail/parts/<version>/ 下。
//
// 判定两条路:
//  1. 快路径:HeadObject 回显 x-amz-meta-sail-manifest-key(0 次额外请求,
//     Stat 本来就要发这次 HEAD)。
//  2. 兜底:元数据被桶迁移/replication/第三方重传抹掉时,读对象开头
//     4KiB 看是不是 sail_manifest==1 的 JSON。
//
// 只靠任一条都不成立:只靠元数据 → 抹掉即读不出;只靠 JSON → 一个恰好
// 以 {"sail_manifest":1 开头的用户文件会被误判。因此再加一条「manifest 恒小」
// 的体积上界(见 maxManifestSize),把误判概率压到实际为零。
const (
	// metaManifestKey 是「本对象是 manifest」的元数据标记。
	metaManifestKey = "x-amz-meta-sail-manifest-key"
	// metaTotalSize 是逻辑文件大小(字节)的元数据标记。
	metaTotalSize = "x-amz-meta-sail-total-size"

	// partsPrefix 是分片对象的保留前缀,由内核在列目录时过滤。
	partsPrefix = ".sail/parts/"
	// sailDir 是保留目录名。
	sailDir = ".sail"

	// manifestProbeSize 是 JSON 兜底判定时允许读取的对象头部上限。必须 >=
	// maxManifestSize:否则一个接近上界的 manifest 在元数据被抹掉后会解析不全,
	// 被误判成普通文件。这个长度的 Range GET 是有界的,不是整文件读。
	manifestProbeSize = maxManifestSize
	// manifestFirstRead 是快路径读 manifest 体的首次长度。绝大多数 manifest
	// 都在这个量级内,一次小 GET 即可;只有超大 manifest 才继续读满
	// manifestProbeSize。
	manifestFirstRead = 4096
	// maxManifestSize 是 manifest 的体积上界。超过它,「JSON 兜底」的误判
	// 防护就不成立(一个真 manifest 也会被当成普通文件),且 256KiB 内的
	// 探针长度才有意义。触发即拒绝提交,提示调大 --chunk-size。
	maxManifestSize = 256 * 1024

	// manifestMagic 是 manifest 的版本哨兵。
	manifestMagic = 1
)

// manifest 是分片文件的元数据。字段一经 P2 定死,后续只增不改语义。
type manifest struct {
	// SailManifest 是格式哨兵,恒为 1。
	SailManifest int `json:"sail_manifest"`
	// Version 是暂存内容的 SHA-256(hex),同时是片目录名。
	Version string `json:"version"`
	// Size 是逻辑文件大小(字节),PROPFIND 的 getcontentlength 取它。
	Size int64 `json:"size"`
	// ChunkSize 是写入时的片大小上限。
	ChunkSize int64 `json:"chunk_size"`
	// Chunks 按逻辑顺序列出每一片。offset 连续递增,length 之和等于 Size。
	Chunks []manifestChunk `json:"chunks"`
	// ContentType 是原文件的类型。manifest 对象自身的 Content-Type 也写这个值
	// (见 write.go),这里冗余一份,便于 JSON 兜底路径也拿得到。
	ContentType string `json:"content_type,omitempty"`
}

type manifestChunk struct {
	Offset int64  `json:"offset"`
	Length int64  `json:"length"`
	ETag   string `json:"etag,omitempty"`
}

// chunkCount 返回片的段数。
func (m *manifest) chunkCount() int { return len(m.Chunks) }

// encode 序列化 manifest。超过 maxManifestSize 即拒绝——否则「manifest 恒小」
// 的误判防护失效,且它自己也会变成一个需要分片的大对象。
func (m *manifest) encode() ([]byte, error) {
	m.SailManifest = manifestMagic
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("s3fs: 序列化 manifest 失败: %w", err)
	}
	if len(b) > maxManifestSize {
		return nil, fmt.Errorf(
			"s3fs: manifest 需要 %d 字节,超过 %d 字节上界(片数 %d):请调大 --chunk-size",
			len(b), maxManifestSize, len(m.Chunks))
	}
	return b, nil
}

// decodeManifest 解析 manifest。只用于内核自己写出的对象,因此校验较宽松;
// 判定入口必须先用 isManifest。
func decodeManifest(b []byte) (*manifest, error) {
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("s3fs: 解析 manifest 失败: %w", err)
	}
	if m.SailManifest != manifestMagic {
		return nil, fmt.Errorf("s3fs: manifest 格式哨兵不匹配: %d", m.SailManifest)
	}
	if m.Version == "" {
		return nil, errors.New("s3fs: manifest 缺少 version")
	}
	var want int64
	for _, c := range m.Chunks {
		if c.Offset != want {
			return nil, fmt.Errorf("s3fs: manifest 分片偏移不连续: 期望 %d,实际 %d", want, c.Offset)
		}
		if c.Length <= 0 {
			return nil, fmt.Errorf("s3fs: manifest 分片长度非法: %d", c.Length)
		}
		want += c.Length
	}
	if want != m.Size {
		return nil, fmt.Errorf("s3fs: manifest 分片总长 %d 与声明的 size %d 不符", want, m.Size)
	}
	return &m, nil
}

// partKey 是某个片的对象 key。序号固定 5 位十进制,便于按前缀列举时天然有序。
func partKey(version string, idx int) string {
	return fmt.Sprintf("%s%s/%05d", partsPrefix, version, idx)
}

// partsPrefixOf 是某个版本的片目录前缀。
func partsPrefixOf(version string) string {
	return partsPrefix + version + "/"
}

// metaManifest 判定 HEAD 响应是否带 manifest 元数据标记。
func metaManifest(h *s3.HeadObjectOutput) bool {
	return h != nil && h.Metadata["sail-manifest-key"] != ""
}

// metaTotalSizeOf 从元数据取逻辑大小;缺失返回 0。
func metaTotalSizeOf(h *s3.HeadObjectOutput) int64 {
	if h == nil {
		return 0
	}
	var n int64
	if _, err := fmt.Sscanf(h.Metadata["sail-total-size"], "%d", &n); err != nil {
		return 0
	}
	return n
}

// lookLikeManifest 是纯函数的 JSON 兜底判定:对象开头必须是 canonical 的
// manifest 且体积在 maxManifestSize 内。
func lookLikeManifest(head []byte, size int64) bool {
	if size <= 0 || size > maxManifestSize {
		return false
	}
	trimmed := bytes.TrimSpace(head)
	// 必须是 canonical 序列化(我们的 encode 用 json.Marshal,键序确定):
	// {"sail_manifest":1,...}。加逗号/右括号的边界,避免前缀恰好相同的普通文件误入。
	if !bytes.HasPrefix(trimmed, []byte(`{"sail_manifest":1,`)) &&
		!bytes.HasPrefix(trimmed, []byte(`{"sail_manifest":1}`)) {
		return false
	}
	var probe struct {
		SailManifest int `json:"sail_manifest"`
	}
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return false
	}
	return probe.SailManifest == manifestMagic
}

// detectMeta 是「只要逻辑元信息、不要分片表」的快路径:Stat / ReadDir 用。
//
// 命中元数据时 0 次额外请求(HEAD 本来就要发);元数据缺失时,只有当对象
// 体积落在 maxManifestSize 内才发一次 bounded GET 做兜底确认。返回的
// vfs.FileInfo 在命中时是逻辑文件信息,否则 zero。
func (f *FS) detectMeta(ctx context.Context, logical, key string, h *s3.HeadObjectOutput) (metaHit, vfs.FileInfo, bool, error) {
	if strings.HasSuffix(key, "/") {
		return metaHit{}, vfs.FileInfo{}, false, nil
	}
	size := aws.ToInt64(h.ContentLength)
	if metaManifest(h) {
		// 快路径:逻辑大小取元数据,无需读 manifest 体。
		total := metaTotalSizeOf(h)
		fi := vfs.FileInfo{
			Name:        baseName(logical),
			Path:        logical,
			Size:        total,
			ModTime:     aws.ToTime(h.LastModified),
			ETag:        unquote(aws.ToString(h.ETag)),
			ContentType: aws.ToString(h.ContentType),
		}
		return metaHit{version: h.Metadata["sail-manifest-key"], size: total}, fi, true, nil
	}
	if !f.chunkedUpload || size <= 0 || size > maxManifestSize {
		// 分片关闭,或对象大得不可能承载 manifest:不发任何额外请求。
		return metaHit{}, vfs.FileInfo{}, false, nil
	}
	body, err := f.readHead(ctx, key, size, manifestProbeSize)
	if err != nil {
		return metaHit{}, vfs.FileInfo{}, false, err
	}
	if !lookLikeManifest(body, size) {
		return metaHit{}, vfs.FileInfo{}, false, nil
	}
	m, err := decodeManifest(body)
	if err != nil {
		return metaHit{}, vfs.FileInfo{}, false, nil
	}
	fi := vfs.FileInfo{
		Name:        baseName(logical),
		Path:        logical,
		Size:        m.Size,
		ModTime:     aws.ToTime(h.LastModified),
		ETag:        unquote(aws.ToString(h.ETag)),
		ContentType: m.ContentType,
	}
	if fi.ContentType == "" {
		fi.ContentType = aws.ToString(h.ContentType)
	}
	return metaHit{version: m.Version, size: m.Size}, fi, true, nil
}

// detectManifest 是「要分片表」的路径:OpenRead / Remove / Rename 用。
// 快路径命中时仍需读 manifest 体拿分片表(通常一次 4KiB 的小 GET)。
func (f *FS) detectManifest(ctx context.Context, key string, h *s3.HeadObjectOutput) (*manifest, bool, error) {
	if metaManifest(h) {
		body, err := f.readManifestBody(ctx, key, aws.ToInt64(h.ContentLength))
		if err != nil {
			return nil, false, err
		}
		m, err := decodeManifest(body)
		if err != nil {
			// 元数据说是 manifest 却解不出来:桶里的数据已损坏,必须报错而不是
			// 静默当成普通文件读(那会把 manifest JSON 当文件内容给客户端)。
			return nil, false, fmt.Errorf("s3fs: %s 标记为分片文件但 manifest 无法解析: %w", key, err)
		}
		return m, true, nil
	}
	size := aws.ToInt64(h.ContentLength)
	if !f.chunkedUpload || size <= 0 || size > maxManifestSize {
		return nil, false, nil
	}
	body, err := f.readHead(ctx, key, size, manifestProbeSize)
	if err != nil {
		return nil, false, err
	}
	if !lookLikeManifest(body, size) {
		return nil, false, nil
	}
	m, err := decodeManifest(body)
	if err != nil {
		return nil, false, nil
	}
	return m, true, nil
}

// readManifestBody 读 manifest 体:先读一小段,只有「没读满对象」时才扩大重读
// (超大 manifest)。绝大多数 manifest 都落在首次读取里。
func (f *FS) readManifestBody(ctx context.Context, key string, size int64) ([]byte, error) {
	body, err := f.readHead(ctx, key, size, manifestFirstRead)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) >= size {
		return body, nil // 已读满整个对象
	}
	return f.readHead(ctx, key, size, manifestProbeSize)
}

// readHead 读取对象开头的 min(size, limit) 字节。
func (f *FS) readHead(ctx context.Context, key string, size, limit int64) ([]byte, error) {
	n := limit
	if size > 0 && size < n {
		n = size
	}
	out, err := f.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(f.bucket),
		Key:    aws.String(key),
		Range:  aws.String(fmt.Sprintf("bytes=0-%d", n-1)),
	})
	if err != nil {
		return nil, fmt.Errorf("s3fs: 读取 %s 头部失败: %w", key, err)
	}
	defer out.Body.Close()
	buf := make([]byte, n)
	read, err := io.ReadFull(out.Body, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("s3fs: 读取 %s 头部失败: %w", key, err)
	}
	return buf[:read], nil
}

// metaHit 是快路径命中分片文件时能拿到的元信息(无需读 manifest 体)。
type metaHit struct {
	version string
	size    int64
}

// chunked 判定一个逻辑路径是不是分片文件;是则返回 manifest 与逻辑 FileInfo。
// 供 OpenRead / Remove 共用——它们都已经发过 HEAD,这里不再多发。
func (f *FS) chunked(ctx context.Context, logical, key string, h *s3.HeadObjectOutput) (*manifest, vfs.FileInfo, bool, error) {
	if strings.HasSuffix(key, "/") {
		return nil, vfs.FileInfo{}, false, nil
	}
	m, ok, err := f.detectManifest(ctx, key, h)
	if err != nil {
		return nil, vfs.FileInfo{}, false, err
	}
	if !ok {
		return nil, vfs.FileInfo{}, false, nil
	}
	return m, manifestInfo(logical, m, aws.ToString(h.ETag), aws.ToTime(h.LastModified)), true, nil
}

// manifestHead 是用 manifest 内容构造出的提交结果 FileInfo(写侧用)。
func manifestInfo(logical string, m *manifest, etag string, modTime time.Time) vfs.FileInfo {
	return vfs.FileInfo{
		Name:        baseName(logical),
		Path:        logical,
		Size:        m.Size,
		ModTime:     modTime,
		ETag:        unquote(etag),
		ContentType: m.ContentType,
	}
}
