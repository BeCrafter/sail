package cmd

import (
	"os"
	"strings"
	"testing"

	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/i18n"
)

// TestSetupSummary 校验写盘后的配置摘要:
// 留空密钥显示派生变量名与 export 指引;明文密钥不回显值(安全);空字段标注。
func TestSetupSummary(t *testing.T) {
	i18n.SetLang(i18n.Zh)
	defer i18n.SetLang(i18n.En)
	// 密钥留空:含变量名、(需先 export)、export 指引与未设置后果
	out := setupSummary("prod", true, config.Profile{Endpoint: "https://s3.example.com", Bucket: "b1"})
	for _, want := range []string{
		"profile: prod (默认)",
		"SAIL_PROD_ACCESS_KEY",
		"(需先 export)",
		"export SAIL_PROD_ACCESS_KEY=",
		"export SAIL_PROD_SECRET_KEY=",
		`缺少 access-key/secret-key`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("留空密钥的摘要应包含 %q:\n%s", want, out)
		}
	}

	// 明文密钥:显示"已填写",不得回显密钥值
	out = setupSummary("prod", false, config.Profile{Endpoint: "https://s3.example.com", AccessKey: "AK-SECRET-123", SecretKey: "SK-SECRET-456"})
	if !strings.Contains(out, "已填写") {
		t.Errorf("明文密钥应显示已填写:\n%s", out)
	}
	if strings.Contains(out, "AK-SECRET-123") || strings.Contains(out, "SK-SECRET-456") {
		t.Errorf("摘要不得回显明文密钥:\n%s", out)
	}
	if strings.Contains(out, "(默认)") {
		t.Errorf("isDefault=false 不应带 (默认):\n%s", out)
	}

	// 空字段标注 (未填
	out = setupSummary("prod", false, config.Profile{})
	if !strings.Contains(out, "(未填") {
		t.Errorf("空字段应标注 (未填):\n%s", out)
	}

	// 仅 sk 留空:export 指引只含 SECRET_KEY
	out = setupSummary("prod", false, config.Profile{Endpoint: "https://s3.example.com", AccessKey: "ak"})
	if strings.Contains(out, "export SAIL_PROD_ACCESS_KEY=") {
		t.Errorf("仅 sk 留空时 export 指引不应含 ACCESS_KEY:\n%s", out)
	}
	if !strings.Contains(out, "export SAIL_PROD_SECRET_KEY=") {
		t.Errorf("仅 sk 留空时 export 指引应含 SECRET_KEY:\n%s", out)
	}
}

// TestRenderProfileEnvPlaceholder 校验 ak/sk 留空时按 profile 派生占位符,
// 多个 profile 的占位符互不相同(不再共享同一组环境变量)。
func TestRenderProfileEnvPlaceholder(t *testing.T) {
	empty := config.Profile{Endpoint: "https://s3.example.com", PathStyle: true}

	out := renderProfile("test", empty)
	if !strings.Contains(out, "access-key: ${SAIL_TEST_ACCESS_KEY}") || !strings.Contains(out, "secret-key: ${SAIL_TEST_SECRET_KEY}") {
		t.Errorf("test 留空应写派生占位符:\n%s", out)
	}
	if out = renderProfile("prod", empty); !strings.Contains(out, "${SAIL_PROD_ACCESS_KEY}") {
		t.Errorf("prod 留空应写 ${SAIL_PROD_ACCESS_KEY}:\n%s", out)
	}
	if out = renderProfile("staging-eu", empty); !strings.Contains(out, "${SAIL_STAGING_EU_ACCESS_KEY}") {
		t.Errorf("staging-eu 留空应写 ${SAIL_STAGING_EU_ACCESS_KEY}:\n%s", out)
	}
	// 核心回归:prod 块不得引用 test 的占位符
	if out = renderProfile("prod", empty); strings.Contains(out, "${SAIL_TEST_ACCESS_KEY}") {
		t.Errorf("prod 不应引用 test 的占位符:\n%s", out)
	}
}

