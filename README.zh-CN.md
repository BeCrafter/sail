# sail — S3 对象存储 CLI

> 基于标准 S3 协议的命令行工具。单个静态二进制,零运行时依赖,跨平台开箱即用。

**[English](./README.md) · 简体中文**

`sail` 面向任何兼容 S3 协议的对象存储服务(AWS S3、MinIO、阿里云 OSS 以及各类自建 S3 兼容服务),提供上传/下载、列举、删除、复制/移动、内容查看、预签名 URL 等日常操作。它不绑定任何特定云厂商,配置即用。

- 开源地址:<https://github.com/BeCrafter/sail>
- 问题反馈:<https://github.com/BeCrafter/sail/issues>

## 特性

- **标准 S3 协议**:path-style + SigV4 签名,兼容 AWS S3 / MinIO / 阿里云 OSS 及各类自建 S3 兼容服务
- **丰富的传输**:单文件、目录递归、管道流式上传,大文件自动分片(5MB/16 并发)
- **批量操作**:URL 通配符(`cp/rm 's3://b/*.log'` 展开)、批量删除(DeleteObjects 每批 1000)、管道逐行删除(`ls | rm -r -`)
- **对象与桶管理**:下载、列举(长格式/目录视图/树形/排序/列桶)、目录占位对象(mkdir/rmdir)、桶管理(mb/rb)、删除(批量/管道)、复制/移动(本地↔s3、s3↔s3 服务端复制零带宽)
- **增量同步**:rsync 式 `sync`(大小+时间比对,`--checksum` 内容校验、`--update`、`--exclude/--include` 过滤、`--delete`、`--dry-run`),本地↔s3↔s3
- **检索统计**:`find`(名称/大小/时间过滤)、`du`(按前缀层级统计占用)
- **内容查看**:多格式智能渲染——文本/JSON/YAML/CSV/XML/图片终端字符画/二进制;`head`/`tail`/`wc`/`grep` 流式读写不落盘
- **校验与鉴权**:`checksum`(md5/sha256 计算与比对)、`presign` 预签名 URL、基于 CDN 域名的公开访问地址
- **WebDAV 网关**:`serve webdav` 把 bucket 挂成网络盘,macOS Finder / Windows 资源管理器直接读写,零客户端安装
- **多 profile 配置**:prod / test / staging 等多环境切换,密钥可引用环境变量避免明文
- **跨平台**:macOS / Linux,单二进制下载即用;支持 shell 自动补全(zsh / bash / fish)

## 安装

### 方式一:npm 安装(推荐,跨平台)

```bash
npm install -g @becrafter/sail
```

npm 会按操作系统和 CPU 架构自动只下载一个匹配的平台二进制,`sail` 命令开箱即用。支持 macOS(arm64/x64)、Linux(arm64/x64),二进制托管在 npm registry,无需额外联网下载。

**免安装、临时直用(npx)**:无需全局安装,直接用最新发布版:

```bash
npx -y @becrafter/sail@latest <命令>
npx -y @becrafter/sail@latest --help
npx -y @becrafter/sail@latest config setup
```

`-y` 自动确认下载该包;`@latest` 固定拉取最新发布版(而非可能过期的本地缓存),确保始终运行当前版本。

### 方式二:下载二进制

