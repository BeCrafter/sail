# @becrafter/sail

> S3 object storage CLI — a single static binary, zero runtime dependencies.

`sail` works with any S3-compatible object storage service (AWS S3, MinIO, Alibaba Cloud OSS, and various self-hosted S3-compatible services), providing everyday operations such as upload/download, listing, deletion, copy/move, content viewing, and presigned URLs. This npm package is a cross-platform installer: it fetches the matching static binary for your platform.

## Install

### Global install

```bash
npm install -g @becrafter/sail
```

### No-install one-off run via npx

Run the latest published version directly, without a global install:

```bash
npx -y @becrafter/sail@latest <command>
npx -y @becrafter/sail@latest --help
npx -y @becrafter/sail@latest config setup
```

`-y` auto-confirms downloading the package; `@latest` pins the most recent published release instead of a stale cached one, so you always run the current version.

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

| Group | Command | Description |
|-------|---------|-------------|
| Transfer | `sail cp` / `upload` / `download` | Upload/download; s3↔s3 uses server-side copy (zero bandwidth) |
| Transfer | `sail mv` | Move objects/files (copy then delete source) |
| Transfer | `sail rm` | Delete objects (multi-arg / glob / recursive / piped) |
| Transfer | `sail sync` | rsync-style incremental sync (size + mtime, `--checksum`, filters, `--delete`) |
| Transfer | `sail mb` / `rb` | Create / delete buckets |
| Transfer | `sail mkdir` / `rmdir` | Create / delete directory placeholder objects |
| List & stats | `sail ls` | List objects or buckets (long format / sort / dirs) |
| List & stats | `sail tree` | Show object/file tree |
| List & stats | `sail find` | Find objects by name/size/time |
| List & stats | `sail du` | Summarize object size under a prefix |
| List & stats | `sail stat` | View object/file metadata |
| Content | `sail view` / `cat` | View contents (multi-format smart rendering / raw) |
| Content | `sail head` / `tail` | Read the head / tail of an object/file (Range) |
| Content | `sail wc` | Count lines, words, and bytes |
| Content | `sail grep` | Regex line-by-line search |
| Checksum & access | `sail checksum` | Compute md5/sha256 or show the raw ETag |
| Checksum & access | `sail presign` | Generate a presigned download URL |
| Checksum & access | `sail url` | Generate a CDN access URL |
| Config | `sail config` | Manage configuration (`config setup` wizard) |

All commands support `--help` for detailed usage and examples.

## Documentation

Full documentation — config guide, path syntax, AWS CLI cross-check, implementation details, and more — see the [repo README](https://github.com/BeCrafter/sail#readme).

- Source: <https://github.com/BeCrafter/sail>
- Issues: <https://github.com/BeCrafter/sail/issues>

## License

MIT
