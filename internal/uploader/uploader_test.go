package uploader

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{5 * 1024 * 1024, "5.0 MiB"},
		{2 << 30, "2.0 GiB"},
		{1024 * 1024 * 1024 * 1024, "1.0 TiB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d) = %q,期望 %q", c.in, got, c.want)
		}
	}
}

func TestBuildKey(t *testing.T) {
	cases := []struct {
		prefix, rel string
		want        string
	}{
		{"", "a/b.txt", "a/b.txt"}, // 空 prefix:直接用 rel
		{"mirror", "a/b.txt", "mirror/a/b.txt"},
		{"mirror/", "a.txt", "mirror/a.txt"},  // prefix 尾斜杠被裁
		{"/mirror", "a.txt", "mirror/a.txt"},  // prefix 首斜杠被裁
		{"/mirror/", "a.txt", "mirror/a.txt"}, // 首尾都裁
		{"a/b", "c.txt", "a/b/c.txt"},
	}
	for _, c := range cases {
		if got := buildKey(c.prefix, c.rel); got != c.want {
			t.Errorf("buildKey(%q,%q) = %q,期望 %q", c.prefix, c.rel, got, c.want)
		}
	}
}

func TestProgressReaderRead(t *testing.T) {
	pr := newProgressReader(bytes.NewReader([]byte("hello")), 5)
	buf := make([]byte, 3)
	n, err := pr.Read(buf)
	if n != 3 || err != nil {
		t.Fatalf("首次 Read = (%d,%v),期望 (3,nil)", n, err)
	}
	pr.mu.Lock()
	if pr.read != 3 {
		t.Errorf("read 计数 = %d,期望 3", pr.read)
	}
	pr.mu.Unlock()
	pr.Read(buf) // 消费剩余
	pr.mu.Lock()
	if pr.read != 5 {
		t.Errorf("读尽后 read = %d,期望 5", pr.read)
	}
	pr.mu.Unlock()
}

// uploadClientRecorder 实现 manager.UploadAPIClient,把每次 PutObject 的
// key 与 body 记下来,使 manager.Uploader 的上传路径可在无网络下走通。
type uploadClientRecorder struct {
	keys []string
	body string
}

func (r *uploadClientRecorder) PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if in.Key != nil {
		r.keys = append(r.keys, *in.Key)
	}
	if in.Body != nil {
		b, err := io.ReadAll(in.Body)
		if err != nil {
			return nil, err
		}
		r.body += string(b)
	}
	return &s3.PutObjectOutput{}, nil
}
func (r *uploadClientRecorder) UploadPart(context.Context, *s3.UploadPartInput, ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	return nil, nil
}
func (r *uploadClientRecorder) CreateMultipartUpload(context.Context, *s3.CreateMultipartUploadInput, ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	return nil, nil
}
func (r *uploadClientRecorder) CompleteMultipartUpload(context.Context, *s3.CompleteMultipartUploadInput, ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	return nil, nil
}
func (r *uploadClientRecorder) AbortMultipartUpload(context.Context, *s3.AbortMultipartUploadInput, ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	return nil, nil
}

// newTestUploader 构造指向 recorder 的 Uploader。
func newTestUploader(rec *uploadClientRecorder) *Uploader {
	return &Uploader{
		s3: rec,
		uploader: manager.NewUploader(rec, func(o *manager.Uploader) {
			o.PartSize = partSize
			o.Concurrency = 1
		}),
	}
}

func TestUploadFileCapturesKeyAndBody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(path, []byte("file-content"), 0o600); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	rec := &uploadClientRecorder{}
	u := newTestUploader(rec)

	if err := u.UploadFile(context.Background(), path, "mybucket", "dir/file.txt"); err != nil {
		t.Fatalf("UploadFile 报错: %v", err)
	}
	if len(rec.keys) != 1 || rec.keys[0] != "dir/file.txt" {
		t.Errorf("上传 key = %v,期望 [dir/file.txt]", rec.keys)
	}
	if rec.body != "file-content" {
		t.Errorf("上传内容 = %q,期望 file-content", rec.body)
	}
}

func TestUploadFileMissingPath(t *testing.T) {
	u := newTestUploader(&uploadClientRecorder{})
	if err := u.UploadFile(context.Background(), filepath.Join(t.TempDir(), "nope"), "b", "k"); err == nil {
		t.Errorf("缺失本地文件应报错")
	}
}

func TestUploadStream(t *testing.T) {
	rec := &uploadClientRecorder{}
	u := newTestUploader(rec)
	if err := u.UploadStream(context.Background(), bytes.NewReader([]byte("stream-data")), "b", "k"); err != nil {
		t.Fatalf("UploadStream 报错: %v", err)
	}
	if rec.body != "stream-data" {
		t.Errorf("上传内容 = %q,期望 stream-data", rec.body)
	}
}

// TestUploadDir 用临时目录验证:目录跳过、rel 转 / 分隔、prefix 拼接。
func TestUploadDir(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, content string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("sub/a.txt", "A")
	mustWrite("top.log", "T")

	rec := &uploadClientRecorder{}
	u := newTestUploader(rec)
	if err := u.UploadDir(context.Background(), dir, "bucket", "/mirror/"); err != nil {
		t.Fatalf("UploadDir 报错: %v", err)
	}
	got := append([]string(nil), rec.keys...)
	sort.Strings(got)
	want := []string{"mirror/sub/a.txt", "mirror/top.log"}
	if len(got) != len(want) {
		t.Fatalf("上传对象数 = %d,期望 %d(keys=%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("key[%d] = %q,期望 %q", i, got[i], want[i])
		}
	}
}
