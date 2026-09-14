package s3fs_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/fakes3"
	"github.com/BeCrafter/sail/internal/vfs"
	"github.com/BeCrafter/sail/internal/vfs/s3fs"
)

// 契约把 --chunk-size 的下限钉在 S3 multipart 的 5MiB(见 parseChunkSize),
// 所以测试也必须用 ≥5MiB 的片。3 片 = 15MiB,足以覆盖跨片与覆盖写场景,
// 又不会把内存测试端点撑爆。
const (
	testChunkSize  = 5 << 20
	threeChunkSize = 3 * testChunkSize
)

func newChunkedFS(t *testing.T, chunk int64) (*s3fs.FS, *fakes3.Server) {
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
		StagingDir:    t.TempDir(),
		ChunkedUpload: true,
		ChunkSize:     chunk,
	})
	if err != nil {
		t.Fatalf("构造 s3fs 失败: %v", err)
	}
	return fs, srv
}

// putAll 经 OpenWrite/Commit 完整写入一个对象,返回提交结果。
func putAll(t *testing.T, fs *s3fs.FS, path string, data []byte, contentType string) vfs.FileInfo {
	t.Helper()
	w, err := fs.OpenWrite(context.Background(), path)
	if err != nil {
		t.Fatalf("OpenWrite %s 失败: %v", path, err)
	}
	defer w.Close()
	if setter, ok := w.(vfs.WriteOptioner); ok {
		setter.SetWriteOptions(vfs.WriteOptions{ContentType: contentType, ContentLength: int64(len(data))})
	}
	if _, err := w.Write(data); err != nil {
		t.Fatalf("Write %s 失败: %v", path, err)
	}
	fi, err := w.Commit(context.Background())
	if err != nil {
		t.Fatalf("Commit %s 失败: %v", path, err)
	}
	return fi
}

