package webdavfs_test

import (
	"bytes"
	"context"
	"encoding/xml"
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

const bucket = "b"

type gateway struct {
	ts  *httptest.Server
	s3  *fakes3.Server
	cli *http.Client
}

func newGateway(t *testing.T, prefix string, maxUploadSize int64) *gateway {
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
		Prefix:        prefix,
		StagingDir:    t.TempDir(),
		MaxUploadSize: maxUploadSize,
	})
	if err != nil {
		t.Fatalf("构造 s3fs 失败: %v", err)
	}
	gw, err := webdavfs.NewServer(webdavfs.Config{
		FileSystem:        webdavfs.New(core),
		User:              "alice",
		Password:          "secret",
		MaxUploadSize:     maxUploadSize,
		MaxUploadSizeText: "1.0 KiB",
	})
	if err != nil {
		t.Fatalf("构造 WebDAV 网关失败: %v", err)
	}
	ts := httptest.NewServer(gw)
	t.Cleanup(ts.Close)
	return &gateway{ts: ts, s3: s3srv, cli: ts.Client()}
}

// do 发起一次带 Basic 认证的请求(auth 为 false 时不带凭据)。
func (g *gateway) do(t *testing.T, method, path, body string, auth bool, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, g.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if auth {
		req.SetBasicAuth("alice", "secret")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := g.cli.Do(req)
	if err != nil {
		t.Fatalf("%s %s 失败: %v", method, path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func bodyOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应体失败: %v", err)
	}
	return string(b)
}

const propfindAll = `<?xml version="1.0" encoding="utf-8"?><D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`

type multistatus struct {
	Responses []struct {
		Href     string `xml:"href"`
		Propstat []struct {
			Prop struct {
				ETag          string `xml:"getetag"`
				ContentLength string `xml:"getcontentlength"`
				ContentType   string `xml:"getcontenttype"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"response"`
}

func (m multistatus) etags() []string {
	var out []string
	for _, r := range m.Responses {
		for _, ps := range r.Propstat {
			out = append(out, ps.Prop.ETag)
		}
	}
	return out
}

func parseMulti(t *testing.T, resp *http.Response) multistatus {
	t.Helper()
	var ms multistatus
	if err := xml.Unmarshal([]byte(bodyOf(t, resp)), &ms); err != nil {
		t.Fatalf("解析 multistatus 失败: %v", err)
	}
	return ms
}

func TestUnauthenticatedRequestRejected(t *testing.T) {
	g := newGateway(t, "", 0)
	resp := g.do(t, "PROPFIND", "/", propfindAll, false, map[string]string{"Depth": "1"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("期望 401,实际 %d", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatal("401 响应必须带 WWW-Authenticate")
	}
}

// 列目录必须走单次分页列举,不得退化成每项 HEAD,更不能发 GetObject。
func TestListDirectoryDoesNotGetObjects(t *testing.T) {
	g := newGateway(t, "", 0)
	for i := range 1000 {
		g.s3.Put(bucket, fmt.Sprintf("data/%04d.bin", i), []byte("payload"), "")
	}

	resp := g.do(t, "PROPFIND", "/data", propfindAll, true, map[string]string{"Depth": "1"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("期望 207,实际 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	ms := parseMulti(t, resp)
	if len(ms.Responses) != 1001 {
		t.Fatalf("期望 1001 条(目录 + 1000 个对象),实际 %d", len(ms.Responses))
	}
	if n := g.s3.Counts.Get.Load(); n != 0 {
		t.Fatalf("列目录不应发 GetObject,实际 %d", n)
	}
	if n := g.s3.Counts.Head.Load(); n > 3 {
		t.Fatalf("列目录不应退化成逐项 HEAD,实际 %d 次", n)
	}
	if n := g.s3.Counts.List.Load(); n < 1 || n > 3 {
		t.Fatalf("期望 1..3 次分页 ListObjectsV2,实际 %d", n)
	}
}

func TestPutPropfindGetShareETag(t *testing.T) {
	g := newGateway(t, "", 0)

	put := g.do(t, "PUT", "/big.bin", "hello world", true, nil)
	if put.StatusCode != http.StatusCreated {
		t.Fatalf("PUT 期望 201,实际 %d: %s", put.StatusCode, bodyOf(t, put))
	}
	putETag := put.Header.Get("ETag")
	if putETag == "" {
		t.Fatal("PUT 响应必须带 ETag")
	}

	get := g.do(t, "GET", "/big.bin", "", true, nil)
	if got := get.Header.Get("ETag"); got != putETag {
		t.Fatalf("GET ETag %q 与 PUT %q 不一致", got, putETag)
	}

	pf := g.do(t, "PROPFIND", "/big.bin", propfindAll, true, map[string]string{"Depth": "0"})
	etags := parseMulti(t, pf).etags()
	if len(etags) != 1 || etags[0] != putETag {
		t.Fatalf("PROPFIND ETag %v 与 PUT %q 不一致", etags, putETag)
	}
}

func TestDownloadSupportsRange(t *testing.T) {
	g := newGateway(t, "", 0)
	payload := make([]byte, 200000)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	g.s3.Put(bucket, "blob.bin", payload, "")

	resp := g.do(t, "GET", "/blob.bin", "", true, map[string]string{"Range": "bytes=50000-50009"})
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("期望 206,实际 %d", resp.StatusCode)
	}
	if cr := resp.Header.Get("Content-Range"); cr != "bytes 50000-50009/200000" {
		t.Fatalf("Content-Range 不符: %q", cr)
	}
	if got := bodyOf(t, resp); got != string(payload[50000:50010]) {
		t.Fatalf("区间内容与源文件不一致")
	}
}

func TestUploadOverLimitFailsFastWithGuidance(t *testing.T) {
	g := newGateway(t, "", 1024)

	resp := g.do(t, "PUT", "/too-big.bin", strings.Repeat("x", 4096), true, nil)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("期望 413,实际 %d", resp.StatusCode)
	}
	body := bodyOf(t, resp)
	if !strings.Contains(body, "sail cp") || !strings.Contains(body, "--backend-max-object-size") {
		t.Fatalf("413 响应体应给出可操作指引,实际: %s", body)
	}
	if _, ok := g.s3.Get(bucket, "too-big.bin"); ok {
		t.Fatal("超限上传不应在桶内留下残留对象")
	}
	if g.s3.Counts.Put.Load() != 0 {
		t.Fatalf("超限上传不应发出 PutObject,实际 %d", g.s3.Counts.Put.Load())
	}
}

func TestMkcolDirectorySurvivesRefresh(t *testing.T) {
	g := newGateway(t, "", 0)

	mk := g.do(t, "MKCOL", "/newdir", "", true, nil)
	if mk.StatusCode != http.StatusCreated {
		t.Fatalf("MKCOL 期望 201,实际 %d: %s", mk.StatusCode, bodyOf(t, mk))
	}
	resp := g.do(t, "PROPFIND", "/", propfindAll, true, map[string]string{"Depth": "1"})
	ms := parseMulti(t, resp)
	hrefs := make([]string, 0, len(ms.Responses))
	for _, r := range ms.Responses {
		hrefs = append(hrefs, r.Href)
	}
	if len(ms.Responses) != 2 {
		t.Fatalf("期望根 + newdir 两条,实际 %v", hrefs)
	}
	if !strings.Contains(strings.Join(hrefs, " "), "newdir") {
		t.Fatalf("新建目录应出现在列表中,实际 %v", hrefs)
	}
}

func TestDirectoryMoveReturns501(t *testing.T) {
	g := newGateway(t, "", 0)
	if mk := g.do(t, "MKCOL", "/d1", "", true, nil); mk.StatusCode != http.StatusCreated {
		t.Fatalf("MKCOL 失败: %d", mk.StatusCode)
	}
	resp := g.do(t, "MOVE", "/d1", "", true, map[string]string{"Destination": g.ts.URL + "/d2"})
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("目录 MOVE 期望 501,实际 %d", resp.StatusCode)
	}
	resp = g.do(t, "COPY", "/d1", "", true, map[string]string{"Destination": g.ts.URL + "/d2"})
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("目录 COPY 期望 501,实际 %d", resp.StatusCode)
	}
}

// 目标已存在且是目录时,webdav 会先 RemoveAll(目标) 再 Rename:必须提前拦住,
// 否则一个文件 MOVE 就能把整个目录递归删掉。
func TestMoveOntoExistingDirectoryDoesNotWipeIt(t *testing.T) {
	g := newGateway(t, "", 0)
	if mk := g.do(t, "MKCOL", "/docs", "", true, nil); mk.StatusCode != http.StatusCreated {
		t.Fatalf("MKCOL 失败: %d", mk.StatusCode)
	}
	g.do(t, "PUT", "/docs/a.txt", "A", true, nil)
	g.do(t, "PUT", "/docs/b.txt", "B", true, nil)
	g.do(t, "PUT", "/x.bin", "X", true, nil)

	resp := g.do(t, "MOVE", "/x.bin", "", true, map[string]string{
		"Destination": g.ts.URL + "/docs",
		"Overwrite":   "T",
	})
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusCreated {
		t.Fatalf("把文件 MOVE 到已存在目录上不应成功,实际 %d", resp.StatusCode)
	}
	if _, ok := g.s3.Get(bucket, "docs/a.txt"); !ok {
		t.Fatal("目标目录内容被删除了")
	}
	if _, ok := g.s3.Get(bucket, "docs/b.txt"); !ok {
		t.Fatal("目标目录内容被删除了")
	}
	if _, ok := g.s3.Get(bucket, "x.bin"); !ok {
		t.Fatal("源对象不应被删除")
	}
}

// MKCOL 到已存在的资源上必须失败,否则同名文件旁会多出一个 "x/" 标记对象。
func TestMkcolOnExistingResourceFails(t *testing.T) {
	g := newGateway(t, "", 0)
	g.do(t, "PUT", "/a.txt", "A", true, nil)

	resp := g.do(t, "MKCOL", "/a.txt", "", true, nil)
	if resp.StatusCode == http.StatusCreated {
		t.Fatalf("MKCOL 到同名文件上应失败,实际 %d", resp.StatusCode)
	}
	if _, ok := g.s3.Get(bucket, "a.txt/"); ok {
		t.Fatal("不应产生 a.txt/ 标记对象")
	}

	if mk := g.do(t, "MKCOL", "/d", "", true, nil); mk.StatusCode != http.StatusCreated {
		t.Fatalf("首次 MKCOL 应成功: %d", mk.StatusCode)
	}
	if again := g.do(t, "MKCOL", "/d", "", true, nil); again.StatusCode == http.StatusCreated {
		t.Fatalf("重复 MKCOL 应失败,实际 %d", again.StatusCode)
	}
}

func TestFileMoveRenamesObject(t *testing.T) {
	g := newGateway(t, "", 0)
	if put := g.do(t, "PUT", "/x.bin", "data", true, nil); put.StatusCode != http.StatusCreated {
		t.Fatalf("PUT 失败: %d", put.StatusCode)
	}
	resp := g.do(t, "MOVE", "/x.bin", "", true, map[string]string{"Destination": g.ts.URL + "/y.bin"})
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("文件 MOVE 期望 201/204,实际 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	if _, ok := g.s3.Get(bucket, "x.bin"); ok {
		t.Fatal("源对象应已删除")
	}
	if obj, ok := g.s3.Get(bucket, "y.bin"); !ok || string(obj.Data) != "data" {
		t.Fatalf("目标对象不符: %+v", obj)
	}
}

func TestDeleteFileAndDirectory(t *testing.T) {
	g := newGateway(t, "", 0)
	g.do(t, "PUT", "/dir/a.txt", "a", true, nil)
	g.do(t, "PUT", "/dir/b.txt", "b", true, nil)

	resp := g.do(t, "DELETE", "/dir/a.txt", "", true, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE 文件期望 204,实际 %d", resp.StatusCode)
	}
	resp = g.do(t, "DELETE", "/dir", "", true, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE 目录期望 204,实际 %d", resp.StatusCode)
	}
	if keys := g.s3.Keys(bucket); len(keys) != 0 {
		t.Fatalf("删除后应无残留,实际 %v", keys)
	}
}

func TestPathTraversalRejectedWithinPrefix(t *testing.T) {
	g := newGateway(t, "tenant-a", 0)
	g.s3.Put(bucket, "tenant-b/secret.txt", []byte("secret"), "")
	g.s3.Put(bucket, "tenant-a/own.txt", []byte("own"), "")

	for _, p := range []string{"/../tenant-b/secret.txt", "/%2e%2e/tenant-b/secret.txt", "/a/../../tenant-b/secret.txt"} {
		resp := g.do(t, "GET", p, "", true, nil)
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("%s 应被拒绝,却返回 200", p)
		}
	}
	if obj, ok := g.s3.Get(bucket, "tenant-b/secret.txt"); !ok || string(obj.Data) != "secret" {
		t.Fatal("跨前缀对象不应被触碰")
	}

	resp := g.do(t, "GET", "/own.txt", "", true, nil)
	if resp.StatusCode != http.StatusOK || bodyOf(t, resp) != "own" {
		t.Fatalf("本前缀内访问应正常,实际 %d", resp.StatusCode)
	}
}

func TestContentTypePassthrough(t *testing.T) {
	g := newGateway(t, "", 0)
	put := g.do(t, "PUT", "/doc.data", "body", true, map[string]string{"Content-Type": "application/x-sail-test"})
	if put.StatusCode != http.StatusCreated {
		t.Fatalf("PUT 期望 201,实际 %d", put.StatusCode)
	}
	get := g.do(t, "GET", "/doc.data", "", true, nil)
	if ct := get.Header.Get("Content-Type"); ct != "application/x-sail-test" {
		t.Fatalf("GET 应原样回库存的 Content-Type,实际 %q", ct)
	}
	// PROPFIND 的响应体是 WebDAV XML,不能被被请求对象的类型覆盖。
	pf := g.do(t, "PROPFIND", "/", propfindAll, true, map[string]string{"Depth": "1"})
	if ct := pf.Header.Get("Content-Type"); !strings.Contains(ct, "xml") {
		t.Fatalf("PROPFIND 响应应为 XML,实际 %q", ct)
	}
}

func TestMissingObjectReturns404(t *testing.T) {
	g := newGateway(t, "", 0)
	resp := g.do(t, "GET", "/nope.bin", "", true, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("期望 404,实际 %d", resp.StatusCode)
	}
}

// .sail/ 是内核保留前缀,不进列表。
func TestReservedPrefixHiddenFromListing(t *testing.T) {
	g := newGateway(t, "", 0)
	g.s3.Put(bucket, ".sail/manifest.json", []byte("{}"), "")
	g.s3.Put(bucket, "visible.txt", []byte("v"), "")

	resp := g.do(t, "PROPFIND", "/", propfindAll, true, map[string]string{"Depth": "1"})
	ms := parseMulti(t, resp)
	if len(ms.Responses) != 2 {
		hrefs := make([]string, 0, len(ms.Responses))
		for _, r := range ms.Responses {
			hrefs = append(hrefs, r.Href)
		}
		t.Fatalf("期望根 + visible.txt 两条,实际 %v", hrefs)
	}
}

// 客户端用分块传输(Content-Length 未知)且超限时,仍要在读满请求体前拿到 413,
// 而不是 webdav 的 405。
func TestChunkedUploadOverLimitStillReturns413(t *testing.T) {
	g := newGateway(t, "", 1024)

	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write(bytes.Repeat([]byte("x"), 65536))
		_ = pw.Close()
	}()
	req, err := http.NewRequest("PUT", g.ts.URL+"/chunked.bin", pr)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.SetBasicAuth("alice", "secret")
	req.ContentLength = -1 // 强制分块传输
	resp, err := g.cli.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("期望 413,实际 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	if _, ok := g.s3.Get(bucket, "chunked.bin"); ok {
		t.Fatal("超限上传不应留下残留对象")
	}
}
