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
