# sail — S3 对象存储 CLI

> 基于标准 S3 协议的命令行工具。单个静态二进制,零运行时依赖,跨平台开箱即用。

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
- **多 profile 配置**:prod / test / staging 等多环境切换,密钥可引用环境变量避免明文
- **跨平台**:macOS / Linux,单二进制下载即用;支持 shell 自动补全(zsh / bash / fish)

## 安装

### 方式一:npm 安装(推荐,跨平台)

```bash
npm install -g @becrafter/sail
```

npm 会按操作系统和 CPU 架构自动只下载一个匹配的平台二进制,`sail` 命令开箱即用。支持 macOS(arm64/x64)、Linux(arm64/x64),二进制托管在 npm registry,无需额外联网下载。

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

交互式生成或更新 `~/.sail/config.yaml`(`--reset` 重置为全新配置;文件已存在时新增或重配一个 profile,保留其它),并可选安装 shell 自动补全。向导要点:

- `endpoint` 为必填项,留空会原地重问
- `access-key` / `secret-key` 可直接输入明文;回车留空则引用按 profile 派生的环境变量(机制见下方"密钥安全"),写盘后会打印需要 `export` 的变量名
- 重配已有 profile 时,已配置的明文密钥不回显,回车即保留
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
