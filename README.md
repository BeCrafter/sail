# sail — S3 Object Storage CLI

**[简体中文](./README.zh-CN.md) · English**

> A command-line tool built on the standard S3 protocol. A single static binary, zero runtime dependencies, cross-platform out of the box.

`sail` works with any S3-compatible object storage service (AWS S3, MinIO, Alibaba Cloud OSS, and various self-hosted S3-compatible services), providing everyday operations such as upload/download, listing, deletion, copy/move, content viewing, and presigned URLs. It isn't tied to any specific cloud vendor — configure and go.

- Open source: <https://github.com/BeCrafter/sail>
- Issues: <https://github.com/BeCrafter/sail/issues>

## Features

- **Standard S3 protocol**: path-style + SigV4 signing, compatible with AWS S3 / MinIO / Alibaba Cloud OSS and various self-hosted S3-compatible services
- **Rich transfer modes**: single files, recursive directories, piped streaming upload; large files auto-chunked (5MB / 16 concurrent)
- **Batch operations**: URL globbing (`cp/rm 's3://b/*.log'` expansion), batch delete (DeleteObjects, 1000 per batch), line-by-line piped delete (`ls | rm -r -`)
- **Object & bucket management**: download, listing (long format / directory view / tree / sort / list buckets), directory placeholder objects (mkdir/rmdir), bucket management (mb/rb), delete (batch/piped), copy/move (local↔s3, s3↔s3 server-side copy with zero bandwidth)
- **Incremental sync**: rsync-style `sync` (size + mtime comparison, `--checksum` content verification, `--update`, `--exclude/--include` filtering, `--delete`, `--dry-run`), local↔s3↔s3
- **Search & statistics**: `find` (name/size/time filtering), `du` (prefix-level usage)
- **Content viewing**: multi-format smart rendering — text/JSON/YAML/CSV/XML/image terminal ASCII art/binary; `head`/`tail`/`wc`/`grep` stream without writing to disk
- **Checksum & auth**: `checksum` (md5/sha256 computation and comparison), `presign` presigned URLs, public access URLs based on a CDN domain
- **Multi-profile config**: prod / test / staging environment switching, keys can reference env vars to avoid plaintext
- **Cross-platform**: macOS / Linux, single binary, download and use; shell auto-completion (zsh / bash / fish)

## Install

### Method 1: npm install (recommended, cross-platform)

```bash
npm install -g @becrafter/sail
```

npm automatically downloads a single binary matching your OS and CPU architecture — the `sail` command works out of the box. Supports macOS (arm64/x64) and Linux (arm64/x64). Binaries are hosted on the npm registry, so no additional download is needed.

### Method 2: Download a binary

