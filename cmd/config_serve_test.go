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
		"webdav", // 服务:只配 WebDAV
		"team",   // prefix(通用)
		"single", // 认证模式:单用户
		"alice",  // user
		"${APW}", // password
		"n",      // chunked-upload
		"",       // staging-dir 空
		":8443",  // WebDAV listen
		"",       // tls-cert 空 → serve over HTTP
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
		"y", "webdav", "", "multi", // 配置;服务 webdav;prefix 留空;多用户
		"alice", "pw-a", "alice/", "10GB", // 用户 1
		"bob", "pw-b", "bob/", "", // 用户 2:quota 留空
		"", // 空名字结束
		"", // chunked-upload 默认 false
		"", // staging-dir 空
		"", // WebDAV listen 留空
		"", // tls-cert 空
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
		"y", "webdav", "", "multi", // 门/服务/prefix/多用户
		"alice", "pw-a", "alice/", "", // 用户 1
		"alice", "pw-x", "", "", // 重名 → 拒绝
		"bob", "pw-b", "", "", // 前缀空 → 会包住 alice 生效前缀 → 拒绝
		"bob", "pw-b", "bob/", "", // 合法
		"",             // 结束
		"", "", "", "", // chunked/staging/listen/tls
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
		"y", "webdav", "", "multi",
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
		"y", "webdav", "", "multi", // 门/服务/prefix/多用户
		"",            // 空名字,直接结束用户表
		"",            // 回退单用户?回车=Y
		"alice", "pw", // 单用户凭据
		"", "", "", "", // chunked/staging/listen/tls
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
		SMB: config.SMBConfig{Listen: ":2445", Share: "team-share", ServerName: "SAIL-TEST"},
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

