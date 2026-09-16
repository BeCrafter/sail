package quotafs

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BeCrafter/sail/internal/vfs"
)

// --- I5 内部测试:注入时钟与故障计数器,验证快照刷新失败沿用旧值 ---

type fakeCore struct {
	mu    sync.Mutex
	sizes map[string]int64
}

func (c *fakeCore) Stat(_ context.Context, p string) (vfs.FileInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.sizes[p]
	if !ok {
		return vfs.FileInfo{}, vfs.ErrNotExist
	}
	return vfs.FileInfo{Name: p, Path: p, Size: s}, nil
}
func (c *fakeCore) ReadDir(context.Context, string) ([]vfs.FileInfo, error) {
	return nil, vfs.ErrNotSupported
}
func (c *fakeCore) OpenRead(context.Context, string) (vfs.ReadSeekCloser, error) {
	return nil, vfs.ErrNotSupported
}
func (c *fakeCore) Remove(_ context.Context, p string, _ bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sizes, p)
	return nil
}
func (c *fakeCore) Rename(context.Context, string, string) error { return nil }

// fakeWriter 是 no-op 写句柄:配额会计测试只关心准入/预留,不关心落盘。
type fakeWriter struct{ core *fakeCore }

func (w *fakeWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *fakeWriter) Commit(context.Context) (vfs.FileInfo, error) {
	return vfs.FileInfo{}, nil
}
func (w *fakeWriter) Abort() error { return nil }
func (w *fakeWriter) Close() error { return nil }

func (c *fakeCore) OpenWrite(context.Context, string) (vfs.WriteHandle, error) {
	return &fakeWriter{core: c}, nil
}

type fakeCounter struct {
	mu     sync.Mutex
	value  int64
	fail   bool
	called int
}

func (c *fakeCounter) Usage(context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.called++
	if c.fail {
		return 0, errors.New("backend unavailable")
	}
	return c.value, nil
}

// 快照刷新失败:沿用旧快照、产生告警日志,准入继续可用且不过量放行。
func TestSnapshotRefreshFailureKeepsOldValueAndWarns(t *testing.T) {
	core := &fakeCore{}
	counter := &fakeCounter{value: 80}
	buf := &syncBuf{}
	fs := New(core, counter, 100, time.Minute, log.New(buf, "", 0))
	now := time.Now()
	fs.now = func() time.Time { return now }

	// 首次刷新成功:80/100,放行 10。
	w, err := fs.OpenWrite(context.Background(), "/a")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.(*quotaWriter).AdmitWrite(context.Background(), 10); err != nil {
		t.Fatalf("首次准入应放行: %v", err)
	}
	w.Abort()
	w.Close()

	// TTL 过期 + 计数器故障:沿用旧值 80,准入继续可用,且日志告警。
	now = now.Add(2 * time.Minute)
	counter.mu.Lock()
	counter.fail = true
	counter.mu.Unlock()
	w2, err := fs.OpenWrite(context.Background(), "/b")
	if err != nil {
		t.Fatal(err)
	}
	if err := w2.(*quotaWriter).AdmitWrite(context.Background(), 10); err != nil {
		t.Fatalf("刷新失败应沿用旧快照: %v", err)
	}
	w2.Abort()
	w2.Close()
	// 刷新是后台单飞(SWR):等它落定再断言,否则会读到「还没写日志」的中间态,
	// 而且会与后续步骤触发的刷新撞在同一个在途槽位上。
	waitCondition(t, func() bool {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		return !fs.refreshing
	})
	if !strings.Contains(buf.String(), "keeping previous value") {
		t.Errorf("应出现沿用旧值告警,实际: %s", buf.String())
	}

	// 恢复后拿到新值:计数器报 0 → 限额内大幅放行。
	// 注意 SWR 语义:过期后的第一次准入仍用旧快照(80),刷新在后台进行;
	// 等刷新落定后再准入,才按新值(0)放行。
	now = now.Add(2 * time.Minute)
	counter.mu.Lock()
	counter.fail = false
	counter.value = 0
	counter.mu.Unlock()

	w3, err := fs.OpenWrite(context.Background(), "/c")
	if err != nil {
		t.Fatal(err)
	}
	_ = w3.(*quotaWriter).AdmitWrite(context.Background(), 10) // 触发后台刷新(用旧值判定)
	w3.Abort()
	w3.Close()

	waitCondition(t, func() bool {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		return !fs.refreshing && fs.usage == 0
	})

	w4, err := fs.OpenWrite(context.Background(), "/d")
	if err != nil {
		t.Fatal(err)
	}
	if err := w4.(*quotaWriter).AdmitWrite(context.Background(), 90); err != nil {
		t.Fatalf("刷新落定后应按新快照放行: %v", err)
	}
	w4.Abort()
	w4.Close()
}