Download the binary for your platform from the [Releases page](https://github.com/BeCrafter/sail/releases) and place it in your `PATH`.

### Method 3: go install

```bash
go install github.com/BeCrafter/sail@latest
```

### Method 4: Build from source

```bash
git clone https://github.com/BeCrafter/sail.git
cd sail && go build -o sail .
```

## Config

### Quick init

```bash
sail config setup
```

Interactively generates or updates `~/.sail/config.yaml` (`--reset` resets to a fresh config; if the file exists, adds or reconfigures a profile while keeping the others), and optionally installs shell auto-completion. Wizard highlights:

- `endpoint` is required — leaving it empty re-prompts in place
- `access-key` / `secret-key` can be entered in plaintext; press Enter on empty to reference per-profile env vars (see "Key security" below). After writing, it prints the variable names you need to `export`
- When reconfiguring an existing profile, already-configured plaintext keys are not echoed — press Enter to keep them
- After writing, prints a config summary with empty fields clearly marked, for easy review of missing items

```yaml
# Keys can reference environment variables via ${VAR} to avoid plaintext.
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
    # cdn-bucket-path: false  # whether the CDN domain already contains the bucket path; comment out to auto-detect
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

Leaving `access-key`/`secret-key` empty in the wizard automatically writes per-profile placeholders `SAIL_<PROFILE>_(ACCESS|SECRET)_KEY` (profile name uppercased, non-alphanumeric characters such as hyphens converted to underscores; if the sanitized name is empty, falls back to the global `SAIL_ACCESS_KEY` name). You can also manually change it to any `${VAR}` in the config file.

### cdn-domain notes

`cdn-domain` is used by `sail url` to generate public access URLs for files — fill in the CDN domain of your storage service.

**Bucket dedup**: `sail url` checks whether the `cdn-domain` path already contains a bucket segment (path-style `.../bucket/`); if so, it doesn't append it again, avoiding broken links like `.../bucket/bucket/key`. Auto-detection only inspects path segments and does not infer subdomains. If auto-detection fails or you have a special mapping (e.g. the domain maps directly to a bucket, or the URL doesn't contain the bucket), use the `cdn-bucket-path` config option to declare it explicitly: `true` means the domain already contains the bucket (do not append), `false` means it does not (always append), and commenting it out enables auto-detection. You can also use `--no-bucket` to specify it once.

**Note**: only files in buckets with `public-read` permission can be accessed via the CDN domain; private buckets can only be accessed through authenticated GetObject.

### region & path-style notes

These two are common S3 protocol parameters — choose based on the storage service you connect to:

| Parameter | Meaning | AWS S3 | MinIO / self-hosted | Alibaba Cloud OSS |
|------|------|--------|-------------|-----------|
| `region` | Data center region | Fill in the actual value, e.g. `us-east-1` | Leave empty | Fill in e.g. `oss-cn-hangzhou` |
| `path-style` | URL addressing style | `false` (virtual-hosted) | `true` | `false` |

- **path-style**: when `true` the URL is `endpoint/bucket/key`; when `false` the URL is `bucket.endpoint/key`. Self-hosted S3-compatible services usually only support path-style.
- **region**: usually left empty for self-hosted services. AWS SDK internal rules require a non-empty region; when empty, the code uses `us-east-1` as a placeholder (it doesn't affect the actual request target, since endpoint is overridden).

### Key security

Keys in the config file can be written two ways: plaintext, or `${VAR}` referencing an environment variable (to avoid plaintext on disk):

```yaml
access-key: my-plain-access-key        # Option 1: plaintext
access-key: ${SAIL_TEST_ACCESS_KEY}    # Option 2: reference an environment variable
```

In `sail config setup`, leaving the key empty on Enter automatically uses option 2 and derives the variable name per profile (profile `test` → `SAIL_TEST_ACCESS_KEY`, `staging-eu` → `SAIL_STAGING_EU_ACCESS_KEY`, i.e. uppercased, non-alphanumeric characters such as hyphens converted to underscores; falls back to `SAIL_ACCESS_KEY` if the sanitized name is empty), so environments don't share. At the end of the wizard it prints the variable names to export, for example:

```bash
export SAIL_TEST_ACCESS_KEY="your-access-key"
export SAIL_TEST_SECRET_KEY="your-secret-key"
```

When these variables are not set, sail reports `missing access-key/secret-key` on startup.

Note the distinction between two kinds of env vars: the variables referenced inside the file via `${VAR}` (per-profile, e.g. `SAIL_TEST_ACCESS_KEY`) assign the key values; the `SAIL_ACCESS_KEY` etc. in the "Environment variable overrides" table below are runtime global overrides — once set they take effect directly, ignoring the config file. Precedence: global override env vars > `${VAR}` expansion in config > empty (startup reports missing keys).

### Environment variable overrides

| Variable | Effect |
|------|------|
| `SAIL_ENDPOINT` | Override endpoint |
| `SAIL_ACCESS_KEY` | Override access key |
| `SAIL_SECRET_KEY` | Override secret key |
| `SAIL_BUCKET` | Override default bucket |
| `SAIL_CDN_DOMAIN` | Override CDN domain |

## Usage

> **Path syntax**: `s3://bucket/key` explicitly specifies the bucket; `s3:///key` (empty bucket segment) uses the configured default bucket; `s3://bucket` (with `ls` only) lists buckets. Cross-bucket sync still uses explicit `s3://bucket/key`.

```bash
# Show version
sail --version          # or sail -v

# Copy (local↔s3, s3↔s3); upload/download are aliases for cp
sail cp local.txt s3://mybucket/path/local.txt
sail cp local.txt s3:///path/local.txt           # s3:/// uses the configured default bucket
sail upload local.txt                            # 1 arg: upload to default bucket, key uses the filename
sail cp -r ./dir s3://mybucket/prefix/           # recursively mirror a directory
sail cp 's3://mybucket/logs/*.log' s3://mybucket/archive/   # glob batch copy (* crosses /, preserves hierarchy)
sail cp 's3://mybucket/*.json' ./download-dir/   # glob batch download
cat file | sail upload - s3://mybucket/key       # pipe input

# Bucket management (mb/rb and ls --buckets)
sail mb s3://my-new-bucket
sail rb s3://my-old-bucket                       # deletes only empty buckets; for non-empty, run sail rm -r s3://my-old-bucket/ first
sail ls --buckets                                # list all buckets

# Download (s3→local); download is an alias for cp
sail cp s3://mybucket/key local.txt
sail download s3://mybucket/key                  # 1 arg: download to current directory

# List
sail ls s3://mybucket/prefix/
sail ls -l s3://mybucket/                        # long format: size + modified time
sail ls -l -t s3://mybucket/                     # sort by modified time (new→old), --human for human-readable sizes
sail ls -l -S -r s3://mybucket/                  # sort by size (large→small), then reverse
sail ls -d s3://mybucket/prefix/                 # list only sub-directories at this level (no files), like ls -d

# Find and statistics
sail find s3://mybucket/logs --name '*.log' -l   # glob by filename (repeatable)
sail find s3://mybucket --size +1M --newer 2026-01-01   # size/time filters
sail du -h s3://mybucket/prefix/                 # prefix-level usage
sail du -h --max-depth 1 s3://mybucket           # show only 1 level + total
sail du -s s3://mybucket/prefix/                 # print only the total

# Tree view (S3 prefix or local directory)
sail tree s3://mybucket/prefix/                  # full tree
sail tree -L 2 s3://mybucket/prefix/             # limit depth to 2
sail tree -d s3://mybucket/prefix/               # directories only
sail tree -s --human s3://mybucket/prefix/      # files with human-readable sizes
sail tree ./cmd                                  # local directory tree

# Delete and directory placeholder objects
sail rm s3://mybucket/key
sail rm -r s3://mybucket/prefix/                 # recursive delete (batch DeleteObjects, 1000 per batch)
sail rm key1 key2 key3                          # multi-arg batch
sail rm 's3://mybucket/logs/*.tmp'              # glob match delete
sail ls s3://mybucket/prefix/ | sail rm -r -    # read keys line-by-line from pipe (xargs-style)
sail mkdir s3://mybucket/new/dir/               # directory placeholder object (inherent -p semantics)
sail rmdir s3://mybucket/new/dir/               # deletes only empty directories; use rm -r for non-empty

# Incremental sync (rsync-style: size + modified-time comparison, idempotent; see --help for all options)
sail sync ./dir s3://mybucket/mirror/
sail sync --exclude '*.tmp' --delete ./dir s3://mybucket/mirror/
sail sync --include '*.json' s3://mybucket/mirror/ ./dir2 --dry-run   # whitelist + dry run
sail sync --checksum ./dir s3://mybucket/mirror/  # when sizes match, verify by content md5
sail sync --update ./dir s3://mybucket/mirror/    # transfer only entries newer than the target

# Presigned URL (some services don't support it, see Limitations below)
sail presign s3://mybucket/key --expires 3600

# Generate a CDN access URL
sail url s3://mybucket/path/file.jpg
sail url s3://mybucket/path/file.jpg --cdn https://<your-cdn-domain>
sail url s3://mybucket/path/file.jpg --no-bucket   # CDN domain already contains the bucket path, don't append again

# View object/file content (smart rendering by format, no config needed for local files)
sail view s3://mybucket/config.json              # JSON auto pretty-printed
sail view ./local.log                            # text/code output directly
sail view s3://mybucket/data.csv                # CSV aligned table
sail view s3://mybucket/photo.png               # image terminal ASCII art (half-block chars, visible in any terminal)
sail view s3://mybucket/data.json --raw         # raw output, good for piping: sail view ... --raw | jq .
sail cat s3://mybucket/data.json                 # cat is an alias for view --raw
sail view s3://mybucket/big.json --force        # skip the size limit
sail view s3://mybucket/photo.png --width 60    # set ASCII art column width

# Stream content (s3 paths use Range to fetch only the needed portion, not the whole object)
sail head -n 20 s3://mybucket/logs/app.log      # first N lines
sail head --bytes 4096 s3://mybucket/data.bin   # first N bytes
sail tail -n 50 s3://mybucket/logs/app.log      # last N lines (Range tail window)
sail wc -l s3://mybucket/logs/app.log           # line count (default three columns: lines words bytes)
sail grep -n "ERROR" s3://mybucket/logs/app.log # regex line-by-line search (supports -i/-v/-l/-n)

# Checksum (md5/sha256 streaming computation and comparison, no config needed for local files)
sail checksum s3://mybucket/data.bin            # default md5
sail checksum --algo sha256 --compare ./local.bin s3://mybucket/data.bin
sail checksum --etag s3://mybucket/data.bin     # show the raw ETag (note: multipart object ETag ≠ content md5)

# Copy objects/files (local↔s3, s3↔s3 uses server-side CopyObject with zero bandwidth)
sail cp ./local.txt s3://mybucket/path/copied.txt
sail cp ./local.txt s3://mybucket/path/          # trailing / means into a directory
sail cp s3://mybucket/a.txt ./out.txt
sail cp -r ./dir s3://mybucket/mirror/           # recursively mirror a local directory
sail cp -r s3://mybucket/prefix/ s3://mybucket/dest/   # server-side recursive copy
sail cp --dry-run ./local.txt s3://mybucket/x   # dry run, no actual copy

# Move objects/files (copy then delete source)
sail mv s3://mybucket/a.txt s3://mybucket/moved.txt     # single object, no confirmation
sail mv ./local.txt s3://mybucket/uploaded.txt
sail mv -r s3://mybucket/src/ s3://mybucket/dst/         # recursive, interactive confirmation [y/N]
sail mv -r --yes s3://mybucket/src/ s3://mybucket/dst/   # skip confirmation

# View object/file metadata (HeadObject / os.Stat)
sail stat s3://mybucket/config.json             # size/content-type/last-modified/etag
sail stat ./local.log                           # local file metadata

# Switch profile
sail -p test upload local.txt s3://testbucket/local.txt
```

## Cross-check with AWS CLI

Behavior matches `aws s3`; you can cross-check with the AWS CLI:

```bash
aws s3 ls --endpoint-url <your-s3-endpoint> s3://mybucket/
```

## Limitations

- Bucket and object key naming rules and length limits depend on the connected S3 service; follow each service's constraints.
- **Some S3-compatible services don't support presigned URLs**: certain self-hosted S3 services don't support query string auth (returning "Authorization empty") and only support Authorization header auth. For public access, use a CDN domain to access files that are already set public.

## Implementation details

### S3 compatibility adaptations

Some self-hosted S3-compatible services differ from standard AWS S3; the tool adapts accordingly:

1. **Checksum disabled**: AWS SDK v2 uses `aws-chunked` content encoding + CRC32 trailing checksum by default on upload. Some S3-compatible servers don't decode `aws-chunked`, corrupting stored data with the trailer (especially severe for large multipart uploads). The tool sets `RequestChecksumCalculation = WhenRequired` and `ResponseChecksumValidation = WhenRequired` in both the client and the uploader to disable this behavior.
2. **Region placeholder**: some S3 services have an empty region, but AWS SDK v2's endpoint rules require a non-empty region. The tool uses `us-east-1` as the placeholder (endpoint is overridden by BaseEndpoint, so it doesn't affect the actual request).
3. **CopyObject fallback**: some S3-compatible services' `CopyObject` returns success but produces a 0-byte object. The `cp`/`mv` s3↔s3 path does a HEAD check after CopyObject to verify the target size matches the source; if it doesn't, it automatically falls back to `download→re-upload` to guarantee data correctness. On standard S3 (AWS/MinIO), the CopyObject check passes and zero-bandwidth server-side copy is still used.

## Release

Releases go through GitHub Actions automation: pushing a tag like `vX.Y.Z` triggers cross-compilation + publish to npm, no local login needed.

1. Add `NPM_TOKEN` (npm automation token with `@becrafter` scope publish permission) in the repo's **Settings → Secrets and variables → Actions**.
2. Tag and push:
   ```bash
   git tag v0.1.0 && git push origin v0.1.0
   ```
3. After the workflow finishes, the 4 platform sub-packages + main package are published to `registry.npmjs.org`. You can also trigger it manually from the Actions page and fill in the version.

Local release (without CI) still works: `make release VERSION=0.1.0` (prompts `npm login` if not logged in).

## Contributing

Issues and Pull Requests welcome: <https://github.com/BeCrafter/sail/pulls>

## License

[MIT](./LICENSE)
