package s3fs_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/fakes3"
	"github.com/BeCrafter/sail/internal/vfs"
	"github.com/BeCrafter/sail/internal/vfs/s3fs"
)

const testBucket = "b"

func newFS(t *testing.T, prefix string, maxUploadSize int64) (*s3fs.FS, *fakes3.Server) {
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
	fs, err := s3fs.New(s3fs.Config{
		Client:        s3c,
		Bucket:        testBucket,
		Prefix:        prefix,
		StagingDir:    t.TempDir(),
		MaxUploadSize: maxUploadSize,
	})
	if err != nil {
		t.Fatalf("构造 s3fs 失败: %v", err)
	}
	return fs, srv
}

func TestStatFileUsesHeadObject(t *testing.T) {
	fs, srv := newFS(t, "", 0)
	srv.Put(testBucket, "dir/a.txt", []byte("hello"), "text/plain")

	fi, err := fs.Stat(context.Background(), "/dir/a.txt")
	if err != nil {
		t.Fatalf("Stat 失败: %v", err)
	}
	if fi.IsDir || fi.Name != "a.txt" || fi.Size != 5 {
		t.Fatalf("Stat 结果不符: %+v", fi)
	}
	if fi.ETag == "" {
		t.Fatal("对象 ETag 不应为空")
	}
	if srv.Counts.Head.Load() != 1 {
		t.Fatalf("Stat 应只发一次 HeadObject,实际 %d", srv.Counts.Head.Load())
	}
	if srv.Counts.Get.Load() != 0 {
		t.Fatalf("Stat 不应发 GetObject,实际 %d", srv.Counts.Get.Load())
	}
}

func TestStatDirectoryInferredFromCommonPrefix(t *testing.T) {
	fs, srv := newFS(t, "", 0)
	srv.Put(testBucket, "dir/a.txt", []byte("x"), "")

	fi, err := fs.Stat(context.Background(), "/dir")
	if err != nil {
		t.Fatalf("Stat 目录失败: %v", err)
	}
	if !fi.IsDir {
		t.Fatalf("应判定为目录: %+v", fi)
	}
	if fi.ETag != "" {
		t.Fatalf("目录 ETag 应为空,实际 %q", fi.ETag)
	}
}

func TestStatMissing(t *testing.T) {
	fs, _ := newFS(t, "", 0)
	_, err := fs.Stat(context.Background(), "/nope.txt")
	if !errors.Is(err, vfs.ErrNotExist) {
		t.Fatalf("期望 ErrNotExist,实际 %v", err)
	}
}

func TestReadDirMergesPrefixAndMarkerWithoutGetObject(t *testing.T) {
	fs, srv := newFS(t, "tenant-a", 0)
	srv.Put(testBucket, "tenant-a/data/a.txt", []byte("a"), "")
	srv.Put(testBucket, "tenant-a/data/sub/b.txt", []byte("b"), "")
	srv.Put(testBucket, "tenant-a/data/", nil, "") // 目录标记对象

	infos, err := fs.ReadDir(context.Background(), "/data")
	if err != nil {
		t.Fatalf("ReadDir 失败: %v", err)
	}
	got := map[string]bool{}
	for _, fi := range infos {
		got[fi.Name] = fi.IsDir
	}
	if len(got) != 2 {
		t.Fatalf("期望 a.txt 与 sub 两条,实际 %v", got)
	}
	if isDir, ok := got["a.txt"]; !ok || isDir {
		t.Fatalf("a.txt 应为文件: %v", got)
	}
	if isDir, ok := got["sub"]; !ok || !isDir {
		t.Fatalf("sub 应为目录: %v", got)
	}
	if srv.Counts.Get.Load() != 0 {
		t.Fatalf("ReadDir 不应发 GetObject,实际 %d", srv.Counts.Get.Load())
	}
	if srv.Counts.Head.Load() != 0 {
		t.Fatalf("ReadDir 不应退化成 HeadObject,实际 %d", srv.Counts.Head.Load())
	}
}

