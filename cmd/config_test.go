package cmd

import (
	"strings"
	"testing"

	"github.com/BeCrafter/sail/internal/config"
)

// TestSetupSummary 校验写盘后的配置摘要:
// 留空密钥显示派生变量名与 export 指引;明文密钥不回显值(安全);空字段标注。
func TestSetupSummary(t *testing.T) {
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
	if !strings.Contains(out, `bucket: "b1"`) || !strings.Contains(out, `bucket: "b2"`) {
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
	if out := render(nil); !strings.Contains(out, "# cdn-bucket-path: false") || !strings.Contains(out, "自动检测") {
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
	for _, want := range []string{"已含", "不再追加", "未含", "总是追加"} {
		if !strings.Contains(out, want) {
			t.Errorf("说明注释应包含 %q:\n%s", want, out)
		}
	}
}