// TestRenderConfigFile 校验多 profile 渲染:保留所有 profile、default-profile、
// 且 cdn-bucket-path 仅在配置了 cdn-domain 的 profile 中出现。
func TestRenderConfigFile(t *testing.T) {
	cfg := &config.Config{
		DefaultProfile: "prod",
		Profiles: map[string]config.Profile{
			"prod": {
				Endpoint: "https://s3.example.com", AccessKey: "ak", SecretKey: "sk",
				Bucket: "b1", Region: "us-east-1", PathStyle: true,
				CDNDomain: "",
			},
			"test": {
				Endpoint: "https://s3.example.com", AccessKey: "ak", SecretKey: "sk",
				Bucket: "b2", Region: "us-east-1", PathStyle: true,
				CDNDomain: "https://c.example.com", CDNBucketPath: ptr(false),
			},
		},
	}
	out := renderConfigFile(cfg)
	if !strings.Contains(out, "default-profile: prod") {
		t.Errorf("应输出 default-profile: prod:\n%s", out)
	}
	if !strings.Contains(out, "  prod:") {
		t.Errorf("应输出 prod 块:\n%s", out)
	}
	if !strings.Contains(out, "  test:") {
		t.Errorf("应输出 test 块:\n%s", out)
	}
	// prod 无 cdn-domain,test 有且显式 false → 全文件仅一处 cdn-bucket-path
	if got := strings.Count(out, "cdn-bucket-path"); got != 1 {
		t.Errorf("cdn-bucket-path 应恰好出现 1 次(仅 test 块),实际 %d:\n%s", got, out)
	}
	if !strings.Contains(out, "cdn-bucket-path: false  #") {
		t.Errorf("test 块应输出显式 cdn-bucket-path: false 并带说明:\n%s", out)
	}
	if !strings.Contains(out, `bucket: b1`) || !strings.Contains(out, `bucket: b2`) {
		t.Errorf("应保留各 profile 的 bucket 值:\n%s", out)
	}
}

// TestRenderConfigFileCDNBucketPath 校验 cdn-bucket-path 三态渲染:
// nil(注释+自动检测)、true/false(显式值)。
func TestRenderConfigFileCDNBucketPath(t *testing.T) {
	render := func(bucketPath *bool) string {
		cfg := &config.Config{
			DefaultProfile: "prod",
			Profiles: map[string]config.Profile{
				"prod": {
					Endpoint: "https://s3.example.com", AccessKey: "ak", SecretKey: "sk",
					Bucket: "b", Region: "us-east-1", PathStyle: true,
					CDNDomain: "https://cdn.example.com", CDNBucketPath: bucketPath,
				},
			},
		}
		return renderConfigFile(cfg)
	}

	// nil(自动检测):注释行 + 说明
	if out := render(nil); !strings.Contains(out, "# cdn-bucket-path: false") || !strings.Contains(out, "auto-detect") {
		t.Errorf("nil 应输出注释行(含说明):\n%s", out)
	}

	// true:显式值 + 说明注释
	out := render(ptr(true))
	if !strings.Contains(out, "cdn-bucket-path: true  #") {
		t.Errorf("true 应输出 cdn-bucket-path: true 并带说明:\n%s", out)
	}

	// false:显式值 + 说明注释
	out = render(ptr(false))
	if !strings.Contains(out, "cdn-bucket-path: false  #") {
		t.Errorf("false 应输出 cdn-bucket-path: false 并带说明:\n%s", out)
	}

	// 说明注释应解释用途(避免用户不理解)
	for _, want := range []string{"do not append", "always append"} {
		if !strings.Contains(out, want) {
			t.Errorf("说明注释应包含 %q:\n%s", want, out)
		}
	}
}

// TestRenderConfigFileLang 校验 lang 字段按配置输出。
func TestRenderConfigFileLang(t *testing.T) {
	cfg := &config.Config{
		DefaultProfile: "prod",
		Lang:           "zh",
		Profiles: map[string]config.Profile{
			"prod": {
				Endpoint: "https://s3.example.com", AccessKey: "ak", SecretKey: "sk",
				Bucket: "b", Region: "us-east-1", PathStyle: true,
			},
		},
	}
	out := renderConfigFile(cfg)
	if !strings.Contains(out, "lang: zh") {
		t.Errorf("lang: zh 应输出:\n%s", out)
	}
	cfg.Lang = ""
	out = renderConfigFile(cfg)
	if strings.Contains(out, "lang:") {
		t.Errorf("lang 为空时不应输出 lang 行:\n%s", out)
	}
}

