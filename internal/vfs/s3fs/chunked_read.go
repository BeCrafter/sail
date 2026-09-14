package s3fs

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/BeCrafter/sail/internal/vfs"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// chunkedReader 是分片文件的 io.ReadSeeker:逻辑区间 → (片号, 片内区间) 的
// 按需映射。Seek 只改偏移、不发请求;Read 才 GetObject 取「当前片」的一段,
// 因此不落盘、不整文件入内存,跨片顺序读逐片推进。
//
// 与 P1 的 readFile 语义逐字节等价:http.ServeContent 的 Seek(0, SeekEnd) +
// Seek(start, SeekStart) 组合最多只产生一次 GetObject。
type chunkedReader struct {
	ctx    context.Context
	client *s3.Client
	bucket string
	// version 是片目录名,取自 manifest。
	version string
	// sizes 是每片的逻辑长度,与 chunkedReader 的片序号一一对应。
	sizes []int64
	// starts[i] 是第 i 片的逻辑起始偏移。
	starts []int64
	size   int64
	info   vfs.FileInfo

	off int64
	// idx 是当前已打开的片序号;-1 表示没有打开的片。
	idx  int
	body io.ReadCloser
	// remaining 是当前片尚未读出的字节数。
	remaining int64
}

var _ vfs.ReadSeekCloser = (*chunkedReader)(nil)

func newChunkedReader(ctx context.Context, client *s3.Client, bucket string, m *manifest, info vfs.FileInfo) *chunkedReader {
	sizes := make([]int64, len(m.Chunks))
	starts := make([]int64, len(m.Chunks))
	for i, c := range m.Chunks {
		sizes[i] = c.Length
		starts[i] = c.Offset
	}
	return &chunkedReader{
		ctx:     ctx,
		client:  client,
		bucket:  bucket,
		version: m.Version,
		sizes:   sizes,
		starts:  starts,
		size:    m.Size,
		info:    info,
		idx:     -1,
	}
}

// locate 返回容纳逻辑偏移 off 的片序号。二分,与片的段数成对数。
func (r *chunkedReader) locate(off int64) int {
	if off < 0 {
		return 0
	}
	i := sort.Search(len(r.starts), func(i int) bool {
		return r.starts[i]+r.sizes[i] > off
	})
	if i >= len(r.starts) {
		return len(r.starts) - 1
	}
	return i
}

func (r *chunkedReader) readChunk(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.off >= r.size {
		return 0, io.EOF
	}
	if len(r.sizes) == 0 {
		return 0, io.EOF
	}
	want := r.locate(r.off)
	if r.body == nil || r.idx != want {
		if err := r.open(want, r.off); err != nil {
			return 0, err
		}
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.body.Read(p)
	r.off += int64(n)
	r.remaining -= int64(n)
	if err == io.EOF {
		// 片读尽(理论上 readRange 已按片长截断,这里是兜底):
		// 关掉当前片,让下一次 Read 推进到下一片。
		r.closeBody()
		if n > 0 {
			return n, nil
		}
		return 0, io.EOF
	}
	if err != nil {
		return n, fmt.Errorf("s3fs: 读取 %s 分片失败: %w", r.info.Path, err)
	}
	if r.remaining == 0 {
		r.closeBody()
	}
	return n, nil
}

func (r *chunkedReader) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.off + offset
	case io.SeekEnd:
		abs = r.size + offset
	default:
		return 0, fmt.Errorf("s3fs: 非法的 Seek whence %d", whence)
	}
	if abs < 0 {
		return 0, fmt.Errorf("s3fs: 非法的 Seek 位置 %d", abs)
	}
	if abs != r.off {
		r.closeBody()
		r.off = abs
	}
	return abs, nil
}

func (r *chunkedReader) Close() error {
	r.closeBody()
	return nil
}

// open 打开第 idx 片,并从片内偏移 from 开始(片内偏移 = from - starts[idx])。
func (r *chunkedReader) open(idx int, from int64) error {
	r.closeBody()
	start := r.starts[idx]
	size := r.sizes[idx]
	inner := from - start
	if inner < 0 {
		inner = 0
	}
	// 只取「本片剩余」这一段,不越片读取——否则会把下一片的字节当成本片内容。
	last := size - 1
	req := &s3.GetObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(partKey(r.version, idx)),
		Range:  aws.String(fmt.Sprintf("bytes=%d-%d", inner, last)),
	}
	out, err := r.client.GetObject(r.ctx, req)
	if err != nil {
		r.closeBody()
		if isNotFound(err) {
			return fmt.Errorf("s3fs: %s 的分片 %d 缺失: %w", r.info.Path, idx, vfs.ErrNotExist)
		}
		return fmt.Errorf("s3fs: 读取 %s 分片 %d 失败: %w", r.info.Path, idx, err)
	}
	if out.ContentLength != nil && aws.ToInt64(out.ContentLength) != last-inner+1 {
		// 片对象被截断:继续读会静默给出错误内容,必须报错。
		out.Body.Close()
		return fmt.Errorf("s3fs: %s 的分片 %d 长度不符(期望 %d): %w",
			r.info.Path, idx, last-inner+1, vfs.ErrNotExist)
	}
	r.idx = idx
	r.body = out.Body
	r.remaining = last - inner + 1
	return nil
}

func (r *chunkedReader) closeBody() {
	if r.body != nil {
		r.body.Close()
		r.body = nil
	}
	r.idx = -1
	r.remaining = 0
}

func (r *chunkedReader) Read(p []byte) (int, error) { return r.readChunk(p) }
