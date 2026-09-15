package webdavfs_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/fakes3"
	"github.com/BeCrafter/sail/internal/vfs/s3fs"
	"github.com/BeCrafter/sail/internal/webdavfs"
)

func newCachedGateway(t *testing.T, ttl time.Duration) *gateway {
	t.Helper()
	s3srv := fakes3.New()
	t.Cleanup(s3srv.Close)
	s3c, err := client.New(context.Background(), &config.Resolved{
		Endpoint: s3srv.URL(), AccessKey: "ak", SecretKey: "sk", Region: "us-east-1", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	core, err := s3fs.New(s3fs.Config{Client: s3c, Bucket: bucket, StagingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("s3fs: %v", err)
	}
	gw, err := webdavfs.NewServer(webdavfs.Config{
		FileSystem: webdavfs.NewWithListingCache(core, ttl),
		User:       "alice", Password: "secret",
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	ts := httptest.NewServer(gw)
	t.Cleanup(ts.Close)
	return &gateway{ts: ts, s3: s3srv, cli: ts.Client()}
}

// 第二次 PROPFIND 同一目录应命中缓存:不再发 ListObjectsV2,且内容必须完整
// (曾有一个 bug:缓存写入漏了深拷贝,命中时返回一批零值 FileInfo,
//
//	表现为挂载后目录全空 —— 只断言「不再 List」抓不住,必须断言 href)。
func TestListingCacheServesSecondPropfind(t *testing.T) {
	g := newCachedGateway(t, time.Minute)
	g.s3.Put(bucket, "a.txt", []byte("a"), "")
	g.s3.Put(bucket, "d/b.txt", []byte("b"), "")

	before := g.s3.Counts.List.Load()
	resp1 := g.do(t, "PROPFIND", "/", propfindAll, true, map[string]string{"Depth": "1"})
	after1 := g.s3.Counts.List.Load()
	if after1 == before {
		t.Fatal("首次列目录应发 ListObjectsV2")
	}
	assertRootHrefs(t, resp1)

	resp2 := g.do(t, "PROPFIND", "/", propfindAll, true, map[string]string{"Depth": "1"})
	if after2 := g.s3.Counts.List.Load(); after2 != after1 {
		t.Fatalf("第二次列根目录应命中缓存(不再 List),List 从 %d 增到 %d", after1, after2)
	}
	// 缓存命中这条路径同样必须给出正确的子项 href。
	assertRootHrefs(t, resp2)
}

// assertRootHrefs 断言根目录列表含预期子项且不是「全部为 /」的零值形态。
func assertRootHrefs(t *testing.T, resp *http.Response) {
	t.Helper()
	hrefs := map[string]bool{}
	for _, r := range parseMulti(t, resp).Responses {
		hrefs[r.Href] = true
	}
	for _, want := range []string{"/", "/a.txt", "/d/"} {
		if !hrefs[want] {
			t.Fatalf("根列表缺少 %q,实际 %v(若全部是 \"/\" 说明缓存内容为零值)", want, hrefs)
		}
	}
}

// 在同一目录下 PUT 后,列表缓存必须失效,否则新文件要等 TTL 才可见。
func TestListingCacheInvalidatedOnPut(t *testing.T) {
	g := newCachedGateway(t, time.Minute)
	g.s3.Put(bucket, "d/old.txt", []byte("x"), "")

	// 预热缓存
	g.do(t, "PROPFIND", "/d", propfindAll, true, map[string]string{"Depth": "1"})
	// 新建文件
	if r := g.do(t, "PUT", "/d/new.txt", "N", true, nil); r.StatusCode != http.StatusCreated {
		t.Fatalf("PUT 期望 201,实际 %d", r.StatusCode)
	}
	// 再列:缓存已失效,必须能看到新文件
	resp := g.do(t, "PROPFIND", "/d", propfindAll, true, map[string]string{"Depth": "1"})
	ms := parseMulti(t, resp)
	var seen bool
	for _, r := range ms.Responses {
		if r.Href == "/d/new.txt" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("PUT 后列表缓存未失效,新文件不可见")
	}
}

// DELETE 后列表缓存也要失效。
func TestListingCacheInvalidatedOnDelete(t *testing.T) {
	g := newCachedGateway(t, time.Minute)
	g.s3.Put(bucket, "d/keep.txt", []byte("k"), "")
	g.s3.Put(bucket, "d/gone.txt", []byte("g"), "")

	g.do(t, "PROPFIND", "/d", propfindAll, true, map[string]string{"Depth": "1"})
	if r := g.do(t, "DELETE", "/d/gone.txt", "", true, nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE 期望 204,实际 %d", r.StatusCode)
	}
	resp := g.do(t, "PROPFIND", "/d", propfindAll, true, map[string]string{"Depth": "1"})
	for _, r := range parseMulti(t, resp).Responses {
		if r.Href == "/d/gone.txt" {
			t.Fatal("DELETE 后列表缓存未失效,已删文件仍可见")
		}
	}
}
