// Package quotafs 是 vfs.FileSystem 的配额装饰器:在任意内核(如 s3fs)之上
// 做「用户前缀物理字节」的空间配额会计。协议无关——未来 SMB 壳可直接复用。
//
// 会计口径(冻结契约):
//   - 配额限制桶内物理字节消耗(与账单一致),由内核可选实现的 QuotaCounter
//     (如 s3fs.Usage:ListObjectsV2 分页求和,含 .sail/ 分片部件与 manifest)提供快照;
//   - 快照惰性获取、按 TTL 刷新;刷新失败沿用旧快照并告警,不阻塞服务(I5);
//   - 快照窗口内的增量靠在途预留兜住:OpenWrite 登记 → Commit/Abort/Close
//     三出口幂等注销(I3);
//   - 超限一律返回 vfs.ErrInsufficientStorage(壳映射为 507),不新造错误码(I2);
//   - 快照精度边界:窗口内尽力准确,网关外写入(如 sail cp 直写)在下次刷新前
//     不可见——这是声明式窗口,不是强一致账本。
package quotafs

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BeCrafter/sail/internal/vfs"
)

// DefaultTTL 是配额快照的默认刷新周期。
const DefaultTTL = 5 * time.Minute

// refreshFailTTL 是刷新失败后的重试间隔。失败不能把整段 TTL 都推后:
// 一次超时/断连会让快照脏满 5 分钟,期间用户按旧值被拒。
const refreshFailTTL = 30 * time.Second

// usageRefreshTimeout 是单次全量统计的时限。统计是 O(前缀对象数) 的分页列举,
// 大前缀可达数十秒;给个上限避免极端情况下无限占用(超时按失败处理,沿用旧值)。
const usageRefreshTimeout = 2 * time.Minute

// QuotaCounter 由内核可选实现:按物理字节统计本核前缀下的总用量。
// s3fs.FS 已实现(Usage);计数器缺失时快照恒按 0 计(仅预留口径的弱约束,
// 不阻塞可用性)。
type QuotaCounter interface {
	Usage(ctx context.Context) (int64, error)
}

// FS 是带配额的 vfs.FileSystem 装饰器。limit <= 0 表示不限额。SetQuota
// 原子改参数,热加载复用同一实例、不重建栈(I7)。
type FS struct {
	core    vfs.FileSystem
	counter QuotaCounter
	logger  *log.Logger
	ttl     time.Duration

	// label 是日志前缀(通常是用户名),多用户下区分来源;atomic 存读,
	// 因为日志可能在持锁路径上打(不能再去抢 fs.mu)。
	label atomic.Value // string

	mu       sync.Mutex
	limit    int64 // <= 0 = 不限额
	usage    int64 // 快照用量(最后一次成功统计值 + 窗口内提交增量)
	expires  time.Time
	fresh    bool  // 快照是否曾成功获取;从未成功按 0 计,不为配额阻塞可用性
	inflight int64 // 在途预留总和(字节)

	// 刷新协调:单飞。refreshing 为真时 refreshDone 是本次刷新的完成信号;
	// 等待方只等信号,不等锁 —— 大前缀的列举不再让准入串行排队。
	refreshing  bool
	refreshDone chan struct{}

	// 只在首次发现 counter 缺失时告警一次。
	warnNoCounter sync.Once

	// now 可注入以便测试;默认 time.Now。
	now func() time.Time
}

// New 构造配额装饰器。limitBytes <= 0 表示不限额;ttl <= 0 取 DefaultTTL;
// counter 为 nil 时快照恒为 0。logger 可为 nil。
func New(core vfs.FileSystem, counter QuotaCounter, limitBytes int64, ttl time.Duration, logger *log.Logger) *FS {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &FS{
		core:    core,
		counter: counter,
		logger:  logger,
		ttl:     ttl,
		limit:   limitBytes,
		now:     time.Now,
	}
}

