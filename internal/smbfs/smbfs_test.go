package smbfs

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/fakes3"
	"github.com/BeCrafter/sail/internal/vfs"
	"github.com/BeCrafter/sail/internal/vfs/quotafs"
	"github.com/BeCrafter/sail/internal/vfs/s3fs"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smb2 "github.com/hirochachacha/go-smb2"
)

const (
	testBucket = "b"
	testUser   = "alice"
	testPass   = "pw"
	testShare  = "sail"
)

// mountShare 起一个完整的真实端点:fakes3 后端 → s3fs 内核 → (可选配额装饰器)
// → 缓存 → SMB 壳,再用真实 SMB2 客户端挂上去。整条链路只差一个真 S3 服务,
// 故协议协商、认证、树连接、读写/列目录全都在测试覆盖内。
func mountShare(t *testing.T, ttl time.Duration, preset map[string][]byte) (*smb2.Share, *fakes3.Server) {
	t.Helper()
	sh, fake, _, _ := mountShareCore(t, ttl, preset, 0)
	return sh, fake
}

func mountShareCore(t *testing.T, ttl time.Duration, preset map[string][]byte, quota int64) (*smb2.Share, *fakes3.Server, *s3fs.FS, *Server) {
	t.Helper()
	fake := fakes3.New()
	t.Cleanup(fake.Close)
	for k, v := range preset {
		fake.Put(testBucket, k, v, "application/octet-stream")
	}
	s3c, err := client.New(context.Background(), &config.Resolved{
		Endpoint:  fake.URL(),
		AccessKey: "ak",
		SecretKey: "sk",
		Region:    "us-east-1",
		PathStyle: true,
	})
	if err != nil {
		t.Fatalf("构造 S3 client 失败: %v", err)
	}
	core, err := s3fs.New(s3fs.Config{Client: s3c, Bucket: testBucket, StagingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("构造 s3fs 失败: %v", err)
	}
	var fs vfs.FileSystem = core
	if quota > 0 {
		q := quotafs.New(core, core, quota, 0, log.New(io.Discard, "", 0))
		q.SetLabel("quotafs[" + testUser + "]")
		q.Refresh(context.Background())
		fs = q
	}
	srv, err := New(Config{
		Users:       []UserEntry{{Name: testUser, Password: testPass, FileSystem: fs}},
		Share:       testShare,
		ServerName:  "TEST",
		StagingDir:  t.TempDir(),
		DirCacheTTL: ttl,
		Logger:      log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("装配 SMB 服务失败: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- srv.serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		srv.Close()
		if err := <-errc; err != nil {
			t.Errorf("服务退出报错: %v", err)
		}
	})

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 10*time.Second)
	if err != nil {
		t.Fatalf("连接 SMB 服务失败: %v", err)
	}
	d := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{User: testUser, Password: testPass}}
	sess, err := d.Dial(conn)
	if err != nil {
		t.Fatalf("SMB 会话建立失败: %v", err)
	}
	share, err := sess.Mount(testShare)
	if err != nil {
		t.Fatalf("挂载共享失败: %v", err)
	}
	t.Cleanup(func() {
		share.Umount()
		sess.Logoff()
		conn.Close()
	})
	return share, fake, core, srv
}

// newTestClient 造一个指向 fakes3 的 S3 client(白盒测试里直接拿内核用)。
func newTestClient(t *testing.T, fake *fakes3.Server) *s3.Client {
	t.Helper()
	c, err := client.New(context.Background(), &config.Resolved{
		Endpoint: fake.URL(), AccessKey: "ak", SecretKey: "sk", Region: "us-east-1", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("构造 S3 client 失败: %v", err)
	}
	return c
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return b
}

// 写-读往返(经真实 SMB2 客户端):覆盖协商、认证、CREATE、WRITE、READ。
func TestWireWriteReadRoundTrip(t *testing.T) {
	sh, _ := mountShare(t, time.Minute, nil)

	f, err := sh.Create("round.bin")
	if err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}
	want := pattern(256<<10 + 7)
	if _, err := f.Write(want); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	got, err := sh.ReadFile("round.bin")
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if sha256.Sum256(got) != sha256.Sum256(want) {
		t.Fatalf("读回内容不一致: %d 字节 vs %d 字节", len(got), len(want))
	}
}

