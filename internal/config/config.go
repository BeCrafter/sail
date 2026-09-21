package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BeCrafter/sail/internal/i18n"
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
	// Serve 是 sail serve webdav 的部署参数。与连接参数同属一个 profile,
	// 由 serve 命令按「flag > serve.* > flag 默认」的优先级合并。
	Serve ServeConfig `mapstructure:"serve"`
}

// UserConfig 是 serve.users 用户表里的一名用户。字段支持 ${VAR} 环境变量
// 展开(与 serve 其余字段一致),密码可留环境变量、不落明文。
type UserConfig struct {
	Name     string `mapstructure:"name"`
	Password string `mapstructure:"password"`
	// Prefix 是相对 serve.prefix 的空间段;省略 = base 前缀本身。
	Prefix string `mapstructure:"prefix"`
	// Quota 是空间配额(如 "10GB",支持 MB/GB/TB);省略 = 不限额。语法校验见 users.go,
	// 配额执行由 P2 的 quotafs 落地。
	Quota string `mapstructure:"quota"`
}

// ServeConfig 是 serve webdav 的全部 flag 参数的配置落点。字段语义与
// cmd/serve.go 里的同名 flag 一一对应;大小类字段保持 flag 的字符串格式
// (如 "5TiB"),合并后在 cmd 层统一 parseSize。
//
// 注意:新增 serve flag 时必须同步加到这里——config.Load 按结构体反序列化,
// 结构体之外的键会在 config setup 重写文件时被静默丢弃。
type ServeConfig struct {
	Listen         string       `mapstructure:"listen"`
	Prefix         string       `mapstructure:"prefix"`
	User           string       `mapstructure:"user"`
	Password       string       `mapstructure:"password"`
	Users          []UserConfig `mapstructure:"users"`
	TLSCert        string       `mapstructure:"tls-cert"`
	TLSKey         string       `mapstructure:"tls-key"`
	StagingDir     string       `mapstructure:"staging-dir"`
	BackendMaxSize string       `mapstructure:"backend-max-object-size"`
	MaxUploadSize  string       `mapstructure:"max-upload-size"`
	ChunkedUpload  bool         `mapstructure:"chunked-upload"`
	ChunkSize      string       `mapstructure:"chunk-size"`
	// DirCacheTTL 是目录列表缓存时长(flag --dir-cache-ttl);空 = 用 flag
	// 默认(60s),"0" = 关闭缓存。
	DirCacheTTL string `mapstructure:"dir-cache-ttl"`
	// Prewarm 是后台保热的目录清单(flag --prewarm);空 = 不预热。
	// 多用户模式下同一清单会在每个用户自己的空间内生效。
	Prewarm []string `mapstructure:"prewarm"`
	// SMB 是 `serve smb` 独有的参数。协议无关的那些(prefix / users /
	// staging-dir / 上限 / 分片 / dir-cache-ttl / prewarm)两种协议共用上面
	// 同名字段 —— 同一个 bucket 的共享方式换协议时,用户表与空间划分不该跟着
	// 重写一遍;只有端口、共享名这类协议特有的才分家。
	SMB SMBConfig `mapstructure:"smb"`
}

// SMBConfig 是 serve.smb 块:`sail serve smb` 的专属参数落点。字段语义与
// cmd/serve_smb.go 的同名 flag 一一对应。
type SMBConfig struct {
	// Listen 是 SMB 的监听地址。刻意不继承 serve.listen:两种协议各跑各的
	// 进程,把它们塞到同一个端口上只会更意外。
	Listen string `mapstructure:"listen"`
	// Share 是单用户模式下的共享名(默认 "sail")。
	Share string `mapstructure:"share"`
	// ServerName 是 NTLM 挑战里服务端自称的名字(默认 "SAIL")。
	ServerName string `mapstructure:"server-name"`
}

// Config 是 ~/.config/sail/config.yaml 的整体结构
type Config struct {
	DefaultProfile string             `mapstructure:"default-profile"`
	Lang           string             `mapstructure:"lang"`
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
	Serve         ServeConfig
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

// ConfigPath 返回配置文件默认路径 ~/.config/sail/config.yaml。
// 刻意不读 $XDG_CONFIG_HOME:行为可预测优先(见 README「配置」)。
func ConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "sail", "config.yaml"), nil
}

// legacyHint 在「用的是默认路径、且迁移前的旧文件仍在」时返回一句提示,否则空串。
// 只提示、不读取旧文件:配置位置已迁移,不做静默回退(旧文件可能属于旧版本,
// 悄悄生效反而更意外)。
func legacyHint(usedPath string) string {
	def, err := ConfigPath()
	if err != nil || usedPath != def {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	old := filepath.Join(home, ".sail", "config.yaml")
	if _, err := os.Stat(old); err != nil {
		return ""
	}
	return i18n.Tf(" (note: an older config still exists at %s; the default location is now %s — move it over to keep your profiles)", old, def)
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
		base := fmt.Errorf(i18n.T("read config %s failed: %w"), path, err)
		if hint := legacyHint(path); hint != "" {
			return nil, fmt.Errorf("%w%s", base, hint)
		}
		return nil, base
	}
	var c Config
	if err := v.Unmarshal(&c); err != nil {
		return nil, fmt.Errorf(i18n.T("parse config failed: %w"), err)
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
		return nil, fmt.Errorf(i18n.T("profile %q not found in config file"), profile)
	}

	var users []UserConfig
	for _, u := range p.Serve.Users {
		users = append(users, UserConfig{
			Name:     expandEnv(u.Name),
			Password: expandEnv(u.Password),
			Prefix:   expandEnv(u.Prefix),
			Quota:    expandEnv(u.Quota),
		})
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
		Serve: ServeConfig{
			Listen:         expandEnv(p.Serve.Listen),
			Prefix:         expandEnv(p.Serve.Prefix),
			User:           expandEnv(p.Serve.User),
			Password:       expandEnv(p.Serve.Password),
			Users:          users,
			TLSCert:        expandEnv(p.Serve.TLSCert),
			TLSKey:         expandEnv(p.Serve.TLSKey),
			StagingDir:     expandEnv(p.Serve.StagingDir),
			BackendMaxSize: expandEnv(p.Serve.BackendMaxSize),
			MaxUploadSize:  expandEnv(p.Serve.MaxUploadSize),
			ChunkedUpload:  p.Serve.ChunkedUpload,
			ChunkSize:      expandEnv(p.Serve.ChunkSize),
			DirCacheTTL:    expandEnv(p.Serve.DirCacheTTL),
			Prewarm:        expandEnvList(p.Serve.Prewarm),
			SMB: SMBConfig{
				Listen:     expandEnv(p.Serve.SMB.Listen),
				Share:      expandEnv(p.Serve.SMB.Share),
				ServerName: expandEnv(p.Serve.SMB.ServerName),
			},
		},
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
		return nil, fmt.Errorf(i18n.T("profile %q missing endpoint"), profile)
	}
	if r.AccessKey == "" || r.SecretKey == "" {
		return nil, fmt.Errorf(i18n.T("profile %q missing access-key/secret-key"), profile)
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

// expandEnvList 对 []string 逐项做 ${VAR} 展开,供 serve.prewarm 这类列表字段使用。
func expandEnvList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, expandEnv(s))
	}
	return out
}