func TestReadDirPaginates(t *testing.T) {
	fs, srv := newFS(t, "", 0)
	for i := range 1100 {
		srv.Put(testBucket, fmt.Sprintf("bulk/%04d.bin", i), []byte("x"), "")
	}
	infos, err := fs.ReadDir(context.Background(), "/bulk")
	if err != nil {
		t.Fatalf("ReadDir 失败: %v", err)
	}
	if len(infos) != 1100 {
		t.Fatalf("期望 1100 条,实际 %d", len(infos))
	}
	if n := srv.Counts.List.Load(); n < 2 {
		t.Fatalf("1100 个对象应触发分页(≥2 次 ListObjectsV2),实际 %d", n)
	}
}

func TestReadDirEmptyMarkerDirectory(t *testing.T) {
	fs, srv := newFS(t, "", 0)
	srv.Put(testBucket, "empty/", nil, "")

	infos, err := fs.ReadDir(context.Background(), "/empty")
	if err != nil {
		t.Fatalf("ReadDir 失败: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("空目录应无子项,实际 %v", infos)
	}
}

func TestOpenReadSeekAndRange(t *testing.T) {
	fs, srv := newFS(t, "", 0)
	payload := bytes.Repeat([]byte("0123456789"), 1000)
	srv.Put(testBucket, "big.bin", payload, "")

	rc, err := fs.OpenRead(context.Background(), "/big.bin")
	if err != nil {
		t.Fatalf("OpenRead 失败: %v", err)
	}
	defer rc.Close()

	size, err := rc.Seek(0, io.SeekEnd)
	if err != nil || size != int64(len(payload)) {
		t.Fatalf("Seek(End) = %d, %v", size, err)
	}
	if _, err := rc.Seek(500, io.SeekStart); err != nil {
		t.Fatalf("Seek 失败: %v", err)
	}
	buf := make([]byte, 10)
	if _, err := io.ReadFull(rc, buf); err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !bytes.Equal(buf, payload[500:510]) {
		t.Fatalf("区间内容不符: %q", buf)
	}
	if srv.Counts.Get.Load() != 1 {
		t.Fatalf("一次区间读应只发一次 GetObject,实际 %d", srv.Counts.Get.Load())
	}
}

func TestOpenWriteCommitAndAbort(t *testing.T) {
	fs, srv := newFS(t, "tenant-a", 0)
	ctx := context.Background()

	w, err := fs.OpenWrite(ctx, "/out.bin")
	if err != nil {
		t.Fatalf("OpenWrite 失败: %v", err)
	}
	if opt, ok := w.(vfs.WriteOptioner); ok {
		opt.SetWriteOptions(vfs.WriteOptions{ContentType: "application/x-test", ContentLength: 3})
	}
	if _, err := w.Write([]byte("abc")); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	// 提交前桶内不得出现对象。
	if _, ok := srv.Get(testBucket, "tenant-a/out.bin"); ok {
		t.Fatal("Commit 之前不应产生对象")
	}
	fi, err := w.Commit(ctx)
	if err != nil {
		t.Fatalf("Commit 失败: %v", err)
	}
	if fi.Size != 3 || fi.ETag == "" {
		t.Fatalf("Commit 结果不符: %+v", fi)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close 应返回 nil: %v", err)
	}
	obj, ok := srv.Get(testBucket, "tenant-a/out.bin")
	if !ok || string(obj.Data) != "abc" {
		t.Fatalf("对象内容不符: %+v", obj)
	}
	if obj.ContentType != "application/x-test" {
		t.Fatalf("Content-Type 应透传,实际 %q", obj.ContentType)
	}

	// Abort 不落对象。
	w2, err := fs.OpenWrite(ctx, "/dropped.bin")
	if err != nil {
		t.Fatalf("OpenWrite 失败: %v", err)
	}
	if _, err := w2.Write([]byte("zzz")); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	if err := w2.Abort(); err != nil {
		t.Fatalf("Abort 失败: %v", err)
	}
	if _, ok := srv.Get(testBucket, "tenant-a/dropped.bin"); ok {
		t.Fatal("Abort 后不应产生对象")
	}
}

func TestOpenWriteRejectsTooLarge(t *testing.T) {
	fs, srv := newFS(t, "", 16)
	w, err := fs.OpenWrite(context.Background(), "/big.bin")
	if err != nil {
		t.Fatalf("OpenWrite 失败: %v", err)
	}
	defer w.Close()
	if _, err := w.Write(bytes.Repeat([]byte("x"), 32)); !errors.Is(err, vfs.ErrTooLarge) {
		t.Fatalf("期望 ErrTooLarge,实际 %v", err)
	}
	if _, ok := srv.Get(testBucket, "big.bin"); ok {
		t.Fatal("超限上传不应留下残留对象")
	}
}

func TestPathTraversalRejected(t *testing.T) {
	fs, srv := newFS(t, "tenant-a", 0)
	srv.Put(testBucket, "tenant-b/secret.txt", []byte("secret"), "")

	for _, p := range []string{"/../tenant-b/secret.txt", "/data/../../tenant-b/secret.txt"} {
		if _, err := fs.Stat(context.Background(), p); !errors.Is(err, vfs.ErrNotExist) {
			t.Fatalf("%s: 期望 ErrNotExist,实际 %v", p, err)
		}
	}
	if srv.Counts.Head.Load() != 0 {
		t.Fatalf("越界路径不应产生任何后端请求,实际 %d", srv.Counts.Head.Load())
	}
}

func TestRemoveRecursive(t *testing.T) {
	fs, srv := newFS(t, "", 0)
	srv.Put(testBucket, "dir/a.txt", []byte("a"), "")
	srv.Put(testBucket, "dir/sub/b.txt", []byte("b"), "")
	srv.Put(testBucket, "dir/", nil, "")
	srv.Put(testBucket, "other.txt", []byte("o"), "")

	if err := fs.Remove(context.Background(), "/dir", true); err != nil {
		t.Fatalf("Remove 失败: %v", err)
	}
	if keys := srv.Keys(testBucket); len(keys) != 1 || keys[0] != "other.txt" {
		t.Fatalf("递归删除后剩余 key 不符: %v", keys)
	}
}

func TestRenameObjectLevel(t *testing.T) {
	fs, srv := newFS(t, "", 0)
	srv.Put(testBucket, "a.txt", []byte("data"), "text/plain")

	if err := fs.Rename(context.Background(), "/a.txt", "/b.txt"); err != nil {
		t.Fatalf("Rename 失败: %v", err)
	}
	if _, ok := srv.Get(testBucket, "a.txt"); ok {
		t.Fatal("源对象应已删除")
	}
	obj, ok := srv.Get(testBucket, "b.txt")
	if !ok || string(obj.Data) != "data" {
		t.Fatalf("目标对象不符: %+v", obj)
	}
}

func TestRenameDirectoryNotSupported(t *testing.T) {
	fs, srv := newFS(t, "", 0)
	srv.Put(testBucket, "dir/a.txt", []byte("a"), "")

	err := fs.Rename(context.Background(), "/dir", "/dir2")
	if !errors.Is(err, vfs.ErrNotSupported) {
		t.Fatalf("目录级 Rename 期望 ErrNotSupported,实际 %v", err)
	}
}

func TestPrefixScoping(t *testing.T) {
	fs, srv := newFS(t, "tenant-a", 0)
	srv.Put(testBucket, "tenant-a/visible.txt", []byte("v"), "")
	srv.Put(testBucket, "tenant-b/hidden.txt", []byte("h"), "")

	infos, err := fs.ReadDir(context.Background(), "/")
	if err != nil {
		t.Fatalf("ReadDir 失败: %v", err)
	}
	if len(infos) != 1 || infos[0].Name != "visible.txt" {
		t.Fatalf("共享根只应看到本前缀内容,实际 %v", infos)
	}
}

func TestCommitIsIdempotent(t *testing.T) {
	fs, srv := newFS(t, "", 0)
	ctx := context.Background()
	w, err := fs.OpenWrite(ctx, "/once.txt")
	if err != nil {
		t.Fatalf("OpenWrite 失败: %v", err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	first, err := w.Commit(ctx)
	if err != nil {
		t.Fatalf("Commit 失败: %v", err)
	}
	second, err := w.Commit(ctx)
	if err != nil {
		t.Fatalf("重复 Commit 失败: %v", err)
	}
	if first.ETag != second.ETag {
		t.Fatalf("重复 Commit 应返回同一结果: %q vs %q", first.ETag, second.ETag)
	}
	if n := srv.Counts.Put.Load() + srv.Counts.List.Load(); n == 0 {
		t.Fatal("应发生过上传")
	}
}

func TestNormalizeRejectsEmptyPath(t *testing.T) {
	fs, _ := newFS(t, "", 0)
	if _, err := fs.Stat(context.Background(), ""); !errors.Is(err, vfs.ErrNotExist) {
		t.Fatalf("空路径期望 ErrNotExist,实际 %v", err)
	}
	if _, err := fs.OpenRead(context.Background(), "/"); !errors.Is(err, vfs.ErrNotSupported) {
		t.Fatalf("读取根期望 ErrNotSupported,实际 %v", err)
	}
}

// 超过分片下限的对象必须走 multipart,且数据与 Content-Type 都要保住。
func TestMultipartUploadKeepsDataAndContentType(t *testing.T) {
	fs, srv := newFS(t, "", 0)
	payload := bytes.Repeat([]byte("m"), 6<<20) // 6MiB > 5MiB 分片下限,强制多分片
	ctx := context.Background()

	w, err := fs.OpenWrite(ctx, "/big.bin")
	if err != nil {
		t.Fatalf("OpenWrite 失败: %v", err)
	}
	defer w.Close()
	if opt, ok := w.(vfs.WriteOptioner); ok {
		opt.SetWriteOptions(vfs.WriteOptions{
			ContentType:   "application/x-multipart",
			ContentLength: int64(len(payload)),
		})
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	fi, err := w.Commit(ctx)
	if err != nil {
		t.Fatalf("Commit 失败: %v", err)
	}
	if fi.Size != int64(len(payload)) {
		t.Fatalf("提交大小 %d,期望 %d", fi.Size, len(payload))
	}
	if n := srv.Counts.Multipart.Load(); n < 1 {
		t.Fatalf("6MiB 对象应走 multipart,实际建单 %d 次", n)
	}
	obj, ok := srv.Get(testBucket, "big.bin")
	if !ok || !bytes.Equal(obj.Data, payload) {
		t.Fatal("multipart 上传的数据不完整")
	}
	if obj.ContentType != "application/x-multipart" {
		t.Fatalf("multipart 上传应保留 Content-Type,实际 %q", obj.ContentType)
	}
}

// 声明长度与实际写入量不符时(客户端中途断开/请求体破损)拒绝提交,
// 否则截断的数据会覆盖桶里的好对象。
func TestCommitRefusesTruncatedBody(t *testing.T) {
	fs, srv := newFS(t, "", 0)
	srv.Put(testBucket, "keep.bin", []byte("original-good-data"), "text/plain")
	ctx := context.Background()

	w, err := fs.OpenWrite(ctx, "/keep.bin")
	if err != nil {
		t.Fatalf("OpenWrite 失败: %v", err)
	}
	defer w.Close()
	if opt, ok := w.(vfs.WriteOptioner); ok {
		opt.SetWriteOptions(vfs.WriteOptions{ContentLength: 100})
	}
	if _, err := w.Write([]byte("partial")); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	if _, err := w.Commit(ctx); err == nil {
		t.Fatal("长度不符时 Commit 必须报错")
	}
	obj, ok := srv.Get(testBucket, "keep.bin")
	if !ok || string(obj.Data) != "original-good-data" {
		t.Fatalf("桶里原有对象不应被截断数据覆盖,实际 %+v", obj)
	}
}