// SetQuota 原子修改配额上限(字节);<= 0 = 不限额。既有连接与在途预留
// 不受影响——下一次准入自然按新口径计算(配额热更新场景)。上限变化时
// 顺带作废快照:运维调大配额往往与「刚清理过空间」同时发生。
func (fs *FS) SetQuota(limitBytes int64) {
	fs.mu.Lock()
	fs.limit = limitBytes
	fs.invalidateSnapshotLocked()
	fs.mu.Unlock()
}

// Invalidate 作废当前用量快照,让下一次准入强制重新统计(如清理空间后
// 想立刻恢复写入)。不阻塞、可在任意场景调用;失败/未配 counter 时行为不变。
func (fs *FS) Invalidate() {
	fs.mu.Lock()
	fs.invalidateSnapshotLocked()
	fs.mu.Unlock()
}

// invalidateSnapshotLocked 作废快照并强制下一次准入**等一次**刷新(fresh=false),
// 而不是走 TTL 到期的 SWR(后者仍先给旧值)。显式失效是「我现在就要准数」的
// 场景(刚清理空间、刚改配额),等一次是值得的;等待有上限(initialSnapshotWait)。
// 调用方需持锁。
func (fs *FS) invalidateSnapshotLocked() {
	fs.fresh = false
	fs.expires = time.Time{}
}

// SetLabel 设置日志前缀(通常是用户名),便于多用户下区分来源。可在运行期调用。
func (fs *FS) SetLabel(label string) {
	fs.label.Store(label)
}

// logf 打一条带实例前缀的日志;logger 为 nil 时静默。不取 fs.mu ——
// 调用点可能正持锁。
func (fs *FS) logf(format string, args ...any) {
	if fs.logger == nil {
		return
	}
	label, _ := fs.label.Load().(string)
	if label == "" {
		label = "quotafs"
	}
	fs.logger.Printf(label+": "+format, args...)
}

// QuotaUsage 返回配额口径的只读快照(已用 / 剩余字节),供协议壳向客户端
// 播报(RFC 4331 的 DAV:quota-*-bytes)。刻意**不触发刷新**:属性请求在
// PROPFIND 热路径上,不能为它打后端;读的是缓存快照 + 在途预留。
// ok=false 表示没配配额(limit<=0)或没有计数器,调用方不应播报该属性。
func (fs *FS) QuotaUsage() (used, avail int64, ok bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.limit <= 0 || fs.counter == nil {
		return 0, 0, false
	}
	used = fs.usage + fs.inflight
	if used < 0 {
		used = 0
	}
	avail = fs.limit - used
	if avail < 0 {
		avail = 0
	}
	return used, avail, true
}

// Limit 返回当前配额上限(诊断用)。
func (fs *FS) Limit() int64 {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.limit
}

// initialSnapshotWait 是「从未有过快照」时准入愿意等待的时长。等不到就按 0
// 计(配额退化为只看在途预留),而不是把写请求无限期挂住。
const initialSnapshotWait = 3 * time.Second

// snapshotUsage 返回用于准入算术的用量,并在需要时触发刷新。自行加锁,
// 不要求调用方持锁。三种情形:
//   - 新鲜:直接给快照;
//   - 过期但有旧值:给旧值 + 后台单飞刷新(SWR)—— 大前缀的统计可达数十秒,
//     不能让它把该用户的写请求串行阻塞(实测痛点);
//   - 从未成功:等一次刷新,至多 initialSnapshotWait;超时按 0 计并告警。
func (fs *FS) snapshotUsage(ctx context.Context) int64 {
	if fs.counter == nil {
		return 0
	}
	fs.mu.Lock()
	if fs.fresh && !fs.now().After(fs.expires) {
		u := fs.usage
		fs.mu.Unlock()
		return u
	}
	if fs.fresh {
		u := fs.usage
		fs.mu.Unlock()
		fs.startRefresh(ctx)
		return u
	}
	inBackoff := fs.now().Before(fs.expires)
	fs.mu.Unlock()
	if inBackoff {
		// 上一次刷新刚失败过(expires 已推到 refreshFailTTL 之后):不要每个写
		// 请求都白等一轮,直接按 0 计 —— 配额退化为只看在途预留,直到重试成功。
		return 0
	}

	fs.startRefresh(ctx)
	if fs.waitRefresh(initialSnapshotWait) {
		fs.mu.Lock()
		u := fs.usage
		fs.mu.Unlock()
		return u
	}
	fs.logf("initial usage snapshot still running after %s; admitting against 0 (quota is best-effort until it lands)", initialSnapshotWait)
	return 0
}