func readAll(t *testing.T, fs *s3fs.FS, path string) []byte {
	t.Helper()
	r, err := fs.OpenRead(context.Background(), path)
	if err != nil {
		t.Fatalf("OpenRead %s 失败: %v", path, err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return b
}

// seq 生成确定性内容,长度 n。用 1MiB 的滑动模式,便于跨片区分片边界。
func seq(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((i / 1024) % 251)
	}
	return b
}

func keysWithPrefix(srv *fakes3.Server, prefix string) []string {
	var out []string
	for _, k := range srv.Keys(testBucket) {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

func versionsOf(parts []string) map[string]bool {
	versions := map[string]bool{}
	for _, k := range parts {
		seg := strings.Split(k, "/")
		if len(seg) >= 3 {
			versions[seg[2]] = true
		}
	}
	return versions
}

// --- Scenario: 上传超过片阈值的文件后原样取回 ---------------------------------

func TestChunkedUploadRoundTrip(t *testing.T) {
	fs, srv := newChunkedFS(t, testChunkSize)
	data := seq(threeChunkSize) // 3 片
	fi := putAll(t, fs, "/big.bin", data, "application/octet-stream")

	if fi.Size != int64(len(data)) {
		t.Fatalf("提交结果应为逻辑大小 %d,实际 %d", len(data), fi.Size)
	}
	if fi.ETag == "" {
		t.Fatal("提交结果必须带 ETag")
	}

	manifestObj, ok := srv.Get(testBucket, "big.bin")
	if !ok {
		t.Fatal("逻辑 key 上应有 manifest 对象")
	}
	if !strings.Contains(string(manifestObj.Data), `"sail_manifest":1`) {
		t.Fatalf("逻辑 key 上的对象不是 manifest: %s", manifestObj.Data)
	}
	if manifestObj.ContentType != "application/octet-stream" {
		t.Fatalf("manifest 的 Content-Type 应为原文件类型,实际 %q", manifestObj.ContentType)
	}
	if manifestObj.Metadata["sail-manifest-key"] == "" || manifestObj.Metadata["sail-total-size"] != "15728640" {
		t.Fatalf("manifest 元数据不完整: %+v", manifestObj.Metadata)
	}

	parts := keysWithPrefix(srv, ".sail/parts/")
	if len(parts) != 3 {
		t.Fatalf("应有 3 片,实际 %d: %v", len(parts), parts)
	}
	for _, k := range parts {
		o, _ := srv.Get(testBucket, k)
		if len(o.Data) > testChunkSize {
			t.Fatalf("片 %s 超过 --chunk-size: %d 字节", k, len(o.Data))
		}
	}

	if got := readAll(t, fs, "/big.bin"); !bytes.Equal(got, data) {
		t.Fatalf("分片文件读回内容与源不一致(长度 %d vs %d)", len(got), len(data))
	}
}

// --- Scenario: 逻辑大小而非 manifest 物理大小 ---------------------------------

func TestChunkedStatReportsLogicalSize(t *testing.T) {
	fs, srv := newChunkedFS(t, testChunkSize)
	data := seq(threeChunkSize)
	putAll(t, fs, "/big.bin", data, "application/octet-stream")

	before := srv.Counts.Get.Load()
	fi, err := fs.Stat(context.Background(), "/big.bin")
	if err != nil {
		t.Fatalf("Stat 失败: %v", err)
	}
	if fi.Size != int64(len(data)) {
		t.Fatalf("Stat 应报逻辑大小 %d,实际 %d", len(data), fi.Size)
	}
	if fi.IsDir {
		t.Fatal("分片文件不应被判为目录")
	}
	if fi.ContentType != "application/octet-stream" {
		t.Fatalf("Content-Type 应为原文件类型,实际 %q", fi.ContentType)
	}
	if srv.Counts.Get.Load() != before {
		t.Fatal("元数据快路径下 Stat 不应发 GetObject")
	}
}

func TestChunkedReadDirReportsLogicalSizeAndHidesInternals(t *testing.T) {
	fs, srv := newChunkedFS(t, testChunkSize)
	putAll(t, fs, "/dir/big.bin", seq(threeChunkSize), "application/octet-stream")
	putAll(t, fs, "/dir/small.txt", seq(20), "text/plain")

	entries, err := fs.ReadDir(context.Background(), "/dir")
	if err != nil {
		t.Fatalf("ReadDir 失败: %v", err)
	}
	byName := map[string]vfs.FileInfo{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	if got := byName["big.bin"].Size; got != int64(threeChunkSize) {
		t.Fatalf("列目录应报分片文件的逻辑大小 %d,实际 %d", threeChunkSize, got)
	}
	if got := byName["small.txt"].Size; got != 20 {
		t.Fatalf("普通文件大小应为 20,实际 %d", got)
	}
	for _, e := range entries {
		if e.Name == ".sail" || strings.HasPrefix(e.Path, "/.sail") {
			t.Fatalf("保留前缀 .sail 不应出现在列目录结果里: %+v", entries)
		}
	}

	root, err := fs.ReadDir(context.Background(), "/")
	if err != nil {
		t.Fatalf("ReadDir / 失败: %v", err)
	}
	for _, e := range root {
		if e.Name == ".sail" {
			t.Fatalf("根目录不应出现 .sail: %+v", root)
		}
	}
	_ = srv
}

// --- Scenario: 跨片 Range 读 -------------------------------------------------

func TestChunkedCrossChunkRangeRead(t *testing.T) {
	fs, srv := newChunkedFS(t, testChunkSize)
	data := seq(threeChunkSize)
	putAll(t, fs, "/big.bin", data, "application/octet-stream")

	r, err := fs.OpenRead(context.Background(), "/big.bin")
	if err != nil {
		t.Fatalf("OpenRead 失败: %v", err)
	}
	defer r.Close()
	// 从第 1 片末尾跨到第 2 片开头。
	start := int64(testChunkSize) - 50
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		t.Fatalf("Seek 失败: %v", err)
	}
	before := srv.Counts.Get.Load()
	buf := make([]byte, 100)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("跨片读取失败: %v", err)
	}
	if !bytes.Equal(buf, data[start:start+100]) {
		t.Fatalf("跨片区间内容与源不一致")
	}
	if got := srv.Counts.Get.Load() - before; got != 2 {
		t.Fatalf("跨片读取应只 GetObject 2 片,实际 %d", got)
	}
}

func TestChunkedOffsetSeekOpensOnlyOneChunk(t *testing.T) {
	fs, srv := newChunkedFS(t, testChunkSize)
	data := seq(threeChunkSize)
	putAll(t, fs, "/big.bin", data, "application/octet-stream")

	r, err := fs.OpenRead(context.Background(), "/big.bin")
	if err != nil {
		t.Fatalf("OpenRead 失败: %v", err)
	}
	defer r.Close()

	// 模拟 http.ServeContent 的定位组合:先 SeekEnd 再 SeekStart。
	if _, err := r.Seek(0, io.SeekEnd); err != nil {
		t.Fatalf("SeekEnd 失败: %v", err)
	}
	// 落在第 3 片内。
	start := int64(2*testChunkSize) + 10
	if got, err := r.Seek(start, io.SeekStart); err != nil || got != start {
		t.Fatalf("SeekStart 失败: %v", err)
	}
	before := srv.Counts.Get.Load()
	buf := make([]byte, 100)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !bytes.Equal(buf, data[start:start+100]) {
		t.Fatalf("偏移读取内容不符")
	}
	if got := srv.Counts.Get.Load() - before; got != 1 {
		t.Fatalf("落在单片内的区间读应只 GetObject 1 片,实际 %d(做了整文件从头读?)", got)
	}
}

func TestChunkedSeekEndReportsLogicalSize(t *testing.T) {
	fs, _ := newChunkedFS(t, testChunkSize)
	putAll(t, fs, "/big.bin", seq(threeChunkSize), "application/octet-stream")
	r, err := fs.OpenRead(context.Background(), "/big.bin")
	if err != nil {
		t.Fatalf("OpenRead 失败: %v", err)
	}
	defer r.Close()
	got, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		t.Fatalf("SeekEnd 失败: %v", err)
	}
	if got != int64(threeChunkSize) {
		t.Fatalf("SeekEnd 应为逻辑大小 %d,实际 %d", threeChunkSize, got)
	}
}

// --- Scenario: 覆盖写不丢数据、不留可见残留 ------------------------------------

func TestChunkedOverwriteSwapsAndCleansOldChunks(t *testing.T) {
	fs, srv := newChunkedFS(t, testChunkSize)
	v1 := seq(threeChunkSize) // 3 片
	putAll(t, fs, "/big.bin", v1, "application/octet-stream")
	if parts := keysWithPrefix(srv, ".sail/parts/"); len(parts) != 3 {
		t.Fatalf("v1 应有 3 片,实际 %d", len(parts))
	}

	v2 := seq(2 * threeChunkSize) // 6 片,内容与 v1 不同
	putAll(t, fs, "/big.bin", v2, "application/octet-stream")

	if got := readAll(t, fs, "/big.bin"); !bytes.Equal(got, v2) {
		t.Fatal("覆盖写后读到的不是 v2")
	}
	parts := keysWithPrefix(srv, ".sail/parts/")
	if len(parts) != 6 {
		t.Fatalf("覆盖写后应只剩 v2 的 6 片,实际 %d: %v", len(parts), parts)
	}
	if vs := versionsOf(parts); len(vs) != 1 {
		t.Fatalf("应只剩一个版本目录,实际 %v", vs)
	}
}

// --- Scenario: 删除分片文件后桶内可判、可收 ------------------------------------

func TestChunkedRemoveDeletesManifestAndChunks(t *testing.T) {
	fs, srv := newChunkedFS(t, testChunkSize)
	putAll(t, fs, "/big.bin", seq(threeChunkSize), "application/octet-stream")

	if err := fs.Remove(context.Background(), "/big.bin", false); err != nil {
		t.Fatalf("Remove 失败: %v", err)
	}
	if _, ok := srv.Get(testBucket, "big.bin"); ok {
		t.Fatal("逻辑 key 应立即不可见")
	}
	if left := keysWithPrefix(srv, ".sail/parts/"); len(left) != 0 {
		t.Fatalf("片目录应被清理,残留 %v", left)
	}
	if _, err := fs.Stat(context.Background(), "/big.bin"); !errors.Is(err, vfs.ErrNotExist) {
		t.Fatalf("删除后 Stat 应报 ErrNotExist,实际 %v", err)
	}
}

// --- Scenario: manifest 判定的两条路都成立 ------------------------------------

func TestChunkedSequentialReadAdvancesPerChunk(t *testing.T) {
	fs, srv := newChunkedFS(t, testChunkSize)
	data := seq(threeChunkSize)
	putAll(t, fs, "/big.bin", data, "application/octet-stream")

	r, err := fs.OpenRead(context.Background(), "/big.bin")
	if err != nil {
		t.Fatalf("OpenRead 失败: %v", err)
	}
	defer r.Close()

	// 只读满第 1 片:应只打开 manifest + 第 1 片,绝不预热第 2 片。
	buf := make([]byte, testChunkSize)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("读取第 1 片失败: %v", err)
	}
	if !bytes.Equal(buf, data[:testChunkSize]) {
		t.Fatal("第 1 片内容与源不一致")
	}
	if got := srv.Counts.Get.Load(); got != 2 {
		t.Fatalf("读满第 1 片应只 GetObject 2 次(manifest + 首片),实际 %d", got)
	}

	// 越过片边界:第 2 片的内容必须正确(跨片衔接不透传错位字节)。
	next := make([]byte, 64)
	if _, err := io.ReadFull(r, next); err != nil {
		t.Fatalf("跨片读取失败: %v", err)
	}
	if !bytes.Equal(next, data[testChunkSize:testChunkSize+64]) {
		t.Fatal("跨片衔接内容与源不一致")
	}
	if got := srv.Counts.Get.Load(); got != 3 {
		t.Fatalf("越过片边界后应新增 1 次 GetObject,累计 3 次,实际 %d", got)
	}
}

