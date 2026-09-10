package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func resolveFrom(t *testing.T, body string) *Resolved {
	t.Helper()
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r, err := cfg.Resolve("prod")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return r
}

// TestEnvVarName 校验按 profile 派生环境变量名的清洗规则:
// 大写归一、非 [A-Z0-9] 压缩为单个 _、去首尾 _、清洗后为空回退全局名。
func TestEnvVarName(t *testing.T) {
	cases := []struct {
		profile string
		field   string
		want    string
	}{
		{"prod", "ACCESS_KEY", "SAIL_PROD_ACCESS_KEY"},
		{"test", "SECRET_KEY", "SAIL_TEST_SECRET_KEY"},
		{"staging-eu", "ACCESS_KEY", "SAIL_STAGING_EU_ACCESS_KEY"},
		{"my.prod", "ACCESS_KEY", "SAIL_MY_PROD_ACCESS_KEY"},
		{"my_prod", "ACCESS_KEY", "SAIL_MY_PROD_ACCESS_KEY"},
		{"Prod", "ACCESS_KEY", "SAIL_PROD_ACCESS_KEY"},
		{"2fa", "ACCESS_KEY", "SAIL_2FA_ACCESS_KEY"},
		{"a b", "ACCESS_KEY", "SAIL_A_B_ACCESS_KEY"},
		{"staging--eu", "ACCESS_KEY", "SAIL_STAGING_EU_ACCESS_KEY"},
		{"-prod-", "ACCESS_KEY", "SAIL_PROD_ACCESS_KEY"},
		{"中文prod", "ACCESS_KEY", "SAIL_PROD_ACCESS_KEY"},
		{"中a文b", "ACCESS_KEY", "SAIL_A_B_ACCESS_KEY"},
		{"123", "ACCESS_KEY", "SAIL_123_ACCESS_KEY"},
		{"a--b..c", "ACCESS_KEY", "SAIL_A_B_C_ACCESS_KEY"},
		{"x", "SECRET_KEY", "SAIL_X_SECRET_KEY"},
		// 清洗后为空:回退不带 profile 段的全局名
		{"中文", "ACCESS_KEY", "SAIL_ACCESS_KEY"},
		{"", "ACCESS_KEY", "SAIL_ACCESS_KEY"},
		{"---", "SECRET_KEY", "SAIL_SECRET_KEY"},
		{"___", "ACCESS_KEY", "SAIL_ACCESS_KEY"},
	}
	for _, c := range cases {
		if got := EnvVarName(c.profile, c.field); got != c.want {
			t.Errorf("EnvVarName(%q, %q) = %q, want %q", c.profile, c.field, got, c.want)
		}
	}
}

func TestResolveCDNBucketPath(t *testing.T) {
	// 未声明 -> nil(自动检测)
	r := resolveFrom(t, `default-profile: prod
profiles:
  prod:
    endpoint: https://s3.example.com
    access-key: ak
    secret-key: sk
    bucket: b
`)
	if r.CDNBucketPath != nil {
		t.Errorf("未声明 cdn-bucket-path 应为 nil,got %v", r.CDNBucketPath)
	}

	// true -> 已含 bucket
	r = resolveFrom(t, `default-profile: prod
profiles:
  prod:
    endpoint: https://s3.example.com
    access-key: ak
    secret-key: sk
    bucket: b
    cdn-bucket-path: true
`)
	if r.CDNBucketPath == nil || !*r.CDNBucketPath {
		t.Errorf("cdn-bucket-path: true 应为 *true,got %v", r.CDNBucketPath)
	}

	// false -> 未含 bucket
	r = resolveFrom(t, `default-profile: prod
profiles:
  prod:
    endpoint: https://s3.example.com
    access-key: ak
    secret-key: sk
    bucket: b
    cdn-bucket-path: false
`)
	if r.CDNBucketPath == nil || *r.CDNBucketPath {
		t.Errorf("cdn-bucket-path: false 应为 *false,got %v", r.CDNBucketPath)
	}
}

