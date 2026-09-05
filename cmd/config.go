package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/BeCrafter/sail/internal/config"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "配置管理",
	Long: `配置管理。子命令:
  setup   交互式生成/更新配置文件(默认 ~/.sail/config.yaml,可用 -c 指定路径;
          --reset 重置为全新配置;已有文件时新增或重配一个 profile,保留其它)
setup 向导要点:
  - endpoint 必填,留空原地重问
  - access-key / secret-key 直接输入明文;回车留空则引用按 profile 派生的环境变量
    (如 profile test → SAIL_TEST_ACCESS_KEY),写盘后打印需要 export 的变量名
  - 重配已有 profile 时,已配置的明文密钥不回显,回车即保留
  - 结尾输出配置摘要,空字段明确标注,便于核对缺失项
详见 README「配置」章节。`,
}

var configSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "交互式生成/更新配置文件(支持 -c 指定路径、--reset 重置、新增或重配 profile)",
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
			return fmt.Errorf("创建目录失败: %w", err)
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
		prof := promptReader(reader, "profile 名称", def)
		existing, exists = cfg.Profiles[prof]
		if exists {
			fmt.Printf("profile %q 已存在,将重新配置。\n", prof)
		}
		// endpoint 是启动硬依赖(缺失时任何命令都报错),必须非空;
		// 管道/脚本无输入时 ReadString 恒返回空,靠次数上限避免死循环。
		var endpoint string
		for i := 0; i < 3; i++ {
			endpoint = promptReader(reader, "endpoint (必填,如 https://<your-s3-endpoint>/)", existing.Endpoint)
			if endpoint != "" {
				break
			}
			fmt.Println("endpoint 为必填项,请输入 S3 兼容服务地址。")
		}
		if endpoint == "" {
			return fmt.Errorf("endpoint 连续 3 次为空,已退出。请重新运行 sail config setup")
		}
		akLabel := fmt.Sprintf("access-key (输入密钥;回车留空则引用环境变量 %s)", config.EnvVarName(prof, "ACCESS_KEY"))
		if existing.AccessKey != "" {
			akLabel = "access-key (回车保留已配置值;输入新值或 ${VAR} 替换)"
		}
		skLabel := fmt.Sprintf("secret-key (输入密钥;回车留空则引用环境变量 %s)", config.EnvVarName(prof, "SECRET_KEY"))
		if existing.SecretKey != "" {
			skLabel = "secret-key (回车保留已配置值;输入新值或 ${VAR} 替换)"
		}
		accessKey := promptSecretReader(reader, akLabel, existing.AccessKey)
		secretKey := promptSecretReader(reader, skLabel, existing.SecretKey)
		bucket := promptReader(reader, "默认 bucket (可留空)", existing.Bucket)
		cdnDomain := promptReader(reader, "CDN 域名 (用于 url 命令,可留空)", existing.CDNDomain)
		region := promptReader(reader, "region (云厂商填如 us-east-1,自建服务留空)", existing.Region)
		pathStyle := promptBoolReader(reader, "path-style (自建/MinIO 选 y,AWS S3 选 n)", existing.PathStyle || !exists)
		// cdn-bucket-path 仅在配置了 cdn-domain 时才有意义;空则跳过,留自动检测
		var cdnBucketPath *bool
		if cdnDomain != "" {
			cdnBucketPath = promptTristateReader(reader, "CDN 域名是否已含 bucket 路径?", existing.CDNBucketPath)
		}

		cfg.Profiles[prof] = config.Profile{
			Endpoint: endpoint, AccessKey: accessKey, SecretKey: secretKey,
			Bucket: bucket, Region: region, PathStyle: pathStyle,
			CDNDomain: cdnDomain, CDNBucketPath: cdnBucketPath,
		}
		// 始终允许把本次写入的 profile 设为默认:无默认或本就是默认时默认 yes,
		// 否则默认 no(避免无意切换默认)。
		if promptBoolReader(reader, "设为默认 profile?", cfg.DefaultProfile == "" || prof == cfg.DefaultProfile) {
			cfg.DefaultProfile = prof
		}
		isDefault := cfg.DefaultProfile == prof

		content := renderConfigFile(cfg)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return fmt.Errorf("写入配置失败: %w", err)
		}
		fmt.Printf("\n配置已写入: %s\n", path)
		fmt.Print(setupSummary(prof, isDefault, cfg.Profiles[prof]))

		// 引导安装 shell 自动补全 (仅 macOS)
		if shell := detectShell(); shell != "" {
			fmt.Printf("\n检测到当前 shell: %s\n", shell)
			fmt.Print("是否安装命令自动补全? [Y/n] ")
			if confirmDefault() {
				if err := installCompletion(shell); err != nil {
					fmt.Printf("安装补全失败: %v\n", err)
				}
			} else {
				fmt.Println("跳过补全安装,之后可手动运行: sail completion " + shell)
			}
		} else if runtime.GOOS == "darwin" {
			fmt.Println("\n未检测到支持的 shell,可手动安装补全: sail completion <zsh|bash|fish>")
		}
		return nil
	},
}

