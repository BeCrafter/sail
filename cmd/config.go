package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/i18n"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Config management",
	Long: `Manage configuration. Subcommands:
  setup   interactively generate/update the config file (default ~/.sail/config.yaml, override with -c;
          --reset starts from a fresh config; when the file already exists, adds or reconfigures one profile,
          keeping the others)
setup wizard notes:
  - endpoint is required; leaving it empty re-prompts in place
  - access-key / secret-key can be typed as plaintext; pressing Enter on empty references a per-profile
    derived env var (e.g. profile test → SAIL_TEST_ACCESS_KEY); after writing it prints the vars to export
  - when reconfiguring an existing profile, configured plaintext keys are not echoed; Enter keeps them
  - it ends with a config summary; empty fields are clearly marked for review
See the README "Configuration" section for details.`,
}

var configSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "interactively generate/update the config file (-c path, --reset, add or reconfigure a profile)",
	RunE: func(cmd *cobra.Command, args []string) error {
		var err error

		// 写入路径:优先 -c/--config,缺省回退默认路径
		path := cfgPath
		if path == "" {
			path, err = config.ConfigPath()
			if err != nil {
				return err
			}
		}

		// 加载已有配置;--reset 或文件不存在则视为全新
		var cfg *config.Config
		if cfgSetupReset {
			cfg = &config.Config{Profiles: map[string]config.Profile{}}
		} else if _, statErr := os.Stat(path); statErr != nil {
			cfg = &config.Config{Profiles: map[string]config.Profile{}}
		} else {
			cfg, err = config.Load(path)
			if err != nil {
				return err
			}
		}
		if cfg.Profiles == nil {
			cfg.Profiles = map[string]config.Profile{}
		}

		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf(i18n.T("failed to create directory: %w"), err)
		}

		reader := bufio.NewReader(os.Stdin)
		var existing config.Profile
		exists := false
		def := cfg.DefaultProfile
		if def == "" && len(cfg.Profiles) > 0 {
			def = firstProfileName(cfg.Profiles)
		}
		if def == "" {
			def = "prod"
		}
		prof := promptReader(reader, i18n.T("profile name"), def)
		existing, exists = cfg.Profiles[prof]
		if exists {
			fmt.Printf(i18n.T("profile %q already exists; it will be reconfigured.\n"), prof)
		}
		// endpoint 是启动硬依赖(缺失时任何命令都报错),必须非空;
		// 管道/脚本无输入时 ReadString 恒返回空,靠次数上限避免死循环。
		var endpoint string
		for i := 0; i < 3; i++ {
			endpoint = promptReader(reader, i18n.T("endpoint (required, e.g. https://<your-s3-endpoint>/)"), existing.Endpoint)
			if endpoint != "" {
				break
			}
			fmt.Println(i18n.T("endpoint is required; please enter the S3-compatible service address."))
		}
		if endpoint == "" {
			return errors.New(i18n.T("endpoint left empty 3 times; exiting. Please re-run sail config setup"))
		}
		akLabel := fmt.Sprintf(i18n.T("access-key (enter a key; press Enter on empty to reference env var %s)"), config.EnvVarName(prof, "ACCESS_KEY"))
		if existing.AccessKey != "" {
			akLabel = i18n.T("access-key (press Enter to keep the configured value; enter a new value or ${VAR} to replace)")
		}
		skLabel := fmt.Sprintf(i18n.T("secret-key (enter a key; press Enter on empty to reference env var %s)"), config.EnvVarName(prof, "SECRET_KEY"))
		if existing.SecretKey != "" {
			skLabel = i18n.T("secret-key (press Enter to keep the configured value; enter a new value or ${VAR} to replace)")
		}
		accessKey := promptSecretReader(reader, akLabel, existing.AccessKey)
		secretKey := promptSecretReader(reader, skLabel, existing.SecretKey)
		bucket := promptReader(reader, i18n.T("default bucket (can be empty)"), existing.Bucket)
		cdnDomain := promptReader(reader, i18n.T("CDN domain (used by the url command; can be empty)"), existing.CDNDomain)
		region := promptReader(reader, i18n.T("region (cloud providers fill e.g. us-east-1; self-hosted can leave empty)"), existing.Region)
		pathStyle := promptBoolReader(reader, i18n.T("path-style (choose y for self-hosted/MinIO, n for AWS S3)"), existing.PathStyle || !exists)
		// cdn-bucket-path 仅在配置了 cdn-domain 时才有意义;空则跳过,留自动检测
		var cdnBucketPath *bool
		if cdnDomain != "" {
			cdnBucketPath = promptTristateReader(reader, i18n.T("does the CDN domain already include the bucket path?"), existing.CDNBucketPath)
		}
		// 语言:默认取当前生效语言,回车即固化
		langDef := "en"
		if i18n.Current() == i18n.Zh {
			langDef = "zh"
		}
		langInput := promptReader(reader, i18n.T("language (en|zh)"), langDef)
		if l := i18n.Normalize(langInput); l == i18n.Zh {
			cfg.Lang = "zh"
		} else {
			cfg.Lang = "en"
		}

		cfg.Profiles[prof] = config.Profile{
			Endpoint: endpoint, AccessKey: accessKey, SecretKey: secretKey,
			Bucket: bucket, Region: region, PathStyle: pathStyle,
			CDNDomain: cdnDomain, CDNBucketPath: cdnBucketPath,
		}
		// 始终允许把本次写入的 profile 设为默认:无默认或本就是默认时默认 yes,
		// 否则默认 no(避免无意切换默认)。
		if promptBoolReader(reader, i18n.T("set as the default profile?"), cfg.DefaultProfile == "" || prof == cfg.DefaultProfile) {
			cfg.DefaultProfile = prof
		}
		isDefault := cfg.DefaultProfile == prof

		content := renderConfigFile(cfg)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return fmt.Errorf(i18n.T("failed to write config: %w"), err)
		}
		fmt.Printf(i18n.T("\nconfig written to: %s\n"), path)
		fmt.Print(setupSummary(prof, isDefault, cfg.Profiles[prof]))

		// 引导安装 shell 自动补全 (仅 macOS)
		if shell := detectShell(); shell != "" {
			fmt.Printf(i18n.T("\ndetected current shell: %s\n"), shell)
			fmt.Print(i18n.T("install shell completion? [Y/n] "))
			if confirmDefault() {
				if err := installCompletion(shell); err != nil {
					fmt.Printf(i18n.T("completion install failed: %v\n"), err)
				}
			} else {
				fmt.Println(i18n.T("skipped completion install; install later with: sail completion " + shell))
			}
		} else if runtime.GOOS == "darwin" {
			fmt.Println(i18n.T("\nno supported shell detected; install completion manually with: sail completion <zsh|bash|fish>"))
		}
		return nil
	},
}

