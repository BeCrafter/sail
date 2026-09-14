package webdavfs

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BeCrafter/sail/internal/vfs"
)

// 写路径上报的上传错误必须换成 413/507 + 可操作指引,而不是 webdav 默认的 405。
func TestGuardWriterMapsUploadFailures(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		want     int
		wantBody string
	}{
		{
			name:     "超大",
			err:      fmt.Errorf("s3fs: /big.bin: %w", vfs.ErrTooLarge),
			want:     http.StatusRequestEntityTooLarge,
			wantBody: "--backend-max-object-size",
		},
		{
			name:     "暂存盘不足",
			err:      fmt.Errorf("s3fs: 暂存目录空间不足: %w", vfs.ErrInsufficientStorage),
			want:     http.StatusInsufficientStorage,
			wantBody: "--staging-dir",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := &requestState{cache: newReadCache()}
			st.setFatal(c.err)
			rec := httptest.NewRecorder()
			gw := &guardWriter{ResponseWriter: rec, state: st}

			// 模拟 webdav.handlePut 在 io.Copy 出错后写 405。
			gw.WriteHeader(http.StatusMethodNotAllowed)
			if rec.Code != c.want {
				t.Fatalf("期望 %d,实际 %d", c.want, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), c.wantBody) {
				t.Fatalf("指引文案缺少 %q: %s", c.wantBody, rec.Body.String())
			}
			// handler 后续写的状态文本必须被丢弃。
			if _, err := gw.Write([]byte("Method Not Allowed")); err != nil {
				t.Fatalf("Write 不应报错: %v", err)
			}
			if strings.Contains(rec.Body.String(), "Method Not Allowed") {
				t.Fatal("被接管后仍写入了 webdav 的状态文本")
			}
		})
	}
}

// 与上传无关的写错误保持 webdav 的默认状态码,不做改写。
func TestGuardWriterLeavesOtherErrorsAlone(t *testing.T) {
	st := &requestState{cache: newReadCache()}
	st.setFatal(errors.New("某种内部错误"))
	rec := httptest.NewRecorder()
	gw := &guardWriter{ResponseWriter: rec, state: st}
	gw.WriteHeader(http.StatusMethodNotAllowed)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("期望 405,实际 %d", rec.Code)
	}
}

// GET/HEAD 的 Content-Type 用对象里存的值覆盖 ServeContent 的扩展名推断;
// 其余方法(PROPFIND 的 XML)不得被覆盖。
func TestGuardWriterOverridesContentTypeOnlyForGetHead(t *testing.T) {
	cases := []struct {
		method string
		wantCT string
	}{
		{http.MethodGet, "application/x-store"},
		{http.MethodHead, "application/x-store"},
		{"PROPFIND", "application/xml"},
	}
	for _, c := range cases {
		t.Run(c.method, func(t *testing.T) {
			st := &requestState{cache: newReadCache(), method: c.method, contentType: "application/x-store"}
			rec := httptest.NewRecorder()
			gw := &guardWriter{ResponseWriter: rec, state: st}
			rec.Header().Set("Content-Type", "application/xml") // handler 自己定的类型
			gw.WriteHeader(http.StatusOK)
			if got := rec.Header().Get("Content-Type"); got != c.wantCT {
				t.Fatalf("%s 的 Content-Type 期望 %q,实际 %q", c.method, c.wantCT, got)
			}
		})
	}
}

func TestNewServerRejectsAnonymousSharing(t *testing.T) {
	if _, err := NewServer(Config{FileSystem: New(nil)}); err == nil {
		t.Fatal("缺少凭据时必须拒绝启动")
	}
}

func TestDavPathRejectsTraversal(t *testing.T) {
	for _, p := range []string{"/../x", "/a/../../x"} {
		if _, err := davPath(p); err == nil {
			t.Fatalf("%s 应被拒绝", p)
		}
	}
	if got, err := davPath("/a/b"); err != nil || got != "/a/b" {
		t.Fatalf("davPath(/a/b) = %q, %v", got, err)
	}
	if got, err := davPath("/a/"); err != nil || got != "/a/" {
		t.Fatalf("davPath(/a/) = %q, %v", got, err)
	}
}

func TestReadCacheInvalidate(t *testing.T) {
	c := newReadCache()
	c.putAll([]vfs.FileInfo{
		{Path: "/a", Name: "a"},
		{Path: "/a/b", Name: "b"},
		{Path: "/c", Name: "c"},
	})
	c.invalidate("/a")
	if _, ok := c.get("/a"); ok {
		t.Fatal("/a 应被失效")
	}
	if _, ok := c.get("/a/b"); ok {
		t.Fatal("/a/b 应被失效")
	}
	if _, ok := c.get("/c"); !ok {
		t.Fatal("/c 不应被失效")
	}
}