// cfgSetupReset 为 true 时,setup 丢弃现有配置,重置为单 profile 的全新配置。
var cfgSetupReset bool

func init() {
	configCmd.AddCommand(configSetupCmd)
	configSetupCmd.Flags().BoolVar(&cfgSetupReset, "reset", false, "丢弃现有配置,重置为单 profile 的全新配置")
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
		display = "已配置,回车保留"
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
	fmt.Printf("%s [y/n, 回车=自动检测]: ", label)
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
	b.WriteString("# S3 兼容存储 CLI 配置\n")
	b.WriteString("# 密钥两种写法:直接明文,或 ${VAR} 引用环境变量(避免明文)。\n")
	b.WriteString("# setup 留空的密钥会生成 ${SAIL_<PROFILE>_(ACCESS|SECRET)_KEY} 占位符,也可手动改为任意 ${VAR}。\n")
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
	const cdnHint = "CDN 域名 URL 是否已含 bucket 路径:true=已含(不再追加),false=未含(总是追加)"
	cdp := ""
	if p.CDNDomain != "" {
		if p.CDNBucketPath == nil {
			cdp = fmt.Sprintf("    # cdn-bucket-path: false  # %s;注释掉(默认)则自动检测\n", cdnHint)
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
		title += " (默认)"
	}
	fmt.Fprintf(&b, "配置摘要  profile: %s\n", title)
	fmt.Fprintf(&b, "  endpoint:   %s\n", p.Endpoint)
	if p.AccessKey != "" {
		b.WriteString("  access-key: 已填写(明文)\n")
	} else {
		fmt.Fprintf(&b, "  access-key: 引用环境变量 %s (需先 export)\n", config.EnvVarName(prof, "ACCESS_KEY"))
	}
	if p.SecretKey != "" {
		b.WriteString("  secret-key: 已填写(明文)\n")
	} else {
		fmt.Fprintf(&b, "  secret-key: 引用环境变量 %s (需先 export)\n", config.EnvVarName(prof, "SECRET_KEY"))
	}
	bucket := p.Bucket
	if bucket == "" {
		bucket = "(未填)"
	}
	fmt.Fprintf(&b, "  bucket:     %s\n", bucket)
	region := p.Region
	if region == "" {
		region = "(未填,自建服务可留空)"
	}
	fmt.Fprintf(&b, "  region:     %s\n", region)
	cdn := p.CDNDomain
	if cdn == "" {
		cdn = "(未填,url 命令不可用)"
	}
	fmt.Fprintf(&b, "  cdn-domain: %s\n", cdn)

	var missing []string
	if p.AccessKey == "" {
		missing = append(missing, fmt.Sprintf("  export %s=<你的 AccessKey>", config.EnvVarName(prof, "ACCESS_KEY")))
	}
	if p.SecretKey == "" {
		missing = append(missing, fmt.Sprintf("  export %s=<你的 SecretKey>", config.EnvVarName(prof, "SECRET_KEY")))
	}
	if len(missing) > 0 {
		fmt.Fprintf(&b, "\n注意: profile %s 的密钥留空,已引用环境变量,使用前请先设置:\n", prof)
		for _, m := range missing {
			b.WriteString(m + "\n")
		}
		fmt.Fprintf(&b, "未设置时,sail 命令会报错: profile %q 缺少 access-key/secret-key\n", prof)
	}
	return b.String()
}