type syncBuf struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// 刷新失败只退避 refreshFailTTL(30s),而不是把整段 TTL(这里 1 分钟)推后:
// 一次超时/断连不该让快照脏满整个窗口。
func TestRefreshFailureRetriesAfterShortBackoff(t *testing.T) {
	ctx := context.Background()
	core := &fakeCore{}
	counter := &fakeCounter{value: 50}
	fs := New(core, counter, 100, time.Minute, nil)
	now := time.Now()
	fs.now = func() time.Time { return now }
	calls := func() int {
		counter.mu.Lock()
		defer counter.mu.Unlock()
		return counter.called
	}
	admit := func(path string, n int64) error {
		w, err := fs.OpenWrite(ctx, path)
		if err != nil {
			return err
		}
		defer w.Close()
		return w.(*quotaWriter).AdmitWrite(ctx, n)
	}

	if err := admit("/a", 10); err != nil { // 首次成功刷新
		t.Fatalf("首次准入应放行: %v", err)
	}
	base := calls()
	if base == 0 {
		t.Fatal("首次准入应触发一次统计")
	}

	// TTL 过期 + 计数器故障:失败 → 退避 30s。
	now = now.Add(2 * time.Minute)
	counter.mu.Lock()
	counter.fail = true
	counter.mu.Unlock()
	if err := admit("/b", 10); err != nil {
		t.Fatalf("刷新失败应沿用旧快照: %v", err)
	}
	// 过期后的刷新是后台单飞(SWR):轮询等它真的发出。
	afterFail := waitCalls(t, calls, base)
	if afterFail <= base {
		t.Fatal("过期后应尝试刷新")
	}
	// 等刷新失败落定(expires 推到退避之后)。
	waitCondition(t, func() bool {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		return !fs.refreshing
	})

	// 退避未到期:不再尝试。
	now = now.Add(refreshFailTTL - time.Second)
	if err := admit("/c", 10); err != nil {
		t.Fatalf("退避期内应沿用旧快照: %v", err)
	}
	if got := calls(); got != afterFail {
		t.Fatalf("退避期内不应再刷新:之前 %d 次,现在 %d 次", afterFail, got)
	}

	// 退避到期(远早于整段 TTL):再次尝试。
	now = now.Add(2 * time.Second)
	counter.mu.Lock()
	counter.fail = false
	counter.mu.Unlock()
	if err := admit("/d", 10); err != nil {
		t.Fatalf("退避到期后应重试并放行: %v", err)
	}
	if got := waitCalls(t, calls, afterFail); got <= afterFail {
		t.Fatalf("退避到期后应重新统计:之前 %d 次,现在 %d 次", afterFail, got)
	}
}

// waitCalls 轮询等 counter 调用次数超过 base(后台刷新是异步的),超时返回当前值。
func waitCalls(t *testing.T, calls func() int, base int) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if n := calls(); n > base {
			return n
		}
		time.Sleep(5 * time.Millisecond)
	}
	return calls()
}

// waitCondition 轮询等条件成立,超时即失败。
func waitCondition(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("条件在 3s 内未满足")
}

// Invalidate 作废快照:TTL 内也会在下一次准入强制重新统计(清理空间后
// 无需等窗口过期即可恢复写入)。
func TestInvalidateForcesRefresh(t *testing.T) {
	ctx := context.Background()
	core := &fakeCore{}
	counter := &fakeCounter{value: 95}
	fs := New(core, counter, 100, time.Hour, nil)
	now := time.Now()
	fs.now = func() time.Time { return now }
	admit := func(path string, n int64) error {
		w, err := fs.OpenWrite(ctx, path)
		if err != nil {
			return err
		}
		defer w.Close()
		return w.(*quotaWriter).AdmitWrite(ctx, n)
	}

	if err := admit("/a", 10); err == nil || !errors.Is(err, vfs.ErrInsufficientStorage) {
		t.Fatalf("95/100 下写 10 应被拒: %v", err)
	}

	// 外部释放了空间,但 TTL(1 小时)未到:仍按旧快照拒绝。
	counter.mu.Lock()
	counter.value = 0
	counter.mu.Unlock()
	if err := admit("/b", 10); err == nil {
		t.Fatal("TTL 内不应自动刷新,应仍按旧快照拒绝")
	}

	// Invalidate 后立即按新口径放行。
	fs.Invalidate()
	if err := admit("/c", 90); err != nil {
		t.Fatalf("Invalidate 后应按新快照放行: %v", err)
	}
}

