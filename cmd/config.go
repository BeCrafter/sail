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
	GroupID: "config",
	Use:     "config",
	Short:   "Config management",
	Long: `Manage configuration. Subcommands:
  setup   interactively generate/update the config file (default ~/.sail/config.yaml, override with -c;
          --reset starts from a fresh config; when the file already exists, adds or reconfigures one profile,
          keeping the others)
setup wizard notes:
  - endpoint is required; leaving it empty re-prompts in place
  - access-key / secret-key can be typed as plaintext; pressing Enter on empty references a per-profile
    derived env var (e.g. profile test → SAIL_TEST_ACCESS_KEY); after writing it prints the vars to export
  - when reconfiguring an existing profile, configured plaintext keys are not echoed; Enter keeps them
  - the WebDAV gateway (serve block) is guided as well: listen / prefix / auth (single or multi-user) / TLS /
    chunked-upload / staging-dir; multi-user tables are validated in place (duplicate names, nested prefixes,
    quota syntax — quota accepts MB/GB/TB). An existing serve block defaults to "keep" —
    choose append to add users to the existing table, reconfigure to edit it, or remove to drop it. Size limits,
    dir-cache-ttl and prewarm are not asked here; edit the config file to change them
  - inputs are normalized where possible: a bare port gets its colon (8443 -> :8443), a URL without a
    scheme gets https://, single-letter quota units become MB/GB/TB, ~ is expanded in paths, and y/n
    answers accept yes/true/1/on; invalid values are re-prompted with an explanation
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
		// 漏写 scheme 是常见笔误(会得到难懂的 "not a valid URI");能补就补。
		if norm := normalizeURL(endpoint); norm != endpoint {
			fmt.Printf(i18n.T("note: endpoint has no scheme; using %s\n"), norm)
			endpoint = norm
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
		if norm := normalizeURL(cdnDomain); norm != cdnDomain {
			fmt.Printf(i18n.T("note: CDN domain has no scheme; using %s\n"), norm)
			cdnDomain = norm
		}
		region := promptReader(reader, i18n.T("region (cloud providers fill e.g. us-east-1; self-hosted can leave empty)"), existing.Region)
		pathStyle := promptBoolReader(reader, i18n.T("path-style (choose y for self-hosted/MinIO, n for AWS S3)"), existing.PathStyle || !exists)
		// cdn-bucket-path 仅在配置了 cdn-domain 时才有意义;空则跳过,留自动检测
		var cdnBucketPath *bool
		if cdnDomain != "" {
			cdnBucketPath = promptTristateReader(reader, i18n.T("does the CDN domain already include the bucket path?"), existing.CDNBucketPath)
		}
		// serve 块(WebDAV 网关):已有配置走「保留/重配/删除」三态(默认保留),
		// 新配置走可选引导提问。
		serve, err := collectServeConfig(reader, existing.Serve)
		if err != nil {
			return err
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
			Serve: serve,
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

// parseBoolInput 宽容地解析 y/n 类回答:接受 yes/true/1/on(中英文)等常见写法。
// 返回 (值, 是否识别);未识别由调用方决定是重问还是取默认。
func parseBoolInput(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes", "true", "1", "on", "是", "对", "好":
		return true, true
	case "n", "no", "false", "0", "off", "否", "不", "不是":
		return false, true
	}
	return false, false
}

// normalizeURL 给缺 scheme 的地址补 https://:endpoint / cdn-domain 常见漏写,
// 补全后可避免 SDK 报「not a valid URI」或拼出打不开的 CDN 链接。
func normalizeURL(v string) string {
	if v == "" || strings.Contains(v, "://") {
		return v
	}
	return "https://" + v
}

// promptBoolReader 从共享 reader 读取 y/n,返回布尔值。默认 yes(def=true)。
// 宽容解析(yes/true/1/on…)并对无法识别的输入重问(至多 3 次后取默认),
// 避免误输入被静默当成 no。
func promptBoolReader(r *bufio.Reader, label string, def bool) bool {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for i := 0; i < 3; i++ {
		fmt.Printf("%s [%s]: ", label, hint)
		line, _ := r.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return def
		}
		if v, ok := parseBoolInput(line); ok {
			return v
		}
		fmt.Printf(i18n.T("unrecognized answer %q; please answer y or n\n"), line)
	}
	return def
}

// promptTristateReader 读取 y/n/回车,返回 *bool 表示三态:
// 回车或未识别输入返回 def(自动检测);y/yes/true… 返回 true(已含);
// n/no/false… 返回 false(未含)。
func promptTristateReader(r *bufio.Reader, label string, def *bool) *bool {
	fmt.Printf("%s [%s]: ", label, i18n.T("y/n, Enter=auto-detect"))
	line, _ := r.ReadString('\n')
	if v, ok := parseBoolInput(line); ok {
		return &v
	}
	return def
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
%s%s`, name, p.Endpoint, ak, sk, p.Bucket, p.Region, ps, p.CDNDomain, cdp, serveBlock(p.Serve))
}

