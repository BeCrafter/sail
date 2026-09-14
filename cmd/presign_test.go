package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeCrafter/sail/internal/fakes3"
)

// writeTestConfig 写一份指向内存 S3 端点的最小配置,并把全局 flag 指向它。
func writeTestConfig(t *testing.T, endpoint, bucket string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "default-profile: prod\nprofiles:\n  prod:\n    endpoint: " + endpoint +
		"\n    access-key: ak\n    secret-key: sk\n    bucket: " + bucket +
		"\n    region: us-east-1\n    path-style: true\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	origPath, origProfile, origBucket := cfgPath, profile, cfgBucket
	t.Cleanup(func() { cfgPath, profile, cfgBucket = origPath, origProfile, origBucket })
	cfgPath, profile, cfgBucket = path, "prod", ""
}

func TestPresignFailsLoudOnChunkedKey(t *testing.T) {
	srv := fakes3.New()
	t.Cleanup(srv.Close)
	writeTestConfig(t, srv.URL(), "b")

	// 一个分片文件的逻辑 key:对象体是 manifest,标记在用户元数据上。
	srv.PutWithMetadata("b", "big.bin", []byte(`{"sail_manifest":1,"version":"v","size":10,"chunk_size":5,"chunks":[]}`),
		"application/octet-stream", map[string]string{"sail-manifest-key": "v", "sail-total-size": "10"})

	origAllow := presignAllowChunked
	origExpires := presignExpires
	t.Cleanup(func() { presignAllowChunked, presignExpires = origAllow, origExpires })
	presignAllowChunked, presignExpires = false, 3600

	err := presignCmd.RunE(presignCmd, []string{"s3://b/big.bin"})
	if err == nil {
		t.Fatal("对分片 key 预签名应报错,而不是返回只指向 manifest 的 URL")
	}
	if !strings.Contains(err.Error(), "chunks") || !strings.Contains(err.Error(), "--allow-chunked") {
		t.Fatalf("错误应说明原因并给出替代路径,实际: %v", err)
	}

	// --allow-chunked 时应放行(URL 指向 manifest,是调用方自担的选择)。
	presignAllowChunked = true
	if err := presignCmd.RunE(presignCmd, []string{"s3://b/big.bin"}); err != nil {
		t.Fatalf("--allow-chunked 下不应报错,实际: %v", err)
	}
}

func TestPresignPlainKeyStillWorks(t *testing.T) {
	srv := fakes3.New()
	t.Cleanup(srv.Close)
	writeTestConfig(t, srv.URL(), "b")
	srv.Put("b", "plain.bin", []byte("hello"), "text/plain")

	origAllow := presignAllowChunked
	t.Cleanup(func() { presignAllowChunked = origAllow })
	presignAllowChunked = false

	if err := presignCmd.RunE(presignCmd, []string{"s3://b/plain.bin"}); err != nil {
		t.Fatalf("普通对象预签名不应报错,实际: %v", err)
	}
}
