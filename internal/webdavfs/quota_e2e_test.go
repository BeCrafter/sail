package webdavfs_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/fakes3"
	"github.com/BeCrafter/sail/internal/vfs/quotafs"
	"github.com/BeCrafter/sail/internal/vfs/s3fs"
	"github.com/BeCrafter/sail/internal/webdavfs"
)

// quotaGateway 是带配额的单用户网关测试外壳:quotafs 装饰器位于
// s3fs 内核之上,复刻 cmd 的栈构建方式。
type quotaGateway struct {
	ts  *httptest.Server
	s3  *fakes3.Server
	qfs *quotafs.FS
}

func newQuotaGateway(t *testing.T, quota int64) *quotaGateway {
	t.Helper()
	s3srv := fakes3.New()
	t.Cleanup(s3srv.Close)
	s3c, err := client.New(context.Background(), &config.Resolved{
		Endpoint: s3srv.URL(), AccessKey: "ak", SecretKey: "sk",
		Region: "us-east-1", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("构造 S3 client 失败: %v", err)
	}
	core, err := s3fs.New(s3fs.Config{Client: s3c, Bucket: bucket, Prefix: "alice", StagingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("构造 s3fs 失败: %v", err)
	}
	qfs := quotafs.New(core, core, quota, 0, nil)
	gw, err := webdavfs.NewServer(webdavfs.Config{
		Users: []webdavfs.UserEntry{{
			Name:       "alice",
			Password:   "pa",
			FileSystem: webdavfs.NewWithListingCache(qfs, time.Minute),
		}},
	})
	if err != nil {
		t.Fatalf("构造网关失败: %v", err)
	}
	ts := httptest.NewServer(gw)
	t.Cleanup(ts.Close)
	return &quotaGateway{ts: ts, s3: s3srv, qfs: qfs}
}

func (g *quotaGateway) do(t *testing.T, method, path, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, g.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.SetBasicAuth("alice", "pa")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := g.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s 失败: %v", method, path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func putHdr() map[string]string {
	return map[string]string{"Content-Type": "application/octet-stream"}
}

// 场景(P2-3 配额准入拦截):带 Content-Length 的超限 PUT 在读请求体之前
// 返回 507 + 指引,桶内无对象残留。
func TestQuotaPutOverLimitRejectedWith507(t *testing.T) {
	g := newQuotaGateway(t, 10)
	resp := g.do(t, "PUT", "/big.bin", strings.Repeat("x", 100), putHdr())
	if resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("期望 507,实际 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	if _, ok := g.s3.Get(bucket, "alice/big.bin"); ok {
		t.Error("被拒上传不得在桶内留残留")
	}
	if !strings.Contains(bodyOf(t, resp), "quota") {
		t.Errorf("507 应带可操作指引,实际: %s", bodyOf(t, resp))
	}
}

// 场景(P2-4 提交点复核):COPY 无 Content-Length,超限在提交点拦截,
// 目标不出现在桶中。
func TestQuotaCopyOverLimitRejectedAtCommit(t *testing.T) {
	g := newQuotaGateway(t, 60)
	g.s3.Put(bucket, "alice/src.bin", make([]byte, 50), "")
	// 源 50 字节在配额口径内已占 50;COPY 追加 50 → 提交点 100 > 60。
	resp := g.do(t, "COPY", "/src.bin", "", map[string]string{"Destination": "/dst.bin"})
	if resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("COPY 提交点期望 507,实际 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	if _, ok := g.s3.Get(bucket, "alice/dst.bin"); ok {
		t.Error("被拒 COPY 的目标不得落桶")
	}
}

// 场景(P2-5 覆盖写扣旧值):覆盖既有对象按「旧值释放」计算,不误拦;
// 超出新口径仍拦截。
func TestQuotaOverwriteKeepsOldValueReleased(t *testing.T) {
	g := newQuotaGateway(t, 100)
	// 50 字节落桶(50 ≤ 100)。
	if resp := g.do(t, "PUT", "/old.bin", strings.Repeat("a", 50), putHdr()); resp.StatusCode != http.StatusCreated {
		t.Fatalf("首次 PUT 期望 201,实际 %d", resp.StatusCode)
	}
	// 覆盖:50(旧)−50(释放)+60(新)= 60 ≤ 100,放行。
	if resp := g.do(t, "PUT", "/old.bin", strings.Repeat("b", 60), putHdr()); resp.StatusCode != http.StatusCreated {
		t.Fatalf("覆盖写应放行,实际 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	// 新口径 60:再写 50 会超(60+50 > 100),507。
	if resp := g.do(t, "PUT", "/new.bin", strings.Repeat("c", 50), putHdr()); resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("60+50>100 期望 507,实际 %d", resp.StatusCode)
	}
	// 40 放行(60+40 ≤ 100)。
	if resp := g.do(t, "PUT", "/new.bin", strings.Repeat("d", 40), putHdr()); resp.StatusCode != http.StatusCreated {
		t.Fatalf("60+40≤100 应放行,实际 %d", resp.StatusCode)
	}
}

// 配额热更新(P2):SetQuota 原子改参数后,同一网关上旧口径 507、新口径放行。
func TestQuotaHotUpdateOnLiveGateway(t *testing.T) {
	g := newQuotaGateway(t, 10)
	if resp := g.do(t, "PUT", "/a.txt", "12345678", putHdr()); resp.StatusCode != http.StatusCreated {
		t.Fatalf("8 字节应放行,实际 %d", resp.StatusCode)
	}
	if resp := g.do(t, "PUT", "/b.txt", "123456", putHdr()); resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("8+6>10 期望 507,实际 %d", resp.StatusCode)
	}
	g.qfs.SetQuota(20)
	if resp := g.do(t, "PUT", "/b.txt", "123456", putHdr()); resp.StatusCode != http.StatusCreated {
		t.Fatalf("热更新后 8+6≤20 应放行,实际 %d", resp.StatusCode)
	}
	// 既有内容未受热更新影响。
	if obj, ok := g.s3.Get(bucket, "alice/a.txt"); !ok || !bytes.Equal(obj.Data, []byte("12345678")) {
		t.Errorf("热更新不得影响既有对象,实际: %v %v", ok, obj.Data)
	}
}