// yamlNeedsQuote 的判定表。前半是裸写真会出事的值(必须加引号),后半是常见
// 的安全值(必须保持裸写,否则「只在必要时加引号」就名存实亡)。
func TestYAMLNeedsQuote(t *testing.T) {
	mustQuote := []string{
		"",           // 裸写成 null
		"pw ", " pw", // 首尾空白被吃掉
		"a\tb", "a\nb", // 控制字符
		"my #secret", "a #b", // " #" 之后被当注释截断
		"a: b",                   // ": " 变嵌套映射
		"[::1]:8443", "[x", "{x", // flow 集合
		"*alice", "&alice", "!tag", "|p", ">p", "%p", "@a", "`a", "#h", ",c", "]x", "}x",
		"'sq", `has"quote`,
		"- dash", "? q", ":8443", ":", "-", "?", "prod:",
		"true", "false", "yes", "no", "on", "off", "null", "~", "y", "n",
		"123", "1.5", "-2", ".inf",
	}
	for _, v := range mustQuote {
		if !yamlNeedsQuote(v) {
			t.Errorf("%q 裸写不安全,应加引号", v)
		}
	}
	safeRaw := []string{
		"alice", "bob/", "team", "/yiche", "us-east-1", "prod",
		"https://s3.example.com", "bucket-a", "SAIL-TEST",
		"${SAIL_PROD_ACCESS_KEY}", "${ALICE_PW}",
		"a#b", "team#1", // "#" 前无空格:是普通字符,不是注释
		"my:prod", "p#ssword", "p:ss", "C:\\tmp", "中文值", "10GB", "60s", "4GiB",
	}
	for _, v := range safeRaw {
		if yamlNeedsQuote(v) {
			t.Errorf("%q 可以裸写,不该加引号", v)
		}
	}
}

