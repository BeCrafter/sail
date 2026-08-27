# @becrafter/sail

> S3 协议对象存储 CLI —— 单个静态二进制,零运行时依赖。

`sail` 面向任何兼容 S3 协议的对象存储服务(AWS S3、MinIO、阿里云 OSS 及各类自建 S3 兼容服务),提供上传/下载、列举、删除、复制/移动、内容查看、预签名 URL 等日常操作。本 npm 包是跨平台安装器,负责按平台拉取对应的静态二进制。

## 安装

```bash
npm install -g @becrafter/sail
```

## 环境要求

- Node.js `>= 18`(仅用于安装器分发二进制,`sail` 本身无运行时依赖)
- 支持平台:

| OS | Arch | 平台子包 |
|----|------|----------|
| macOS | arm64 (Apple Silicon) | `@becrafter/sail-darwin-arm64` |
| macOS | x64 (Intel) | `@becrafter/sail-darwin-x64` |
| Linux | arm64 | `@becrafter/sail-linux-arm64` |
| Linux | x64 | `@becrafter/sail-linux-x64` |

安装后 npm 会按你的操作系统和 CPU 架构自动只下载一个匹配的平台二进制子包,`sail` 命令开箱即用。

## 快速开始

```bash
sail config init                                # 交互式生成 ~/.sail/config.yaml
sail cp local.txt s3://mybucket/path/local.txt  # 上传
sail ls s3://mybucket/                          # 列举
sail cp s3://mybucket/key local.txt             # 下载
```

配置示例:

```yaml
default-profile: prod
profiles:
  prod:
    endpoint: <your-s3-endpoint>
    access-key: ${SAIL_ACCESS_KEY}
    secret-key: ${SAIL_SECRET_KEY}
    bucket: ""
    region: ""
    path-style: true
    cdn-domain: <your-cdn-domain>
```

密钥用 `${VAR}` 引用环境变量,避免在配置文件中明文存储。

## 常用命令

| 命令 | 说明 |
|------|------|
| `sail cp` / `upload` / `download` | 上传/下载(本地↔s3、s3↔s3) |
| `sail ls` / `tree` | 列举与树形查看 |
| `sail rm` | 删除对象 |
| `sail mv` | 移动对象(复制后删源) |
| `sail stat` | 查看对象元信息 |
| `sail view` / `cat` | 查看对象内容(多格式智能渲染) |
| `sail presign` | 生成预签名 URL |
| `sail url` | 生成 CDN 访问地址 |

## 文档

完整文档、配置说明、路径语法、与 AWS CLI 对照、实现细节等,见[仓库 README](https://github.com/BeCrafter/sail#readme)。

- 源码:<https://github.com/BeCrafter/sail>
- 问题反馈:<https://github.com/BeCrafter/sail/issues>

## 许可证

MIT
