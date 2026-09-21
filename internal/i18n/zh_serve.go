package i18n

func init() {
	register(map[string]string{
		// serve.go — command help metadata
		"Start a server that shares a bucket over a standard protocol":                       "启动服务(把 bucket 通过标准协议共享)",
		"Share a bucket over WebDAV (mountable directly by macOS Finder / Windows Explorer)": "以 WebDAV 协议共享 bucket(macOS Finder / Windows 资源管理器可直接挂载)",
		`Share the whole bucket over the WebDAV protocol (or the prefix given by --prefix); which
bucket that is comes from the profile, so name it with --profile (the global --bucket flag and
SAIL_BUCKET still override it when needed). Clients mount it with capabilities built into the OS,
no software to install.

Design boundaries:
  - Uploads land in full on the --staging-dir staging disk and are only chunked and uploaded to
    S3 on commit; staging peak is about one file's size x concurrent uploads, so point it at a
    disk with enough room.
  - The server deliberately offers no flag to "raise the Windows 50MB gate": that gate lives in
    the client registry and no server-side flag can move it. Use --print-windows-setup for the
    client-side procedure.
  - LOCK is an in-process lock, lost on restart and not shared across instances.
  - Directory-level MOVE/COPY returns 501, leaving the client to fall back to "copy + delete".
  - With --chunked-upload on, files larger than --chunk-size are stored as chunks under the
    reserved .sail/ prefix plus a small manifest object at the logical key: the bucket then
    contains .sail/ objects, and "sail presign" fails loud on such a bucket because a presigned
    URL would hand out the manifest instead of the file.

Examples:
  sail serve webdav --profile prod --listen :8443 \
    --user alice --password '***' --tls-cert c.pem --tls-key k.pem
  sail serve webdav --print-windows-setup`: `以 WebDAV 协议共享整个 bucket(或 --prefix 指定的前缀);共享哪个 bucket 由
--profile 决定(全局 --bucket 与 SAIL_BUCKET 需要时仍可覆盖),客户端用系统自带能力挂载,无需安装任何软件。

设计边界:
  - 上传先落 --staging-dir 暂存盘,提交时才切块上传到 S3;暂存盘峰值
    ≈ 单文件大小 × 并发上传数,建议指向空间足够的磁盘。
  - 服务端不提供"抬高 Windows 50MB 闸门"的参数:那道闸门在客户端注册表,
    服务端改了不生效。用 --print-windows-setup 拿到客户端侧的配置。
  - LOCK 是进程内锁,重启即失效,不跨实例。
  - 目录级 MOVE/COPY 返回 501,由客户端退化为"复制 + 删除"。
  - 开启 --chunked-upload 后,超过 --chunk-size 的文件会拆成保留前缀 .sail/ 下的分片,
    并在逻辑 key 上放一个小的 manifest 对象:桶内因此出现 .sail/ 对象,且
    "sail presign" 会报错拒绝(预签名 URL 只会给到 manifest,不是文件本身)。

示例:
  sail serve webdav --profile prod --listen :8443 \
    --user alice --password '***' --tls-cert c.pem --tls-key k.pem
  sail serve webdav --print-windows-setup`,

		// serve.go — flags
		"listen address": "监听地址",
		"shared root prefix (mapped to /); out-of-prefix paths are always rejected":                                    "共享根前缀(映射为 /);越界路径一律拒绝",
		"Basic auth username (required)":                                                                               "Basic 认证用户名(必填)",
		"Basic auth password (required)":                                                                               "Basic 认证密码(必填)",
		"TLS certificate file (supplying it together with --tls-key enables HTTPS)":                                    "TLS 证书文件(与 --tls-key 同时提供即启用 HTTPS)",
		"TLS private key file (supplying it together with --tls-cert enables HTTPS)":                                   "TLS 私钥文件(与 --tls-cert 同时提供即启用 HTTPS)",
		"declared backend per-object limit (S3 has no capability negotiation, it can't be probed)":                     "声明的后端单对象上限(S3 无能力协商,不可探测)",
		"request body limit, defaults to --backend-max-object-size; over the limit returns 413":                        "请求体上限,默认跟随 --backend-max-object-size;超限返回 413",
		"write staging directory, defaults to the system temp dir; peak is about one file's size x concurrent uploads": "写暂存目录,默认系统临时目录;峰值 ≈ 单文件大小 × 并发上传数",
		"store files larger than --chunk-size as chunks plus a manifest (default off: 1 file = 1 object)":              "把超过 --chunk-size 的文件拆成分片 + manifest 存储(默认关:1 文件 = 1 对象)",
		"max physical chunk size and the chunked-storage threshold (5MiB ~ 5GiB); requires --chunked-upload":           "单个物理片上限,同时是分片阈值(5MiB ~ 5GiB);需配合 --chunked-upload",
		"how long a directory listing is cached (e.g. 60s, 10m; 0 disables); expired entries are served stale while refreshing in the background, so a warm directory never blocks. External bucket changes become visible after at most this long": "目录列表缓存时长(如 60s、10m;0 关闭);过期条目会先返回旧值再后台刷新,热目录永不阻塞。外部对桶的改动最长该时长后可见",
		"directories to keep hot in the background (comma-separated logical paths, e.g. /yiche,/modelImage); each is listed once at startup then refreshed, so the first visit does not pay the full listing cost":                                  "需要后台保热的目录(逗号分隔的逻辑路径,如 /yiche,/modelImage);启动时各列一次后按周期刷新,首次访问不再承担完整列举的开销",
		"print the Windows client registry setup and mount command, then exit": "打印 Windows 客户端注册表配置与挂载命令后退出",

		// serve.go — runtime messages
		"--user and --password are required (from flags or profile %q \"serve\" config): this gateway does not allow anonymous sharing": "--user 与 --password 必填(来自 flag 或 profile %q 的 \"serve\" 配置):本网关不允许匿名共享",
		"--tls-cert and --tls-key must be supplied together":                                                                            "--tls-cert 与 --tls-key 必须同时提供",
		"invalid --backend-max-object-size: %w":                                                                                         "--backend-max-object-size 非法: %w",
		"invalid --max-upload-size: %w":                                                                                                 "--max-upload-size 非法: %w",
		"invalid --chunk-size: %v":                                                                                                      "--chunk-size 非法: %v",
		"--chunk-size must be between %s and %s, got %s":                                                                                "--chunk-size 必须在 %s 与 %s 之间,当前 %s",
		"--chunk-size (%s) exceeds --backend-max-object-size (%s): raise the backend limit or lower the chunk size":                     "--chunk-size(%s)超过 --backend-max-object-size(%s):请调高后端上限或调小片大小",
		"sail webdav started: %s://%s  bucket=%s profile=%s%s user=%s max-object-size=%s staging=%s chunked=%s\n":                       "sail webdav 已启动: %s://%s  bucket=%s profile=%s%s user=%s 单对象上限=%s 暂存=%s 分片=%s\n",
		"  mount at: %s\n": "  挂载地址: %s\n",
		"  (localhost = this machine; LAN IP = other devices)\n":                                                                               "  (localhost = 本机;局域网 IP = 其它设备)\n",
		"  users come from --user/--password flags: config file changes will NOT hot-apply; restart to change users\n":                         "  用户来自 --user/--password flag:配置文件变更不会热生效,增删用户需重启\n",
		"  no config file path resolved: hot reload disabled\n":                                                                                "  未解析到配置文件路径:热加载已关闭\n",
		"serve.users and user/password are mutually exclusive (profile %q): configure either the users list or the single-user pair, not both": "serve.users 与 user/password 互斥(profile %q):两者只能配置其一",
		"config change detected: reloading user table":                                                                                         "检测到配置变更:正在重载用户表",
		"reload rejected, keeping previous user table: %v":                                                                                     "重载被拒绝,沿用原用户表: %v",
		"user table reloaded: %d user(s), %d stack(s), gen=%d":                                                                                 "用户表已重载: %d 名用户, %d 个栈, gen=%d",
		"invalid --dir-cache-ttl: %w":                                                                                                          "非法的 --dir-cache-ttl: %w",
		"no bucket to share: pass --bucket, set SAIL_BUCKET, or add \"bucket\" to profile %q in the config file — WebDAV exposes a whole bucket, and without one there is nothing to share": "没有可共享的 bucket:请传 --bucket、设置 SAIL_BUCKET,或在配置文件里给 profile %q 添加 \"bucket\" —— WebDAV 共享的是整桶,没有桶就无从共享",
		"config change on %q is a cold-zone field and requires a restart to take effect":                                                                                                    "配置项 %q 属冷区,需重启才能生效",
		"WARN: creating directory for user space %q failed (users still work; retried on next reload): %v":                                                                                  "WARN: 为用户空间 %q 创建目录失败(不影响用户使用;下次 reload 会重试): %v",
		"directory created for user space: %s/":                                        "已为用户空间创建目录: %s/",
		"WARN: hot reload unavailable (fsnotify error: %v); will retry":                "警告: 热加载不可用(fsnotify 错误: %v);稍后重试",
		"WARN: cannot watch config directory %s: %v; will retry":                       "警告: 无法监听配置目录 %s: %v;稍后重试",
		"WARN: config watcher lost (directory replaced or watcher closed); rebuilding": "警告: 配置监听已失效(目录被替换或监听通道关闭);正在重建",
		"WARN: config watch error: %v":                                                 "WARN: 配置监听出错: %v",

		// serve.go — --print-windows-setup output
		`Mount a sail WebDAV drive in Windows Explorer
=============================================

1) Raise the WebClient upload limit (about 50MB by default)

   This gate lives in the Windows client registry; changing server-side flags has no effect.
   Create upgrade-webclient.reg and double-click to import (administrator required):

Windows Registry Editor Version 5.00

[HKEY_LOCAL_MACHINE\SYSTEM\CurrentControlSet\Services\WebClient\Parameters]
"FileSizeLimitInBytes"=dword:ffffffff
"BasicAuthLevel"=dword:00000002

   Or do it in one go from an administrator PowerShell:

Set-ItemProperty -Path "HKLM:\SYSTEM\CurrentControlSet\Services\WebClient\Parameters" -Name FileSizeLimitInBytes -Value 4294967295 -Type DWord
Set-ItemProperty -Path "HKLM:\SYSTEM\CurrentControlSet\Services\WebClient\Parameters" -Name BasicAuthLevel -Value 2 -Type DWord

2) The WebClient service must be restarted for the new limit to take effect

Restart-Service WebClient
# If it reports the service is missing or cannot restart, use instead:
net stop webclient
net start webclient

3) Mount the drive

   HTTPS (recommended, Basic credentials never cross the network in the clear):
     net use Z: \\<host>@SSL@%s\DavWWWRoot /user:<username>

   HTTP (only on a trusted intranet):
     net use Z: \\<host>@%s\DavWWWRoot /user:<username>

   Unmount: net use Z: /delete
`: `Windows 资源管理器挂载 sail WebDAV 网盘
========================================

1) 提高 WebClient 的上传上限(默认约 50MB)

   这是 Windows 客户端注册表里的闸门,改服务端参数不会生效。
   新建 upgrade-webclient.reg,双击导入(需管理员):

Windows Registry Editor Version 5.00

[HKEY_LOCAL_MACHINE\SYSTEM\CurrentControlSet\Services\WebClient\Parameters]
"FileSizeLimitInBytes"=dword:ffffffff
"BasicAuthLevel"=dword:00000002

   或者用管理员 PowerShell 一次到位:

Set-ItemProperty -Path "HKLM:\SYSTEM\CurrentControlSet\Services\WebClient\Parameters" -Name FileSizeLimitInBytes -Value 4294967295 -Type DWord
Set-ItemProperty -Path "HKLM:\SYSTEM\CurrentControlSet\Services\WebClient\Parameters" -Name BasicAuthLevel -Value 2 -Type DWord

2) 必须重启 WebClient 服务,新上限才会生效

Restart-Service WebClient
# 若提示服务不存在或无法重启,改用:
net stop webclient
net start webclient

3) 挂载网盘

   HTTPS(推荐,Basic 凭据不会明文过网):
     net use Z: \\<主机>@SSL@%s\DavWWWRoot /user:<用户名>

   HTTP(仅在可信内网使用):
     net use Z: \\<主机>@%s\DavWWWRoot /user:<用户名>

   卸载:net use Z: /delete
`,

		// serve.go — 父命令帮助里的一行 SMB 入口
		`Share one bucket with the file manager built into the OS; clients install nothing.
Which bucket is shared comes from the profile, so name it with --profile (the global
--bucket flag and SAIL_BUCKET still override it when needed).

  serve webdav  -- share over the WebDAV protocol (HTTPS optional)
  serve smb     -- share over the SMB2 protocol (clients mount a network drive)`: `把某个 bucket 共享给系统自带的文件管理器,客户端零安装。
共享哪个 bucket 由 profile 决定,用 --profile 指定(全局 --bucket 与 SAIL_BUCKET 需要时仍可覆盖)。

  serve webdav  —— 以 WebDAV 协议共享(HTTPS 可选)
  serve smb     —— 以 SMB2 协议共享(客户端挂载网络盘)`,

		// serve_smb.go — command help metadata
		"Share a bucket over SMB2 (mountable directly by macOS Finder / Windows Explorer)": "以 SMB2 协议共享 bucket(macOS Finder / Windows 资源管理器可直接挂载)",
		`Share the whole bucket over SMB2 (or the prefix given by --prefix); which bucket that is
comes from the profile, so name it with --profile (the global --bucket flag and SAIL_BUCKET
still override it when needed). Clients mount it with capabilities built into the OS, no
software to install.

Design boundaries:
  - SMB2's WRITE carries a 64-bit offset; the kernel's write handle is a sequential stream.
    Positional writes therefore land in a local staging file first and are uploaded when the
    client closes the handle, so point --staging-dir at a disk with room for the files being
    written (staging peak is about one file's size x concurrently open files, counted twice
    while the upload reads it back).
  - A failed commit cannot be reported to the client: the SMB2 library closes the handle
    without checking the result, so the client sees a successful CLOSE. Failures are logged
    on the server instead — watch the log if a file looks unchanged.
  - Quota and per-file size limits are enforced at that same commit point, so they inherit its
    silence: the object is correctly not written, but the client is not told. The library also
    translates filesystem errors to SMB status codes itself with no hook to override, so even
    where an error does reach the client there is no distinct "out of space" code.
  - User names and passwords are NTLM credentials, not HTTP Basic; a client that authenticates
    is choosing a share, and in multi-user mode each user gets their own share named after
    them (a share binds exactly one filesystem, so "one share, different content per user"
    is not expressible).
  - The user table is NOT hot-reloaded: the library can add shares and users but never remove
    them, so changing serve.users requires a restart.
  - With --chunked-upload on, files larger than --chunk-size are stored as chunks under the
    reserved .sail/ prefix plus a small manifest object at the logical key: the bucket then
    contains .sail/ objects, and "sail presign" fails loud on such a bucket because a presigned
    URL would hand out the manifest instead of the file.

Examples:
  sail serve smb --profile prod --listen :1445 \
    --user alice --password '***' --share sail
  sail serve smb --profile prod --prefix shared --share sail
    # then mount smb://host:1445/sail`: `以 SMB2 协议共享整个 bucket(或 --prefix 指定的前缀);共享哪个 bucket 由 --profile
决定(全局 --bucket 与 SAIL_BUCKET 需要时仍可覆盖),客户端用系统自带能力挂载,无需安装任何软件。

设计边界:
  - SMB2 的 WRITE 带 64 位 offset,而内核的写句柄是顺序流:定位写先落本地暂存文件,
    客户端关闭句柄时才上传。--staging-dir 因此要指向空间足够的盘(峰值 ≈ 单文件大小 ×
    并发打开的文件数,上传回读期间再算一份)。
  - 提交失败无法回传给客户端:SMB2 库关闭句柄时不检查返回值,客户端看到的是成功的
    CLOSE。失败改记服务端日志 —— 文件内容看起来没变就去查日志。
  - 配额与单文件上限在同一个提交点判定,因此也带上了同一条静默:对象确实不会落库,
    但客户端不会被告知。库另外还自己把文件系统错误翻译成 SMB 状态码、没有留映射钩子,
    所以即使错误真的到了客户端,也没有专属的「空间不足」码可用。
  - 用户名/口令是 NTLM 凭据,不是 HTTP Basic;多用户模式下每个用户一个共享、共享名即
    用户名(一个共享只能绑一个文件系统,「同一共享按凭据显示不同内容」表达不出来)。
  - 用户表不热加载:库能加共享与用户却没有删的接口,改 serve.users 需要重启。
  - 开启 --chunked-upload 后,超过 --chunk-size 的文件以「分片 + manifest」存储(片在
    保留前缀 .sail/ 下):此时桶里会有 .sail/ 对象,且 "sail presign" 会对这类桶直接报错
    —— 预签名 URL 会给出 manifest 而不是文件本身。

示例:
  sail serve smb --profile prod --listen :1445 \
    --user alice --password '***' --share sail
  sail serve smb --profile prod --prefix shared --share sail
    # 然后挂载 smb://host:1445/sail`,
		"listen address; 445 is the port SMB clients dial by default but it needs root, so this defaults to a high port and clients name it when mounting": "监听地址;445 是 SMB 客户端默认拨的端口,但它是特权端口,故这里默认用高位端口,由客户端在挂载时指定",
		"NTLM user name (required)": "NTLM 用户名(必填)",
		"NTLM password (required)":  "NTLM 口令(必填)",
		"share name for the single-user case; in multi-user mode each user gets a share named after them instead":                                                                                                         "单用户模式下的共享名;多用户模式下每个用户一个共享、共享名即用户名",
		"the name this server calls itself in the NTLM challenge (clients display it)":                                                                                                                                    "服务端在 NTLM 挑战里自称的名字(客户端界面会显示它)",
		"per-file size limit, defaults to --backend-max-object-size; over the limit nothing is written, but the client is not told (the check runs at close, and the library ignores that result — see the command help)": "单文件大小上限,默认取 --backend-max-object-size;超限的对象不会落库,但客户端不会被告知(判定发生在关闭句柄时,而库不看那次返回值——详见命令帮助)",
		"staging directory for positional writes, defaults to the system temp dir; peak is about one file's size x concurrently open files":                                                                               "定位写的暂存目录,默认用系统临时目录;峰值 ≈ 单文件大小 × 并发打开的文件数",
		"how long a directory listing and its entry metadata are cached (e.g. 60s, 10m; 0 disables); SMB's directory listing stats every entry one by one, so turning this off costs one backend round trip per entry":    "目录列表与条目元信息的缓存时长(如 60s、10m;0 = 关闭);SMB 的目录列举要对每个条目单独取一次元信息,关掉缓存等于每个条目一次后端往返",

		// serve_smb.go — 运行期文案
		"sail smb started: %s  bucket=%s profile=%s%s user=%s max-object-size=%s staging=%s chunked=%s share=%s\n": "sail smb 已启动:%s  bucket=%s profile=%s%s user=%s max-object-size=%s staging=%s chunked=%s share=%s\n",
		"  share %s -> user %s\n": "  共享 %s -> 用户 %s\n",
		"  the user table is read at startup only: changing serve.users requires a restart\n":                                                                                            "  用户表只在启动时读取:改 serve.users 需要重启才生效\n",
		"WARN: creating directory for user space %q failed (users still work; restarting retries): %v":                                                                                   "警告: 为用户空间 %q 创建目录失败(用户仍可用;重启会重试): %v",
		"no bucket to share: pass --bucket, set SAIL_BUCKET, or add \"bucket\" to profile %q in the config file — SMB exposes a whole bucket, and without one there is nothing to share": "没有可共享的 bucket:请传 --bucket、设 SAIL_BUCKET,或给配置文件里 profile %q 加上 \"bucket\" —— SMB 共享的是整桶,没有桶就无从共享",
		"invalid --share %q: SMB share names must not contain a path separator":                                                                                                          "无效的 --share %q:SMB 共享名里不能含路径分隔符",
		"SMB share name %q must not be empty":                                                                                       "SMB 共享名 %q 不能为空",
		"SMB share name %q contains a path separator, which SMB does not allow":                                                     "SMB 共享名 %q 含路径分隔符,SMB 不允许这样命名",
		"SMB share name %q is reserved by the SMB protocol (IPC$)":                                                                  "SMB 共享名 %q 是 SMB 协议保留名(IPC$)",
		"SMB share name %q contains \":\", which clients may read as a port or stream separator":                                    "SMB 共享名 %q 含 \":\",客户端可能将其识别为端口或流分隔符",
		"SMB user name %q must not be empty":                                                                                        "SMB 用户名 %q 不能为空",
		"SMB user name %q (the share name in multi-user mode) contains a path separator, which SMB does not allow":                  "SMB 用户名 %q(多用户模式下即共享名)含路径分隔符,SMB 不允许这样命名",
		"SMB user name %q (the share name in multi-user mode) is reserved by the SMB protocol (IPC$)":                               "SMB 用户名 %q(多用户模式下即共享名)是 SMB 协议保留名(IPC$)",
		"SMB user name %q (the share name in multi-user mode) contains \":\", which clients may read as a port or stream separator": "SMB 用户名 %q(多用户模式下即共享名)含 \":\",客户端可能将其识别为端口或流分隔符",
		"invalid SMB share name %q": "无效的 SMB 共享名 %q",
		"invalid SMB user name %q":  "无效的 SMB 用户名 %q",
		"SMB users %q and %q resolve to the same share %q; share names must differ": "SMB 用户 %q 与 %q 映射到同一个共享 %q;共享名必须不同",
	})
}
