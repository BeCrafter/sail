package i18n

func init() {
	register(map[string]string{
		// serve.go — command help metadata
		"Start a server that shares a bucket over a standard protocol": "启动服务(把 bucket 通过标准协议共享)",
		`Share a bucket with the file manager built into the OS; clients install nothing.

  serve webdav  -- share over the WebDAV protocol (HTTPS optional)`: `把 bucket 共享给系统自带的文件管理器,客户端零安装。

  serve webdav  —— 以 WebDAV 协议共享(HTTPS 可选)`,
		"Share a bucket over WebDAV (mountable directly by macOS Finder / Windows Explorer)": "以 WebDAV 协议共享 bucket(macOS Finder / Windows 资源管理器可直接挂载)",
		`Share the whole bucket over the WebDAV protocol (or the prefix given by --prefix); clients
mount it with capabilities built into the OS, no software to install.

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
  sail serve webdav --bucket mybucket --listen :8443 \
    --user alice --password '***' --tls-cert c.pem --tls-key k.pem
  sail serve webdav --print-windows-setup`: `以 WebDAV 协议共享整个 bucket(或 --prefix 指定的前缀),客户端用系统自带能力挂载,
无需安装任何软件。

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
  sail serve webdav --bucket mybucket --listen :8443 \
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
		"--user and --password are required: this gateway does not allow anonymous sharing":                                             "--user 与 --password 必填:本网关不允许匿名共享",
		"--user and --password are required (from flags or profile %q \"serve\" config): this gateway does not allow anonymous sharing": "--user 与 --password 必填(来自 flag 或 profile %q 的 \"serve\" 配置):本网关不允许匿名共享",
		"--tls-cert and --tls-key must be supplied together":                                                                            "--tls-cert 与 --tls-key 必须同时提供",
		"invalid --backend-max-object-size: %w":                                                                                         "--backend-max-object-size 非法: %w",
		"invalid --max-upload-size: %w":                                                                                                 "--max-upload-size 非法: %w",
		"invalid --chunk-size: %v":                                                                                                      "--chunk-size 非法: %v",
		"--chunk-size must be between %s and %s, got %s":                                                                                "--chunk-size 必须在 %s 与 %s 之间,当前 %s",
		"--chunk-size (%s) exceeds --backend-max-object-size (%s): raise the backend limit or lower the chunk size":                     "--chunk-size(%s)超过 --backend-max-object-size(%s):请调高后端上限或调小片大小",
		"sail webdav started: %s://%s  bucket=%s profile=%s%s user=%s max-object-size=%s staging=%s chunked=%s\n":                       "sail webdav 已启动: %s://%s  bucket=%s profile=%s%s user=%s 单对象上限=%s 暂存=%s 分片=%s\n",
		"  mount at: %s\n": "  挂载地址: %s\n",
		"  (localhost = this machine; LAN IP = other devices)\n": "  (localhost = 本机;局域网 IP = 其它设备)\n",

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
	})
}