// serveBlockEmpty 判定 serve 块是否为纯默认(全空)。ServeConfig 含切片,
// 不能用 == 比较;新增字段必须同步加进这里,否则"仅配了该字段"的块会被
// 误判为空而整块丢弃。
func serveBlockEmpty(s config.ServeConfig) bool {
	return s.Listen == "" && s.Prefix == "" && s.User == "" && s.Password == "" &&
		len(s.Users) == 0 &&
		s.TLSCert == "" && s.TLSKey == "" && s.StagingDir == "" &&
		s.BackendMaxSize == "" && s.MaxUploadSize == "" &&
		!s.ChunkedUpload && s.ChunkSize == "" &&
		s.DirCacheTTL == "" && len(s.Prewarm) == 0
}

// yamlQuote 把用户自由输入的值渲染成 YAML 双引号标量:值里的 "#"、":"、
// 首尾空白不再被解析器截断(否则密码会被静默改写)。${VAR} 原样保留,
// Resolve 阶段照常展开。
func yamlQuote(v string) string {
	return fmt.Sprintf("%q", v)
}

// serveBlock 渲染 profile 下的 serve 块;全部字段为空(纯默认)时返回空串,
// 不产生冗余块。空字段注释掉以便手工补齐;password 原样写入,${VAR} 得以保留。
func serveBlock(s config.ServeConfig) string {
	if serveBlockEmpty(s) {
		return ""
	}
	f := func(key, val string) string {
		if val == "" {
			return fmt.Sprintf("      # %s:\n", key)
		}
		return fmt.Sprintf("      %s: %s\n", key, val)
	}
	var b strings.Builder
	b.WriteString("    serve:\n")
	b.WriteString(f("listen", s.Listen))
	b.WriteString(f("prefix", s.Prefix))
	b.WriteString(f("user", s.User))
	b.WriteString(f("password", s.Password))
	b.WriteString(serveUsersBlock(s.Users))
	b.WriteString(f("tls-cert", s.TLSCert))
	b.WriteString(f("tls-key", s.TLSKey))
	b.WriteString(f("staging-dir", s.StagingDir))
	b.WriteString(f("backend-max-object-size", s.BackendMaxSize))
	b.WriteString(f("max-upload-size", s.MaxUploadSize))
	b.WriteString(f("chunk-size", s.ChunkSize))
	fmt.Fprintf(&b, "      chunked-upload: %t\n", s.ChunkedUpload)
	b.WriteString(f("dir-cache-ttl", s.DirCacheTTL))
	b.WriteString(servePrewarmBlock(s.Prewarm))
	return b.String()
}

// serveUsersBlock 渲染 serve.users 用户表;无用户返回空串。
// 项缩进 8 空格、字段 10 空格(与 README 示例一致)。空字段注释掉以便手工
// 补齐;name/password/prefix/quota 是自由输入,统一经 yamlQuote 加引号。
func serveUsersBlock(users []config.UserConfig) string {
	if len(users) == 0 {
		return ""
	}
	uf := func(key, val string) string {
		if val == "" {
			return fmt.Sprintf("          # %s:\n", key)
		}
		return fmt.Sprintf("          %s: %s\n", key, yamlQuote(val))
	}
	var b strings.Builder
	b.WriteString("      users:\n")
	for _, u := range users {
		fmt.Fprintf(&b, "        - name: %s\n", yamlQuote(u.Name))
		b.WriteString(uf("password", u.Password))
		b.WriteString(uf("prefix", u.Prefix))
		b.WriteString(uf("quota", u.Quota))
	}
	return b.String()
}

// servePrewarmBlock 渲染 prewarm 目录清单;空列表渲染成注释行以便手工补齐。
func servePrewarmBlock(dirs []string) string {
	if len(dirs) == 0 {
		return "      # prewarm:\n"
	}
	var b strings.Builder
	b.WriteString("      prewarm:\n")
	for _, d := range dirs {
		fmt.Fprintf(&b, "        - %s\n", yamlQuote(d))
	}
	return b.String()
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

// serveDigest 生成 serve 块的一行摘要(绝不显示密码)。空块 = 未配置;
// 非空列出 listen / 认证方式 / 前缀 / TLS;凭据缺失时提示 serve 会拒绝启动。
func serveDigest(s config.ServeConfig) string {
	if serveBlockEmpty(s) {
		return i18n.T("(unset; sail serve webdav uses flag defaults)")
	}
	var parts []string
	if s.Listen != "" {
		parts = append(parts, "listen="+s.Listen)
	}
	switch {
	case len(s.Users) > 0:
		parts = append(parts, i18n.Tf("%d user(s)", len(s.Users)))
	case s.User != "" && s.Password != "":
		parts = append(parts, i18n.Tf("single user %s", s.User))
	default:
		parts = append(parts, i18n.T("(no credentials; serve webdav will refuse to start)"))
	}
	if s.Prefix != "" {
		parts = append(parts, "prefix="+s.Prefix)
	}
	if s.TLSCert != "" {
		parts = append(parts, "tls")
	}
	return strings.Join(parts, " ")
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
	fmt.Fprintf(&b, "  serve:      %s\n", serveDigest(p.Serve))
	if !serveBlockEmpty(p.Serve) {
		b.WriteString(i18n.T("note: backend-max-object-size / max-upload-size / chunk-size / dir-cache-ttl / prewarm are kept as configured (not asked here); edit the config file to change them") + "\n")
	}

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
