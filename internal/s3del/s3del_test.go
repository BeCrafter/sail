package s3del_test

import (
	"context"
	"fmt"
	"net/http"
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
	// 探测结论现在是进程级(按 bucket 共享),测试之间必须清干净。
	s3del.ResetProbes()
	t.Cleanup(s3del.ResetProbes)
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
	srv.BatchDeleteStatus.Store(http.StatusNotImplemented) // 405/501 类:一次即确证

	srv.Put(testBucket, "a.txt", []byte("x"), "text/plain")
	srv.Put(testBucket, "a2.txt", []byte("x"), "text/plain")
	if err := d.DeleteKeys(ctx, []string{"a.txt", "a2.txt"}); err != nil {
		t.Fatalf("第一次删除失败: %v", err)
	}
	probed := srv.Counts.DeleteBatch.Load()
	if probed == 0 {
		t.Fatal("第一次删除应探测批量端点,实际一次都没发")
	}

	srv.Put(testBucket, "b.txt", []byte("x"), "text/plain")
	srv.Put(testBucket, "b2.txt", []byte("x"), "text/plain")
	if err := d.DeleteKeys(ctx, []string{"b.txt", "b2.txt"}); err != nil {
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
	srv.Put(testBucket, "a2.txt", []byte("x"), "text/plain")
	if err := d.DeleteKeys(ctx, []string{"a.txt", "a2.txt"}); err != nil {
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
	srv.Put(testBucket, "b2.txt", []byte("x"), "text/plain")
	if err := d.DeleteKeys(ctx, []string{"b.txt", "b2.txt"}); err != nil {
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
	srv.BatchDeleteStatus.Store(http.StatusNotImplemented) // 一次即确证,预热后即进入冷却
	srv.DeleteDelay.Store(int64(200 * time.Millisecond))

	// 预热:把首次「批量端点不可用」探测(含 SDK 重试)排除在计时窗口外。
	srv.Put(testBucket, "warm.txt", []byte("x"), "text/plain")
	srv.Put(testBucket, "warm2.txt", []byte("x"), "text/plain")
	if err := d.DeleteKeys(ctx, []string{"warm.txt", "warm2.txt"}); err != nil {
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

// 只有**确证**「批量端点不支持」才缓存结论并冷却:501 一次即确证;500 需同
// 签名连续两次(网关常用 500 代答不支持,但单次 500 更可能是抖动);逐 key
// 失败与请求取消都不得确证。
func TestBatchFailureClassification(t *testing.T) {
	ctx := context.Background()

	t.Run("501 一次即确证", func(t *testing.T) {
		d, srv := newDeleterFor(t)
		srv.FailBatchDelete.Store(true)
		srv.BatchDeleteStatus.Store(http.StatusNotImplemented)

		srv.Put(testBucket, "a.txt", []byte("x"), "text/plain")
		srv.Put(testBucket, "a2.txt", []byte("x"), "text/plain")
		if err := d.DeleteKeys(ctx, []string{"a.txt", "a2.txt"}); err != nil {
			t.Fatalf("删除失败: %v", err)
		}
		probed := srv.Counts.DeleteBatch.Load()
		srv.Put(testBucket, "b.txt", []byte("x"), "text/plain")
		srv.Put(testBucket, "b2.txt", []byte("x"), "text/plain")
		if err := d.DeleteKeys(ctx, []string{"b.txt", "b2.txt"}); err != nil {
			t.Fatalf("删除失败: %v", err)
		}
		if n := srv.Counts.DeleteBatch.Load(); n != probed {
			t.Fatalf("501 已确证,冷却期内不应再撞批量端点:之前 %d,现在 %d", probed, n)
		}
	})

	t.Run("500 需连续两次同签名", func(t *testing.T) {
		d, srv := newDeleterFor(t)
		srv.FailBatchDelete.Store(true) // 默认 500

		// 第一次:单次 500 判为抖动 → 不缓存结论。
		srv.Put(testBucket, "a.txt", []byte("x"), "text/plain")
		srv.Put(testBucket, "a2.txt", []byte("x"), "text/plain")
		if err := d.DeleteKeys(ctx, []string{"a.txt", "a2.txt"}); err != nil {
			t.Fatalf("删除失败: %v", err)
		}
		first := srv.Counts.DeleteBatch.Load()

		// 第二次:再来一次同签名 500 → 确证并进入冷却;本次仍会探测。
		srv.Put(testBucket, "b.txt", []byte("x"), "text/plain")
		srv.Put(testBucket, "b2.txt", []byte("x"), "text/plain")
		if err := d.DeleteKeys(ctx, []string{"b.txt", "b2.txt"}); err != nil {
			t.Fatalf("删除失败: %v", err)
		}
		second := srv.Counts.DeleteBatch.Load()
		// 注意:单次 deleteBatch 调用会被 SDK 按 5xx 重试(实测 3 个请求),
		// 故这里只断言「是否又探测过」,不写死每调用一次的请求数。
		if second <= first {
			t.Fatalf("单次 500 不应被当成确证:第二次仍应探测批量端点(之前 %d,现在 %d)", first, second)
		}

		// 第三次:已确证 → 不再探测。
		srv.Put(testBucket, "c.txt", []byte("x"), "text/plain")
		srv.Put(testBucket, "c2.txt", []byte("x"), "text/plain")
		if err := d.DeleteKeys(ctx, []string{"c.txt"}); err != nil {
			t.Fatalf("删除失败: %v", err)
		}
		if n := srv.Counts.DeleteBatch.Load(); n != second {
			t.Fatalf("两次同签名 500 后应确证并停止探测:之前 %d,现在 %d", second, n)
		}
	})

	t.Run("逐 key 失败不确证", func(t *testing.T) {
		d, srv := newDeleterFor(t)
		srv.FailBatchDeleteKeys.Store(true)

		srv.Put(testBucket, "a.txt", []byte("x"), "text/plain")
		srv.Put(testBucket, "a2.txt", []byte("x"), "text/plain")
		if err := d.DeleteKeys(ctx, []string{"a.txt", "a2.txt"}); err != nil {
			t.Fatalf("删除失败: %v", err)
		}
		first := srv.Counts.DeleteBatch.Load()
		srv.Put(testBucket, "b.txt", []byte("x"), "text/plain")
		srv.Put(testBucket, "b2.txt", []byte("x"), "text/plain")
		if err := d.DeleteKeys(ctx, []string{"b.txt", "b2.txt"}); err != nil {
			t.Fatalf("删除失败: %v", err)
		}
		if n := srv.Counts.DeleteBatch.Load(); n <= first {
			t.Fatalf("逐 key 失败说明端点可用,下次仍应走批量:之前 %d,现在 %d", first, n)
		}
	})

	t.Run("请求取消不确证", func(t *testing.T) {
		d, srv := newDeleterFor(t)
		srv.Put(testBucket, "a.txt", []byte("x"), "text/plain")
		srv.Put(testBucket, "a2.txt", []byte("x"), "text/plain")
		cc, cancel := context.WithCancel(ctx)
		cancel()
		_ = d.DeleteKeys(cc, []string{"a.txt"}) // 必然失败,但结论不作数

		probed := srv.Counts.DeleteBatch.Load()
		srv.Put(testBucket, "b.txt", []byte("x"), "text/plain")
		srv.Put(testBucket, "b2.txt", []byte("x"), "text/plain")
		if err := d.DeleteKeys(ctx, []string{"b.txt", "b2.txt"}); err != nil {
			t.Fatalf("删除失败: %v", err)
		}
		if n := srv.Counts.DeleteBatch.Load(); n <= probed {
			t.Fatalf("取消不得被当成「端点不支持」,下次应重试批量:之前 %d,现在 %d", probed, n)
		}
	})
}

// 单键删除不走批量端点:批量对一个 key 没有任何收益(同样一次请求),而在
// 「不支持批量端点」的网关上还要多付一次注定失败的探测(含 SDK 重试)。
func TestDeleteSingleKeySkipsBatchEndpoint(t *testing.T) {
	d, srv := newDeleterFor(t)
	srv.Put(testBucket, "one.txt", []byte("x"), "text/plain")

	if err := d.DeleteKeys(context.Background(), []string{"one.txt"}); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if n := srv.Counts.DeleteBatch.Load(); n != 0 {
		t.Errorf("单键删除不应触碰批量端点,实际 %d 次", n)
	}
	if n := srv.Counts.Delete.Load(); n != 1 {
		t.Errorf("应发生 1 次单对象删除,实际 %d", n)
	}
}
