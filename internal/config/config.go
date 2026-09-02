package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/viper"
)

// Profile 是单个存储服务的配置档 (prod / test / internal)
type Profile struct {
	Endpoint  string `mapstructure:"endpoint"`
	AccessKey string `mapstructure:"access-key"`
	SecretKey string `mapstructure:"secret-key"`
	Bucket    string `mapstructure:"bucket"`
	Region    string `mapstructure:"region"`
	PathStyle bool   `mapstructure:"path-style"`
	CDNDomain string `mapstructure:"cdn-domain"`
	// CDNBucketPath 显式声明 cdn-domain 是否已含 bucket 路径。
	// nil = 自动检测;true = 已含(不再追加);false = 未含(总是追加)。
	CDNBucketPath *bool `mapstructure:"cdn-bucket-path"`
}

// Config 是 ~/.sail/config.yaml 的整体结构
type Config struct {
	DefaultProfile string             `mapstructure:"default-profile"`
	Profiles       map[string]Profile `mapstructure:"profiles"`
}

// Resolved 是最终生效的、供 client 使用的配置
type Resolved struct {
	ProfileName   string
	Endpoint      string
	AccessKey     string
	SecretKey     string
	Bucket        string
	Region        string
	PathStyle     bool
	CDNDomain     string
	CDNBucketPath *bool
}

var envVarPattern = regexp.MustCompile(`\$\{([A-Z0-9_]+)\}`)

// EnvVarName 按 profile 派生环境变量名:SAIL_<清洗后PROFILE>_<FIELD>。
// profile 名大写后仅保留 [A-Z0-9],其余字符视为分隔符并压缩为单个 _(去首尾);
// 清洗后为空则回退为 SAIL_<FIELD>。结果只含 [A-Z0-9_],必可被 envVarPattern 展开。
func EnvVarName(profile, field string) string {
	var b strings.Builder
	prevSep := true // 起始视为分隔符,自然去掉首部 _
	for _, r := range strings.ToUpper(profile) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			if prevSep {
				b.WriteByte('_')
			}
			b.WriteRune(r)
			prevSep = false
		} else {
			prevSep = true
		}
	}
	if s := b.String(); s != "" {
		return "SAIL" + s + "_" + field
	}
	return "SAIL_" + field
}

// ConfigPath 返回配置文件默认路径 ~/.sail/config.yaml
func ConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".sail", "config.yaml"), nil
}

// Load 从给定路径加载配置;path 为空则用默认路径。
// 返回未解析环境变量的原始 Config。
func Load(path string) (*Config, error) {
	if path == "" {
		p, err := ConfigPath()
		if err != nil {
			return nil, err
		}
		path = p
	}
	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("读取配置 %s 失败: %w", path, err)
	}
	var c Config
	if err := v.Unmarshal(&c); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}
	return &c, nil
}

// Resolve 根据 profile 名解析出最终配置。优先级:环境变量 > 配置文件。
// profile 为空时取 default-profile。
func (c *Config) Resolve(profile string) (*Resolved, error) {
	if profile == "" {
		profile = c.DefaultProfile
		if profile == "" {
			profile = "prod"
		}
	}
	p, ok := c.Profiles[profile]
	if !ok {
		return nil, fmt.Errorf("profile %q 不存在于配置文件中", profile)
	}

	r := &Resolved{
		ProfileName:   profile,
		Endpoint:      expandEnv(p.Endpoint),
		AccessKey:     expandEnv(p.AccessKey),
		SecretKey:     expandEnv(p.SecretKey),
		Bucket:        expandEnv(p.Bucket),
		Region:        p.Region,
		PathStyle:     p.PathStyle,
		CDNDomain:     expandEnv(p.CDNDomain),
		CDNBucketPath: p.CDNBucketPath,
	}

	// 环境变量覆盖
	if v := os.Getenv("SAIL_ENDPOINT"); v != "" {
		r.Endpoint = v
	}
	if v := os.Getenv("SAIL_ACCESS_KEY"); v != "" {
		r.AccessKey = v
	}
	if v := os.Getenv("SAIL_SECRET_KEY"); v != "" {
		r.SecretKey = v
	}
	if v := os.Getenv("SAIL_BUCKET"); v != "" {
		r.Bucket = v
	}
	if v := os.Getenv("SAIL_CDN_DOMAIN"); v != "" {
		r.CDNDomain = v
	}

	if r.Endpoint == "" {
		return nil, fmt.Errorf("profile %q 缺少 endpoint", profile)
	}
	if r.AccessKey == "" || r.SecretKey == "" {
		return nil, fmt.Errorf("profile %q 缺少 access-key/secret-key", profile)
	}
	// path-style 默认 true (自建 S3 兼容服务常用)
	if !r.PathStyle {
		r.PathStyle = true
	}
	return r, nil
}

// expandEnv 把 ${VAR} 替换为对应环境变量值;未设置则保留空串。
func expandEnv(s string) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(m string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(m, "${"), "}")
		return os.Getenv(name)
	})
}
