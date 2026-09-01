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