// blockingCounter 的第一次 Usage 立即返回,之后的调用会阻塞在 block 上,
// 用于验证「过期后的准入不等刷新」。
type blockingCounter struct {
	value int64
	block chan struct{}
	calls int32
}

func (c *blockingCounter) Usage(context.Context) (int64, error) {
	if atomic.AddInt32(&c.calls, 1) > 1 {
		<-c.block
	}
	return c.value, nil
}

// SWR:快照过期后的准入用旧值立即返回,刷新在后台进行 —— 大前缀的全量统计
// 可达数十秒,不能让该用户的写请求排队等它(这正是本次优化的痛点)。
func TestExpiredSnapshotDoesNotBlockAdmission(t *testing.T) {
	ctx := context.Background()
	core := &fakeCore{}
	block := make(chan struct{})
	counter := &blockingCounter{value: 40, block: block}
	fs := New(core, counter, 100, time.Minute, nil)
	now := time.Now()
	fs.now = func() time.Time { return now }

	// 首次:从未成功,必须等一次(这里计数器不阻塞)。
	w, err := fs.OpenWrite(ctx, "/a")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.(*quotaWriter).AdmitWrite(ctx, 10); err != nil {
		t.Fatalf("首次准入应放行: %v", err)
	}
	w.Abort()
	w.Close()

	// 过期:下一次统计会阻塞,准入仍应立刻返回(用旧值 40 判定)。
	now = now.Add(2 * time.Minute)
	start := time.Now()
	w2, err := fs.OpenWrite(ctx, "/b")
	if err != nil {
		t.Fatal(err)
	}
	admitErr := w2.(*quotaWriter).AdmitWrite(ctx, 50) // 40+50=90 ≤ 100
	elapsed := time.Since(start)
	w2.Abort()
	w2.Close()
	if admitErr != nil {
		t.Fatalf("过期后应按旧快照放行: %v", admitErr)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("过期后的准入不应等待刷新,实际耗时 %v", elapsed)
	}
	close(block) // 放行后台刷新,避免 goroutine 泄漏
}

// 删除会释放空间:成功后快照作废,下一次准入按新口径判定,而不是继续按旧值
// 拒绝(「删了东西还写不进去」)。
func TestRemoveInvalidatesSnapshot(t *testing.T) {
	ctx := context.Background()
	core := &fakeCore{}
	counter := &fakeCounter{value: 95}
	fs := New(core, counter, 100, time.Hour, nil)
	now := time.Now()
	fs.now = func() time.Time { return now }
	admit := func(p string, n int64) error {
		w, err := fs.OpenWrite(ctx, p)
		if err != nil {
			return err
		}
		defer w.Close()
		return w.(*quotaWriter).AdmitWrite(ctx, n)
	}

	if err := admit("/a", 10); err == nil {
		t.Fatal("前置:95/100 下写 10 应被拒")
	}

	// 用户删掉文件(计数器随之变小),删除本身作废快照。
	if err := fs.Remove(ctx, "/a", false); err != nil {
		t.Fatalf("Remove 失败: %v", err)
	}
	counter.mu.Lock()
	counter.value = 0
	counter.mu.Unlock()

	if err := admit("/b", 90); err != nil {
		t.Fatalf("删除后应按新快照放行: %v", err)
	}
}

// 改名同样作废快照:覆盖已存在目标会把目标的旧字节替换成源的字节,总量随之变化。
func TestRenameInvalidatesSnapshot(t *testing.T) {
	ctx := context.Background()
	core := &fakeCore{}
	fs := New(core, &fakeCounter{value: 10}, 100, time.Hour, nil)
	now := time.Now()
	fs.now = func() time.Time { return now }

	// 先建立快照。
	w, err := fs.OpenWrite(ctx, "/a")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.(*quotaWriter).AdmitWrite(ctx, 5); err != nil {
		t.Fatalf("前置准入应放行: %v", err)
	}
	w.Abort()
	w.Close()

	if err := fs.Rename(ctx, "/a", "/b"); err != nil {
		t.Fatalf("Rename 失败: %v", err)
	}
	fs.mu.Lock()
	fresh := fs.fresh
	fs.mu.Unlock()
	if fresh {
		t.Error("改名后应作废快照(下次准入重新统计)")
	}
}
