package webdavfs_test

import (
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// 本文件覆盖「真实挂载」视角的端到端协议流:OPTIONS 能力探测 → PROPFIND 列目录 →
// GET/Range 读取,以及 Depth 语义、HEAD、LOCK/UNLOCK 等前面未覆盖的协议角落。
// 复用 webdavfs_test.go 的 newGateway/do/bodyOf/propfindAll/parseMulti 辅助。

// mountProbe 断言 OPTIONS 不带认证时放行并返回能力头 —— 这是 macOS Finder /
// Windows WebClient 挂载的第一步,曾因被 Basic 认证拦成 401 而卡在「连接中」。
func TestMountOptionsAllowedWithoutAuth(t *testing.T) {
	g := newGateway(t, "", 0)

	resp := g.do(t, "OPTIONS", "/", "", false, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("OPTIONS 不带认证应 200,实际 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	if dav := resp.Header.Get("DAV"); dav != "1, 2" {
		t.Fatalf("DAV 头应为 \"1, 2\",实际 %q", dav)
	}
	if allow := resp.Header.Get("Allow"); !strings.Contains(allow, "PROPFIND") {
		t.Fatalf("Allow 头应含 PROPFIND,实际 %q", allow)
	}
}

// 挂载后 Finder 看到的完整序列:OPTIONS(探测)→ PROPFIND Depth:1(列根)。
// 根目录、平铺文件、子目录三种条目都必须出现在 multistatus 里。
func TestMountSequenceOptionsThenPropfind(t *testing.T) {
	g := newGateway(t, "", 0)
	g.s3.Put(bucket, "a.txt", []byte("A"), "")
	g.s3.Put(bucket, "b.txt", []byte("B"), "")
	g.s3.Put(bucket, "dir/c.txt", []byte("C"), "")

	// 先探测
	opts := g.do(t, "OPTIONS", "/", "", false, nil)
	if opts.StatusCode != http.StatusOK {
		t.Fatalf("OPTIONS 期望 200,实际 %d", opts.StatusCode)
	}

	// 再列根
	resp := g.do(t, "PROPFIND", "/", propfindAll, true, map[string]string{"Depth": "1"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND 期望 207,实际 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	ms := parseMulti(t, resp)
	hrefs := map[string]bool{}
	for _, r := range ms.Responses {
		hrefs[r.Href] = true
	}
	for _, want := range []string{"/", "/a.txt", "/b.txt", "/dir/"} {
		if !hrefs[want] {
			t.Fatalf("根列表缺少 %q,实际 %v", want, hrefs)
		}
	}
}

// Depth: infinity 必须递归展开整棵树(嵌套目录 + 深层文件都可见)。
func TestPropfindDepthInfinityListsTree(t *testing.T) {
	g := newGateway(t, "", 0)
	g.s3.Put(bucket, "root.txt", []byte("r"), "")
	g.s3.Put(bucket, "d1/a.txt", []byte("a"), "")
	g.s3.Put(bucket, "d1/d2/b.txt", []byte("b"), "")

	resp := g.do(t, "PROPFIND", "/", propfindAll, true, map[string]string{"Depth": "infinity"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("期望 207,实际 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	ms := parseMulti(t, resp)
	hrefs := map[string]bool{}
	for _, r := range ms.Responses {
		hrefs[r.Href] = true
	}
	for _, want := range []string{"/", "/root.txt", "/d1/", "/d1/a.txt", "/d1/d2/", "/d1/d2/b.txt"} {
		if !hrefs[want] {
			t.Fatalf("深度遍历缺少 %q,实际 %v", want, hrefs)
		}
	}
}

// HEAD 只回元信息(Content-Length + ETag),不带响应体。
func TestHeadReturnsMetadataNoBody(t *testing.T) {
	g := newGateway(t, "", 0)
	g.s3.Put(bucket, "doc.txt", []byte("hello world"), "text/plain")

	resp := g.do(t, "HEAD", "/doc.txt", "", true, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD 期望 200,实际 %d", resp.StatusCode)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "11" {
		t.Fatalf("Content-Length 应为 11,实际 %q", cl)
	}
	if et := resp.Header.Get("ETag"); et == "" {
		t.Fatal("HEAD 应带 ETag")
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 0 {
		t.Fatalf("HEAD 不应有响应体,实际 %d 字节", len(body))
	}
}

// PROPFIND 一个不存在的路径必须返回 404,而不是空 207。
func TestPropfindNonexistentReturns404(t *testing.T) {
	g := newGateway(t, "", 0)
	resp := g.do(t, "PROPFIND", "/nope", propfindAll, true, map[string]string{"Depth": "0"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("PROPFIND 不存在路径期望 404,实际 %d", resp.StatusCode)
	}
}

// GET 目录应返回 405(目录不可下载)。
func TestGetDirectoryReturns405(t *testing.T) {
	g := newGateway(t, "", 0)
	g.s3.Put(bucket, "dir/x.txt", []byte("x"), "")
	resp := g.do(t, "GET", "/dir", "", true, nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET 目录期望 405,实际 %d", resp.StatusCode)
	}
}

// LOCK → 拿 token → UNLOCK 释放。锁是进程内实现,只验证协议往返可用。
func TestLockUnlockRoundTrip(t *testing.T) {
	g := newGateway(t, "", 0)
	g.s3.Put(bucket, "f.txt", []byte("data"), "")

	lockBody := `<?xml version="1.0" encoding="utf-8"?>` +
		`<D:lockinfo xmlns:D="DAV:">` +
		`<D:lockscope><D:exclusive/></D:lockscope>` +
		`<D:locktype><D:write/></D:locktype>` +
		`<D:owner><D:href>alice</D:href></D:owner>` +
		`</D:lockinfo>`

	lock := g.do(t, "LOCK", "/f.txt", lockBody, true, map[string]string{"Depth": "0"})
	if lock.StatusCode != http.StatusOK {
		t.Fatalf("LOCK 期望 200,实际 %d: %s", lock.StatusCode, bodyOf(t, lock))
	}
	token := lock.Header.Get("Lock-Token")
	if token == "" || !strings.HasPrefix(token, "<") || !strings.HasSuffix(token, ">") {
		t.Fatalf("LOCK 应回 Lock-Token 头(形如 <token>),实际 %q", token)
	}

	unlock := g.do(t, "UNLOCK", "/f.txt", "", true, map[string]string{"Lock-Token": token})
	if unlock.StatusCode != http.StatusNoContent {
		t.Fatalf("UNLOCK 期望 204,实际 %d: %s", unlock.StatusCode, bodyOf(t, unlock))
	}
}

// 读取路径的 Range 语义:整文件与区间都逐字节一致,且对象列表稳定(排序列目录用)。
func TestGetWholeAndRangeByteExact(t *testing.T) {
	g := newGateway(t, "", 0)
	data := make([]byte, 2048)
	for i := range data {
		data[i] = byte(i % 251)
	}
	g.s3.Put(bucket, "blob.bin", data, "application/octet-stream")

	whole := g.do(t, "GET", "/blob.bin", "", true, nil)
	if whole.StatusCode != http.StatusOK {
		t.Fatalf("GET 期望 200,实际 %d", whole.StatusCode)
	}
	if b := bodyOf(t, whole); b != string(data) {
		t.Fatal("整文件下载内容不一致")
	}

	part := g.do(t, "GET", "/blob.bin", "", true, map[string]string{"Range": "bytes=1000-1499"})
	if part.StatusCode != http.StatusPartialContent {
		t.Fatalf("Range 期望 206,实际 %d", part.StatusCode)
	}
	if b := bodyOf(t, part); b != string(data[1000:1500]) {
		t.Fatal("Range 区间内容不一致")
	}
	if cr := part.Header.Get("Content-Range"); cr != "bytes 1000-1499/2048" {
		t.Fatalf("Content-Range 不符: %q", cr)
	}
}

// 列目录结果按名字排序,供 Finder 稳定呈现。
func TestPropfindListingsSortedByName(t *testing.T) {
	g := newGateway(t, "", 0)
	for _, k := range []string{"c.txt", "a.txt", "b.txt"} {
		g.s3.Put(bucket, k, []byte(k), "")
	}

	resp := g.do(t, "PROPFIND", "/", propfindAll, true, map[string]string{"Depth": "1"})
	ms := parseMulti(t, resp)
	var names []string
	for _, r := range ms.Responses {
		if r.Href == "/" {
			continue
		}
		names = append(names, strings.TrimSuffix(strings.TrimPrefix(r.Href, "/"), "/"))
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("列目录未按名字排序: %v", names)
	}
}
