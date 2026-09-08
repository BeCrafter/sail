# @becrafter/sail

> S3 object storage CLI — a single static binary, zero runtime dependencies.

`sail` works with any S3-compatible object storage service (AWS S3, MinIO, Alibaba Cloud OSS, and various self-hosted S3-compatible services), providing everyday operations such as upload/download, listing, deletion, copy/move, content viewing, and presigned URLs. This npm package is a cross-platform installer: it fetches the matching static binary for your platform.

## Install

```bash
npm install -g @becrafter/sail
```

## Requirements

- Node.js `>= 18` (only used by the installer to distribute the binary; `sail` itself has no runtime dependencies)
- Supported platforms:

| OS | Arch | Platform subpackage |
|----|------|---------------------|
| macOS | arm64 (Apple Silicon) | `@becrafter/sail-darwin-arm64` |
| macOS | x64 (Intel) | `@becrafter/sail-darwin-x64` |
| Linux | arm64 | `@becrafter/sail-linux-arm64` |
| Linux | x64 | `@becrafter/sail-linux-x64` |

After install, npm automatically downloads only the one platform binary matching your OS and CPU architecture — the `sail` command works out of the box.

## Quick start

```bash
sail config setup                               # interactively generate/update ~/.sail/config.yaml
sail cp local.txt s3://mybucket/path/local.txt  # upload
sail ls s3://mybucket/                          # list
sail cp s3://mybucket/key local.txt             # download
```

Example config:

```yaml
lang: en
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
    # cdn-bucket-path: false  # whether the CDN domain already includes the bucket path; comment out to auto-detect
```

Keys reference environment variables via `${VAR}`, avoiding plaintext storage in the config file.

## Common commands

| Command | Description |
|---------|-------------|
| `sail cp` / `upload` / `download` | Upload/download (local↔s3, s3↔s3) |
| `sail ls` / `tree` | List and tree view |
| `sail rm` | Delete objects |
| `sail mv` | Move objects (copy then delete source) |
| `sail stat` | View object metadata |
| `sail view` / `cat` | View object contents (multi-format smart rendering) |
| `sail presign` | Generate presigned URLs |
| `sail url` | Generate a CDN access URL |

## Documentation

Full documentation — config guide, path syntax, AWS CLI cross-check, implementation details, and more — see the [repo README](https://github.com/BeCrafter/sail#readme).

- Source: <https://github.com/BeCrafter/sail>
- Issues: <https://github.com/BeCrafter/sail/issues>

## License

MIT