func TestChunkedManifestDetectionFallsBackToJSON(t *testing.T) {
	fs, srv := newChunkedFS(t, testChunkSize)
	// 先构造正常分片文件,再把 manifest 体以「元数据被抹掉」的方式重放:
	// 模拟桶迁移 / 第三方工具重传后 x-amz-meta-* 丢失。
	putAll(t, fs, "/big.bin", seq(threeChunkSize), "application/octet-stream")
	manifestObj, _ := srv.Get(testBucket, "big.bin")
	srv.Put(testBucket, "big.bin", manifestObj.Data, "application/octet-stream")

	fi, err := fs.Stat(context.Background(), "/big.bin")
	if err != nil {
		t.Fatalf("Stat 失败: %v", err)
	}
	if fi.Size != int64(threeChunkSize) {
		t.Fatalf("JSON 兜底应报逻辑大小 %d,实际 %d", threeChunkSize, fi.Size)
	}
	if got := readAll(t, fs, "/big.bin"); !bytes.Equal(got, seq(threeChunkSize)) {
		t.Fatal("JSON 兜底读取内容不符")
	}
}

// --- Scenario: 非分片文件不被误判 ---------------------------------------------

func TestManifestMisdetectionGuard(t *testing.T) {
	fs, srv := newChunkedFS(t, testChunkSize)
	// 以 manifest 魔数开头、但体积远超 256KiB 上界的普通文件。
	payload := append([]byte(`{"sail_manifest":1,"note":"this is user data, not a manifest"}`),
		bytes.Repeat([]byte("x"), 300*1024)...)
	srv.Put(testBucket, "plain.bin", payload, "application/octet-stream")

	before := srv.Counts.Get.Load()
	fi, err := fs.Stat(context.Background(), "/plain.bin")
	if err != nil {
		t.Fatalf("Stat 失败: %v", err)
	}
	if fi.Size != int64(len(payload)) {
		t.Fatalf("应报真实大小 %d,实际 %d", len(payload), fi.Size)
	}
	if srv.Counts.Get.Load() != before {
		t.Fatal("超过体积上界的普通文件不应被探测(不该发 GET)")
	}
	if got := readAll(t, fs, "/plain.bin"); !bytes.Equal(got, payload) {
		t.Fatal("普通文件应原样返回,不做 manifest 解析")
	}
}

