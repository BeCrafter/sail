package cmd

import (
	"os"
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