// Refresh 主动触发一次快照刷新(不等待)。供启动时预热:让首次准入就用上
// 真实用量,而不是走「从未成功」的等待路径。
func (fs *FS) Refresh(ctx context.Context) {
	if fs.counter == nil {
		return
	}
	fs.startRefresh(ctx)
}

// startRefresh 启动一次后台刷新;已有在途刷新时直接返回(单飞)。
func (fs *FS) startRefresh(ctx context.Context) {
	fs.mu.Lock()
	if fs.refreshing || fs.counter == nil {
		fs.mu.Unlock()
		return
	}
	fs.refreshing = true
	done := make(chan struct{})
	fs.refreshDone = done
	fs.mu.Unlock()

	go func() {
		defer close(done)
		// 脱离请求的 ctx:准入发生在上传请求里,客户端中途断开不该让快照
		// 刷新失败(与 webdavfs 后台刷新列表的处理一致)。
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), usageRefreshTimeout)
		defer cancel()
		u, err := fs.counter.Usage(rctx)

		fs.mu.Lock()
		defer fs.mu.Unlock()
		if err != nil {
			// logf 不取 fs.mu,故这里持锁打日志是安全的。
			fs.logf("refresh usage snapshot failed, keeping previous value %d (retry in %s): %v", fs.usage, refreshFailTTL, err)
			fs.expires = fs.now().Add(refreshFailTTL)
		} else {
			fs.usage = u
			fs.fresh = true
			fs.expires = fs.now().Add(fs.ttl)
		}
		fs.refreshing = false
		fs.refreshDone = nil
	}()
}

