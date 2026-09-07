package i18n

func init() {
	register(map[string]string{
		"S3 object storage CLI": "S3 对象存储 CLI",
		`sail is a command-line tool for S3-compatible object storage, modeled on Linux/macOS file commands.
It covers object transfer (cp/mv/rm/sync/mb/rb), listing and stats (ls/tree/find/du/stat),
content viewing (view/head/tail/wc/grep), and validation/access (checksum/presign/url).
A single static binary with zero runtime dependencies, compatible with AWS S3 / MinIO / Aliyun OSS
and self-hosted S3-compatible services.

Use --help on any command for detailed usage and examples.`: `sail 是 S3 协议对象存储的命令行工具,以 Linux/macOS 文件命令为基准。
支持对象传输(cp/mv/rm/sync/mb/rb)、检索统计(ls/tree/find/du/stat)、
内容查看(view/head/tail/wc/grep)、校验与访问(checksum/presign/url)。
单二进制零运行时依赖,兼容 AWS S3 / MinIO / 阿里云 OSS 及自建 S3 兼容服务。

所有命令统一用 --help 查看详细说明与示例。`,
		"config file path (default ~/.sail/config.yaml)": "配置文件路径 (默认 ~/.sail/config.yaml)",
		"profile to use (default default-profile)":       "使用哪个 profile (默认 default-profile)",
		"override endpoint":                              "覆盖 endpoint",
		"override default bucket":                        "覆盖默认 bucket",
		"language (en|zh; default: auto-detect)":         "语言 (en|zh;默认自动检测)",
		"no bucket specified, use s3://bucket/key or s3:///key (uses the configured default bucket)": "未指定 bucket,请用 s3://bucket/key 或 s3:///key(用配置默认 bucket)",

		// root.go — command group titles
		"Object / bucket transfer": "对象与桶操作",
		"List and stats":           "列举与统计",
		"View content":             "内容查看",
		"Checksum and access":      "校验与访问",
		"Config":                   "配置",
	})
}
