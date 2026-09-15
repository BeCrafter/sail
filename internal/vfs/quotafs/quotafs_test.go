package quotafs_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/fakes3"
	"github.com/BeCrafter/sail/internal/vfs"
	"github.com/BeCrafter/sail/internal/vfs/quotafs"
	"github.com/BeCrafter/sail/internal/vfs/s3fs"
)

const qbucket = "qb"

// admitter 与 webdavfs 壳侧的准入接口同形:quotaWriter 实现它,
// 壳在读取请求体之前调用。
type admitter interface {
	AdmitWrite(ctx context.Context, contentLength int64) error
}

type env struct {
	t   *testing.T
	s3  *fakes3.Server
	qfs *quotafs.FS
}

func newEnv(t *testing.T, limit int64) *env {
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
	core, err := s3fs.New(s3fs.Config{Client: s3c, Bucket: qbucket, StagingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("构造 s3fs 失败: %v", err)
	}
	return &env{t: t, s3: s3srv, qfs: quotafs.New(core, core, limit, 0, nil)}
}

// writer 打开写句柄并按声明长度准入(cl < 0 = 长度未知,不预留)。
func (e *env) writer(path string, cl int64) (vfs.WriteHandle, error) {
	e.t.Helper()
	w, err := e.qfs.OpenWrite(context.Background(), path)
	if err != nil {
		return nil, err
	}
	if adm, ok := w.(admitter); ok {
		if err := adm.AdmitWrite(context.Background(), cl); err != nil {
			w.Close()
			return nil, err
		}
	}
	return w, nil
}

func isQuotaErr(err error) bool { return errors.Is(err, vfs.ErrInsufficientStorage) }

// 场景(配额准入拦截):声明长度超限在读请求体之前拒绝,桶内无残留;
// 限额内准入通过并正常提交。
func TestQuotaAdmissionRejectsBeforeBody(t *testing.T) {
	e := newEnv(t, 10)
	if _, err := e.writer("/f.bin", 100); !isQuotaErr(err) {
		t.Fatalf("准入应拒绝且为 ErrInsufficientStorage,实际: %v", err)
	}
	if _, ok := e.s3.Get(qbucket, "f.bin"); ok {
		t.Error("被拒绝的写不得在桶内留残留")
	}
	w, err := e.writer("/ok.bin", 8)
	if err != nil {
		t.Fatalf("限额内准入应通过: %v", err)
	}
	if _, err := io.Copy(w, strings.NewReader("12345678")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Commit(context.Background()); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, ok := e.s3.Get(qbucket, "ok.bin"); !ok {
		t.Error("限额内提交应落桶")
	}
}

// 场景(覆盖写扣旧值):既有目标的大小从准入算术扣除;提交后窗口口径更新。
func TestQuotaOverwriteSubtractsOldValue(t *testing.T) {
	e := newEnv(t, 100)
	e.s3.Put(qbucket, "old.bin", make([]byte, 50), "")
	// 覆盖 50 字节旧对象:50(快照)− 50(旧值)+ 60(新值)= 60 ≤ 100,放行。
	w, err := e.writer("/old.bin", 60)
	if err != nil {
		t.Fatalf("覆盖写应放行: %v", err)
	}
	if _, err := io.Copy(w, bytes.NewReader(make([]byte, 60))); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Commit(context.Background()); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// 提交后窗口口径 = 60:再开 50 字节的写会超(60+50 > 100),40 放行。
	if _, err := e.writer("/x.bin", 50); !isQuotaErr(err) {
		t.Errorf("60+50>100 应被拒绝,实际: %v", err)
	}
	if w2, err := e.writer("/y.bin", 40); err != nil {
		t.Errorf("60+40≤100 应放行: %v", err)
	} else {
		w2.Abort()
		w2.Close()
	}
}

// 场景(提交点复核):COPY 无 Content-Length,超限只能在提交点发现;
// 拒绝后目标不出现在桶中,计数被正确释放,后续限内写不受影响。
func TestQuotaCommitRecheckForUnknownLength(t *testing.T) {
	e := newEnv(t, 60)
	e.s3.Put(qbucket, "src.bin", make([]byte, 50), "")
	// COPY 语义:cl = -1,准入不预留;写 50 字节后提交:50(快照)+ 50(实际)> 60。
	w, err := e.writer("/dst.bin", -1)
	if err != nil {
		t.Fatalf("未知长度准入应放行: %v", err)
	}
	if _, err := io.Copy(w, bytes.NewReader(make([]byte, 50))); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Commit(context.Background()); !isQuotaErr(err) {
		t.Fatalf("提交点复核应拒绝,实际: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.s3.Get(qbucket, "dst.bin"); ok {
		t.Error("提交点被拒的目标不得落桶")
	}
	// 复核失败后状态干净:限内写放行。
	w2, err := e.writer("/small.bin", 10)
	if err != nil {
		t.Fatalf("失败后限内写应放行: %v", err)
	}
	w2.Abort()
	w2.Close()
}

// I3:预留随 Commit/Abort/Close 三出口幂等注销;在途预留参与准入算术。
func TestQuotaReservationReleasedIdempotently(t *testing.T) {
	e := newEnv(t, 100)
	w, err := e.writer("/a.bin", 60)
	if err != nil {
		t.Fatal(err)
	}
	// 在途 60 已占用:另一笔 50 会超(0 快照 + 60 在途 + 50 > 100)。
	if _, err := e.writer("/b.bin", 50); !isQuotaErr(err) {
		t.Fatalf("在途预留应参与准入算术,实际: %v", err)
	}
	if err := w.Abort(); err != nil {
		t.Fatalf("Abort 失败: %v", err)
	}
	if err := w.Abort(); err != nil {
		t.Fatalf("重复 Abort 必须幂等: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("重复 Close 必须幂等: %v", err)
	}
	// 预留已释放且未多扣:50 重新放行。
	w2, err := e.writer("/b.bin", 50)
	if err != nil {
		t.Fatalf("预留注销后应放行: %v", err)
	}
	w2.Abort()
	w2.Close()
}

// 配额热更新:SetQuota 原子改参数,同一实例口径立即变化(不重建栈)。
func TestQuotaHotUpdate(t *testing.T) {
	e := newEnv(t, 10)
	// 提交 6 字节落桶 → 窗口口径 6。
	w, err := e.writer("/f", 6)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(w, strings.NewReader("123456")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// 旧限额 10:6+6=12 超限。
	if _, err := e.writer("/g", 6); !isQuotaErr(err) {
		t.Fatalf("旧限额下应拒绝: %v", err)
	}
	// 热更新为 20:同一 qfs 实例,6+6≤20 放行。
	e.qfs.SetQuota(20)
	if e.qfs.Limit() != 20 {
		t.Fatalf("Limit() = %d, want 20", e.qfs.Limit())
	}
	w3, err := e.writer("/g", 6)
	if err != nil {
		t.Fatalf("热更新后应放行: %v", err)
	}
	w3.Abort()
	w3.Close()
}

// 不限额(limit <= 0)任意放行,准入零成本。
func TestQuotaUnlimited(t *testing.T) {
	e := newEnv(t, 0)
	for _, cl := range []int64{1 << 20, -1} {
		w, err := e.writer("/big.bin", cl)
		if err != nil {
			t.Fatalf("不限额应放行(cl=%d): %v", cl, err)
		}
		w.Abort()
		w.Close()
	}
}
