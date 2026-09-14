package s3fs

import (
	"context"
	"errors"
	"testing"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/fakes3"
	"github.com/BeCrafter/sail/internal/vfs"
)

func newFSForInternal(t *testing.T) (*FS, *fakes3.Server) {
	t.Helper()
	srv := fakes3.New()
	t.Cleanup(srv.Close)
	s3c, err := client.New(context.Background(), &config.Resolved{
		Endpoint:  srv.URL(),
		AccessKey: "ak",
		SecretKey: "sk",
		Region:    "us-east-1",
		PathStyle: true,
	})
	if err != nil {
		t.Fatalf("构造 S3 client 失败: %v", err)
	}
	fs, err := New(Config{Client: s3c, Bucket: "b", StagingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("构造 s3fs 失败: %v", err)
	}
	return fs, srv
}

// 提交点必须是 File.Stat():写句柄 Close 在任何情况下都不能把成功的上传判成失败。
func TestCloseNeverFails(t *testing.T) {
	fs, srv := newFSForInternal(t)
	w, err := fs.OpenWrite(context.Background(), "/x.bin")
	if err != nil {
		t.Fatalf("OpenWrite 失败: %v", err)
	}
	if _, err := w.Write([]byte("abc")); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	if _, err := w.Commit(context.Background()); err != nil {
		t.Fatalf("Commit 失败: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Commit 成功后 Close 必须返回 nil,实际 %v", err)
	}
	// 幂等:重复 Close 仍返回 nil。
	if err := w.Close(); err != nil {
		t.Fatalf("重复 Close 必须返回 nil,实际 %v", err)
	}
	if _, ok := srv.Get("b", "x.bin"); !ok {
		t.Fatal("对象应已写入")
	}
}

// 暂存盘空间不足要在写入前被判成 ErrInsufficientStorage(壳据此回 507)。
func TestReserveRejectsInsufficientStagingSpace(t *testing.T) {
	if _, ok := availableBytes(t.TempDir()); !ok {
		t.Skip("当前平台不支持暂存盘空间探测")
	}
	fs, _ := newFSForInternal(t)
	w, err := fs.OpenWrite(context.Background(), "/huge.bin")
	if err != nil {
		t.Fatalf("OpenWrite 失败: %v", err)
	}
	defer w.Close()
	// 声明一个不可能满足的长度,模拟磁盘装不下。
	if opt, ok := w.(vfs.WriteOptioner); ok {
		opt.SetWriteOptions(vfs.WriteOptions{ContentLength: 1 << 62})
	}
	if _, err := w.Write([]byte("x")); !errors.Is(err, vfs.ErrInsufficientStorage) {
		t.Fatalf("期望 ErrInsufficientStorage,实际 %v", err)
	}
}

func TestPartSizeForScalesWithObjectSize(t *testing.T) {
	cases := []struct {
		size int64
		want int64
	}{
		{0, minPartSize},
		{1 << 30, minPartSize},
		{int64(maxParts) * minPartSize, minPartSize},
		// 超过 10000 × 5MiB 后必须显式放大分片,否则命中 S3 的分片数上限。
		{int64(maxParts)*minPartSize + 1, minPartSize + (1 << 20)},
	}
	for _, c := range cases {
		got := partSizeFor(c.size)
		if got != c.want {
			t.Errorf("partSizeFor(%d) = %d,期望 %d", c.size, got, c.want)
		}
		if got < minPartSize || got%(1<<20) != 0 {
			t.Errorf("partSizeFor(%d) = %d,必须是不小于 5MiB 的整 MiB", c.size, got)
		}
		if c.size > 0 && got*maxParts < c.size {
			t.Errorf("partSizeFor(%d) = %d 不足以在 %d 个分片内装下对象", c.size, got, maxParts)
		}
	}
}

// 写过程出错后,即便 webdav 仍按 PUT 流程调用 Stat(),也绝不能提交截断数据。
func TestCommitRefusesAfterFailedWrite(t *testing.T) {
	fs, srv := newFSForInternal(t)
	fs.maxUploadSize = 8
	w, err := fs.OpenWrite(context.Background(), "/partial.bin")
	if err != nil {
		t.Fatalf("OpenWrite 失败: %v", err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("0123456789")); !errors.Is(err, vfs.ErrTooLarge) {
		t.Fatalf("期望 ErrTooLarge,实际 %v", err)
	}
	if _, err := w.Commit(context.Background()); !errors.Is(err, vfs.ErrTooLarge) {
		t.Fatalf("写失败后 Commit 必须拒绝,实际 %v", err)
	}
	if _, ok := srv.Get("b", "partial.bin"); ok {
		t.Fatal("不得留下截断的残留对象")
	}
}

func TestNormalizeKeepsDirMarkerTrailingSlash(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/", "/"},
		{"/a", "/a"},
		{"/a/", "/a/"},
		{"/a/b/", "/a/b/"},
		{"a/b", "/a/b"},
		{"/a//b", "/a/b"},
	}
	for _, c := range cases {
		got, err := normalize(c.in)
		if err != nil {
			t.Fatalf("normalize(%q) 失败: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("normalize(%q) = %q,期望 %q", c.in, got, c.want)
		}
	}
}
