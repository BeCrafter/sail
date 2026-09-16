package cmd

import (
	"bufio"
	"reflect"
	"strings"
	"testing"

	"github.com/BeCrafter/sail/internal/config"
)

// newReader 把逐行输入喂给向导。注意:prompt 助手忽略读取错误,
// 输入耗尽等价于回车(取默认值)—— 需要"回车"时留空串或用尽输入皆可。
func newReader(lines ...string) *bufio.Reader {
	return bufio.NewReader(strings.NewReader(strings.Join(lines, "\n") + "\n"))
}

func TestCollectServeGateDefaultNo(t *testing.T) {
	got, err := collectServeConfig(newReader("n"), config.ServeConfig{})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if !serveBlockEmpty(got) {
		t.Errorf("答 n 不应产生 serve 块,实际 %+v", got)
	}
}

func TestCollectServeSingleUser(t *testing.T) {
	in := []string{
		"y",      // 配置 serve 块
		":8443",  // listen
		"team",   // prefix
		"n",      // 单用户
		"alice",  // user
		"${APW}", // password
		"",       // tls-cert 空 → serve over HTTP
		"n",      // chunked-upload
		"",       // staging-dir 空
	}
	got, err := collectServeConfig(newReader(in...), config.ServeConfig{})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if got.Listen != ":8443" || got.Prefix != "team" {
		t.Errorf("listen/prefix 不符: %+v", got)
	}
	if got.User != "alice" || got.Password != "${APW}" {
		t.Errorf("单用户凭据不符: %+v", got)
	}
	if len(got.Users) != 0 {
		t.Errorf("单用户模式不应带 users 表: %+v", got.Users)
	}
	if got.TLSCert != "" || got.TLSKey != "" {
		t.Errorf("无证书时 TLS 应全空: %+v", got)
	}
	if got.ChunkedUpload {
		t.Errorf("chunked-upload 应为 false")
	}
}

func TestCollectServeMultiUser(t *testing.T) {
	in := []string{
		"y", "", "", // 配置;listen/prefix 留空
		"y",                               // 多用户
		"alice", "pw-a", "alice/", "10GB", // 用户 1
		"bob", "pw-b", "bob/", "", // 用户 2:quota 留空
		"", // 空名字结束
		"", // tls-cert 空
		"", // chunked-upload 默认 false
		"", // staging-dir 空
	}
	got, err := collectServeConfig(newReader(in...), config.ServeConfig{})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(got.Users) != 2 {
		t.Fatalf("应有 2 名用户: %+v", got.Users)
	}
	if got.User != "" || got.Password != "" {
		t.Errorf("多用户模式不应带单用户凭据: %+v", got)
	}
	a, b := got.Users[0], got.Users[1]
	if a.Name != "alice" || a.Password != "pw-a" || a.Prefix != "alice/" || a.Quota != "10GB" {
		t.Errorf("alice 字段不符: %+v", a)
	}
	if b.Name != "bob" || b.Password != "pw-b" || b.Prefix != "bob/" || b.Quota != "" {
		t.Errorf("bob 字段不符: %+v", b)
	}
}

// 非法候选(重名 / 生效前缀嵌套)应被丢弃并重问,合法候选正常入表。
func TestCollectServeMultiUserValidationRetry(t *testing.T) {
	in := []string{
		"y", "", "", "y", // 门/listen/prefix/多用户
		"alice", "pw-a", "alice/", "", // 用户 1
		"alice", "pw-x", "", "", // 重名 → 拒绝
		"bob", "pw-b", "", "", // 前缀空 → 会包住 alice 生效前缀 → 拒绝
		"bob", "pw-b", "bob/", "", // 合法
		"",         // 结束
		"", "", "", // tls/chunked/staging
	}
	got, err := collectServeConfig(newReader(in...), config.ServeConfig{})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(got.Users) != 2 || got.Users[0].Name != "alice" || got.Users[1].Name != "bob" {
		t.Fatalf("重试后用户表不符: %+v", got.Users)
	}
	if got.Users[1].Password != "pw-b" || got.Users[1].Prefix != "bob/" {
		t.Errorf("被拒候选混入: %+v", got.Users[1])
	}
}

// 连续 3 次非法候选 → 返回错误(防管道输入死循环)。
func TestCollectServeMultiUserAbortAfterThree(t *testing.T) {
	in := []string{
		"y", "", "", "y",
		"a", "", "", "", // 密码空 → 拒绝
		"b", "", "", "", // 拒绝
		"c", "", "", "", // 拒绝 → 第 3 次,放弃
	}
	_, err := collectServeConfig(newReader(in...), config.ServeConfig{})
	if err == nil {
		t.Fatal("3 次非法候选后期望报错")
	}
	if !strings.Contains(err.Error(), "still invalid after 3 attempts") {
		t.Errorf("错误信息不符: %v", err)
	}
}

// 多用户模式下一个用户都没加 → 回退单用户(默认 Y)。
func TestCollectServeMultiUserEmptyFallsBack(t *testing.T) {
	in := []string{
		"y", "", "", "y", // 门/listen/prefix/多用户
		"",            // 空名字,直接结束用户表
		"",            // 回退单用户?回车=Y
		"alice", "pw", // 单用户凭据
		"", "", "", // tls/chunked/staging
	}
	got, err := collectServeConfig(newReader(in...), config.ServeConfig{})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(got.Users) != 0 {
		t.Errorf("回退后不应有 users 表: %+v", got.Users)
	}
	if got.User != "alice" || got.Password != "pw" {
		t.Errorf("回退单用户凭据不符: %+v", got)
	}
}

