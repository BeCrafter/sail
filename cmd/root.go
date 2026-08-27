package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/s3path"
)

var (
	cfgPath     string
	profile     string
	cfgBucket   string
	cfgEndpoint string
)

var rootCmd = &cobra.Command{
	Use:          "sail",
	Short:        "S3 对象存储 CLI",
	Long:         "sail 是 S3 协议对象存储的命令行工具,支持复制/移动/列举/删除/查看/预签名,单二进制零运行时依赖。",
	SilenceUsage: true,
	CompletionOptions: cobra.CompletionOptions{
		HiddenDefaultCmd: true,
	},
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfgPath, "config", "c", "", "配置文件路径 (默认 ~/.sail/config.yaml)")
	rootCmd.PersistentFlags().StringVarP(&profile, "profile", "p", "", "使用哪个 profile (默认 default-profile)")
	rootCmd.PersistentFlags().StringVar(&cfgEndpoint, "endpoint", "", "覆盖 endpoint")
	rootCmd.PersistentFlags().StringVar(&cfgBucket, "bucket", "", "覆盖默认 bucket")

	// 关闭字母序排序,按注册顺序展示(对象操作 → 列举查看 → 访问地址 → 配置)
	cobra.EnableCommandSorting = false
	rootCmd.AddCommand(cpCmd, mvCmd, rmCmd, lsCmd, treeCmd, statCmd, viewCmd, presignCmd, urlCmd, configCmd)
}

// Execute 运行根命令
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
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
			return nil, fmt.Errorf("未指定 bucket,请用 s3://bucket/key 或 s3:///key(用配置默认 bucket)")
		}
		p.Bucket = r.Bucket
	}
	return p, nil
}
