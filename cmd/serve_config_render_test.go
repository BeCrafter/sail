package cmd

import (
	"os"
	"strings"
	"testing"

	"github.com/BeCrafter/sail/internal/config"
)

func TestRenderProfileServeBlockRoundtrip(t *testing.T) {
	cfg := &config.Config{
		DefaultProfile: "prod",
		Profiles: map[string]config.Profile{
			"prod": {
				Endpoint: "https://s3.example.com", AccessKey: "ak", SecretKey: "sk", Bucket: "b",
				Serve: config.ServeConfig{
					Listen: ":8443", User: "alice", Password: "${SAIL_PROD_SERVE_PASSWORD}",
					ChunkedUpload: true, ChunkSize: "10MiB",
				},
			},
		},
	}
	rendered := renderConfigFile(cfg)
	t.Logf("rendered:\n%s", rendered)

	// 写盘后重新 Load + Resolve,确认 serve 块能往返
	p := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(p, []byte(rendered), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r, err := loaded.Resolve("prod")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Serve.Listen != ":8443" || r.Serve.User != "alice" || r.Serve.ChunkSize != "10MiB" || !r.Serve.ChunkedUpload {
		t.Errorf("serve 块往返错误: %+v", r.Serve)
	}
}

// TestRenderProfileServeBlockUsersRoundtrip 锁住多用户表与新增字段的往返。
// 这是 serve.users 静默丢弃缺陷的回归测试:旧实现不渲染 users,此用例必失败。
func TestRenderProfileServeBlockUsersRoundtrip(t *testing.T) {
	t.Setenv("ALICE_PW", "s3cret-from-env")
	cfg := &config.Config{
		DefaultProfile: "prod",
		Profiles: map[string]config.Profile{
			"prod": {
				Endpoint: "https://s3.example.com", AccessKey: "ak", SecretKey: "sk", Bucket: "b",
				Serve: config.ServeConfig{
					Listen: ":8443", Prefix: "team/",
					DirCacheTTL: "5m",
					Prewarm:     []string{"/yiche", "/modelImage"},
					Users: []config.UserConfig{
						{Name: "alice", Password: "${ALICE_PW}", Prefix: "alice/", Quota: "10GB"},
						{Name: "bob", Password: "pw-bob"},
					},
				},
			},
		},
	}
	rendered := renderConfigFile(cfg)
	t.Logf("rendered:\n%s", rendered)

	p := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(p, []byte(rendered), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r, err := loaded.Resolve("prod")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(r.Serve.Users) != 2 {
		t.Fatalf("users 表往返丢失: %+v", r.Serve.Users)
	}
	alice, bob := r.Serve.Users[0], r.Serve.Users[1]
	if alice.Name != "alice" || alice.Prefix != "alice/" || alice.Quota != "10GB" {
		t.Errorf("alice 字段往返错误: %+v", alice)
	}
	if alice.Password != "s3cret-from-env" {
		t.Errorf("${VAR} 密码未展开或引号渲染破坏了展开: %q", alice.Password)
	}
	if bob.Name != "bob" || bob.Password != "pw-bob" || bob.Prefix != "" || bob.Quota != "" {
		t.Errorf("bob(空 prefix/quota)往返错误: %+v", bob)
	}
	if r.Serve.DirCacheTTL != "5m" || len(r.Serve.Prewarm) != 2 || r.Serve.Prewarm[0] != "/yiche" {
		t.Errorf("dir-cache-ttl/prewarm 往返错误: %q %v", r.Serve.DirCacheTTL, r.Serve.Prewarm)
	}
}

// TestRenderProfileServeBlockUsersOnly 仅配 users 的块不能被判空丢弃。
func TestRenderProfileServeBlockUsersOnly(t *testing.T) {
	cfg := &config.Config{
		DefaultProfile: "prod",
		Profiles: map[string]config.Profile{
			"prod": {
				Endpoint: "https://s3.example.com", AccessKey: "ak", SecretKey: "sk",
				Serve: config.ServeConfig{
					Users: []config.UserConfig{{Name: "alice", Password: "pw"}},
				},
			},
		},
	}
	rendered := renderConfigFile(cfg)
	if !strings.Contains(rendered, "      users:") || !strings.Contains(rendered, `- name: alice`) {
		t.Errorf("仅含 users 的 serve 块被丢弃:\n%s", rendered)
	}

	p := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(p, []byte(rendered), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r, err := loaded.Resolve("prod")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(r.Serve.Users) != 1 || r.Serve.Users[0].Name != "alice" {
		t.Errorf("仅含 users 的块往返丢失: %+v", r.Serve.Users)
	}
}

func TestRenderProfileServeBlockSMBRoundtrip(t *testing.T) {
	cfg := &config.Config{
		DefaultProfile: "prod",
		Profiles: map[string]config.Profile{
			"prod": {
				Endpoint: "https://s3.example.com", AccessKey: "ak", SecretKey: "sk", Bucket: "b",
				Serve: config.ServeConfig{SMB: config.SMBConfig{
					Listen: ":2445", Share: "team-share", ServerName: "SAIL-TEST",
				}},
			},
		},
	}
	rendered := renderConfigFile(cfg)
	if !strings.Contains(rendered, "      smb:\n") || !strings.Contains(rendered, `        listen: ":2445"`) {
		t.Fatalf("SMB 配置块未渲染:\n%s", rendered)
	}

	p := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(p, []byte(rendered), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r, err := loaded.Resolve("prod")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := r.Serve.SMB; got.Listen != ":2445" || got.Share != "team-share" || got.ServerName != "SAIL-TEST" {
		t.Errorf("SMB 配置往返错误: %+v", got)
	}
}