// TestRenderScalarsRoundtrip 是引号策略的核心回归:向导渲染出的配置必须能被
// sail 自己逐字读回。修复前 endpoint/access-key/serve.* 等 14 个标量是裸拼的,
// 值含 "["(如 IPv6 监听地址)会让整份 YAML 不可解析,含 " #" 会被静默截断。
func TestRenderScalarsRoundtrip(t *testing.T) {
	values := []string{
		"[::1]:8443", "*alice", "&alice", "my #secret", "pw ", " pw",
		"a: b", "true", "123", "#hash", "-dash", ":8443", "|pipe",
		`has"quote`, `back\slash`, "中文值", "${SAIL_VAR}",
		"alice", "bob/", "/yiche", "C:\\tmp", "a#b", "team#1",
	}
	for _, v := range values {
		prof := config.Profile{
			Endpoint: v, AccessKey: v, SecretKey: v,
			Bucket: v, Region: v, CDNDomain: v,
			Serve: config.ServeConfig{
				Listen: v, Prefix: v, User: v, Password: v,
				TLSCert: v, TLSKey: v, StagingDir: v,
				BackendMaxSize: v, MaxUploadSize: v, ChunkSize: v, DirCacheTTL: v,
				Users:   []config.UserConfig{{Name: v, Password: v, Prefix: v, Quota: v}},
				Prewarm: []string{v},
				SMB:     config.SMBConfig{Listen: v, Share: v, ServerName: v},
			},
		}
		// profile 名固定为 prod:viper 会把映射键小写化,拿值当键会测到那个
		// 既有行为而非渲染器。profile 名本身的加引号另见 TestRenderProfileNameQuoted。
		// default-profile 是值不是键,可以放心用自由值测。
		cfg := &config.Config{DefaultProfile: v, Profiles: map[string]config.Profile{"prod": prof}}

		rendered := renderConfigFile(cfg)
		p := t.TempDir() + "/config.yaml"
		if err := os.WriteFile(p, []byte(rendered), 0o600); err != nil {
			t.Fatal(err)
		}
		loaded, err := config.Load(p)
		if err != nil {
			t.Errorf("值 %q 渲染出的 YAML 无法读回: %v\n%s", v, err, rendered)
			continue
		}
		if loaded.DefaultProfile != v {
			t.Errorf("值 %q: default-profile 往返失败: got %q", v, loaded.DefaultProfile)
		}
		got, ok := loaded.Profiles["prod"]
		if !ok {
			t.Errorf("值 %q: profile 块丢失:\n%s", v, rendered)
			continue
		}
		checks := []struct {
			name, got, want string
		}{
			{"endpoint", got.Endpoint, v},
			{"access-key", got.AccessKey, v},
			{"secret-key", got.SecretKey, v},
			{"bucket", got.Bucket, v},
			{"region", got.Region, v},
			{"cdn-domain", got.CDNDomain, v},
			{"serve.listen", got.Serve.Listen, v},
			{"serve.prefix", got.Serve.Prefix, v},
			{"serve.user", got.Serve.User, v},
			{"serve.password", got.Serve.Password, v},
			{"serve.tls-cert", got.Serve.TLSCert, v},
			{"serve.tls-key", got.Serve.TLSKey, v},
			{"serve.staging-dir", got.Serve.StagingDir, v},
			{"serve.backend-max-object-size", got.Serve.BackendMaxSize, v},
			{"serve.max-upload-size", got.Serve.MaxUploadSize, v},
			{"serve.chunk-size", got.Serve.ChunkSize, v},
			{"serve.dir-cache-ttl", got.Serve.DirCacheTTL, v},
			{"serve.smb.listen", got.Serve.SMB.Listen, v},
			{"serve.smb.share", got.Serve.SMB.Share, v},
			{"serve.smb.server-name", got.Serve.SMB.ServerName, v},
		}
		for _, c := range checks {
			if c.got != c.want {
				t.Errorf("值 %q: %s 往返失败: got %q\n%s", v, c.name, c.got, rendered)
			}
		}
		if len(got.Serve.Users) != 1 {
			t.Errorf("值 %q: users 表丢失: %+v", v, got.Serve.Users)
		} else {
			u := got.Serve.Users[0]
			if u.Name != v || u.Password != v || u.Prefix != v || u.Quota != v {
				t.Errorf("值 %q: users 字段往返失败: %+v", v, u)
			}
		}
		if len(got.Serve.Prewarm) != 1 || got.Serve.Prewarm[0] != v {
			t.Errorf("值 %q: prewarm 往返失败: %v", v, got.Serve.Prewarm)
		}
	}
}

// profile 名会作为映射键渲染,尾冒号之类的值会让整份 YAML 不可解析,必须加引号。
// (Viper 会把键小写化,故这里用全小写的名字,避免把那个既有行为混进来。
// 另注:含 "." 的名字会被 viper 当键分隔符拆开,那是 viper 的限制,加引号救不了。)
func TestRenderProfileNameQuoted(t *testing.T) {
	for _, name := range []string{"prod:", "my:prod", "a b"} {
		cfg := &config.Config{
			DefaultProfile: name,
			Profiles: map[string]config.Profile{
				name: {Endpoint: "https://s3.example.com", AccessKey: "ak", SecretKey: "sk"},
			},
		}
		rendered := renderConfigFile(cfg)
		p := t.TempDir() + "/config.yaml"
		if err := os.WriteFile(p, []byte(rendered), 0o600); err != nil {
			t.Fatal(err)
		}
		loaded, err := config.Load(p)
		if err != nil {
			t.Errorf("profile 名 %q 渲染后无法读回: %v\n%s", name, err, rendered)
			continue
		}
		if _, ok := loaded.Profiles[name]; !ok {
			t.Errorf("profile 名 %q: 块丢失: %+v", name, loaded.Profiles)
		}
		if loaded.DefaultProfile != name {
			t.Errorf("profile 名 %q: default-profile 往返失败: %q", name, loaded.DefaultProfile)
		}
	}
}
