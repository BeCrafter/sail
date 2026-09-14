package webdavfs_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/fakes3"
	"github.com/BeCrafter/sail/internal/vfs/s3fs"
	"github.com/BeCrafter/sail/internal/webdavfs"
)

// 契约把片大小下限钉在 5MiB,测试用 3 片 = 15MiB。
const (
	gwChunkSize  = 5 << 20
	gwThreeChunk = 3 * gwChunkSize
)

func newChunkedGateway(t *testing.T) *gateway {
	t.Helper()
	s3srv := fakes3.New()
	t.Cleanup(s3srv.Close)
	s3c, err := client.New(context.Background(), &config.Resolved{
		Endpoint:  s3srv.URL(),
		AccessKey: "ak",
		SecretKey: "sk",
		Region:    "us-east-1",
		PathStyle: true,
	})
	if err != nil {
		t.Fatalf("构造 S3 client 失败: %v", err)
	}
	core, err := s3fs.New(s3fs.Config{
		Client:        s3c,
		Bucket:        bucket,
		StagingDir:    t.TempDir(),
		ChunkedUpload: true,
		ChunkSize:     gwChunkSize,
	})
	if err != nil {
		t.Fatalf("构造 s3fs 失败: %v", err)
	}
	gw, err := webdavfs.NewServer(webdavfs.Config{
		FileSystem:        webdavfs.New(core),
		User:              "alice",
		Password:          "secret",
		MaxUploadSize:     0,
		MaxUploadSizeText: "unlimited",
	})
	if err != nil {
		t.Fatalf("构造 WebDAV 网关失败: %v", err)
	}
	ts := httptest.NewServer(gw)
	t.Cleanup(ts.Close)
	return &gateway{ts: ts, s3: s3srv, cli: ts.Client()}
}

func gwSeq(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((i / 1024) % 251)
	}
	return b
}

// 经挂载点上传一个 15MiB 文件(3 片),再经挂载点原样读回。
func TestChunkedPutGetRoundTripThroughGateway(t *testing.T) {
	g := newChunkedGateway(t)
	data := gwSeq(gwThreeChunk)

	resp := g.do(t, "PUT", "/big.bin", string(data), true, map[string]string{"Content-Type": "application/octet-stream"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT 期望 201,实际 %d", resp.StatusCode)
	}
	if len(keysWithPrefixS3(g, ".sail/parts/")) == 0 {
		t.Fatal("应产生分片对象")
	}

	got := g.do(t, "GET", "/big.bin", "", true, nil)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("GET 期望 200,实际 %d", got.StatusCode)
	}
	body, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatalf("读取响应体失败: %v", err)
	}
	if !bytes.Equal(body, data) {
		t.Fatalf("经挂载点读回内容与源不一致(%d vs %d 字节)", len(body), len(data))
	}
}