// 已有 serve 块:回车即逐字保留(数据不丢的根治路径)。
func TestCollectServeKeepIsVerbatim(t *testing.T) {
	existing := config.ServeConfig{
		Listen: ":9443", Prefix: "team/", DirCacheTTL: "5m",
		Prewarm: []string{"/hot"},
		Users: []config.UserConfig{
			{Name: "alice", Password: "${APW}", Prefix: "alice/", Quota: "10GB"},
			{Name: "bob", Password: "pw"},
		},
		BackendMaxSize: "10TiB", MaxUploadSize: "1TiB", ChunkSize: "8GiB",
		ChunkedUpload: true, StagingDir: "/tmp/stage",
	}
	got, err := collectServeConfig(newReader(""), existing)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if !reflect.DeepEqual(got, existing) {
		t.Errorf("保留路径必须逐字返回 existing:\n got %+v\nwant %+v", got, existing)
	}
}

// 已有 serve 块:答 d 删除。
func TestCollectServeRemove(t *testing.T) {
	existing := config.ServeConfig{Users: []config.UserConfig{{Name: "alice", Password: "pw"}}}
	got, err := collectServeConfig(newReader("d"), existing)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if !serveBlockEmpty(got) {
		t.Errorf("删除后应为空块,实际 %+v", got)
	}
}

// 重新配置只覆盖被提问的字段,尺寸/缓存/预热等未提问字段逐字保留。
func TestCollectServeReconfigurePreservesUnasked(t *testing.T) {
	existing := config.ServeConfig{
		Listen: ":8443", User: "alice", Password: "pw",
		BackendMaxSize: "10TiB", MaxUploadSize: "1TiB", ChunkSize: "8GiB",
		DirCacheTTL: "5m", Prewarm: []string{"/hot"},
	}
	in := []string{
		"r", // 重新配置
		"",  // listen 回车保留
		"",  // prefix
		"",  // 单用户(默认)
		"",  // user 回车保留
		"",  // password 回车保留
		"",  // tls-cert 空
		"",  // chunked-upload 默认
		"",  // staging-dir
	}
	got, err := collectServeConfig(newReader(in...), existing)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if got.User != "alice" || got.Password != "pw" || got.Listen != ":8443" {
		t.Errorf("被提问字段应回车保留: %+v", got)
	}
	if got.BackendMaxSize != "10TiB" || got.MaxUploadSize != "1TiB" || got.ChunkSize != "8GiB" ||
		got.DirCacheTTL != "5m" || len(got.Prewarm) != 1 || got.Prewarm[0] != "/hot" {
		t.Errorf("未提问字段被改动: %+v", got)
	}
}

// 从多用户切到单用户:users 表被清空(互斥的显式选择,非静默丢弃)。
func TestCollectServeModeSwitchClearsUsers(t *testing.T) {
	existing := config.ServeConfig{
		User: "old", Password: "oldpw",
		Users: []config.UserConfig{{Name: "alice", Password: "pw"}}, // 非法态:两者同设
	}
	in := []string{
		"r",           // 重新配置
		"",            // listen
		"",            // prefix
		"n",           // 切单用户
		"alice", "pw", // 新的单用户凭据
		"", "", "", // tls/chunked/staging
	}
	got, err := collectServeConfig(newReader(in...), existing)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(got.Users) != 0 {
		t.Errorf("切单用户后 users 应清空: %+v", got.Users)
	}
	if got.User != "alice" || got.Password != "pw" {
		t.Errorf("单用户凭据不符: %+v", got)
	}
}

// 已有用户表:选 a 追加用户,其余 serve 字段逐字保留。
func TestCollectServeAppendUsers(t *testing.T) {
	existing := config.ServeConfig{
		Listen: ":8443", Prefix: "team/",
		DirCacheTTL: "5m", Prewarm: []string{"/hot"},
		Users: []config.UserConfig{
			{Name: "alice", Password: "pw-a", Prefix: "alice/", Quota: "10GB"},
		},
	}
	in := []string{
		"a",                            // 追加
		"bob", "pw-b", "bob/", "500MB", // 新用户
		"", // 结束
	}
	got, err := collectServeConfig(newReader(in...), existing)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(got.Users) != 2 || got.Users[0].Name != "alice" || got.Users[1].Name != "bob" {
		t.Fatalf("追加后用户表不符: %+v", got.Users)
	}
	if got.Users[0].Quota != "10GB" || got.Users[1].Quota != "500MB" {
		t.Errorf("十进制单位应原样保留: %+v", got.Users)
	}
	if got.Listen != ":8443" || got.Prefix != "team/" || got.DirCacheTTL != "5m" ||
		len(got.Prewarm) != 1 || got.Prewarm[0] != "/hot" {
		t.Errorf("追加不得改动其余 serve 字段: %+v", got)
	}
}

// 没有用户表时选 a:给出提示并重问,回车即保留原块。
func TestCollectServeAppendNoUsersTable(t *testing.T) {
	existing := config.ServeConfig{User: "alice", Password: "pw"}
	got, err := collectServeConfig(newReader("a", ""), existing)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if !reflect.DeepEqual(got, existing) {
		t.Errorf("无表可追加时应保持原块: %+v", got)
	}
}

// quota 就地校验:非法值只重问 quota 本身,合法后正常入表。
func TestCollectServeQuotaValidatedInline(t *testing.T) {
	in := []string{
		"y", "", "", "y", // 门/多用户
		"alice", "pw", "alice/", "10XB", // 非法单位
		"10GB",     // 只重问 quota → 合法
		"",         // 结束
		"", "", "", // tls/chunked/staging
	}
	got, err := collectServeConfig(newReader(in...), config.ServeConfig{})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(got.Users) != 1 || got.Users[0].Quota != "10GB" {
		t.Fatalf("非法 quota 应在就地重问后接受合法值: %+v", got.Users)
	}
}
