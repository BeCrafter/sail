package s3fs

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// 网关支持批量删除时走批量端点:整棵子树连「同名对象本身」合并为一次请求,
// 全程不产生逐个删除。
func TestDeleteUsesBatchEndpointWhenSupported(t *testing.T) {
	fs, srv := newFSForInternal(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		srv.Put("b", fmt.Sprintf("d/%d.txt", i), []byte("x"), "text/plain")
	}

	if err := fs.Remove(ctx, "/d", true); err != nil {
		t.Fatalf("递归删除失败: %v", err)
	}
	if n := srv.Counts.DeleteBatch.Load(); n != 1 {
		t.Fatalf("子树与同名对象应合并为 1 次批量删除,实际 %d", n)
	}
	if n := srv.Counts.Delete.Load(); n != 0 {
		t.Fatalf("批量可用时不应退化成逐个删除,实际 %d 次", n)
	}
	if keys := srv.Keys("b"); len(keys) != 0 {
		t.Fatalf("对象未被删净: %v", keys)
	}
}

// 网关不支持批量删除时回退到并发单删,结果正确。
func TestDeleteFallsBackWhenBatchUnsupported(t *testing.T) {
	fs, srv := newFSForInternal(t)
	ctx := context.Background()
	srv.FailBatchDelete.Store(true)
	for i := 0; i < 3; i++ {
		srv.Put("b", fmt.Sprintf("d/%d.txt", i), []byte("x"), "text/plain")
	}

	if err := fs.Remove(ctx, "/d", true); err != nil {
		t.Fatalf("递归删除失败: %v", err)
	}
	// 3 个列表项 + 1 次「同名对象本身」(无此对象,删除幂等)。
	if n := srv.Counts.Delete.Load(); n != 4 {
		t.Fatalf("应回退为 4 次逐个删除,实际 %d", n)
	}
	if keys := srv.Keys("b"); len(keys) != 0 {
		t.Fatalf("对象未被删净: %v", keys)
	}
}

// 删不存在的对象:HEAD 已判明不存在,不再发注定空转的 DELETE。
func TestRemoveMissingObjectSkipsDelete(t *testing.T) {
	fs, srv := newFSForInternal(t)
	ctx := context.Background()

	if err := fs.Remove(ctx, "/nope.txt", false); err != nil {
		t.Fatalf("删除不存在的对象应幂等成功,实际报错: %v", err)
	}
	if n := srv.Counts.Delete.Load(); n != 0 {
		t.Fatalf("对象不存在时不应发删除请求,实际 %d 次", n)
	}
	if n := srv.Counts.DeleteBatch.Load(); n != 0 {
		t.Fatalf("对象不存在时不应发批量删除请求,实际 %d 次", n)
	}
}

// 回退路径是并发的:并发度 10 下,10 个对象的总耗时应接近单个延迟,
// 而不是 10 倍。
func TestDeleteFallbackIsConcurrent(t *testing.T) {
	fs, srv := newFSForInternal(t)
	ctx := context.Background()
	srv.FailBatchDelete.Store(true)
	// 用 501(端点不支持的规范答法)让一次失败即确证:5xx 走「连续两次」
	// 判据,预热一次不足以进入冷却,计时窗口会被探测(含 SDK 重试)污染。
	srv.BatchDeleteStatus.Store(http.StatusNotImplemented)
	srv.DeleteDelay.Store(int64(200 * time.Millisecond))

	// 预热:先删一个对象,把「批量端点不可用」的探测消耗掉——首次探测含
	// SDK 重试,耗时不可控,留在计时窗口里会掩盖并发信号。
	srv.Put("b", "warm.txt", []byte("x"), "text/plain")
	if err := fs.Remove(ctx, "/warm.txt", false); err != nil {
		t.Fatalf("预热删除失败: %v", err)
	}

	const n = 10
	for i := 0; i < n; i++ {
		srv.Put("b", fmt.Sprintf("many/%d.txt", i), []byte("x"), "text/plain")
	}
	start := time.Now()
	if err := fs.Remove(ctx, "/many", true); err != nil {
		t.Fatalf("并发删除失败: %v", err)
	}
	elapsed := time.Since(start)

	if keys := srv.Keys("b"); len(keys) != 0 {
		t.Fatalf("对象未被删净: %v", keys)
	}
	// 串行下限 = n × 200ms 注入延迟;并发应显著低于它。
	if serial := time.Duration(n) * 200 * time.Millisecond; elapsed >= serial {
		t.Fatalf("回退路径似乎是串行的:%d 个对象耗时 %v,串行下限 %v", n, elapsed, serial)
	}
}