// 提交点在句柄关闭:库在每次 WRITE 后都会调一次 Sync,若 Sync 就提交,大文件
// 会退化成「每个分块一次全量上传」。这里用后端可见的对象长度钉住这条语义。
func TestCommitHappensOnCloseNotOnSync(t *testing.T) {
	sh, fake := mountShare(t, time.Minute, nil)

	f, err := sh.Create("commit.bin")
	if err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}
	if _, err := f.Write(pattern(64 << 10)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if obj, ok := fake.Get(testBucket, "commit.bin"); !ok || len(obj.Data) != 0 {
		t.Fatalf("句柄未关闭时后端不应有内容(库的 CREATE 会落一个 0 字节对象): ok=%v len=%d", ok, len(obj.Data))
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	if obj, ok := fake.Get(testBucket, "commit.bin"); !ok || len(obj.Data) != 64<<10 {
		t.Fatalf("关闭后后端应有完整内容: ok=%v len=%d", ok, len(obj.Data))
	}
}

// 列目录的缓存:第一次列 20 个条目会把列表与每个条目的元信息一并缓存,
// 于是紧随其后的逐条 Stat 命中缓存,TTL 内再列一次不碰后端。
//
// 这条守的是 SMB 壳存在的理由:上游库对每个条目单独调一次 Stat,没有缓存就是
// 「条目数 × 一次往返」。
func TestListingIsCached(t *testing.T) {
	preset := map[string][]byte{}
	for i := range 20 {
		preset["d/f"+string(rune('a'+i))+".txt"] = []byte("x")
	}
	sh, fake := mountShare(t, time.Minute, preset)

	if entries, err := sh.ReadDir("d"); err != nil || len(entries) != 20 {
		t.Fatalf("首次列目录失败或条目数不符: %d, %v", len(entries), err)
	}
	before := counts(fake)
	if entries, err := sh.ReadDir("d"); err != nil || len(entries) != 20 {
		t.Fatalf("再次列目录失败: %v", err)
	}
	if after := counts(fake); after != before {
		t.Fatalf("TTL 内重复列目录不应产生后端调用: before=%+v after=%+v", before, after)
	}

	// 关掉缓存作对照:同样的第二次列举必须真的打到后端。
	sh2, fake2 := mountShare(t, 0, preset)
	if _, err := sh2.ReadDir("d"); err != nil {
		t.Fatalf("对照首次列目录失败: %v", err)
	}
	b2 := counts(fake2)
	if _, err := sh2.ReadDir("d"); err != nil {
		t.Fatalf("对照再次列目录失败: %v", err)
	}
	if counts(fake2) == b2 {
		t.Fatal("关闭缓存后重复列目录仍无后端调用,缓存开关未生效")
	}
}

// 写路径的失效:落盘后同一目录的列表与条目元信息必须立刻反映新文件,
// 不能等 TTL 过期。
func TestListingInvalidatedOnWrite(t *testing.T) {
	sh, _ := mountShare(t, time.Hour, nil)

	if entries, err := sh.ReadDir(""); err != nil || len(entries) != 0 {
		t.Fatalf("空目录列取失败: %d, %v", len(entries), err)
	}
	f, err := sh.Create("new.txt")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if _, err := f.Write([]byte("hello")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	entries, err := sh.ReadDir("")
	if err != nil {
		t.Fatalf("再次列目录失败: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "new.txt" || entries[0].Size() != 5 {
		t.Fatalf("写入后目录列表未刷新: %+v", entries)
	}
	fi, err := sh.Stat("new.txt")
	if err != nil || fi.Size() != 5 {
		t.Fatalf("写入后 Stat 未刷新: %v, %v", fi, err)
	}
}

// 改名与删除:两端缓存都要失效。
func TestRenameAndRemoveInvalidate(t *testing.T) {
	sh, fake := mountShare(t, time.Hour, map[string][]byte{"old.txt": []byte("data")})

	if entries, err := sh.ReadDir(""); err != nil || len(entries) != 1 {
		t.Fatalf("前置列目录失败: %d, %v", len(entries), err)
	}
	if err := sh.Rename("old.txt", "moved.txt"); err != nil {
		t.Fatalf("改名失败: %v", err)
	}
	if _, ok := fake.Get(testBucket, "moved.txt"); !ok {
		t.Fatal("改名后目标对象不存在")
	}
	entries, err := sh.ReadDir("")
	if err != nil || len(entries) != 1 || entries[0].Name() != "moved.txt" {
		t.Fatalf("改名后目录列表未刷新: %+v, %v", entries, err)
	}
	if err := sh.Remove("moved.txt"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if entries, err := sh.ReadDir(""); err != nil || len(entries) != 0 {
		t.Fatalf("删除后目录列表未刷新: %+v, %v", entries, err)
	}
}

// 定位写是「读改写」而不是「整文件替换」:只在偏移 5 写 2 字节,前缀不能被丢掉。
// 这条同时钉住暂存层的 loadFromBackend。
func TestPositionalWriteIsReadModifyWrite(t *testing.T) {
	_, fake, core, _ := mountShareCore(t, 0, map[string][]byte{"keep.bin": []byte("AAAAAAAAAA")}, 0)
	b := newBackend(newCachedFS(core, 0, nil), t.TempDir(), log.New(io.Discard, "", 0))

	f, err := b.OpenFile("/keep.bin")
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	w, ok := f.(interface {
		WriteAt([]byte, int64) (int, error)
	})
	if !ok {
		t.Fatal("句柄未实现定位写")
	}
	if _, err := w.WriteAt([]byte("BB"), 5); err != nil {
		t.Fatalf("定位写失败: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	obj, ok := fake.Get(testBucket, "keep.bin")
	if !ok || string(obj.Data) != "AAAAABBAAA" {
		t.Fatalf("定位写应保留既有前缀: ok=%v data=%q", ok, string(obj.Data))
	}
}

// 配额超限:数据不落库 —— 这是配额要紧的那一半。
//
// 刻意不断言「客户端看到什么错误」:走 WritableFile 路径时准入在提交点做,
// 而库关闭句柄时不检查返回值,客户端因此拿不到专属错误码(它只会看到服务端
// 日志里的那行告警)。这条限制写在 README 的 SMB 限制一节里,别让测试给出
// 一个比现实更漂亮的印象。
func TestQuotaRefusalLeavesBackendUntouched(t *testing.T) {
	sh, fake, _, _ := mountShareCore(t, 0, nil, 1<<10)

	f, err := sh.Create("big.bin")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	// Create 会先落一个 0 字节对象(库的 CREATE 语义),写内容落暂存、关闭才提交。
	if _, err := f.Write(pattern(4 << 10)); err != nil {
		t.Logf("写入阶段就报了错(取决于库的路径):%v", err)
	}
	f.Close()

	if obj, ok := fake.Get(testBucket, "big.bin"); ok && len(obj.Data) != 0 {
		t.Fatalf("超配额的文件不得落库: len=%d", len(obj.Data))
	}
}

// 目录删除:非空目录不删(SMB2 语义)。客户端按自己那份可能过时的视图判断
// 「目录空了」,若壳直接走递归删除,别人刚写进去的对象会被顺手清掉。
func TestDeleteDirRefusesNonEmpty(t *testing.T) {
	_, fake, core, _ := mountShareCore(t, time.Minute, map[string][]byte{
		"d/keep.bin": []byte("must survive"),
	}, 0)
	b := newBackend(newCachedFS(core, time.Minute, log.New(io.Discard, "", 0)), t.TempDir(), log.New(io.Discard, "", 0))

	err := b.DeleteDir("/d")
	if err == nil {
		t.Fatal("非空目录应拒绝删除")
	}
	if !errors.Is(err, vfs.ErrExist) {
		t.Fatalf("拒绝理由应是 ErrExist(可判定),实际 %v", err)
	}
	if _, ok := fake.Get(testBucket, "d/keep.bin"); !ok {
		t.Fatal("被拒绝的删除不得动到子树里的对象")
	}

	// 空目录(只剩标记对象)照常可删 —— 且标记必须**真的消失**:它是目录
	// 唯一的存在形式,删不掉的话目录会在缓存过期/重新挂载后自己回来。
	if err := b.MkDir("/empty", 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := b.DeleteDir("/empty"); err != nil {
		t.Fatalf("空目录应可删: %v", err)
	}
	if obj, ok := fake.Get(testBucket, "empty/"); ok {
		t.Fatalf("删空目录后标记对象仍在(key=empty/ len=%d):目录会自己回来", len(obj.Data))
	}
}

// 删除空目录走真实客户端:库在关闭句柄时调 DeleteDir,链路上任何一环把标记
// 对象漏掉,用户看到的就是「删掉的文件夹过一会儿又出现」。
func TestClientRmdirRemovesMarker(t *testing.T) {
	sh, fake := mountShare(t, time.Minute, nil)

	if err := sh.Mkdir("dir1", 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if _, ok := fake.Get(testBucket, "dir1/"); !ok {
		t.Fatal("前置:目录标记对象 dir1/ 应存在")
	}
	if err := sh.Remove("dir1"); err != nil {
		t.Fatalf("客户端删除目录失败: %v", err)
	}
	if obj, ok := fake.Get(testBucket, "dir1/"); ok {
		t.Fatalf("客户端删除目录后标记对象仍在(key=dir1/ len=%d)", len(obj.Data))
	}
	if keys := fake.Keys(testBucket); len(keys) != 0 {
		t.Fatalf("删除后桶内应已空,实际 %v", keys)
	}
}

// 多用户:每个用户一个共享,且只能挂自己的那些。
func TestMultiUserSharesAreIsolated(t *testing.T) {
	fake := fakes3.New()
	t.Cleanup(fake.Close)
	s3c, err := client.New(context.Background(), &config.Resolved{
		Endpoint: fake.URL(), AccessKey: "ak", SecretKey: "sk", Region: "us-east-1", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("构造 S3 client 失败: %v", err)
	}
	mk := func(prefix string) vfs.FileSystem {
		fs, err := s3fs.New(s3fs.Config{Client: s3c, Bucket: testBucket, Prefix: prefix, StagingDir: t.TempDir()})
		if err != nil {
			t.Fatalf("构造 s3fs 失败: %v", err)
		}
		return fs
	}
	srv, err := New(Config{
		Users: []UserEntry{
			{Name: "alice", Password: "pw-a", FileSystem: mk("a")},
			{Name: "bob", Password: "pw-b", FileSystem: mk("b")},
		},
		ServerName: "TEST",
		StagingDir: t.TempDir(),
		Logger:     log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	if got := srv.Shares(); len(got) != 2 || got[0].Name != "alice" || got[1].Name != "bob" {
		t.Fatalf("多用户共享名应为用户名: %+v", got)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- srv.serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		srv.Close()
		<-errc
	})

	dial := func(user, pass, share string) (*smb2.Session, *smb2.Share, error) {
		conn, err := net.DialTimeout("tcp", ln.Addr().String(), 10*time.Second)
		if err != nil {
			return nil, nil, err
		}
		d := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{User: user, Password: pass}}
		sess, err := d.Dial(conn)
		if err != nil {
			conn.Close()
			return nil, nil, err
		}
		sh, err := sess.Mount(share)
		if err != nil {
			sess.Logoff()
			conn.Close()
			return nil, nil, err
		}
		t.Cleanup(func() { sh.Umount(); sess.Logoff(); conn.Close() })
		return sess, sh, nil
	}

	_, ash, err := dial("alice", "pw-a", "alice")
	if err != nil {
		t.Fatalf("alice 挂载自己的共享失败: %v", err)
	}
	f, err := ash.Create("mine.txt")
	if err != nil {
		t.Fatalf("alice 写入失败: %v", err)
	}
	f.Write([]byte("a"))
	f.Close()
	if _, ok := fake.Get(testBucket, "a/mine.txt"); !ok {
		t.Fatal("alice 的写入未落在自己的前缀下")
	}

	if _, _, err := dial("bob", "pw-b", "alice"); err == nil {
		t.Fatal("bob 不应能挂载 alice 的共享")
	}
}

// 共享名/用户名校验:多用户下共享名即用户名,非法字符必须启动即报错。
func TestShareNameValidation(t *testing.T) {
	cases := []struct {
		name  string
		users []UserEntry
		share string
		want  string
	}{
		{"多用户用户名带分隔符", []UserEntry{{Name: "a/b"}, {Name: "c"}}, "sail", "path separator"},
		{"多用户用户名重复映射", []UserEntry{{Name: "a"}, {Name: "A"}}, "sail", "share names must differ"},
		{"保留名", []UserEntry{{Name: "alice"}}, "IPC$", "IPC$"},
		{"默认端口写法", []UserEntry{{Name: "alice"}}, "sail:1445", "contains \":\""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := New(Config{Users: c.users, Share: c.share, Logger: log.New(io.Discard, "", 0)})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("期望报错含 %q,实际 %v", c.want, err)
			}
		})
	}
}

// cacheKey 归一化:内核列目录给出的子项路径不带尾斜杠,请求可能带,
// 不归一化就永远命不中缓存。
func TestCacheKeyNormalization(t *testing.T) {
	cases := map[string]string{
		"/image":    "/image",
		"/image/":   "/image",
		"image":     "/image",
		"":          "/",
		"/":         "/",
		"/a/b/../c": "/a/c",
	}
	for in, want := range cases {
		if got := cacheKey(in); got != want {
			t.Errorf("cacheKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// 无效共享名之外,还要挡住「没有用户」的空配置。
func TestNewRejectsEmptyUserTable(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("没有用户时应拒绝装配")
	}
}

// counts 把后端请求计数收敛成一个可比较的值(测试只关心「有没有打后端」)。
func counts(f *fakes3.Server) [4]int64 {
	return [4]int64{f.Counts.Head.Load(), f.Counts.Get.Load(), f.Counts.List.Load(), f.Counts.Copy.Load()}
}

// 缓存的内存上界。目录列表与条目元信息都是驻留内存的,而 files 表是**按条目**
// 写入的 —— 列过什么就是个只增不减的集合,长时间跑在大桶上不能由着它涨。
// 这里把预算压到最小,验证淘汰真的发生、记账与实际内容一致、LRU 表不漏元素。
func TestCacheStaysWithinBudget(t *testing.T) {
	// core 传 nil:store 只写缓存,不触达后端。
	c := newCachedFS(nil, time.Minute, log.New(io.Discard, "", 0))
	c.maxDirs = 4
	c.maxBytes = 8 * fileInfoCost

	for i := range 50 {
		dir := fmt.Sprintf("/d%02d", i)
		entries := make([]vfs.FileInfo, 3)
		for j := range entries {
			entries[j] = vfs.FileInfo{
				Name: fmt.Sprintf("f%d", j),
				Path: fmt.Sprintf("%s/f%d", dir, j),
				Size: int64(j),
			}
		}
		c.store(dir, entries)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.dirs) > c.maxDirs {
		t.Fatalf("目录缓存超出预算: %d > %d", len(c.dirs), c.maxDirs)
	}
	if c.bytes > c.maxBytes {
		t.Fatalf("缓存占用超出预算: %d > %d", c.bytes, c.maxBytes)
	}
	// 记账必须与实际内容对得上:多算会让缓存提前失效,少算就是内存只涨不落。
	var want int64
	for _, d := range c.dirs {
		want += d.bytes
	}
	for _, f := range c.files {
		want += f.bytes
	}
	if want != c.bytes {
		t.Fatalf("预算记账与实际内容不符: 记账 %d,实际 %d", c.bytes, want)
	}
	// LRU 表漏元素 = 淘汰够不着它,那部分内存就再也降不下来。
	if got := c.lru.Len(); got != len(c.dirs)+len(c.files) {
		t.Fatalf("LRU 元素数 %d,缓存条目数 %d", got, len(c.dirs)+len(c.files))
	}
}

// entries 数不超过预算时不该淘汰:淘汰只该在超预算时发生。
func TestCacheKeepsEverythingUnderBudget(t *testing.T) {
	c := newCachedFS(nil, time.Minute, log.New(io.Discard, "", 0))
	for i := range 20 {
		dir := fmt.Sprintf("/d%02d", i)
		c.store(dir, []vfs.FileInfo{{Name: "f", Path: dir + "/f"}})
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.dirs) != 20 {
		t.Fatalf("预算内不该淘汰: 期望 20 个目录,实际 %d", len(c.dirs))
	}
}

// invalidate 的作用域:写某个文件只让它自己与直接父目录失效 —— 根目录的列举
// 是整棵树里最贵的一次,每写一个文件都作废它,缓存就白做了。
func TestInvalidateScopeIsTheTouchedBranch(t *testing.T) {
	sh, fake := mountShare(t, time.Minute, map[string][]byte{
		"d/x.bin": []byte("x"),
		"d/y.bin": []byte("y"),
	})
	_ = sh

	core, err := s3fs.New(s3fs.Config{
		Client:     newTestClient(t, fake),
		Bucket:     testBucket,
		StagingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("构造 s3fs 失败: %v", err)
	}
	c := newCachedFS(core, time.Minute, log.New(io.Discard, "", 0))
	ctx := context.Background()
	for _, p := range []string{"/", "/d"} {
		if _, err := c.ReadDir(ctx, p); err != nil {
			t.Fatalf("预热 %s 失败: %v", p, err)
		}
	}

	before := counts(fake)
	c.invalidate("/d/x.bin", false)
	if _, err := c.ReadDir(ctx, "/d"); err != nil {
		t.Fatalf("列 /d 失败: %v", err)
	}
	if counts(fake) == before {
		t.Fatal("直接父目录应失效并因此重新列举")
	}
	after := counts(fake)
	if _, err := c.ReadDir(ctx, "/"); err != nil {
		t.Fatalf("列 / 失败: %v", err)
	}
	if counts(fake) != after {
		t.Fatal("根目录不该被一次普通写入作废")
	}
}
