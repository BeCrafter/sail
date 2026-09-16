package s3del_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/fakes3"
	"github.com/BeCrafter/sail/internal/s3del"
)

const testBucket = "b"

func newDeleterFor(t *testing.T) (*s3del.Deleter, *fakes3.Server) {
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
	return s3del.New(s3c, testBucket), srv
}

// noCooldown 把探测冷却期设为 0:失败的探测不进入冷却,下一次调用立即重探。
// 用于让「批量端点恢复后自愈」这条路径免于真实等待 5 分钟。
func noCooldown(t *testing.T) {
	t.Helper()
	orig := s3del.ProbeTTL
	s3del.ProbeTTL = 0
	t.Cleanup(func() { s3del.ProbeTTL = orig })
}

// 支持批量端点时:一次批量请求删掉整批,不产生逐个删除。
func TestDeleteKeysUsesBatchEndpoint(t *testing.T) {
	d, srv := newDeleterFor(t)
	ctx := context.Background()
	keys := make([]string, 5)
	for i := range keys {
		keys[i] = fmt.Sprintf("d/%d.txt", i)
		srv.Put(testBucket, keys[i], []byte("x"), "text/plain")
	}

	if err := d.DeleteKeys(ctx, keys); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if n := srv.Counts.DeleteBatch.Load(); n != 1 {
		t.Fatalf("应发 1 次批量删除,实际 %d", n)
	}
	if n := srv.Counts.Delete.Load(); n != 0 {
		t.Fatalf("批量可用时不应逐个删除,实际 %d 次", n)
	}
	if got := srv.Keys(testBucket); len(got) != 0 {
		t.Fatalf("对象未被删净: %v", got)
	}
}

// 批量端点不可用时回退到逐个删除,结果仍然正确(幂等可重试)。
func TestDeleteKeysFallsBackToOneByOne(t *testing.T) {
	d, srv := newDeleterFor(t)
	ctx := context.Background()
	srv.FailBatchDelete.Store(true)
	keys := []string{"a.txt", "b.txt", "c.txt"}
	for _, k := range keys {
		srv.Put(testBucket, k, []byte("x"), "text/plain")
	}

	if err := d.DeleteKeys(ctx, keys); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if n := srv.Counts.Delete.Load(); n != 3 {
		t.Fatalf("应回退为 3 次逐个删除,实际 %d", n)
	}
	if got := srv.Keys(testBucket); len(got) != 0 {
		t.Fatalf("对象未被删净: %v", got)
	}
}

// 探测结论被缓存:冷却期内的后续删除不再撞批量端点。
// 计数按「前后是否变化」断言,不写死绝对值以免与实现细节耦合。
func TestUnsupportedConclusionIsCached(t *testing.T) {
	d, srv := newDeleterFor(t)
	ctx := context.Background()
	srv.FailBatchDelete.Store(true)

	srv.Put(testBucket, "a.txt", []byte("x"), "text/plain")
	if err := d.DeleteKeys(ctx, []string{"a.txt"}); err != nil {
		t.Fatalf("第一次删除失败: %v", err)
	}
	probed := srv.Counts.DeleteBatch.Load()
	if probed == 0 {
		t.Fatal("第一次删除应探测批量端点,实际一次都没发")
	}

	srv.Put(testBucket, "b.txt", []byte("x"), "text/plain")
	if err := d.DeleteKeys(ctx, []string{"b.txt"}); err != nil {
		t.Fatalf("第二次删除失败: %v", err)
	}
	if n := srv.Counts.DeleteBatch.Load(); n != probed {
		t.Fatalf("冷却期内不应再撞批量端点:之前 %d 次,现在 %d 次", probed, n)
	}
}

// 冷却期过后重探批量端点:网关修复后无需重启即可自愈。
func TestReprobesAfterCooldown(t *testing.T) {
	noCooldown(t)
	d, srv := newDeleterFor(t)
	ctx := context.Background()
	srv.FailBatchDelete.Store(true)

	srv.Put(testBucket, "a.txt", []byte("x"), "text/plain")
	if err := d.DeleteKeys(ctx, []string{"a.txt"}); err != nil {
		t.Fatalf("第一次删除失败: %v", err)
	}
	afterFirst := srv.Counts.DeleteBatch.Load()
	if afterFirst == 0 {
		t.Fatal("第一次删除应探测批量端点,实际一次都没发")
	}

	// 网关被修复,冷却期为 0,下一次删除立即重探。
	srv.FailBatchDelete.Store(false)

	individualsBefore := srv.Counts.Delete.Load()
	srv.Put(testBucket, "b.txt", []byte("x"), "text/plain")
	if err := d.DeleteKeys(ctx, []string{"b.txt"}); err != nil {
		t.Fatalf("重探时的删除失败: %v", err)
	}
	if n := srv.Counts.DeleteBatch.Load(); n <= afterFirst {
		t.Fatalf("冷却期后应重探批量端点:之前 %d 次,现在 %d 次", afterFirst, n)
	}
	if got := srv.Counts.Delete.Load() - individualsBefore; got != 0 {
		t.Fatalf("重探命中批量后不应再逐个删除,实际 %d 次", got)
	}
}

// 回退必须并发:并发度 10 下 10 个对象的总耗时接近单个注入延迟,而非 10 倍。
func TestFallbackIsConcurrent(t *testing.T) {
	d, srv := newDeleterFor(t)
	ctx := context.Background()
	srv.FailBatchDelete.Store(true)
	srv.DeleteDelay.Store(int64(200 * time.Millisecond))

	// 预热:把首次「批量端点不可用」探测(含 SDK 重试)排除在计时窗口外。
	srv.Put(testBucket, "warm.txt", []byte("x"), "text/plain")
	if err := d.DeleteKeys(ctx, []string{"warm.txt"}); err != nil {
		t.Fatalf("预热删除失败: %v", err)
	}

	const n = 10
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("f%d.bin", i)
		srv.Put(testBucket, keys[i], []byte("x"), "text/plain")
	}

	start := time.Now()
	if err := d.DeleteKeys(ctx, keys); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	elapsed := time.Since(start)

	if serial := time.Duration(n) * 200 * time.Millisecond; elapsed >= serial {
		t.Fatalf("回退路径似乎是串行的:%d 个对象耗时 %v,串行下限 %v", n, elapsed, serial)
	}
}

// 跨 bucket 操作:DeleteKeysIn 的目标 bucket 必须显式生效。
func TestDeleteKeysInUsesGivenBucket(t *testing.T) {
	d, srv := newDeleterFor(t)
	ctx := context.Background()
	srv.Put("other", "a.txt", []byte("x"), "text/plain")

	if err := d.DeleteKeysIn(ctx, "other", []string{"a.txt"}); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if got := srv.Keys("other"); len(got) != 0 {
		t.Fatalf("对象未被删净: %v", got)
	}
}