// 重新配置只覆盖被提问的字段,尺寸/缓存/预热/未选中协议的字段逐字保留。
func TestCollectServeReconfigurePreservesUnasked(t *testing.T) {
	existing := config.ServeConfig{
		Listen: ":8443", User: "alice", Password: "pw",
		BackendMaxSize: "10TiB", MaxUploadSize: "1TiB", ChunkSize: "8GiB",
		DirCacheTTL: "5m", Prewarm: []string{"/hot"},
		SMB: config.SMBConfig{Listen: ":2445", Share: "team-share", ServerName: "SAIL-TEST"},
	}
	in := []string{
		"r",      // 重新配置
		"webdav", // 服务:只配 WebDAV(SMB 字段应原样保留)
		"",       // prefix
		"single", // 单用户
		"",       // user 回车保留
		"",       // password 回车保留
		"",       // chunked-upload 默认
		"",       // staging-dir
		"",       // WebDAV listen 回车保留
		"",       // tls-cert 空
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
	if got.SMB != existing.SMB {
		t.Errorf("未选中协议的字段被改动: %+v", got.SMB)
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
		"webdav",      // 服务
		"",            // prefix
		"single",      // 切单用户
		"alice", "pw", // 新的单用户凭据
		"", "", "", "", // chunked/staging/listen/tls
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
		"y", "webdav", "", "multi", // 门/服务/多用户
		"alice", "pw", "alice/", "10XB", // 非法单位
		"10GB",         // 只重问 quota → 合法
		"",             // 结束
		"", "", "", "", // chunked/staging/listen/tls
	}
	got, err := collectServeConfig(newReader(in...), config.ServeConfig{})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(got.Users) != 1 || got.Users[0].Quota != "10GB" {
		t.Fatalf("非法 quota 应在就地重问后接受合法值: %+v", got.Users)
	}
}

func TestParseProtocols(t *testing.T) {
	ok := []struct {
		in   string
		want serveProtocols
	}{
		{"webdav", serveProtocols{webdav: true}},
		{"WEBDAV", serveProtocols{webdav: true}},
		{" w ", serveProtocols{webdav: true}},
		{"smb", serveProtocols{smb: true}},
		{"S", serveProtocols{smb: true}},
		{"both", serveProtocols{webdav: true, smb: true}},
		{"all", serveProtocols{webdav: true, smb: true}},
		{"smb+webdav", serveProtocols{webdav: true, smb: true}},
	}
	for _, c := range ok {
		got, recognized := parseProtocols(c.in)
		if !recognized || got != c.want {
			t.Errorf("%q 应解析为 %+v,得到 %+v(识别=%v)", c.in, c.want, got, recognized)
		}
	}
	for _, bad := range []string{"", "ftp", "webdav smb", "y", "n", "1"} {
		if _, recognized := parseProtocols(bad); recognized {
			t.Errorf("%q 不该被识别为协议选择", bad)
		}
	}
}

// 默认协议只认协议专属字段:serve.listen/tls-* 属 WebDAV,serve.smb.* 属 SMB;
// 只配共享字段或全新配置时默认 WebDAV(延续向导既有行为)。
func TestDefaultProtocols(t *testing.T) {
	both := serveProtocols{webdav: true, smb: true}
	webdavOnly := serveProtocols{webdav: true}
	smbOnly := serveProtocols{smb: true}
	cases := []struct {
		name     string
		existing config.ServeConfig
		want     serveProtocols
	}{
		{"全新", config.ServeConfig{}, webdavOnly},
		{"只配 WebDAV(IPv6 省略)", config.ServeConfig{Listen: ":8443"}, webdavOnly},
		{"只配 TLS 也算 WebDAV", config.ServeConfig{TLSCert: "/c.pem", TLSKey: "/k.pem"}, webdavOnly},
		{"只配 SMB", config.ServeConfig{SMB: config.SMBConfig{Listen: ":2445"}}, smbOnly},
		{"两者都有", config.ServeConfig{Listen: ":8443", SMB: config.SMBConfig{Share: "team"}}, both},
		// 共享字段不表态:用户表/前缀是两种协议共用的,不能据此推断
		{"只配共享字段", config.ServeConfig{Prefix: "team/", Users: []config.UserConfig{{Name: "a"}}}, webdavOnly},
	}
	for _, c := range cases {
		if got := defaultProtocols(c.existing); got != c.want {
			t.Errorf("%s: 默认协议应为 %+v,得到 %+v", c.name, c.want, got)
		}
	}
}

// 只配 SMB:不碰 WebDAV 字段,SMB 三字段落库。
func TestCollectServeSMBOnly(t *testing.T) {
	in := []string{
		"y",      // 配置 serve 块
		"smb",    // 服务:只配 SMB
		"",       // prefix(通用)
		"single", // 认证模式
		"alice", "pw",
		"", "", // chunked-upload / staging-dir
		":2445",      // smb listen
		"team-share", // share
		"SAIL-TEST",  // server-name
	}
	got, err := collectServeConfig(newReader(in...), config.ServeConfig{})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if got.Listen != "" || got.TLSCert != "" {
		t.Errorf("未选 WebDAV 时不该问出 listen/tls: %+v", got)
	}
	if got.User != "alice" || got.Password != "pw" {
		t.Errorf("凭据是共享字段,应照样问: %+v", got)
	}
	if got.SMB.Listen != ":2445" || got.SMB.Share != "team-share" || got.SMB.ServerName != "SAIL-TEST" {
		t.Errorf("SMB 字段不符: %+v", got.SMB)
	}
}

// 选 both:共享字段只问一遍,两种协议的专属字段都问到。
func TestCollectServeBothProtocols(t *testing.T) {
	in := []string{
		"y", "both",
		"team",   // prefix(通用)
		"single", // 认证模式
		"alice", "pw",
		"", "", // chunked-upload / staging-dir
		":8443", // WebDAV listen
		"",      // tls-cert 空
		":2445", // smb listen
		"team-share",
		"SAIL-TEST",
	}
	got, err := collectServeConfig(newReader(in...), config.ServeConfig{})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if got.Listen != ":8443" || got.Prefix != "team" || got.User != "alice" {
		t.Errorf("WebDAV/共享字段不符: %+v", got)
	}
	if got.SMB.Listen != ":2445" || got.SMB.Share != "team-share" || got.SMB.ServerName != "SAIL-TEST" {
		t.Errorf("SMB 字段不符: %+v", got.SMB)
	}
}

// 未选中的协议只跳过提问,绝不清空已有值(两协议不是互斥关系)。
func TestCollectServeUnselectedProtocolKeepsValues(t *testing.T) {
	existing := config.ServeConfig{
		Listen: ":8443", TLSCert: "/c.pem", TLSKey: "/k.pem",
		User: "alice", Password: "pw",
		SMB: config.SMBConfig{Listen: ":2445", Share: "team-share", ServerName: "SAIL-TEST"},
	}
	in := []string{
		"r",      // 重新配置
		"smb",    // 只配 SMB
		"",       // prefix
		"single", // 单用户
		"", "",   // user / password 回车保留
		"", "", // chunked-upload / staging-dir
		"", "", "", // smb listen / share / server-name 回车保留
	}
	got, err := collectServeConfig(newReader(in...), existing)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if got.Listen != ":8443" || got.TLSCert != "/c.pem" || got.TLSKey != "/k.pem" {
		t.Errorf("未选中 WebDAV 时其字段应原样保留: %+v", got)
	}
	if got.SMB != existing.SMB {
		t.Errorf("SMB 字段应回车保留: %+v", got.SMB)
	}
}

// 反向:只配 WebDAV 时 SMB 字段原样保留。
func TestCollectServeUnselectedSMBKeepsValues(t *testing.T) {
	existing := config.ServeConfig{
		Listen: ":8443", User: "alice", Password: "pw",
		SMB: config.SMBConfig{Listen: ":2445", Share: "team-share", ServerName: "SAIL-TEST"},
	}
	in := []string{
		"r",      // 重新配置
		"webdav", // 只配 WebDAV
		"",       // prefix
		"single", // 单用户
		"", "",   // user / password 回车保留
		"", "", // chunked-upload / staging-dir
		"", // WebDAV listen 回车保留
		"", // tls-cert 空
	}
	got, err := collectServeConfig(newReader(in...), existing)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if got.SMB != existing.SMB {
		t.Errorf("未选中 SMB 时其字段应原样保留: %+v", got.SMB)
	}
	if got.Listen != ":8443" || got.User != "alice" {
		t.Errorf("WebDAV 字段应回车保留: %+v", got)
	}
}

// share 含路径分隔符时就地重问(镜像 mergeServeSMB 的规则)。
func TestPromptSMBShareRejectsSeparator(t *testing.T) {
	if got := promptSMBShare(newReader("bad/share", `also\bad`, "good-share"), ""); got != "good-share" {
		t.Errorf("非法 share 应被拒绝并重问,得到 %q", got)
	}
	// 输入耗尽 → 取默认,不产生非法值
	if got := promptSMBShare(newReader("bad/share"), "def-share"); got != "def-share" {
		t.Errorf("连续非法应回退默认值,得到 %q", got)
	}
}

func TestParseAuthMode(t *testing.T) {
	for _, in := range []string{"single", "SINGLE", " s ", "s", "1", "global", "g", "one"} {
		if m, ok := parseAuthMode(in); !ok || m != authSingle {
			t.Errorf("%q 应解析为 single,得到 %v(识别=%v)", in, m, ok)
		}
	}
	for _, in := range []string{"multi", "Multi", "m", "2", "users", "u"} {
		if m, ok := parseAuthMode(in); !ok || m != authMulti {
			t.Errorf("%q 应解析为 multi,得到 %v(识别=%v)", in, m, ok)
		}
	}
	// 旧的 y/n 语义已废弃(用户已确认不兼容):y/n 应被当成未识别而重问
	for _, bad := range []string{"", "y", "n", "both", "webdav", "0"} {
		if _, ok := parseAuthMode(bad); ok {
			t.Errorf("%q 不该被识别为认证模式", bad)
		}
	}
}

// 具名选择:未识别的回答重问;连续未识别后回退默认。
func TestPromptAuthMode(t *testing.T) {
	if got := promptAuthMode(newReader("y", "multi"), authSingle); got != authMulti {
		t.Errorf("重问后应接受 multi,得到 %v", got)
	}
	if got := promptAuthMode(newReader("y", "n", "x"), authMulti); got != authMulti {
		t.Errorf("连续未识别应回退默认 multi,得到 %v", got)
	}
}

// 三层顺序:通用(前缀/认证/分片/暂存)→ WebDAV(listen/TLS)→ SMB(listen/share/
// server-name),且共享字段选 both 时也只问一遍。输入按该顺序喂,任一层错位都会
// 让字段落到别处,断言随即失败。
func TestCollectServeSectionOrder(t *testing.T) {
	in := []string{
		"y", "both",
		"team",                             // 通用:前缀
		"multi",                            // 通用:认证模式
		"alice", "pw", "alice/", "1GB", "", // 通用:用户表(空名字结束)
		"y",          // 通用:chunked-upload
		"/tmp/stage", // 通用:staging-dir
		":8443",      // WebDAV:listen
		"/c.pem",     // WebDAV:tls-cert
		"/k.pem",     // WebDAV:tls-key
		":2445",      // SMB:listen
		"team-share", // SMB:share
		"SAIL-TEST",  // SMB:server-name
	}
	got, err := collectServeConfig(newReader(in...), config.ServeConfig{})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if got.Prefix != "team" || got.ChunkedUpload != true || got.StagingDir != "/tmp/stage" {
		t.Errorf("通用层字段不符: %+v", got)
	}
	if len(got.Users) != 1 || got.Users[0].Name != "alice" || got.Users[0].Quota != "1GB" {
		t.Errorf("通用层用户表不符: %+v", got.Users)
	}
	if got.Listen != ":8443" || got.TLSCert != "/c.pem" || got.TLSKey != "/k.pem" {
		t.Errorf("WebDAV 层字段不符: %+v", got)
	}
	if got.SMB.Listen != ":2445" || got.SMB.Share != "team-share" || got.SMB.ServerName != "SAIL-TEST" {
		t.Errorf("SMB 层字段不符: %+v", got.SMB)
	}
}

// 分层摘要:只输出有内容的层;shared 行带上向导不问但会保留的字段;绝不显示密码。
func TestServeSummaryLines(t *testing.T) {
	if lines := serveSummaryLines(config.ServeConfig{}); lines != nil {
		t.Errorf("空块应返回 nil(由调用方给一行未配置提示),得到 %v", lines)
	}

	s := config.ServeConfig{
		Prefix: "team", User: "alice", Password: "S3CRET-PW-XYZ",
		ChunkedUpload: true, StagingDir: "/tmp/stage",
		BackendMaxSize: "5TiB", DirCacheTTL: "60s", Prewarm: []string{"/hot", "/cold"},
		Listen: ":8443", TLSCert: "/c.pem",
		SMB: config.SMBConfig{Listen: ":2445", Share: "team-share", ServerName: "SAIL-TEST"},
	}
	lines := serveSummaryLines(s)
	if len(lines) != 3 {
		t.Fatalf("应输出 shared/webdav/smb 三层,得到 %d 行: %v", len(lines), lines)
	}
	for i, layer := range []string{"shared:", "webdav:", "smb:"} {
		if !strings.HasPrefix(lines[i], layer) {
			t.Errorf("第 %d 层应以 %q 开头: %q", i, layer, lines[i])
		}
		if lines[i][len(layer)] != ' ' {
			t.Errorf("第 %d 层的层名应对齐到同一列: %q", i, lines[i])
		}
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"prefix=team", "single user alice", "chunked=on", "staging=/tmp/stage",
		"max-object=5TiB", "dir-cache-ttl=60s", "prewarm=/hot,/cold",
		"listen=:8443", "tls", "share=team-share", "server-name=SAIL-TEST",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("摘要应含 %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "S3CRET-PW-XYZ") {
		t.Errorf("摘要不得显示密码:\n%s", joined)
	}

	// 只配 SMB:webdav 层整层不出现在摘要里
	lines = serveSummaryLines(config.ServeConfig{SMB: config.SMBConfig{Listen: ":2445"}})
	for _, l := range lines {
		if strings.HasPrefix(l, "webdav:") {
			t.Errorf("未配置的层不该输出: %v", lines)
		}
	}
	if len(lines) != 2 {
		t.Errorf("只配 SMB 时应是 shared+smb 两层,得到 %v", lines)
	}

	// 无凭据时明确提示 serve 会拒绝启动
	lines = serveSummaryLines(config.ServeConfig{Listen: ":8443"})
	if !strings.Contains(strings.Join(lines, "\n"), "no credentials") {
		t.Errorf("缺凭据应提示: %v", lines)
	}
}
