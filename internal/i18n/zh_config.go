package i18n

func init() {
	register(map[string]string{
		// config.go — command help metadata
		"Config management": "配置管理",
		`Manage configuration. Subcommands:
  setup   interactively generate/update the config file (default ~/.config/sail/config.yaml, override with -c;
          --reset starts from a fresh config; when the file already exists, adds or reconfigures one profile,
          keeping the others)
setup wizard notes:
  - endpoint is required; leaving it empty re-prompts in place
  - access-key / secret-key can be typed as plaintext; pressing Enter on empty references a per-profile
    derived env var (e.g. profile test → SAIL_TEST_ACCESS_KEY); after writing it prints the vars to export
  - when reconfiguring an existing profile, configured plaintext keys are not echoed; Enter keeps them
  - the gateway (serve block) is guided as well, in layers. It is shared by "sail serve webdav" and
    "sail serve smb", so the wizard asks which services to configure (webdav | smb | both) first, then walks
    the layers in order: the shared settings (prefix, auth mode — single-user or multi-user — chunked-upload,
    staging-dir), then each selected service's own settings (WebDAV: listen, TLS; SMB: listen, share,
    server-name). Multi-user tables are validated in place (duplicate names, nested prefixes, quota syntax —
    quota accepts MB/GB/TB). An existing serve block defaults to "keep" — choose append to add users to the
    existing table, reconfigure to edit it, or remove to drop it. A service that is not selected is left as
    configured, never cleared. Size limits, dir-cache-ttl and prewarm are not asked here; edit the config file
    to change them
  - inputs are normalized where possible: a bare port gets its colon (8443 -> :8443), a URL without a
    scheme gets https://, single-letter quota units become MB/GB/TB, ~ is expanded in paths, and yes/no
    answers accept yes/true/1/on (the service and auth-mode choices take their own keywords); invalid values
    are re-prompted with an explanation
  - it ends with a config summary; empty fields are clearly marked for review
See the README "Configuration" section for details.`: `配置管理。子命令:
  setup   交互式生成/更新配置文件(默认 ~/.config/sail/config.yaml,可用 -c 指定路径;
          --reset 重置为全新配置;已有文件时新增或重配一个 profile,保留其它)
setup 向导要点:
  - endpoint 必填,留空原地重问
  - access-key / secret-key 直接输入明文;回车留空则引用按 profile 派生的环境变量
    (如 profile test → SAIL_TEST_ACCESS_KEY),写盘后打印需要 export 的变量名
  - 重配已有 profile 时,已配置的明文密钥不回显,回车即保留
  - 网关(serve 块)同样引导配置,且是分层的。该块由 "sail serve webdav" 与 "sail serve smb" 共用:
    向导先问要配置哪些服务(webdav | smb | both),再按层依次走——通用设置(前缀、认证模式
    single/multi、chunked-upload、staging-dir),然后逐个服务问各自的专属设置(WebDAV: listen、TLS;
    SMB: listen、share、server-name)。多用户表就地校验(重名、前缀嵌套、quota 语法——quota 支持
    MB/GB/TB)。已有 serve 块默认保留,可选追加用户、重新配置或删除。未被选中的服务按原配置保留,
    绝不会被清空。尺寸上限、dir-cache-ttl 与 prewarm 不在向导内提问,如需修改请编辑配置文件
  - 输入会尽量归一化: 裸端口补冒号(8443 -> :8443)、URL 缺协议头补 https://、quota 单字母单位
    补全为 MB/GB/TB、路径里的 ~ 自动展开、yes/no 回答接受 yes/true/1/on(服务与认证模式选择有各自的
    关键字);非法值会说明原因后重问
  - 结尾输出配置摘要,空字段明确标注,便于核对缺失项
详见 README「配置」章节。`,
		"interactively generate/update the config file (-c path, --reset, add or reconfigure a profile)": "交互式生成/更新配置文件(支持 -c 指定路径、--reset 重置、新增或重配 profile)",
		"discard the existing config and reset to a single fresh profile":                                "丢弃现有配置,重置为单 profile 的全新配置",

		// config.go — setup wizard prompts and labels
		"profile name": "profile 名称",
		"profile %q already exists; it will be reconfigured.\n":                                            "profile %q 已存在,将重新配置。\n",
		"endpoint (required, e.g. https://<your-s3-endpoint>/)":                                            "endpoint (必填,如 https://<your-s3-endpoint>/)",
		"endpoint is required; please enter the S3-compatible service address.":                            "endpoint 为必填项,请输入 S3 兼容服务地址。",
		"endpoint left empty 3 times; exiting. Please re-run sail config setup":                            "endpoint 连续 3 次为空,已退出。请重新运行 sail config setup",
		"access-key (enter a key; press Enter on empty to reference env var %s)":                           "access-key (输入密钥;回车留空则引用环境变量 %s)",
		"secret-key (enter a key; press Enter on empty to reference env var %s)":                           "secret-key (输入密钥;回车留空则引用环境变量 %s)",
		"access-key (press Enter to keep the configured value; enter a new value or ${VAR} to replace)":    "access-key (回车保留已配置值;输入新值或 ${VAR} 替换)",
		"secret-key (press Enter to keep the configured value; enter a new value or ${VAR} to replace)":    "secret-key (回车保留已配置值;输入新值或 ${VAR} 替换)",
		"configured, press Enter to keep":                                                                  "已配置,回车保留",
		"default bucket (can be empty)":                                                                    "默认 bucket (可留空)",
		"CDN domain (used by the url command; can be empty)":                                               "CDN 域名 (用于 url 命令,可留空)",
		"region (cloud providers fill e.g. us-east-1; self-hosted can leave empty)":                        "region (云厂商填如 us-east-1,自建服务留空)",
		"path-style (choose y for self-hosted/MinIO, n for AWS S3)":                                        "path-style (自建/MinIO 选 y,AWS S3 选 n)",
		"does the CDN domain already include the bucket path?":                                             "CDN 域名是否已含 bucket 路径?",
		"y/n, Enter=auto-detect":                                                                           "y/n, 回车=自动检测",
		"language (en|zh)":                                                                                 "语言 (en|zh)",
		"set as the default profile?":                                                                      "设为默认 profile?",
		"\nconfig written to: %s\n":                                                                        "\n配置已写入: %s\n",
		"\ndetected current shell: %s\n":                                                                   "\n检测到当前 shell: %s\n",
		"install shell completion? [Y/n] ":                                                                 "是否安装命令自动补全? [Y/n] ",
		"completion install failed: %v\n":                                                                  "安装补全失败: %v\n",
		"skipped completion install; install later with: sail completion ":                                 "跳过补全安装,之后可手动运行: sail completion ",
		"\nno supported shell detected; install completion manually with: sail completion <zsh|bash|fish>": "\n未检测到支持的 shell,可手动安装补全: sail completion <zsh|bash|fish>",
		"failed to create directory: %w":                                                                   "创建目录失败: %w",
		"failed to write config: %w":                                                                       "写入配置失败: %w",

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

		// config_serve.go — setup wizard: serve block (shared by serve webdav / serve smb)
		`configure the serve block (shared by "sail serve webdav" and "sail serve smb")?`: `是否为 "sail serve webdav" 与 "sail serve smb" 共用的 serve 块做配置?`,
		"serve block: Enter=keep, r=reconfigure, d=remove":                                "serve 块: 回车=保留, r=重新配置, d=删除",
		"serve block: Enter=keep, a=append users, r=reconfigure, d=remove":                "serve 块: 回车=保留, a=追加用户, r=重新配置, d=删除",
		"no users table to append to; choose r to configure multi-user mode":              "当前没有用户表可追加;如需多用户请选 r 重新配置",
		"append users to the existing table:":                                             "在现有用户表基础上追加用户:",
		"serve block updated (users appended); all other serve fields are kept as-is":     "serve 块已更新(用户已追加);其余 serve 字段原样保留",
		"  - name=%s prefix=%s quota=%s\n":                                                "  - 用户名=%s 前缀=%s 配额=%s\n",
		"unlimited":                                                                       "不限额",
		"(base prefix)":                                                                   "(base 前缀)",
		"each user has: name (login name, unique), password (login password), prefix (their private space under the shared prefix), quota (space limit for their objects)": "每个用户包含: name(登录名,不可重复)、password(登录密码)、prefix(该用户的私有空间,相对共享前缀)、quota(其对象的空间上限)",
		"quota units: MB / GB / TB (e.g. 500MB, 10GB, 1TB; a plain number means bytes)":                                                                                    "quota 单位: MB / GB / TB(如 500MB、10GB、1TB;纯数字表示字节数)",
		"unrecognized answer %q; please answer y or n\n":                                                                                                                   "无法识别的回答 %q;请输入 y 或 n\n",
		"note: endpoint has no scheme; using %s\n":                                                                                                                         "注意: endpoint 未带协议头,已按 %s 处理\n",
		"note: CDN domain has no scheme; using %s\n":                                                                                                                       "注意: CDN 域名未带协议头,已按 %s 处理\n",
		"invalid listen address: %v\n":                                                                                                                                     "监听地址不合法: %v\n",
		"note: normalized listen address to %s\n":                                                                                                                          "注意: 监听地址已归一化为 %s\n",
		"%q has no port; use [host]:port, e.g. :8443":                                                                                                                      "%q 缺少端口;请用 [host]:port 形式,如 :8443",
		"port %q must be a number between 1 and 65535":                                                                                                                     "端口 %q 必须是 1-65535 之间的数字",
		"note: expanded ~ to %s\n":                                                                                                                                         "注意: 已将 ~ 展开为 %s\n",
		"warning: %s does not exist; sail serve webdav will fail to start until the file is in place\n":                                                                    "警告: %s 不存在;sail serve webdav 启动会失败,直到该文件就位",
		"note: user prefix is relative to serve.prefix; using %q\n":                                                                                                        "注意: 用户 prefix 是相对 serve.prefix 的路径,已按 %q 处理\n",
		"note: quota unit normalized to %s\n":                                                                                                                              "注意: quota 单位已补全为 %s\n",
		"serve block removed; serve webdav / serve smb will fall back to flags (edit the config file to add one later)":                                                    "serve 块已删除;serve webdav / serve smb 将回退到 flag 默认(之后可手工编辑配置文件重新添加)",
		"serve block kept as-is":                                                                                                                                           "serve 块原样保留",
		"listen address (empty = flag default %s)":                                                                                                                         "listen 监听地址 (留空=flag 默认 %s)",
		"shared prefix mapped to / (empty = the whole bucket)":                                                                                                             "共享前缀(映射为 /,留空=整个 bucket)",
		"shared settings (used by both services)":                                                                                                                          "通用设置(两种服务共用)",
		"WebDAV settings": "WebDAV 设置",
		"SMB settings":    "SMB 设置",
		"auth mode (single = one account for the whole space; multi = serve.users, one private space per user)":                             "认证模式 (single = 一个账号覆盖整个共享空间;multi = serve.users,每人一个独立空间)",
		"unrecognized answer %q; please answer single or multi\n":                                                                           "无法识别的回答 %q;请输入 single 或 multi\n",
		"warning: this profile sets both serve.users and user/password (mutually exclusive); the mode you pick below clears the other side": "警告: 该 profile 同时配置了 serve.users 与 user/password(二者互斥);下面选定的模式会清空另一侧",
		"note: switching to single-user clears the serve.users table; switching to multi-user clears user/password":                         "注意: 切换到单用户会清空 serve.users 表;切换到多用户会清空 user/password",
		"user (login name for serve webdav / serve smb; empty = configure later)":                                                           "user (serve webdav / serve smb 的登录用户名;留空=之后配置)",
		"password for the user (plaintext or ${VAR}; empty = configure later)":                                                              "用户的 password (明文或 ${VAR},留空=之后配置)",
		"keep the %d existing user(s)?":                                                                                         "是否保留已有的 %d 名用户?",
		"no users added; fall back to single-user authentication?":                                                              "未添加任何用户,是否回退到单用户认证?",
		"no users added after 3 attempts; re-run sail config setup or edit the config file manually":                            "连续 3 次未添加用户,已放弃;请重新运行 sail config setup 或手工编辑配置文件",
		"name (login username; Enter on empty to finish adding users)":                                                          "name (登录用户名;留空回车结束添加)",
		"password for user %s (login password; plaintext or ${VAR} env reference)":                                              "用户 %s 的 password (登录密码;明文或 ${VAR} 引用环境变量)",
		"prefix for user %s (this user's private space, relative to serve.prefix, e.g. alice/; empty = the base prefix itself)": "用户 %s 的 prefix (该用户的私有空间,相对 serve.prefix,如 alice/;留空=base 前缀本身)",
		"quota for user %s (space limit for this user's objects, e.g. 500MB or 10GB; empty = unlimited)":                        "用户 %s 的 quota (该用户对象的空间上限,如 500MB 或 10GB;留空=不限额)",
		"quota %s = %d bytes\n": "quota %s = %d 字节\n",
		"invalid quota: %v\n":   "quota 不合法: %v\n",
		"user not added: %v\n":  "用户未添加: %v\n",
		"user table is still invalid after 3 attempts (%v); re-run sail config setup or edit the config file manually": "用户表连续 3 次校验失败 (%v);请重新运行 sail config setup 或手工编辑配置文件",
		"tls-cert (path to the certificate; empty = serve over HTTP)":                                                  "tls-cert (证书路径;留空=以 HTTP 提供服务)",
		"tls-key (path to the private key; required together with tls-cert)":                                           "tls-key (私钥路径;与 tls-cert 需成对提供)",
		"chunked-upload (store files over chunk-size as chunks + a manifest)?":                                         "是否启用 chunked-upload (超过 chunk-size 的文件按分片+manifest 存储)?",
		"staging-dir (write staging directory; empty = the system temp dir)":                                           "staging-dir (写入暂存目录;留空=系统临时目录)",
		"(unset; serve webdav / smb use flag defaults)":                                                                "(未配置,serve webdav / serve smb 使用 flag 默认值)",
		"%d user(s)":     "%d 名用户",
		"single user %s": "单用户 %s",
		"(no credentials; serve will refuse to start)":                                                                                                                        "(无凭据,serve 将拒绝启动)",
		"protocols to configure (webdav|smb|both)":                                                                                                                            "要配置哪些协议 (webdav|smb|both)",
		"unrecognized answer %q; please answer webdav, smb or both\n":                                                                                                         "无法识别的回答 %q;请输入 webdav、smb 或 both\n",
		"note: WebDAV listen/TLS keep their configured values; they are not asked while WebDAV is not selected":                                                               "注意: WebDAV 的 listen/TLS 按原配置保留;未选中 WebDAV 时不会提问这两项",
		"note: SMB listen/share/server-name keep their configured values; they are not asked while SMB is not selected":                                                       "注意: SMB 的 listen/share/server-name 按原配置保留;未选中 SMB 时不会提问这三项",
		"SMB share name for single-user mode (empty = flag default %s)":                                                                                                       "SMB 共享名(单用户模式;留空=flag 默认 %s)",
		"SMB server name (the name this server calls itself in the NTLM challenge; empty = flag default %s)":                                                                  "SMB 服务端名(NTLM 挑战里服务端自称的名字,客户端界面会显示;留空=flag 默认 %s)",
		"invalid share name %q: SMB share names must not contain a path separator\n":                                                                                          "共享名 %q 不合法:SMB 共享名里不能含路径分隔符\n",
		"note: backend-max-object-size / max-upload-size / chunk-size / dir-cache-ttl / prewarm are kept as configured (not asked here); edit the config file to change them": "注意: backend-max-object-size / max-upload-size / chunk-size / dir-cache-ttl / prewarm 保留原配置(不在向导内提问);如需修改请编辑配置文件",

		// users.go — serve.users validation
		"serve.users[%d]: name is required":                                                                "serve.users[%d]: 缺少 name",
		"serve.users[%d] (%q): name must not contain \":\" (Basic auth userinfo separator)":                "serve.users[%d] (%q): 用户名不能包含 \":\"(Basic 认证的用户名分隔符)",
		"serve.users[%d]: duplicate name %q":                                                               "serve.users[%d]: 用户名 %q 重复",
		"serve.users[%d] (%q): password is required (it may reference an environment variable via ${VAR})": "serve.users[%d] (%q): 缺少 password(可通过 ${VAR} 引用环境变量)",
		"serve.users[%d] (%q): invalid prefix: %v":                                                         "serve.users[%d] (%q): prefix 不合法: %v",
		"serve.users[%d] (%q): invalid quota: %v":                                                          "serve.users[%d] (%q): quota 不合法: %v",
		"users %q and %q resolve to the same prefix %q; each user must own a distinct space":               "用户 %q 与 %q 解析到同一前缀 %q;每个用户必须拥有独立空间",
		"prefix %q (user %q) nests inside %q (user %q): the outer user would list the inner user's objects; nested prefixes are rejected at startup": "前缀 %q(用户 %q)嵌套在 %q(用户 %q)之内:外层用户会列举到内层用户的对象,嵌套前缀在启动时被拒绝",
		"prefix %q must be relative to serve.prefix (drop the leading \"/\")":                                                                        "prefix %q 必须是相对 serve.prefix 的相对路径(去掉开头的 \"/\")",
		"prefix %q must not contain \"..\" segments":                                                                                                 "prefix %q 不能包含 \"..\" 段",
		"quota is empty":               "配额为空",
		"quota %q is missing a number": "配额 %q 缺少数字",
		"failed to parse quota %q: %v": "配额 %q 解析失败: %v",
		"unrecognized quota unit in %q (supported: MB/GB/TB; a plain number means bytes)": "配额 %q 的单位无法识别(支持: MB/GB/TB;纯数字表示字节数)",
		"quota %q must be greater than zero":                                              "配额 %q 必须大于零",
	})
}