// waitRefresh 等待至多 d,返回在途刷新是否已完成(无在途时立即返回 true)。
func (fs *FS) waitRefresh(d time.Duration) bool {
	fs.mu.Lock()
	done := fs.refreshDone
	fs.mu.Unlock()
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// existingSize 返回覆盖目标的既有大小;对象不存在或探测失败按 0——
// 失败方向是算术偏保守(多记不多放),不会放行超额写。
func (fs *FS) existingSize(ctx context.Context, path string) int64 {
	fi, err := fs.core.Stat(ctx, path)
	if err != nil || fi.IsDir {
		return 0
	}
	return fi.Size
}

// overLimitErrorLocked 渲染超限错误:带量化上下文;双重包装使
// errors.Is 同时命中 ErrInsufficientStorage(状态码 507)与
// ErrQuotaExceeded(壳的配额专属指引)。调用方需持锁。
func (fs *FS) overLimitErrorLocked(usage, existing, incoming int64) error {
	return fmt.Errorf("quotafs: %w (limit %d, usage %d, in-flight %d, overwrite releases %d, incoming %d): %w",
		vfs.ErrQuotaExceeded, fs.limit, usage, fs.inflight, existing, incoming, vfs.ErrInsufficientStorage)
}

func (fs *FS) Stat(ctx context.Context, p string) (vfs.FileInfo, error) {
	return fs.core.Stat(ctx, p)
}

func (fs *FS) ReadDir(ctx context.Context, p string) ([]vfs.FileInfo, error) {
	return fs.core.ReadDir(ctx, p)
}

func (fs *FS) OpenRead(ctx context.Context, p string) (vfs.ReadSeekCloser, error) {
	return fs.core.OpenRead(ctx, p)
}

// OpenReadWithInfo 转发内核的 vfs.InfoOpener 能力,保持 WebDAV 读路径
// 「打开即 1 RTT」的性能优化不被装饰层吃掉。
func (fs *FS) OpenReadWithInfo(ctx context.Context, p string, fi vfs.FileInfo) (vfs.ReadSeekCloser, error) {
	if opener, ok := fs.core.(vfs.InfoOpener); ok {
		return opener.OpenReadWithInfo(ctx, p, fi)
	}
	return fs.core.OpenRead(ctx, p)
}

// Remove 转发内核删除,成功后作废用量快照:删除会释放空间,不作废的话
// 用户在快照过期前仍会被 507 拒绝(「删了东西还写不进去」)。作废是廉价的
// (只标脏),真正的重新统计发生在下一次准入,且有单飞与退避兜底。
func (fs *FS) Remove(ctx context.Context, p string, recursive bool) error {
	if err := fs.core.Remove(ctx, p, recursive); err != nil {
		return err
	}
	fs.Invalidate()
	return nil
}

// Rename 转发内核改名,成功后同样作废快照:改名本身不改变总量,但覆盖已存在
// 的目标时会把目标的旧字节替换成源的字节,总量随之变化。
func (fs *FS) Rename(ctx context.Context, oldPath, newPath string) error {
	if err := fs.core.Rename(ctx, oldPath, newPath); err != nil {
		return err
	}
	fs.Invalidate()
	return nil
}

// OpenWrite 打开写句柄并包一层配额会计。此时不做准入:Content-Length 尚未
// 传入(壳在 OpenWrite 之后才 SetWriteOptions),准入在壳读请求体前完成。
func (fs *FS) OpenWrite(ctx context.Context, p string) (vfs.WriteHandle, error) {
	w, err := fs.core.OpenWrite(ctx, p)
	if err != nil {
		return nil, err
	}
	return &quotaWriter{fs: fs, core: w, path: p}, nil
}

// quotaWriter 是带配额会计的写句柄:统计实际写入字节,提交点复核,
// Commit/Abort/Close 三出口幂等注销在途预留(I3)。同一请求内单 goroutine
// 顺序调用(webdav:OpenFile → Admit → Write → Stat/Close),reserved 在
// AdmitWrite 后不再变化。
type quotaWriter struct {
	fs   *FS
	core vfs.WriteHandle
	path string

	mu        sync.Mutex
	written   int64
	reserved  int64 // 已预留字节(0 = 未预留/长度未知);AdmitWrite 成功后不变
	released  bool  // 预留是否已注销
	committed bool
	aborted   bool
	closeDone bool
}

// SetWriteOptions 转发内核(暂存盘预留等)。
func (w *quotaWriter) SetWriteOptions(opts vfs.WriteOptions) {
	if opt, ok := w.core.(vfs.WriteOptioner); ok {
		opt.SetWriteOptions(opts)
	}
}

// AdmitWrite 是配额准入入口,由协议壳在「读取请求体之前」调用:按
// 「快照用量 + 在途 − existing(覆盖目标) + 声明长度」判断,超限返回
// vfs.ErrInsufficientStorage。contentLength 未知(如 COPY)传 -1,本轮不
// 预留,由提交点按实际字节复核。通过后登记预留,随 Commit/Abort/Close
// 幂等注销(I3)。
func (w *quotaWriter) AdmitWrite(ctx context.Context, contentLength int64) error {
	if contentLength <= 0 {
		return nil // 长度未知:留给提交点复核
	}
	fs := w.fs
	// 快照与覆盖目标探测都在锁外:两者都可能触发后端请求(统计 / HEAD),
	// 持锁做会把该用户的并发写全部串行排在后面。
	usage := fs.snapshotUsage(ctx)
	existing := fs.existingSize(ctx, w.path)

	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.limit <= 0 { // 锁外期间可能已改配额
		return nil
	}
	if usage+fs.inflight-existing+contentLength <= fs.limit {
		fs.inflight += contentLength
		w.reserved = contentLength
		return nil
	}
	return fs.overLimitErrorLocked(usage, existing, contentLength)
}

func (w *quotaWriter) Write(p []byte) (int, error) {
	n, err := w.core.Write(p)
	if n > 0 {
		w.mu.Lock()
		w.written += int64(n)
		w.mu.Unlock()
	}
	return n, err
}

// Commit 复核实际字节后落内核。提交点为准:覆盖目标既有大小在提交时重新
// 探测,计入「扣旧值」(x/net 的 COPY 覆盖写会先移除目标,届时探测为 0,
// 而快照 TTL 内旧值仍计入用量——算术偏保守,不会漏拦)。
func (w *quotaWriter) Commit(ctx context.Context) (vfs.FileInfo, error) {
	w.mu.Lock()
	if w.committed {
		w.mu.Unlock()
		return w.core.Commit(ctx) // 内核 Commit 幂等,重入转发即可
	}
	if w.aborted {
		w.mu.Unlock()
		return vfs.FileInfo{}, errors.New("quotafs: write handle aborted")
	}
	written, reserved := w.written, w.reserved
	w.committed = true
	w.mu.Unlock()

	// 覆盖前的旧值必须在 core.Commit 之前探测:提交后目标已是新对象,
	// 探测到的就是新大小,「扣旧值」会失真(增量清零)。探测与复核都在锁外
	// (复核内部自行取锁)。
	existing := w.fs.existingSize(ctx, w.path)
	if err := w.recheck(ctx, written, existing); err != nil {
		w.release(reserved)
		w.core.Abort()
		w.core.Close()
		return vfs.FileInfo{}, err
	}

	fi, err := w.core.Commit(ctx)
	if err != nil {
		w.release(reserved)
		w.core.Close()
		return vfs.FileInfo{}, err
	}
	// 提交成功:注销预留与记入增量必须在**同一临界区**完成 —— 分两次加锁时,
	// 两者之间到达的准入既看不到预留、也看不到增量,会少算并放行超额。
	w.fs.mu.Lock()
	w.releaseLocked(reserved)
	w.fs.usage += written - existing
	w.fs.mu.Unlock()
	return fi, nil
}

// recheckLocked 在提交点按实际字节复核。有预留且实际未超出预留时,账已
// 含在预留内、直接放行;否则按「usage + 其余在途 − existing + written」重核。
// existing 由调用方在提交前探测并传入。调用方需持 fs.mu。
// recheck 在提交点按实际字节复核。有预留且实际未超出预留时,账已含在预留
// 内、直接放行;否则按「快照 + 其余在途 − existing + written」重核。
// existing 由调用方在提交前探测并传入。自行加锁(快照获取可能等待刷新)。
func (w *quotaWriter) recheck(ctx context.Context, written, existing int64) error {
	fs := w.fs
	fs.mu.Lock()
	skip := fs.limit <= 0 || (w.reserved > 0 && written <= w.reserved)
	fs.mu.Unlock()
	if skip {
		return nil
	}
	usage := fs.snapshotUsage(ctx)
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.limit <= 0 || (w.reserved > 0 && written <= w.reserved) {
		return nil
	}
	rest := fs.inflight - w.reserved // 其余句柄的在途
	if usage+rest-existing+written > fs.limit {
		return fs.overLimitErrorLocked(usage, existing, written)
	}
	return nil
}

// release 幂等注销在途预留(I3 三出口共用)。
func (w *quotaWriter) release(reserved int64) {
	if reserved <= 0 {
		return
	}
	w.fs.mu.Lock()
	w.releaseLocked(reserved)
	w.fs.mu.Unlock()
}

// releaseLocked 是 release 的持锁版本:调用方需已持有 fs.mu —— 提交成功时
// 要把它与增量入账放进同一临界区。
func (w *quotaWriter) releaseLocked(reserved int64) {
	if reserved > 0 && !w.released {
		w.fs.inflight -= reserved
		w.released = true
	}
}

// Abort 丢弃暂存并注销预留,幂等。
func (w *quotaWriter) Abort() error {
	w.mu.Lock()
	first := !w.aborted
	w.aborted = true
	reserved := w.reserved
	w.mu.Unlock()
	if first {
		w.release(reserved)
	}
	return w.core.Abort()
}

// Close 清理暂存并注销预留,幂等;Commit 成功后不返回错误。
func (w *quotaWriter) Close() error {
	w.mu.Lock()
	first := !w.closeDone
	w.closeDone = true
	reserved := w.reserved
	w.mu.Unlock()
	if first {
		w.release(reserved)
	}
	return w.core.Close()
}