// PROPFIND 必须报逻辑大小与逻辑类型,而不是 manifest JSON 的物理大小。
func TestChunkedPropfindReportsLogicalSize(t *testing.T) {
	g := newChunkedGateway(t)
	data := gwSeq(gwThreeChunk)
	if resp := g.do(t, "PUT", "/big.bin", string(data), true, map[string]string{"Content-Type": "application/pdf"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT 期望 201,实际 %d", resp.StatusCode)
	}

	resp := g.do(t, "PROPFIND", "/big.bin", propfindAll, true, map[string]string{"Depth": "0"})
	ms := parseMulti(t, resp)
	if len(ms.Responses) == 0 {
		t.Fatal("PROPFIND 无响应条目")
	}
	var size, ctype string
	for _, ps := range ms.Responses[0].Propstat {
		if ps.Prop.ContentLength != "" {
			size = ps.Prop.ContentLength
		}
		if ps.Prop.ContentType != "" {
			ctype = ps.Prop.ContentType
		}
	}
	if size != fmt.Sprintf("%d", gwThreeChunk) {
		t.Fatalf("getcontentlength 应为逻辑大小 %d,实际 %q", gwThreeChunk, size)
	}
	if ctype != "application/pdf" {
		t.Fatalf("getcontenttype 应为原文件类型,实际 %q", ctype)
	}
}

// Range 下载跨片时必须只取涉及的分片,且逐字节一致。
func TestChunkedRangeThroughGateway(t *testing.T) {
	g := newChunkedGateway(t)
	data := gwSeq(gwThreeChunk)
	if resp := g.do(t, "PUT", "/big.bin", string(data), true, map[string]string{"Content-Type": "application/octet-stream"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT 期望 201,实际 %d", resp.StatusCode)
	}

	// 跨第 1、2 片边界(偏移 gwChunkSize-50 起 100 字节)。
	start := int64(gwChunkSize) - 50
	before := g.s3.Counts.Get.Load()
	resp := g.do(t, "GET", "/big.bin", "", true, map[string]string{
		"Range": fmt.Sprintf("bytes=%d-%d", start, start+99),
	})
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("期望 206,实际 %d", resp.StatusCode)
	}
	wantCR := fmt.Sprintf("bytes %d-%d/%d", start, start+99, gwThreeChunk)
	if got := resp.Header.Get("Content-Range"); got != wantCR {
		t.Fatalf("Content-Range 不符: got %q want %q", got, wantCR)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, data[start:start+100]) {
		t.Fatal("Range 内容与源不一致")
	}
	// manifest + 2 片(允许有界波动,但绝不能整文件逐片拉取)。
	if got := g.s3.Counts.Get.Load() - before; got > 4 {
		t.Fatalf("跨片 Range 读取了 %d 次 GetObject,疑似整文件从头读", got)
	}
}

// 重命名分片文件后,经挂载点仍能完整读出。
func TestChunkedMoveThroughGateway(t *testing.T) {
	g := newChunkedGateway(t)
	data := gwSeq(gwThreeChunk)
	if resp := g.do(t, "PUT", "/a.bin", string(data), true, map[string]string{"Content-Type": "application/octet-stream"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT 期望 201,实际 %d", resp.StatusCode)
	}
	mv := g.do(t, "MOVE", "/a.bin", "", true, map[string]string{"Destination": g.ts.URL + "/b.bin"})
	if mv.StatusCode/100 != 2 {
		t.Fatalf("MOVE 期望 2xx,实际 %d", mv.StatusCode)
	}
	resp := g.do(t, "GET", "/b.bin", "", true, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET 重命名后的文件期望 200,实际 %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, data) {
		t.Fatal("重命名后内容与源不一致")
	}
}

// DELETE 分片文件必须回收片目录。webdav 的 DELETE 一律走 RemoveAll →
// Remove(recursive=true),此前该分支不回收片,留下永久孤儿。
func TestChunkedDeleteThroughGatewayReclaimsParts(t *testing.T) {
	g := newChunkedGateway(t)
	data := gwSeq(gwThreeChunk)
	if resp := g.do(t, "PUT", "/big.bin", string(data), true, map[string]string{"Content-Type": "application/octet-stream"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT 期望 201,实际 %d", resp.StatusCode)
	}

	resp := g.do(t, "DELETE", "/big.bin", "", true, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE 期望 204,实际 %d", resp.StatusCode)
	}
	if got := g.do(t, "GET", "/big.bin", "", true, nil); got.StatusCode != http.StatusNotFound {
		t.Fatalf("删除后 GET 期望 404,实际 %d", got.StatusCode)
	}
	if left := keysWithPrefixS3(g, ".sail/parts/"); len(left) != 0 {
		t.Fatalf("片目录应被回收,残留 %v", left)
	}
}

// 经挂载点删除目录时,子树内所有分片文件的片都要回收。
func TestChunkedDeleteDirectoryThroughGatewayReclaimsParts(t *testing.T) {
	g := newChunkedGateway(t)
	if resp := g.do(t, "PUT", "/d/one.bin", string(gwSeq(gwThreeChunk)), true, map[string]string{"Content-Type": "application/octet-stream"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT 期望 201,实际 %d", resp.StatusCode)
	}
	if resp := g.do(t, "PUT", "/d/two.bin", string(gwSeq(gwThreeChunk)), true, map[string]string{"Content-Type": "application/octet-stream"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT 期望 201,实际 %d", resp.StatusCode)
	}
	if got := keysWithPrefixS3(g, ".sail/parts/d/"); len(got) != 6 {
		t.Fatalf("目录下应有 6 片,实际 %d", len(got))
	}

	resp := g.do(t, "DELETE", "/d", "", true, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE 目录期望 204,实际 %d", resp.StatusCode)
	}
	if left := keysWithPrefixS3(g, ".sail/parts/d/"); len(left) != 0 {
		t.Fatalf("目录删除后子树内的片应全部回收,残留 %v", left)
	}
}

// 两路径传同一内容 → 覆盖其一 → 另一路径逐字节可读、PROPFIND 大小不变。
// 片目录名曾取自内容哈希,覆盖写会把另一路径共享同哈希的片一起删掉。
func TestChunkedSameContentPathsIsolatedThroughGateway(t *testing.T) {
	g := newChunkedGateway(t)
	data := gwSeq(gwThreeChunk)
	for _, p := range []string{"/colA.bin", "/colB.bin"} {
		if resp := g.do(t, "PUT", p, string(data), true, map[string]string{"Content-Type": "application/octet-stream"}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT %s 期望 201,实际 %d", p, resp.StatusCode)
		}
	}

	if resp := g.do(t, "PUT", "/colA.bin", string(gwSeq(2*gwChunkSize)), true, map[string]string{"Content-Type": "application/octet-stream"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("覆盖 PUT 期望 201,实际 %d", resp.StatusCode)
	}

	resp := g.do(t, "GET", "/colB.bin", "", true, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET colB 期望 200,实际 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取 colB 失败: %v", err)
	}
	if !bytes.Equal(body, data) {
		t.Fatalf("覆盖 colA 后 colB 内容被破坏(读回 %d 字节,期望 %d)", len(body), len(data))
	}

	pf := g.do(t, "PROPFIND", "/colB.bin", propfindAll, true, map[string]string{"Depth": "0"})
	ms := parseMulti(t, pf)
	if len(ms.Responses) == 0 {
		t.Fatal("PROPFIND 无响应条目")
	}
	var size string
	for _, ps := range ms.Responses[0].Propstat {
		if ps.Prop.ContentLength != "" {
			size = ps.Prop.ContentLength
		}
	}
	if size != fmt.Sprintf("%d", gwThreeChunk) {
		t.Fatalf("colB 的 getcontentlength 应为 %d,实际 %q", gwThreeChunk, size)
	}
}

func keysWithPrefixS3(g *gateway, prefix string) []string {
	var out []string
	for _, k := range g.s3.Keys(bucket) {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}