到 [Releases 页面](https://github.com/BeCrafter/sail/releases)下载对应平台的二进制,放入 `PATH` 即可。

### 方式三:go install

```bash
go install github.com/BeCrafter/sail@latest
```

### 方式四:源码构建

```bash
git clone https://github.com/BeCrafter/sail.git
cd sail && go build -o sail .
```

## 配置

### 快速初始化

```bash
sail config setup
```

交互式生成或更新 `~/.config/sail/config.yaml`(`--reset` 重置为全新配置;文件已存在时新增或重配一个 profile,保留其它),并可选安装 shell 自动补全。向导要点:

- `endpoint` 为必填项,留空会原地重问
- `access-key` / `secret-key` 可直接输入明文;回车留空则引用按 profile 派生的环境变量(机制见下方"密钥安全"),写盘后会打印需要 `export` 的变量名
- 重配已有 profile 时,已配置的明文密钥不回显,回车即保留
- WebDAV 网关(`serve:` 块)同样引导配置:listen / prefix / 认证方式(单用户或多用户) / TLS /
  chunked-upload / staging-dir。已有 `serve` 块默认保留,可选追加用户、重新配置或删除;多用户表随输入即校验
  (重名、前缀嵌套、quota 语法);向导不提问的字段(尺寸上限、`dir-cache-ttl`、`prewarm`)原样保留
- 输入会尽量归一化:裸端口自动补冒号(`8443` → `:8443`)、URL 缺协议头补 `https://`、quota 单字母单位
  补全为 `MB`/`GB`/`TB`、路径中的 `~` 自动展开、y/n 回答接受 `yes`/`true`/`1`/`on`;非法值会说明原因后重问
- 写盘后输出配置摘要,空字段明确标注,便于核对缺失项

```yaml
# 密钥可用 ${VAR} 引用环境变量,避免明文。
default-profile: prod
profiles:
  prod:
    endpoint: <your-s3-endpoint>
    access-key: ${SAIL_PROD_ACCESS_KEY}
    secret-key: ${SAIL_PROD_SECRET_KEY}
    bucket: ""
    region: ""
    path-style: true
    cdn-domain: <your-cdn-domain>
    # cdn-bucket-path: false  # CDN 域名是否已含 bucket 路径;注释掉则自动检测
  test:
    endpoint: <your-s3-endpoint-test>
    access-key: ${SAIL_TEST_ACCESS_KEY}
    secret-key: ${SAIL_TEST_SECRET_KEY}
    bucket: ""
    region: ""
    path-style: true
    cdn-domain: <your-cdn-domain-test>
  staging:
    endpoint: <your-s3-endpoint-staging>
    access-key: ${SAIL_STAGING_ACCESS_KEY}
    secret-key: ${SAIL_STAGING_SECRET_KEY}
    bucket: ""
    region: ""
    path-style: true
    cdn-domain: <your-cdn-domain-staging>
```

向导中留空 `access-key`/`secret-key` 时,会自动写入按 profile 派生的占位符 `SAIL_<PROFILE>_(ACCESS|SECRET)_KEY`(profile 名大写、连字符等非字母数字字符转下划线,清洗后为空则回退 `SAIL_ACCESS_KEY` 全局名);配置文件中也可手动改为任意 `${VAR}`。

### serve 块

`serve webdav` 的全部参数都可固化在 profile 下的 `serve:` 块里,启动时不用每次手打;命令行 flag 仍保留,并作为最高优先级覆盖配置。优先级:`flag(显式设置) > profile.serve.* > flag 默认值`。

```yaml
profiles:
  prod:
    endpoint: <your-s3-endpoint>
    access-key: ${SAIL_PROD_ACCESS_KEY}
    secret-key: ${SAIL_PROD_SECRET_KEY}
    bucket: mybucket
    serve:
      listen: ":8443"
      prefix: ""                 # 共享根前缀,空 = 整桶
      user: alice
      password: ${SAIL_PROD_SERVE_PASSWORD}   # 支持明文或 ${VAR} 引用
      # users:                    # 多用户模式(见下文「多用户」),与 user/password 互斥
      #   - name: alice
      #     password: ${ALICE_PASSWORD}
      #     prefix: alice/        # 相对 serve.prefix;省略 = base 前缀本身
      #     quota: 10GB         # 单位只支持 MB/GB/TB;纯数字=字节数
      # tls-cert: /etc/cert.pem   # 与 tls-key 同时提供即启用 HTTPS
      # tls-key: /etc/key.pem
      # staging-dir: /tmp/sail-stage
      # backend-max-object-size: 5TiB
      # max-upload-size: 5TiB     # 空 = 跟随 backend-max-object-size
      # chunked-upload: false
      # chunk-size: 4GiB
      # dir-cache-ttl: 60s
      # prewarm: [/bigdir]        # 需要后台保热的目录
```

`serve` 块各字段与 `serve webdav` 的同名 flag 一一对应(大小类字段用与 flag 相同的字符串格式,如 `5TiB`)。`user`/`password` 可写明文或 `${VAR}` 引用环境变量,与 access-key/secret-key 的密钥安全机制一致;空字段由 flag 默认值兜底。可选的 `users` 列表开启多用户模式——见 WebDAV 网关一节的「多用户」。`sail config setup` 会交互式引导上述字段(含生成并校验 `users` 表);它不提问的字段按文件原样保留。

### cdn-domain 说明

`cdn-domain` 用于 `sail url` 命令生成文件的公开访问地址,填入你的存储服务对应的 CDN 域名即可。

**bucket 去重**:`sail url` 会检查 `cdn-domain` 的路径是否已包含 bucket 段(路径式 `.../bucket/`),若已包含则不再重复拼接,避免生成 `.../bucket/bucket/key` 这类失效链接。自动检测仅按路径段判断、不做子域推断;若自动检测失效或有特殊映射(如域名直接映射到 bucket、URL 不含 bucket),可用配置项 `cdn-bucket-path` 显式声明:`true` 表示域名已含 bucket(不再追加),`false` 表示未含(总是追加),注释掉则自动检测;也可用 `--no-bucket` 单次指定。

**注意**:只有 `public-read` 权限的 bucket 的文件才能通过 CDN 域名访问;私有 bucket 只能通过鉴权的 GetObject 访问。

### region 与 path-style 说明

这两个是 S3 协议的通用参数,根据你接入的存储服务选择:

| 参数 | 含义 | AWS S3 | MinIO / 自建 | 阿里云 OSS |
|------|------|--------|-------------|-----------|
| `region` | 数据中心区域 | 填实际值如 `us-east-1` | 留空 | 填如 `oss-cn-hangzhou` |
| `path-style` | URL 寻址方式 | `false`(virtual-hosted) | `true` | `false` |

- **path-style**:`true` 时 URL 为 `endpoint/bucket/key`;`false` 时 URL 为 `bucket.endpoint/key`。自建 S3 兼容服务通常只支持 path-style。
- **region**:自建服务通常留空。AWS SDK 内部规则要求 region 非空,留空时代码自动用 `us-east-1` 占位(不影响实际请求目标,因 endpoint 已被覆盖)。

### 密钥安全

配置文件中的密钥有两种写法:直接明文,或用 `${VAR}` 引用环境变量(避免明文落盘):

```yaml
access-key: my-plain-access-key        # 写法一:明文
access-key: ${SAIL_TEST_ACCESS_KEY}    # 写法二:引用环境变量
```

`sail config setup` 中回车留空密钥时,自动采用写法二并按 profile 派生变量名(profile `test` → `SAIL_TEST_ACCESS_KEY`,`staging-eu` → `SAIL_STAGING_EU_ACCESS_KEY`,即大写、连字符等非字母数字字符转下划线;清洗后为空回退 `SAIL_ACCESS_KEY`),各环境互不共享。向导结束时会打印需要 export 的变量名,例如:

```bash
export SAIL_TEST_ACCESS_KEY="your-access-key"
export SAIL_TEST_SECRET_KEY="your-secret-key"
```

未设置这些变量时,sail 命令启动会报 `缺少 access-key/secret-key`。

注意区分两类环境变量:文件内 `${VAR}` 引用的变量(按 profile 派生,如 `SAIL_TEST_ACCESS_KEY`)负责给密钥赋值;下方"环境变量覆盖"表中的 `SAIL_ACCESS_KEY` 等是**运行时全局覆盖**,一旦设置会无视配置文件直接生效。生效优先级:全局覆盖环境变量 > 配置文件内 `${VAR}` 展开 > 空(启动报缺少密钥)。

### 环境变量覆盖

| 变量 | 作用 |
|------|------|
| `SAIL_ENDPOINT` | 覆盖 endpoint |
| `SAIL_ACCESS_KEY` | 覆盖 access key |
| `SAIL_SECRET_KEY` | 覆盖 secret key |
| `SAIL_BUCKET` | 覆盖默认 bucket |
| `SAIL_CDN_DOMAIN` | 覆盖 CDN 域名 |

## 使用

> **路径语法**:`s3://bucket/key` 显式指定 bucket;`s3:///key`(空 bucket 段)用配置的默认 bucket;`s3://bucket` 仅 `ls` 列桶。跨桶同步仍用显式 `s3://bucket/key`。

```bash
# 查看版本
sail --version          # 或 sail -v

# 复制(本地↔s3、s3↔s3);upload/download 为 cp 的别名
sail cp local.txt s3://mybucket/path/local.txt
sail cp local.txt s3:///path/local.txt           # s3:/// 用配置默认 bucket
sail upload local.txt                            # 1 参:上传到默认 bucket,key 用文件名
sail cp -r ./dir s3://mybucket/prefix/           # 递归镜像目录
sail cp 's3://mybucket/logs/*.log' s3://mybucket/archive/   # 通配符批复制(* 跨 /,保留层级)
sail cp 's3://mybucket/*.json' ./download-dir/   # 通配符批量下载
cat file | sail upload - s3://mybucket/key       # 管道输入

# 桶管理(mb/rb 与 ls --buckets)
sail mb s3://my-new-bucket
sail rb s3://my-old-bucket                       # 仅删空桶;非空先 sail rm -r s3://my-old-bucket/
sail ls --buckets                                # 列出所有桶

# 下载(s3→本地);download 为 cp 的别名
sail cp s3://mybucket/key local.txt
sail download s3://mybucket/key                  # 1 参:下载到当前目录

# 列举
sail ls s3://mybucket/prefix/
sail ls -l s3://mybucket/                        # 长格式:大小+修改时间
sail ls -l -t s3://mybucket/                     # 按修改时间排序(新→旧),--human 人类可读大小
sail ls -l -S -r s3://mybucket/                  # 按大小排序(大→小)再逆序
sail ls -d s3://mybucket/prefix/                 # 只列该层子目录(不含文件),对齐 ls -d

# 查找与统计
sail find s3://mybucket/logs --name '*.log' -l   # 按文件名通配(可重复多个)
sail find s3://mybucket --size +1M --newer 2026-01-01   # 大小/时间过滤
sail du -h s3://mybucket/prefix/                 # 按前缀层级统计占用
sail du -h --max-depth 1 s3://mybucket           # 只显示 1 层 + 总计
sail du -s s3://mybucket/prefix/                 # 只打印总计

# 树形查看(S3 前缀或本地目录)
sail tree s3://mybucket/prefix/                  # 完整树
sail tree -L 2 s3://mybucket/prefix/             # 限深度 2
sail tree -d s3://mybucket/prefix/               # 只显目录
sail tree -s --human s3://mybucket/prefix/      # 文件附人类可读大小
sail tree ./cmd                                  # 本地目录树

# 删除与目录占位对象
sail rm s3://mybucket/key
sail rm -r s3://mybucket/prefix/                 # 递归删除(批量 DeleteObjects,每批 1000)
sail rm key1 key2 key3                          # 多参数批量
sail rm 's3://mybucket/logs/*.tmp'              # 通配符匹配删除
sail ls s3://mybucket/prefix/ | sail rm -r -    # 管道逐行读取 key(xargs 式)
sail mkdir s3://mybucket/new/dir/               # 目录占位对象(天然 -p 语义)
sail rmdir s3://mybucket/new/dir/               # 只删空目录;非空请用 rm -r

# 增量同步(rsync 式:大小+修改时间比对,幂等;--help 查看全部选项)
sail sync ./dir s3://mybucket/mirror/
sail sync --exclude '*.tmp' --delete ./dir s3://mybucket/mirror/
sail sync --include '*.json' s3://mybucket/mirror/ ./dir2 --dry-run   # 白名单 + 预演
sail sync --checksum ./dir s3://mybucket/mirror/  # 大小相同时按内容 md5 校验
sail sync --update ./dir s3://mybucket/mirror/    # 只传输比目标新的条目

# 预签名 URL(部分服务不支持,见下方"限制")
sail presign s3://mybucket/key --expires 3600

# 生成 CDN 访问地址
sail url s3://mybucket/path/file.jpg
sail url s3://mybucket/path/file.jpg --cdn https://<your-cdn-domain>
sail url s3://mybucket/path/file.jpg --no-bucket   # CDN 域名已含 bucket 路径,不再重复追加

# 查看对象/文件内容(按格式智能渲染,本地文件免配置)
sail view s3://mybucket/config.json              # JSON 自动美化缩进
sail view ./local.log                            # 文本/代码直出
sail view s3://mybucket/data.csv                # CSV 表格对齐
sail view s3://mybucket/photo.png               # 图片终端字符画(半块字符,任意终端可见)
sail view s3://mybucket/data.json --raw         # 原样输出,适合管道:sail view ... --raw | jq .
sail cat s3://mybucket/data.json                 # cat 是 view --raw 的别名
sail view s3://mybucket/big.json --force        # 跳过大小限制
sail view s3://mybucket/photo.png --width 60    # 指定字符画列宽

# 流式读取内容(s3 路径走 Range 只取需要的部分,不下载全量)
sail head -n 20 s3://mybucket/logs/app.log      # 开头 N 行
sail head --bytes 4096 s3://mybucket/data.bin   # 开头 N 字节
sail tail -n 50 s3://mybucket/logs/app.log      # 结尾 N 行(Range 尾部窗口)
sail wc -l s3://mybucket/logs/app.log           # 行数(默认三列:行 词 字节)
sail grep -n "ERROR" s3://mybucket/logs/app.log # 正则逐行搜索(支持 -i/-v/-l/-n)

# 校验和(md5/sha256 流式计算与比对,本地文件免配置)
sail checksum s3://mybucket/data.bin            # 默认 md5
sail checksum --algo sha256 --compare ./local.bin s3://mybucket/data.bin
sail checksum --etag s3://mybucket/data.bin     # 展示原始 ETag(注意:分片对象 ETag≠内容 md5)

# 复制对象/文件(本地↔s3、s3↔s3 走服务端 CopyObject 零带宽)
sail cp ./local.txt s3://mybucket/path/copied.txt
sail cp ./local.txt s3://mybucket/path/          # 尾 / 表示进目录
sail cp s3://mybucket/a.txt ./out.txt
sail cp -r ./dir s3://mybucket/mirror/           # 递归镜像本地目录
sail cp -r s3://mybucket/prefix/ s3://mybucket/dest/   # 服务端递归复制
sail cp --dry-run ./local.txt s3://mybucket/x   # 预演,不实际复制
sail cp --content-type text/markdown ./NOTES.md s3://mybucket/notes   # 指定本次上传的类型(含 -r 递归;s3→s3 复制不受影响)

# 移动对象/文件(复制后删除源)
sail mv s3://mybucket/a.txt s3://mybucket/moved.txt     # 单对象,无确认
sail mv ./local.txt s3://mybucket/uploaded.txt
sail mv -r s3://mybucket/src/ s3://mybucket/dst/         # 递归,交互确认 [y/N]
sail mv -r --yes s3://mybucket/src/ s3://mybucket/dst/   # 跳过确认

# 查看对象/文件元信息(HeadObject / os.Stat)
sail stat s3://mybucket/config.json             # size/content-type/last-modified/etag
sail stat ./local.log                           # 本地文件元信息

# 切换 profile
sail -p test upload local.txt s3://testbucket/local.txt
```

## 与 AWS CLI 对照验证

行为与 `aws s3` 一致,可用 AWS CLI 对照:

```bash
aws s3 ls --endpoint-url <your-s3-endpoint> s3://mybucket/
```

## WebDAV 网关(`sail serve webdav`)

把 bucket(或 `--prefix` 指定的前缀)挂成网络盘:客户端用系统自带的 WebDAV 能力直接读写,
不用装任何软件。列目录、上传、下载、Range 拖进度条、改名、删除的行为与普通网络盘一致。

```bash
# 启动(HTTPS 推荐;同时给 --tls-cert/--tls-key 即启用)
sail serve webdav --bucket mybucket --listen :8443 \
  --user alice --password '***' --tls-cert cert.pem --tls-key key.pem

# 只共享桶内某个前缀(映射为 /,越界路径一律拒绝)
sail serve webdav --bucket mybucket --prefix tenant-a --user alice --password '***'

# 省略 --bucket:与其它命令走同一条解析链(--bucket > SAIL_BUCKET > profile.bucket)
sail serve webdav --profile prod --user alice --password '***'

# 生成 Windows 客户端的一次性注册表配置与挂载命令后退出
sail serve webdav --print-windows-setup
```

桶取自与其它命令完全相同的解析链:`--bucket` > `SAIL_BUCKET` > `profile.bucket`。
三处都取不到桶时拒绝启动;启动横幅打印 `bucket=`、`profile=`、`prefix=`,
让「到底暴露了什么」始终可断言。

除 `--profile` 与全局 `--bucket` 外,下表参数都可写进 profile 的 `serve:` 块(见上文「serve 块」),
启动时省略对应 flag 即从配置读取;flag 显式给出时仍覆盖配置。

| 参数 | 默认 | 说明 |
|---|---|---|
| `--profile` | 配置的 default-profile | 选择共享哪个 profile:桶取该 profile 的 `bucket`(可被全局 `--bucket` 或 `SAIL_BUCKET` 覆盖);三处都为空时拒绝启动 |
| `--listen` | `:8080` | 监听地址(`serve.listen`) |
| `--prefix` | 空 | 共享根前缀(映射为 `/`);越界路径一律拒绝(`serve.prefix`) |
| `--user` / `--password` | 空 | Basic 认证,**为空拒绝启动**,不允许匿名共享(`serve.user`/`serve.password`)。与 `serve.users` 互斥 |
| `--tls-cert` / `--tls-key` | 空 | 同时提供即启用 HTTPS(`serve.tls-cert`/`serve.tls-key`) |
| `--backend-max-object-size` | `5TiB` | 声明的后端单对象上限(S3 无能力协商,不可探测)(`serve.backend-max-object-size`) |
| `--max-upload-size` | 跟随上一项 | 请求体上限,超限在读满请求体前返回 413 + 可操作指引(`serve.max-upload-size`) |
| `--staging-dir` | 系统临时目录 | 写暂存目录;峰值 ≈ 单文件最大值 × 并发上传数(`serve.staging-dir`) |
| `--chunked-upload` | `false` | 把超过 `--chunk-size` 的文件拆成分片 + manifest 存储(关:1 文件 = 1 对象)(`serve.chunked-upload`) |
| `--chunk-size` | `4GiB` | 单个物理片上限,同时是分片阈值(5MiB ~ 5GiB);需配合 `--chunked-upload`(`serve.chunk-size`) |
| `--dir-cache-ttl` | `60s` | 目录列表缓存时长(如 `60s`、`10m`);过期条目**先返回旧值再后台刷新**,热目录永不阻塞;写操作即时失效,`0` 关闭。外部对桶的改动最长该时长后可见(`serve.dir-cache-ttl`) |
| `--prewarm` | 空 | 需要后台保热的目录(逗号分隔逻辑路径,如 `/yiche,/modelImage`);启动时各列一次、之后按 TTL 周期刷新,首次访问不再承担完整列举的开销。用于超大目录(十几万条,首次列举可达数十秒)。多用户模式下同一清单在每个用户各自空间内生效(`serve.prewarm`) |
| `--print-windows-setup` | — | 打印 `.reg` 内容 + PowerShell + 「必须重启 WebClient 服务」提醒后退出 |

启动横幅会按绑定给出挂载地址:通配地址(`:8080`/`0.0.0.0:8080`)时同时列出 `http://localhost:端口/`(本机挂载)和各网卡的局域网 IP(其它设备挂载);显式绑定具体主机时只列该地址。

> **性能**:上传/下载走服务端 Range 流式读写,不整文件入内存;HTTP 连接池按 S3 高并发调优,并发打开多个文件时复用长连接;目录列表带短时缓存。单次打开文件只做 1 次 `HeadObject` + 1 次 `GetObject`。

> **内容类型**:从挂载点上传的文件按名称(扩展名优先,未命中再按内容签名探测)推断 Content-Type——图片/PDF 通过链接打开会直接渲染而不是下载。客户端没发 Content-Type(macOS 自带客户端实测就不发)或只发通用的 `application/octet-stream` 时,一律视为「没指定类型」;客户端发来的其它类型原样保留。

### 多用户(`serve.users`)

一个网关可同时服务多个用户,每人一个独立空间。用户表写进 profile 的 `serve:` 块——每个用户
获得一个 Basic 认证身份和自己的命名空间:

```yaml
    serve:
      listen: ":8443"
      prefix: team/            # base 前缀(冷区:变更需重启)
      users:
        - name: alice
          password: ${ALICE_PASSWORD}   # ${VAR} 引用,与 serve 其余字段一致
          prefix: alice/                # 相对 base;省略 = base 前缀本身
          quota: 10GB                   # 每用户空间上限(单位 MB/GB/TB);热生效,无需重启
        - name: bob
          password: ${BOB_PASSWORD}
          prefix: shared/bob-data/      # 任意相对段
```

- **空间配额(`quota`)**。每个用户的 `quota`(如 `500MB`、`10GB`、`1TB`;单位只支持十进制 `MB`/`GB`/`TB`,纯数字表示字节数)限制其前缀下的
  物理字节消耗——即账单口径,含 `.sail/` 分片部件与 manifest。超限写返回 **507** + 可操作指引:
  请求声明了 Content-Length 时在读请求体之前拦截,COPY(无 Content-Length)在提交点复核;
  覆盖写会扣减旧对象的大小。用量为惰性快照(默认 TTL 5 分钟)+ 在途预留——窗口内尽力准确,
  网关外直写(如 `sail cp`)在下次刷新后可见。快照过期时先沿用旧值、后台单飞刷新,大前缀不会
  阻塞写入;只有启动后的第一次写入会为初始快照短暂等待(≤3 秒)。配置中修改 `quota` 热生效,
  无需重启。配额还会通过 WebDAV 属性播报给客户端(RFC 4331 `DAV:quota-available-bytes` /
  `DAV:quota-used-bytes`,目录 PROPFIND),Finder / 资源管理器因此能显示剩余空间。删除或改名会
  立即作废用量快照,经网关释放的空间在下一次写入即生效,不必等 TTL 走完。
- **结构性隔离**。用户生效前缀 = base `prefix` + 该用户的 `prefix` 段;其触碰的所有对象 key
  (含 `.sail/` 分片部件)都落在前缀内。用户的 `/` 即自己的空间——其他用户的对象结构性不可达,
  `..` 越界被拒绝,访问日志对每个请求归因 `user=<名字>`。
- **热加载**。用户表被监听:编辑配置文件(新增/删除用户、改密码、改前缀、改配额)秒级生效,
  无需重启。`listen`、TLS 证书、`staging-dir`、分片参数、`dir-cache-ttl`、`prewarm` 与 base
  `prefix` 属冷区——变更仅告警「需重启」,运行参数不变。坏 YAML 保留原用户表并告警;配置文件被误删后重建不破坏监听,新内容自动加载。
- **目录自动创建**。用户生效(启动或 reload)时,sail 异步在其前缀写入 0 字节目录标记对象,
  让目录在 S3 控制台与 `sail ls` 中可见。创建幂等且尽力而为:S3 抖动仅告警、用户照常使用
  ——marker 是可见性,不是挂载硬前提。
- **冲突 fail-loud**。`users` 与 `user`/`password` 互斥(跨来源同罪:`--user`/`--password`
  flag 搭配配置 `users` 表同样拒绝)。生效前缀必须两两互异且互不嵌套——`alice/` 与
  `alice/logs/` 不能共存,否则外层用户会列举到内层用户的对象。两项检查在启动与每次 reload
  时执行,违规拒绝并保留原状态。
- **单用户模式不变**。`--user`/`--password`(或 `serve.user`/`serve.password`)行为与以往
  完全一致:base 前缀上的单隐式用户。凭据来自 flag 时关闭热加载,启动时明确告警。

### 客户端挂载

- **macOS Finder**:`前往 → 连接服务器`(⌘K),填 `https://host:8443`,用 `--user`/`--password` 登录。
- **Windows 资源管理器**:先跑 `sail serve webdav --print-windows-setup` 导入注册表配置并重启
  WebClient 服务,再 `net use Z: \\host@SSL@8443\DavWWWRoot /user:alice`。

Windows 默认把单次上传卡在约 50MB,这道闸门在**客户端注册表**,服务端参数改不动它。
所以 sail 不提供 `--max-file-size` 这类看着能抬高闸门的假旋钮——用 `--print-windows-setup`
拿客户端侧的正确做法。

### 两条限制

- **LOCK 是进程内锁**:WebDAV 协议要求的锁由内存实现,进程重启即失效,也不跨实例共享。
  单实例、短事务的网盘场景够用;多实例部署下客户端看到的锁不互通。
- **写路径需要暂存盘**:上传先完整落到 `--staging-dir`,提交时才切块上传到 S3。磁盘峰值
  ≈ 单文件最大值 × 并发上传数;空间不足时在写入前返回 **507** 而不是中途失败。
  大文件多的场景请把 `--staging-dir` 指向空间足够的磁盘。

其他取舍:目录级 `MOVE`/`COPY` 返回 **501**,由客户端退化为「复制 + 删除」(P1 只做对象级移动);
`.sail/` 为保留前缀:列目录时过滤掉,直接按路径访问一律按「不存在」处理
(客户端既读不到、也删不掉分片部件)。

### 分片存储(`--chunked-upload`)

默认关:1 文件 = 1 对象,既有桶与第三方 S3 工具零感知。当后端存在较小的单对象上限时(例如桶
前面挂了一道会拒绝大对象的网关)再打开:超过 `--chunk-size` 的文件会被拆成若干片,存在保留前缀
`.sail/parts/<逻辑路径>/<版本>/` 下,逻辑 key 上只放一个几百字节的 JSON **manifest**。

```bash
# 超过 100MiB 的文件拆片;片在 .sail/ 下,key 上是 manifest
sail serve webdav --bucket mybucket --user alice --password '***' \
  --chunked-upload --chunk-size 100MiB
```

打开后成立的几条规则:

- **manifest 是唯一提交点**。片先全部写完,才覆盖逻辑 key,客户端看不到写了一半的文件;
  读取按 manifest 定位,**Range 只取命中的片**,不整文件入内存、不落盘。
- **片目录按逻辑路径归属**。`/a/big.bin` 的片在 `.sail/parts/a/big.bin/<版本>/`,另一个路径
  即使内容完全相同也各存一份。因此覆盖写只回收自己名下的旧代,删除文件/目录回收本路径的片,
  都不会碰到别的逻辑路径。
- **已知限制:覆盖写会打断在途读**。旧片在 manifest 切换后立即回收,此刻仍在流式读旧内容的
  请求会中途断掉。失败是**响亮的**(响应声称的 `Content-Length` 与实际字节数不符,客户端能发现,
  且从未把新旧内容混读交付),重试即可;目前不做延迟回收或 in-flight reader 登记。
- **片与逻辑 key 走同一个根前缀**(`--prefix`)。配置了共享根前缀时,片也全部落在该前缀之内,
  不会与共用同一个桶的其它实例互相覆盖。
- `--chunk-size` 必须在 `5MiB` ~ `5GiB`(S3 单次 `PutObject` 的硬顶)之间,且不超过
  `--backend-max-object-size`;越界直接拒绝启动,不在运行期炸。
- **`sail presign` 对分片 key 直接报错**:预签名 URL 只会返回 manifest 而不是文件本身。
  这类文件请用 `sail serve webdav` 或 `sail cp` 读取(确实只想要 manifest 时加 `--allow-chunked`)。
- **桶内会出现 `.sail/` 对象**。WebDAV 列目录会过滤掉,但 `sail ls` 等其他客户端会看到它。
  删除或覆盖中途失败仍可能留下孤儿片:它们对列目录不可见、不影响数据一致性。目前还没有
  `sail gc` 子命令来回收,但片目录名按逻辑路径归属,**判定规则已经成立** —— 由 `.sail/parts/<L>/…`
  反推 L 后只需一次 HEAD 即可确认该代是否仍被引用,实现 GC 不必全桶扫描。

## 限制

- Bucket 与 Object key 的命名规则、长度上限取决于所接入的 S3 服务,遵循各服务约束。
- **部分 S3 兼容服务不支持预签名 URL**:某些自建 S3 服务不支持 query string 认证(返回 "Authorization empty"),只支持 Authorization header 认证。如需公开访问,请通过 CDN 域名访问已设置为公开的文件。

## 实现细节

### S3 兼容性适配

部分自建 S3 兼容服务与标准 AWS S3 存在差异,工具已做适配:

1. **checksum 禁用**:AWS SDK v2 默认在上传时使用 `aws-chunked` content encoding + CRC32 trailing checksum。部分 S3 兼容服务端不解码 `aws-chunked`,导致存储的数据被 trailer 污染(大文件 multipart upload 尤其严重)。工具在 client 和 uploader 两处均设置了 `RequestChecksumCalculation = WhenRequired` 和 `ResponseChecksumValidation = WhenRequired` 来禁用此行为。
2. **region 占位**:部分 S3 服务 region 为空,但 AWS SDK v2 的 endpoint 规则要求 region 非空。工具用 `us-east-1` 作为占位值(endpoint 已被 BaseEndpoint 覆盖,实际不影响请求)。
3. **CopyObject 回退**:部分 S3 兼容服务的 `CopyObject` 返回成功但生成 0 字节对象。`cp`/`mv` 的 s3↔s3 路径在 CopyObject 后用 HEAD 校验目标大小与源一致;不一致时自动回退到 `download→re-upload`,保证数据正确。标准 S3(AWS/MinIO)上 CopyObject 校验通过,仍走零带宽服务端复制。

## 发布

发布走 GitHub Actions 自动化:推送形如 `vX.Y.Z` 的 tag 即触发交叉编译 + 发布到 npm,无需本地登录。

1. 在仓库 **Settings → Secrets and variables → Actions** 添加 `NPM_TOKEN`(npm automation token,需有 `@becrafter` scope 发布权)。
2. 打 tag 并推送:
   ```bash
   git tag v0.1.0 && git push origin v0.1.0
   ```
3. workflow 跑完,4 个平台子包 + 主包即发布到 `registry.npmjs.org`。也可在 Actions 页面手动触发并填版本号。

本地发布(无 CI 时)仍可用:`make release VERSION=0.1.0`(未登录会引导 `npm login`)。

## 贡献

欢迎提 Issue 或 Pull Request:<https://github.com/BeCrafter/sail/pulls>

## 许可证

[MIT](./LICENSE)