// TestResolveDefaultProfile 校验 profile 为空时的回退链:default-profile → "prod"。
func TestResolveDefaultProfile(t *testing.T) {
	// 配置了 default-profile:用默认
	cfg, err := Load(writeConfig(t, `default-profile: test
profiles:
  prod:
    endpoint: https://p.example.com
    access-key: ak
    secret-key: sk
  test:
    endpoint: https://t.example.com
    access-key: ak
    secret-key: sk
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r, err := cfg.Resolve("") // 空 profile → default-profile
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.ProfileName != "test" || r.Endpoint != "https://t.example.com" {
		t.Errorf("应回退到 default-profile test,got %q %q", r.ProfileName, r.Endpoint)
	}

	// 无 default-profile:回退 prod
	cfg, err = Load(writeConfig(t, `profiles:
  prod:
    endpoint: https://p.example.com
    access-key: ak
    secret-key: sk
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r, err = cfg.Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.ProfileName != "prod" {
		t.Errorf("无 default 应回退 prod,got %q", r.ProfileName)
	}
}

func TestResolveProfileNotFound(t *testing.T) {
	cfg, err := Load(writeConfig(t, `profiles:
  prod:
    endpoint: https://p.example.com
    access-key: ak
    secret-key: sk
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := cfg.Resolve("ghost"); err == nil {
		t.Errorf("Resolve(ghost) 期望报错")
	}
}

func TestResolveMissingKeys(t *testing.T) {
	// 缺 endpoint
	cfg, err := Load(writeConfig(t, `profiles:
  prod:
    access-key: ak
    secret-key: sk
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := cfg.Resolve("prod"); err == nil {
		t.Errorf("缺 endpoint 应报错")
	}

	// 缺 access-key/secret-key
	cfg, err = Load(writeConfig(t, `profiles:
  prod:
    endpoint: https://p.example.com
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := cfg.Resolve("prod"); err == nil {
		t.Errorf("缺密钥应报错")
	}
}

// TestResolvePathStyleDefaultTrue 校验 path-style 缺省及显式 false 都被强制为 true。
func TestResolvePathStyleDefaultTrue(t *testing.T) {
	// 缺省:应为 true
	r := resolveFrom(t, `default-profile: prod
profiles:
  prod:
    endpoint: https://p.example.com
    access-key: ak
    secret-key: sk
`)
	if !r.PathStyle {
		t.Errorf("缺省 path-style 应为 true")
	}

	// 显式 false:仍强制为 true
	r = resolveFrom(t, `default-profile: prod
profiles:
  prod:
    endpoint: https://p.example.com
    access-key: ak
    secret-key: sk
    path-style: false
`)
	if !r.PathStyle {
		t.Errorf("path-style: false 应被强制为 true(自建 S3 兼容服务默认)")
	}

	// 显式 true:保持 true
	r = resolveFrom(t, `default-profile: prod
profiles:
  prod:
    endpoint: https://p.example.com
    access-key: ak
    secret-key: sk
    path-style: true
`)
	if !r.PathStyle {
		t.Errorf("path-style: true 应保持 true")
	}
}

// TestResolveExpandEnv 校验 ${VAR} 在配置值中被展开;未设置则置空。
func TestResolveExpandEnv(t *testing.T) {
	t.Setenv("SAIL_TEST_AK", "from-env-ak")
	t.Setenv("SAIL_TEST_SK", "from-env-sk")
	// endpoint 混用文本 + 占位符
	t.Setenv("SAIL_TEST_HOST", "s3.example.com")
	r := resolveFrom(t, `default-profile: prod
profiles:
  prod:
    endpoint: https://${SAIL_TEST_HOST}:9000
    access-key: ${SAIL_TEST_AK}
    secret-key: ${SAIL_TEST_SK}
`)
	if r.Endpoint != "https://s3.example.com:9000" {
		t.Errorf("endpoint 展开 = %q,期望 https://s3.example.com:9000", r.Endpoint)
	}
	if r.AccessKey != "from-env-ak" || r.SecretKey != "from-env-sk" {
		t.Errorf("密钥展开错误: %q/%q", r.AccessKey, r.SecretKey)
	}

	// 占位符未设置 → 空串 → 触发缺密钥报错
	cfg, err := Load(writeConfig(t, `default-profile: prod
profiles:
  prod:
    endpoint: https://p.example.com
    access-key: ${SAIL_UNSET_AK_XYZ}
    secret-key: ${SAIL_UNSET_SK_XYZ}
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := cfg.Resolve("prod"); err == nil {
		t.Errorf("占位符未设置导致密钥为空,应报错")
	}
}

// TestResolveEnvOverride 校验 SAIL_* 环境变量优先于配置文件。
func TestResolveEnvOverride(t *testing.T) {
	t.Setenv("SAIL_ENDPOINT", "https://env.example.com")
	t.Setenv("SAIL_ACCESS_KEY", "env-ak")
	t.Setenv("SAIL_SECRET_KEY", "env-sk")
	t.Setenv("SAIL_BUCKET", "env-bucket")
	t.Setenv("SAIL_CDN_DOMAIN", "https://env-cdn.example.com")
	r := resolveFrom(t, `default-profile: prod
profiles:
  prod:
    endpoint: https://cfg.example.com
    access-key: cfg-ak
    secret-key: cfg-sk
    bucket: cfg-bucket
    cdn-domain: https://cfg-cdn.example.com
`)
	if r.Endpoint != "https://env.example.com" || r.AccessKey != "env-ak" ||
		r.SecretKey != "env-sk" || r.Bucket != "env-bucket" || r.CDNDomain != "https://env-cdn.example.com" {
		t.Errorf("环境变量应覆盖配置文件: %+v", r)
	}
}

func TestResolveFieldPassthrough(t *testing.T) {
	r := resolveFrom(t, `default-profile: prod
profiles:
  prod:
    endpoint: https://p.example.com
    access-key: ak
    secret-key: sk
    bucket: mybucket
    region: us-west-2
    path-style: true
    cdn-domain: https://cdn.example.com
`)
	if r.Bucket != "mybucket" || r.Region != "us-west-2" || r.CDNDomain != "https://cdn.example.com" {
		t.Errorf("字段透传错误: %+v", r)
	}
}

// TestLoadErrors 校验 Load 对缺失文件 / 非法 YAML 的报错。
func TestLoadErrors(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Errorf("缺失文件应报错")
	}
	if _, err := Load(writeConfig(t, ":\n  bad: [yaml")); err == nil {
		t.Errorf("非法 YAML 应报错")
	}
}

// TestConfigPath 校验 ConfigPath 拼接 ~/.sail/config.yaml。
func TestConfigPath(t *testing.T) {
	t.Setenv("HOME", "/tmp/fake-home")
	p, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if p != "/tmp/fake-home/.sail/config.yaml" {
		t.Errorf("ConfigPath = %q,期望 /tmp/fake-home/.sail/config.yaml", p)
	}
}