func TestPlainFileUnderThresholdStaysPlain(t *testing.T) {
	fs, srv := newChunkedFS(t, testChunkSize)
	putAll(t, fs, "/small.txt", seq(64), "text/plain") // 64 < chunkSize

	if _, ok := srv.Get(testBucket, "small.txt"); !ok {
		t.Fatal("未超阈值的文件应仍是朴素对象")
	}
	if parts := keysWithPrefix(srv, ".sail/parts/"); len(parts) != 0 {
		t.Fatalf("未超阈值的文件不应产生片对象: %v", parts)
	}
	if got := readAll(t, fs, "/small.txt"); !bytes.Equal(got, seq(64)) {
		t.Fatal("朴素对象读取内容不符")
	}
}

// --- Scenario: MOVE 分片文件仍可用 --------------------------------------------

func TestChunkedRenameKeepsContentReadable(t *testing.T) {
	fs, srv := newChunkedFS(t, testChunkSize)
	data := seq(threeChunkSize)
	putAll(t, fs, "/a/big.bin", data, "application/octet-stream")

	if err := fs.Rename(context.Background(), "/a/big.bin", "/b/renamed.bin"); err != nil {
		t.Fatalf("Rename 失败: %v", err)
	}
	if got := readAll(t, fs, "/b/renamed.bin"); !bytes.Equal(got, data) {
		t.Fatal("重命名后内容应逐字节一致")
	}
	if _, err := fs.Stat(context.Background(), "/a/big.bin"); !errors.Is(err, vfs.ErrNotExist) {
		t.Fatalf("源路径应不可见,实际 %v", err)
	}
	if _, ok := srv.Get(testBucket, "a/big.bin"); ok {
		t.Fatal("源 manifest 应被删除")
	}
}

// --- Scenario: 分片关闭时行为与 P1 完全一致 ------------------------------------

