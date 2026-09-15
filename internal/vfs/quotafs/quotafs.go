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
	"time"

	"github.com/BeCrafter/sail/internal/vfs"
)

// DefaultTTL 是配额快照的默认刷新周期。
const DefaultTTL = 5 * time.Minute

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

	mu       sync.Mutex
	limit    int64 // <= 0 = 不限额
	usage    int64 // 快照用量(最后一次成功统计值 + 窗口内提交增量)
	expires  time.Time
	fresh    bool  // 快照是否曾成功获取;从未成功按 0 计,不为配额阻塞可用性
	inflight int64 // 在途预留总和(字节)

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
// 不受影响——下一次准入自然按新口径计算(配额热更新场景)。
func (fs *FS) SetQuota(limitBytes int64) {
	fs.mu.Lock()
	fs.limit = limitBytes
	fs.mu.Unlock()
}

// Limit 返回当前配额上限(诊断用)。
func (fs *FS) Limit() int64 {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.limit
}

// usageLocked 返回用于准入算术的用量,必要时刷新快照。调用方需持锁。
// 刷新失败沿用旧快照并告警(I5)。
func (fs *FS) usageLocked(ctx context.Context) int64 {
	if fs.counter != nil && (fs.now().After(fs.expires) || !fs.fresh) {
		if u, err := fs.counter.Usage(ctx); err != nil {
			if fs.logger != nil {
				fs.logger.Printf("quotafs: refresh usage snapshot failed, keeping previous value %d: %v", fs.usage, err)
			}
		} else {
			fs.usage = u
			fs.fresh = true
		}
		fs.expires = fs.now().Add(fs.ttl)
	}
	return fs.usage
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

func (fs *FS) Remove(ctx context.Context, p string, recursive bool) error {
	return fs.core.Remove(ctx, p, recursive)
}

func (fs *FS) Rename(ctx context.Context, oldPath, newPath string) error {
	return fs.core.Rename(ctx, oldPath, newPath)
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
	w.fs.mu.Lock()
	defer w.fs.mu.Unlock()
	if w.fs.limit <= 0 {
		return nil
	}
	usage := w.fs.usageLocked(ctx)
	existing := w.fs.existingSize(ctx, w.path)
	if usage+w.fs.inflight-existing+contentLength <= w.fs.limit {
		w.fs.inflight += contentLength
		w.reserved = contentLength
		return nil
	}
	return w.fs.overLimitErrorLocked(usage, existing, contentLength)
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
	// 探测到的就是新大小,「扣旧值」会失真(增量清零)。
	w.fs.mu.Lock()
	existing := w.fs.existingSize(ctx, w.path)
	if err := w.recheckLocked(ctx, written, existing); err != nil {
		w.fs.mu.Unlock()
		w.release(reserved)
		w.core.Abort()
		w.core.Close()
		return vfs.FileInfo{}, err
	}
	w.fs.mu.Unlock()

	fi, err := w.core.Commit(ctx)
	if err != nil {
		w.release(reserved)
		w.core.Close()
		return vfs.FileInfo{}, err
	}
	// 提交成功:注销预留,把「实际字节 − 释放的旧值」记入窗口增量,让
	// TTL 内的后续准入看到最新口径(下次全量刷新会覆盖它)。
	w.release(reserved)
	w.fs.mu.Lock()
	w.fs.usage += written - existing
	w.fs.mu.Unlock()
	return fi, nil
}

// recheckLocked 在提交点按实际字节复核。有预留且实际未超出预留时,账已
// 含在预留内、直接放行;否则按「usage + 其余在途 − existing + written」重核。
// existing 由调用方在提交前探测并传入。调用方需持 fs.mu。
func (w *quotaWriter) recheckLocked(ctx context.Context, written, existing int64) error {
	if w.fs.limit <= 0 || (w.reserved > 0 && written <= w.reserved) {
		return nil
	}
	usage := w.fs.usageLocked(ctx)
	rest := w.fs.inflight - w.reserved // 其余句柄的在途
	if usage+rest-existing+written > w.fs.limit {
		return w.fs.overLimitErrorLocked(usage, existing, written)
	}
	return nil
}

// release 幂等注销在途预留(I3 三出口共用)。
func (w *quotaWriter) release(reserved int64) {
	if reserved <= 0 {
		return
	}
	w.fs.mu.Lock()
	if !w.released {
		w.fs.inflight -= reserved
		w.released = true
	}
	w.fs.mu.Unlock()
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
