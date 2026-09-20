package cmd

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/i18n"
	"github.com/BeCrafter/sail/internal/s3path"
	"github.com/BeCrafter/sail/internal/version"
	"github.com/spf13/cobra"
)

var (
	cfgPath     string
	profile     string
	cfgBucket   string
	cfgEndpoint string
)

var rootCmd = &cobra.Command{
	Use:   "sail",
	Short: "S3 object storage CLI",
	Long: `sail is a command-line tool for S3-compatible object storage, modeled on Linux/macOS file commands.
It covers object transfer (cp/mv/rm/sync/mb/rb), listing and stats (ls/tree/find/du/stat),
content viewing (view/head/tail/wc/grep), validation/access (checksum/presign/url), and sharing
a bucket with a file manager over WebDAV (serve webdav).
A single static binary with zero runtime dependencies, compatible with AWS S3 / MinIO / Aliyun OSS
and self-hosted S3-compatible services.

Use --help on any command for detailed usage and examples.`,
	Version:      version.Version,
	SilenceUsage: true,
	CompletionOptions: cobra.CompletionOptions{
		HiddenDefaultCmd: true,
	},
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfgPath, "config", "c", "", "config file path (default ~/.config/sail/config.yaml)")
	rootCmd.PersistentFlags().StringVarP(&profile, "profile", "p", "", "profile to use (default default-profile)")
	rootCmd.PersistentFlags().StringVar(&cfgEndpoint, "endpoint", "", "override endpoint")
	rootCmd.PersistentFlags().StringVar(&cfgBucket, "bucket", "", "override default bucket")
	rootCmd.PersistentFlags().String("lang", "", "language (en|zh; default: auto-detect)")

	// 自定义 --version / -v 的输出格式,默认模板会带 "sail version" 前缀。
	rootCmd.SetVersionTemplate("sail version {{.Version}}\n")

	// 按主题分组展示。分组由各命令在自己的定义处声明 GroupID(见 cmd/*.go),
	// 不再集中在此按命令名 switch——命令与其分组同处,新增命令时不会漏改。
	// 组内按名称字母序(cobra.EnableCommandSorting)。
	cobra.EnableCommandSorting = true
	rootCmd.AddGroup(
		&cobra.Group{ID: "transfer", Title: "Object / bucket transfer"},
		&cobra.Group{ID: "list", Title: "List and stats"},
		&cobra.Group{ID: "content", Title: "View content"},
		&cobra.Group{ID: "verify", Title: "Checksum and access"},
		&cobra.Group{ID: "server", Title: "Server"},
		&cobra.Group{ID: "config", Title: "Config"},
	)
	// serve 的子命令 webdav 由 cmd/serve.go 的 init() 挂载。
	rootCmd.AddCommand(cpCmd, mvCmd, rmCmd, mkdirCmd, rmdirCmd, mbCmd, rbCmd, syncCmd, lsCmd, treeCmd, findCmd, duCmd, statCmd, viewCmd, headCmd, tailCmd, wcCmd, grepCmd, checksumCmd, presignCmd, urlCmd, serveCmd, configCmd)
	// help 归入「Config」组末尾(Additional 命令区只有内置 completion,已隐藏)
	rootCmd.SetHelpCommandGroupID("config")
	// 隐藏的诊断命令:输出顶层命令清单,供 scripts/check-readme-sync.sh
	// 做文档同步检查。以真实命令树为唯一事实来源,避免脚本里再维护一份
	// 命令名单(那本身就会漂移)。
	rootCmd.AddCommand(commandsDumpCmd)
}

// commandsDumpCmd 打印顶层命令清单,供文档同步检查使用。
// 每行 "name\tgroup\talias1,alias2"(别名可空,列数固定三列)。单独跑
// `sail __commands` 可见,不出现在根 help 里。别名入列是因为 README 里会
// 用 `sail upload` / `sail cat` 这类别名举例,校验脚本必须能把它们认成
// 真实命令,否则只能靠猜——猜错就会把合法文档报成漂移。
var commandsDumpCmd = &cobra.Command{
	Use:     "__commands",
	Short:   "print the command tree as name<TAB>group<TAB>aliases (used by doc-sync checks)",
	GroupID: "config",
	Hidden:  true,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		var lines []string
		for _, c := range rootCmd.Commands() {
			if !c.IsAvailableCommand() {
				continue
			}
			lines = append(lines, c.Name()+"\t"+c.GroupID+"\t"+strings.Join(c.Aliases, ","))
		}
		sort.Strings(lines)
		for _, l := range lines {
			fmt.Fprintln(cmd.OutOrStdout(), l)
		}
		return nil
	},
}

// Execute 运行根命令
func Execute() {
	i18n.SetLang(resolveLang())
	i18n.Apply(rootCmd)
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// langFlagFromArgs 从参数里取 --lang 的值(须在 cobra 解析前手动扫描,
// 以便 --lang zh --help 时帮助也能被翻译)。支持 --lang zh 与 --lang=zh。
func langFlagFromArgs(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--lang" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(args[i], "--lang=") {
			return args[i][len("--lang="):]
		}
	}
	return ""
}

// configFlagFromArgs 从参数里取 -c/--config 的值,供 resolveLang 读取指定路径配置的 lang 字段。
func configFlagFromArgs(args []string) string {
	for i := 0; i < len(args); i++ {
		if (args[i] == "-c" || args[i] == "--config") && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(args[i], "--config=") {
			return args[i][len("--config="):]
		}
	}
	return ""
}

// resolveLang 决定本次运行的语言。优先级:--lang 标志 > 配置文件 lang 字段 >
// 系统 LANG/LC_ALL 自动检测 > 默认英文。读配置仅取 lang 字段,不做 profile 校验;
// 失败静默回退(真正的配置错误在命令真正需要时再报)。
func resolveLang() i18n.Lang {
	if f := langFlagFromArgs(os.Args[1:]); f != "" {
		return i18n.Normalize(f)
	}
	if p := configFlagFromArgs(os.Args[1:]); p != "" {
		if c, err := config.Load(p); err == nil && c.Lang != "" {
			return i18n.Normalize(c.Lang)
		}
	} else if c, err := config.Load(""); err == nil && c.Lang != "" {
		return i18n.Normalize(c.Lang)
	}
	if l := i18n.SystemLang(); l != i18n.En {
		return l
	}
	return i18n.En
}

// loadResolved 加载配置文件并解析为最终生效的配置。
// flag > env > config 的优先级在 config.Resolve 内部处理。
func loadResolved() (*config.Resolved, *config.Config, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	r, err := cfg.Resolve(profile)
	if err != nil {
		return nil, cfg, err
	}
	if cfgEndpoint != "" {
		r.Endpoint = cfgEndpoint
	}
	if cfgBucket != "" {
		r.Bucket = cfgBucket
	}
	return r, cfg, nil
}

// parseS3 解析 s3:// 路径;若 bucket 段为空(s3:///key 形式)则用配置默认 bucket 填充。
// 桶段空且配置无默认桶时报错。s3path 包不依赖 config,故桶填充在 cmd 层做。
func parseS3(arg string, r *config.Resolved) (*s3path.S3Path, error) {
	p, err := s3path.Parse(arg)
	if err != nil {
		return nil, err
	}
	if p.Bucket == "" {
		if r == nil || r.Bucket == "" {
			return nil, errors.New(i18n.T("no bucket specified, use s3://bucket/key or s3:///key (uses the configured default bucket)"))
		}
		p.Bucket = r.Bucket
	}
	return p, nil
}