// cfgSetupReset 为 true 时,setup 丢弃现有配置,重置为单 profile 的全新配置。
var cfgSetupReset bool

func init() {
	configCmd.AddCommand(configSetupCmd)
	configSetupCmd.Flags().BoolVar(&cfgSetupReset, "reset", false, "discard the existing config and reset to a single fresh profile")
}

// promptReader 从共享 reader 读取一行,空输入返回默认值。
func promptReader(r *bufio.Reader, label, def string) string {
	return promptReaderDisplay(r, label, def, def)
}

// promptSecretReader 同 promptReader,但已配置的明文密钥不回显(防终端/日志泄漏);
// ${VAR} 占位符非明文,原样显示。
func promptSecretReader(r *bufio.Reader, label, def string) string {
	display := def
	if display != "" && !strings.HasPrefix(display, "${") {
		display = i18n.T("configured, press Enter to keep")
	}
	return promptReaderDisplay(r, label, def, display)
}

// promptReaderDisplay 从共享 reader 读取一行,空输入返回 def;display 仅用于回显提示。
func promptReaderDisplay(r *bufio.Reader, label, def, display string) string {
	if display != "" {
		fmt.Printf("%s [%s]: ", label, display)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// promptBoolReader 从共享 reader 读取 y/n,返回布尔值。默认 yes(def=true)。
func promptBoolReader(r *bufio.Reader, label string, def bool) bool {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	fmt.Printf("%s [%s]: ", label, hint)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	if line == "" {
		return def
	}
	return line == "y" || line == "yes"
}

// promptTristateReader 读取 y/n/回车,返回 *bool 表示三态:
// 回车或无效输入返回 def(自动检测);y/yes 返回 true(已含);n/no 返回 false(未含)。
func promptTristateReader(r *bufio.Reader, label string, def *bool) *bool {
	fmt.Printf("%s [%s]: ", label, i18n.T("y/n, Enter=auto-detect"))
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	switch line {
	case "y", "yes":
		v := true
		return &v
	case "n", "no":
		v := false
		return &v
	default:
		return def
	}
}

// confirm 询问 y/n,默认 no。用于破坏性操作的确认(mv 递归等)。
func confirm() bool {
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes"
}

// renderConfigFile 渲染整个配置(所有 profile 按名称排序,保证确定性)。
func renderConfigFile(cfg *config.Config) string {
	if cfg == nil {
		cfg = &config.Config{Profiles: map[string]config.Profile{}}
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]config.Profile{}
	}
	names := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# S3-compatible storage CLI config\n")
	b.WriteString("# Two ways to provide keys: plaintext, or ${VAR} referencing an environment variable (avoids plaintext on disk).\n")
	b.WriteString("# Keys left empty by setup generate a ${SAIL_<PROFILE>_(ACCESS|SECRET)_KEY} placeholder; edit to any ${VAR} manually.\n")
	if cfg.Lang != "" {
		fmt.Fprintf(&b, "lang: %s\n", cfg.Lang)
	}
	fmt.Fprintf(&b, "default-profile: %s\n", cfg.DefaultProfile)
	b.WriteString("profiles:\n")
	for _, name := range names {
		b.WriteString(renderProfile(name, cfg.Profiles[name]))
	}
	return b.String()
}

// renderProfile 渲染单个 profile 块;ak/sk 为空时替换为按 profile 派生的
// `${SAIL_<PROFILE>_<FIELD>}` 占位符(见 config.EnvVarName,多个 profile 互不共享),
// cdn-bucket-path 仅在配置了 cdn-domain 时输出(注释行=自动检测,显式值=明确声明)。
func renderProfile(name string, p config.Profile) string {
	ak := p.AccessKey
	if ak == "" {
		ak = "${" + config.EnvVarName(name, "ACCESS_KEY") + "}"
	}
	sk := p.SecretKey
	if sk == "" {
		sk = "${" + config.EnvVarName(name, "SECRET_KEY") + "}"
	}
	ps := "false"
	if p.PathStyle {
		ps = "true"
	}
	// cdn-bucket-path 仅在配置了 cdn-domain 时才有意义:
	// 未配置 CDN 域名则整行不输出(避免冗余);配置了则带说明注释,
	// 注释行(自动检测)或显式 true/false 值由 p.CDNBucketPath 决定。
	// 注释固定英文:这是持久化到用户 ~/.sail/config.yaml 的手工可编辑产物,
	// 不随运行语言切换,保证文件确定性。
	const cdnHint = "whether the CDN domain URL already includes the bucket path: true=yes (do not append), false=no (always append)"
	cdp := ""
	if p.CDNDomain != "" {
		if p.CDNBucketPath == nil {
			cdp = fmt.Sprintf("    # cdn-bucket-path: false  # %s; comment out (default) to auto-detect\n", cdnHint)
		} else {
			cdp = fmt.Sprintf("    cdn-bucket-path: %t  # %s\n", *p.CDNBucketPath, cdnHint)
		}
	}
	return fmt.Sprintf(`  %s:
    endpoint: %s
    access-key: %s
    secret-key: %s
    bucket: "%s"
    region: "%s"
    path-style: %s
    cdn-domain: "%s"
%s`, name, p.Endpoint, ak, sk, p.Bucket, p.Region, ps, p.CDNDomain, cdp)
}

// firstProfileName 返回配置中按名称排序的第一个 profile,无则返回空串。
func firstProfileName(m map[string]config.Profile) string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// setupSummary 生成写盘后的配置摘要,空字段明确标注;密钥留空时
// 追加需 export 的环境变量指引(未设置时的报错一并说明)。
// 明文密钥只显示"已填写"不回显值,避免泄漏到终端/日志。
func setupSummary(prof string, isDefault bool, p config.Profile) string {
	var b strings.Builder
	title := prof
	if isDefault {
		title += i18n.T(" (default)")
	}
	fmt.Fprintf(&b, "%s  profile: %s\n", i18n.T("config summary"), title)
	fmt.Fprintf(&b, "  endpoint:   %s\n", p.Endpoint)
	if p.AccessKey != "" {
		b.WriteString("  access-key: " + i18n.T("set (plaintext)") + "\n")
	} else {
		fmt.Fprintf(&b, "  access-key: %s %s (%s)\n", i18n.T("references env var"), config.EnvVarName(prof, "ACCESS_KEY"), i18n.T("export it first"))
	}
	if p.SecretKey != "" {
		b.WriteString("  secret-key: " + i18n.T("set (plaintext)") + "\n")
	} else {
		fmt.Fprintf(&b, "  secret-key: %s %s (%s)\n", i18n.T("references env var"), config.EnvVarName(prof, "SECRET_KEY"), i18n.T("export it first"))
	}
	bucket := p.Bucket
	if bucket == "" {
		bucket = i18n.T("(unset)")
	}
	fmt.Fprintf(&b, "  bucket:     %s\n", bucket)
	region := p.Region
	if region == "" {
		region = i18n.T("(unset; self-hosted may leave empty)")
	}
	fmt.Fprintf(&b, "  region:     %s\n", region)
	cdn := p.CDNDomain
	if cdn == "" {
		cdn = i18n.T("(unset; the url command is unavailable)")
	}
	fmt.Fprintf(&b, "  cdn-domain: %s\n", cdn)

	var missing []string
	if p.AccessKey == "" {
		missing = append(missing, fmt.Sprintf("  export %s=%s", config.EnvVarName(prof, "ACCESS_KEY"), i18n.T("<your AccessKey>")))
	}
	if p.SecretKey == "" {
		missing = append(missing, fmt.Sprintf("  export %s=%s", config.EnvVarName(prof, "SECRET_KEY"), i18n.T("<your SecretKey>")))
	}
	if len(missing) > 0 {
		fmt.Fprintf(&b, "\n%s\n", i18n.Tf("note: keys for profile %s are empty and reference env vars; set them before use:", prof))
		for _, m := range missing {
			b.WriteString(m + "\n")
		}
		fmt.Fprintf(&b, "%s\n", i18n.Tf("when unset, sail commands fail with: profile %q missing access-key/secret-key", prof))
	}
	return b.String()
}
