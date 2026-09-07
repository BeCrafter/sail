package i18n

func init() {
	register(map[string]string{
		// config.go — command help metadata
		"Config management": "配置管理",
		`Manage configuration. Subcommands:
  setup   interactively generate/update the config file (default ~/.sail/config.yaml, override with -c;
          --reset starts from a fresh config; when the file already exists, adds or reconfigures one profile,
          keeping the others)
setup wizard notes:
  - endpoint is required; leaving it empty re-prompts in place
  - access-key / secret-key can be typed as plaintext; pressing Enter on empty references a per-profile
    derived env var (e.g. profile test → SAIL_TEST_ACCESS_KEY); after writing it prints the vars to export
  - when reconfiguring an existing profile, configured plaintext keys are not echoed; Enter keeps them
  - it ends with a config summary; empty fields are clearly marked for review
See the README "Configuration" section for details.`: `配置管理。子命令:
  setup   交互式生成/更新配置文件(默认 ~/.sail/config.yaml,可用 -c 指定路径;
          --reset 重置为全新配置;已有文件时新增或重配一个 profile,保留其它)
setup 向导要点:
  - endpoint 必填,留空原地重问
  - access-key / secret-key 直接输入明文;回车留空则引用按 profile 派生的环境变量
    (如 profile test → SAIL_TEST_ACCESS_KEY),写盘后打印需要 export 的变量名
  - 重配已有 profile 时,已配置的明文密钥不回显,回车即保留
  - 结尾输出配置摘要,空字段明确标注,便于核对缺失项
详见 README「配置」章节。`,
		"interactively generate/update the config file (-c path, --reset, add or reconfigure a profile)": "交互式生成/更新配置文件(支持 -c 指定路径、--reset 重置、新增或重配 profile)",
		"discard the existing config and reset to a single fresh profile":                                "丢弃现有配置,重置为单 profile 的全新配置",

		// config.go — setup wizard prompts and labels
		"profile name": "profile 名称",
		"profile %q already exists; it will be reconfigured.\n":                                         "profile %q 已存在,将重新配置。\n",
		"endpoint (required, e.g. https://<your-s3-endpoint>/)":                                         "endpoint (必填,如 https://<your-s3-endpoint>/)",
		"endpoint is required; please enter the S3-compatible service address.":                         "endpoint 为必填项,请输入 S3 兼容服务地址。",
		"endpoint left empty 3 times; exiting. Please re-run sail config setup":                         "endpoint 连续 3 次为空,已退出。请重新运行 sail config setup",
		"access-key (enter a key; press Enter on empty to reference env var %s)":                        "access-key (输入密钥;回车留空则引用环境变量 %s)",
		"secret-key (enter a key; press Enter on empty to reference env var %s)":                        "secret-key (输入密钥;回车留空则引用环境变量 %s)",
		"access-key (press Enter to keep the configured value; enter a new value or ${VAR} to replace)": "access-key (回车保留已配置值;输入新值或 ${VAR} 替换)",
		"secret-key (press Enter to keep the configured value; enter a new value or ${VAR} to replace)": "secret-key (回车保留已配置值;输入新值或 ${VAR} 替换)",
		"configured, press Enter to keep":                                                               "已配置,回车保留",
		"default bucket (can be empty)":                                                                 "默认 bucket (可留空)",
		"CDN domain (used by the url command; can be empty)":                                            "CDN 域名 (用于 url 命令,可留空)",
		"region (cloud providers fill e.g. us-east-1; self-hosted can leave empty)":                     "region (云厂商填如 us-east-1,自建服务留空)",
		"path-style (choose y for self-hosted/MinIO, n for AWS S3)":                                     "path-style (自建/MinIO 选 y,AWS S3 选 n)",
		"does the CDN domain already include the bucket path?":                                          "CDN 域名是否已含 bucket 路径?",
		"y/n, Enter=auto-detect":                                           "y/n, 回车=自动检测",
		"language (en|zh)":                                                 "语言 (en|zh)",
		"set as the default profile?":                                      "设为默认 profile?",
		"\nconfig written to: %s\n":                                        "\n配置已写入: %s\n",
		"\ndetected current shell: %s\n":                                   "\n检测到当前 shell: %s\n",
		"install shell completion? [Y/n] ":                                 "是否安装命令自动补全? [Y/n] ",
		"completion install failed: %v\n":                                  "安装补全失败: %v\n",
		"skipped completion install; install later with: sail completion ": "跳过补全安装,之后可手动运行: sail completion ",
		"\nno supported shell detected; install completion manually with: sail completion <zsh|bash|fish>": "\n未检测到支持的 shell,可手动安装补全: sail completion <zsh|bash|fish>",
		"failed to create directory: %w": "创建目录失败: %w",
		"failed to write config: %w":     "写入配置失败: %w",

		// config.go — setup summary
		" (default)":                              " (默认)",
		"config summary":                          "配置摘要",
		"set (plaintext)":                         "已填写(明文)",
		"references env var":                      "引用环境变量",
		"export it first":                         "需先 export",
		"(unset)":                                 "(未填)",
		"(unset; self-hosted may leave empty)":    "(未填,自建服务可留空)",
		"(unset; the url command is unavailable)": "(未填,url 命令不可用)",
		"<your AccessKey>":                        "<你的 AccessKey>",
		"<your SecretKey>":                        "<你的 SecretKey>",
		"note: keys for profile %s are empty and reference env vars; set them before use:": "注意: profile %s 的密钥留空,已引用环境变量,使用前请先设置:",
		"when unset, sail commands fail with: profile %q missing access-key/secret-key":    "未设置时, sail 命令会报错: profile %q 缺少 access-key/secret-key",
	})
}