func TestChunkingDisabledKeepsSingleObject(t *testing.T) {
	fs, srv := newFS(t, "", 0)
	putAll(t, fs, "/big.bin", seq(threeChunkSize), "application/octet-stream")

	o, ok := srv.Get(testBucket, "big.bin")
	if !ok {
		t.Fatal("默认(未开分片)应在逻辑 key 上写朴素对象")
	}
	if !bytes.Equal(o.Data, seq(threeChunkSize)) {
		t.Fatal("默认路径应存完整内容")
	}
	if parts := keysWithPrefix(srv, ".sail/parts/"); len(parts) != 0 {
		t.Fatalf("未开分片不应产生片对象: %v", parts)
	}
	if got := readAll(t, fs, "/big.bin"); !bytes.Equal(got, seq(threeChunkSize)) {
		t.Fatal("默认路径读取内容不符")
	}
}

func TestChunkedConfigRejectsBadChunkSize(t *testing.T) {
	srv := fakes3.New()
	t.Cleanup(srv.Close)
	s3c, err := client.New(context.Background(), &config.Resolved{
		Endpoint: srv.URL(), AccessKey: "ak", SecretKey: "sk", Region: "us-east-1", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("构造 S3 client 失败: %v", err)
	}
	for _, size := range []int64{1 << 10, 6 << 30} {
		if _, err := s3fs.New(s3fs.Config{
			Client: s3c, Bucket: testBucket, StagingDir: t.TempDir(),
			ChunkedUpload: true, ChunkSize: size,
		}); err == nil {
			t.Fatalf("chunk-size=%d 应被拒绝", size)
		}
	}
}

// --- manifest 的分片表与版本号 ------------------------------------------------

func TestChunkedManifestChunkPlan(t *testing.T) {
	fs, srv := newChunkedFS(t, testChunkSize)
	data := seq(threeChunkSize)
	putAll(t, fs, "/big.bin", data, "application/octet-stream")
	o, _ := srv.Get(testBucket, "big.bin")

	var m struct {
		SailManifest int    `json:"sail_manifest"`
		Version      string `json:"version"`
		Size         int64  `json:"size"`
		ChunkSize    int64  `json:"chunk_size"`
		Chunks       []struct {
			Offset int64  `json:"offset"`
			Length int64  `json:"length"`
			ETag   string `json:"etag"`
		} `json:"chunks"`
	}
	if err := json.Unmarshal(o.Data, &m); err != nil {
		t.Fatalf("manifest 不是合法 JSON: %v", err)
	}
	if m.SailManifest != 1 {
		t.Fatalf("manifest 哨兵应为 1,实际 %d", m.SailManifest)
	}
	if m.Size != int64(len(data)) || m.ChunkSize != testChunkSize {
		t.Fatalf("manifest 头部字段不符: %+v", m)
	}
	if len(m.Chunks) != 3 {
		t.Fatalf("分片数应为 3,实际 %d", len(m.Chunks))
	}
	var want int64
	for i, c := range m.Chunks {
		if c.Offset != want || c.Length != testChunkSize {
			t.Fatalf("第 %d 片偏移/长度不符: got (%d,%d) want (%d,%d)", i, c.Offset, c.Length, want, testChunkSize)
		}
		if c.ETag == "" {
			t.Fatalf("第 %d 片缺少 ETag", i)
		}
		want += c.Length
	}
	// version 是暂存内容的 SHA-256。
	sum := sha256.Sum256(data)
	if m.Version != hex.EncodeToString(sum[:]) {
		t.Fatalf("version 应为暂存内容的 SHA-256,实际 %s", m.Version)
	}
}

// --- 小文件探测的代价有界(仅开分片且元数据缺失时) ------------------------------

func TestProbeOnlyForSmallPlainObjects(t *testing.T) {
	fs, srv := newChunkedFS(t, testChunkSize)
	// 模拟第三方工具写入的朴素小对象:没有任何元数据,体积在探测上界内。
	srv.Put(testBucket, "s.txt", seq(64), "text/plain")
	before := srv.Counts.Get.Load()
	fi, err := fs.Stat(context.Background(), "/s.txt")
	if err != nil {
		t.Fatalf("Stat 失败: %v", err)
	}
	if fi.Size != 64 {
		t.Fatalf("大小应为 64,实际 %d", fi.Size)
	}
	if got := srv.Counts.Get.Load() - before; got != 1 {
		t.Fatalf("元数据缺失的小对象应有一次 bounded 探测,实际 %d 次 GET", got)
	}
}
