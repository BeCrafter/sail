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
- **WebDAV gateway**: `serve webdav` mounts a bucket as a network drive — macOS Finder / Windows Explorer read and write directly, with zero client install
- **SMB gateway**: `serve smb` exports a bucket (or a prefix of it) as an SMB2 share — the same zero-install story over the protocol Finder and Explorer mount natively
- **Multi-profile config**: prod / test / staging environment switching, keys can reference env vars to avoid plaintext
- **Cross-platform**: macOS / Linux, single binary, download and use; shell auto-completion (zsh / bash / fish)

## Install

### Method 1: npm install (recommended, cross-platform)

```bash
npm install -g @becrafter/sail
```

npm automatically downloads a single binary matching your OS and CPU architecture — the `sail` command works out of the box. Supports macOS (arm64/x64) and Linux (arm64/x64). Binaries are hosted on the npm registry, so no additional download is needed.

**No-install one-off run via npx** — grab and run the latest published version without a global install:

```bash
npx -y @becrafter/sail@latest <command>
npx -y @becrafter/sail@latest --help
npx -y @becrafter/sail@latest config setup
```

`-y` auto-confirms downloading the package; `@latest` pins the most recent published release instead of a stale cached one, so you always run the current version.

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

Interactively generates or updates `~/.config/sail/config.yaml` (`--reset` resets to a fresh config; if the file exists, adds or reconfigures a profile while keeping the others), and optionally installs shell auto-completion. Wizard highlights:

- `endpoint` is required — leaving it empty re-prompts in place
- `access-key` / `secret-key` can be entered in plaintext; press Enter on empty to reference per-profile env vars (see "Key security" below). After writing, it prints the variable names you need to `export`
- When reconfiguring an existing profile, already-configured plaintext keys are not echoed — press Enter to keep them
- The WebDAV gateway (`serve:` block) is guided as well: listen / prefix / auth (single or multi-user) / TLS /
  chunked-upload / staging-dir. An existing `serve` block defaults to "keep" — choose to append users to the
  existing table, reconfigure it, or remove it; multi-user tables are validated as you enter them (duplicate
  names, nested prefixes, quota syntax), and the fields the wizard does not ask (size limits, `dir-cache-ttl`,
  `prewarm`) are kept as configured
- Inputs are normalized where possible: a bare port gets its colon (`8443` → `:8443`), a URL without a
  scheme gets `https://`, single-letter quota units become `MB`/`GB`/`TB`, `~` is expanded in paths, and
  y/n answers accept `yes`/`true`/`1`/`on`; invalid values are re-prompted with an explanation
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

### serve block

All `serve webdav` parameters can be pinned in a `serve:` block under a profile, so you don't re-type them on every launch; the CLI flags remain and override config as the highest priority. Precedence: `flag (explicitly set) > profile.serve.* > flag default`.

```yaml
profiles:
  prod:
    endpoint: <your-s3-endpoint>
    access-key: ${SAIL_PROD_ACCESS_KEY}
    secret-key: ${SAIL_PROD_SECRET_KEY}
    bucket: mybucket
    serve:
      listen: ":8443"
      prefix: ""                 # shared root prefix; empty = whole bucket
      user: alice
      password: ${SAIL_PROD_SERVE_PASSWORD}   # plaintext or ${VAR} reference
      # users:                    # multi-user mode (see "Multi-user" below); mutually exclusive with user/password
      #   - name: alice
      #     password: ${ALICE_PASSWORD}
      #     prefix: alice/        # relative to serve.prefix; omitted = the base prefix itself
      #     quota: 10GB         # units: MB/GB/TB (decimal); a plain number means bytes
      # tls-cert: /etc/cert.pem   # with tls-key enables HTTPS
      # tls-key: /etc/key.pem
      # staging-dir: /tmp/sail-stage
      # backend-max-object-size: 5TiB
      # max-upload-size: 5TiB     # empty = follow backend-max-object-size
      # chunked-upload: false
      # chunk-size: 4GiB
      # dir-cache-ttl: 60s
      # prewarm: [/bigdir]        # directories to keep hot in the background
```

Each `serve` field maps one-to-one to the same-named `serve webdav` flag (size fields use the same string format as the flags, e.g. `5TiB`). `user`/`password` accept plaintext or a `${VAR}` environment-variable reference, matching the access-key/secret-key key-security mechanism; empty fields fall back to the flag defaults. The optional `users` list enables multi-user mode — see "Multi-user" under the WebDAV gateway. `sail config setup` guides these fields interactively (including generating and validating the `users` table); fields it does not ask are kept as written in the file.

`sail serve smb` reads the same `serve:` block for everything protocol-agnostic (prefix, users, quota,
staging, size limits, chunking, cache TTL, prewarm) — one bucket's sharing layout does not have to be
written twice — and adds a small `serve.smb` sub-block for what is SMB-specific:

```yaml
    serve:
      prefix: shared
      users:
        - name: alice
          password: ${ALICE_PASSWORD}
          prefix: alice/
      smb:
        listen: ":1445"     # SMB's own port, deliberately not inherited from serve.listen
        share: sail         # share name in single-user mode; ignored in multi-user mode
        server-name: SAIL   # the name this server calls itself in the NTLM challenge
```

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

> **Complete flag reference**: the examples below are a guided tour, not an exhaustive list. Every command's full flag set, defaults, and semantics live in `sail <command> --help` (add `--lang zh` for Chinese). This holds for subcommands too: `sail serve webdav --help`, `sail config setup --help`.

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
sail du --human s3://mybucket/prefix/             # prefix-level usage
sail du --human --max-depth 1 s3://mybucket       # show only 1 level + total
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
sail cp --content-type text/markdown ./NOTES.md s3://mybucket/notes   # pin the type for this upload (-r included; s3→s3 copies unaffected)

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

## WebDAV gateway (`sail serve webdav`)

Mount a bucket (or the prefix given by `--prefix`) as a network drive: clients read and write
directly through the WebDAV support built into the OS, with no software to install. Listing,
uploading, downloading, dragging the progress bar with Range requests, renaming, and deleting
all behave like an ordinary network drive.

```bash
# Start (HTTPS recommended; supplying both --tls-cert/--tls-key enables it)
sail serve webdav --profile prod --listen :8443 \
  --user alice --password '***' --tls-cert cert.pem --tls-key key.pem

# Share only a prefix inside the bucket (mapped to /, out-of-prefix paths are always rejected)
sail serve webdav --bucket mybucket --prefix tenant-a --user alice --password '***'

# Print the one-time Windows client registry setup and mount command, then exit
sail serve webdav --print-windows-setup
```

The bucket is taken from the same resolution chain every other command uses: `--bucket` >
`SAIL_BUCKET` > `profile.bucket`. Startup is refused when none of the three yields a bucket;
the startup banner prints `bucket=`, `profile=`, and `prefix=` so what is exposed stays assertable.

Besides `--profile` and the global `--bucket`, every flag in the table below can also be written to
a profile's `serve:` block (see "serve block" above); omit the flag to read it from config, or pass
it explicitly to override the config value.

| Flag | Default | Description |
|---|---|---|
| `--profile` | config `default-profile` | Which profile to share: the bucket comes from that profile's `bucket` (overridable by global `--bucket` or `SAIL_BUCKET`); startup is refused when all three are empty |
| `--listen` | `:8080` | Listen address (`serve.listen`) |
| `--prefix` | empty | Shared root prefix (mapped to `/`); out-of-prefix paths are always rejected (`serve.prefix`) |
| `--user` / `--password` | empty | Basic auth; startup is refused when empty, anonymous sharing is not allowed (`serve.user`/`serve.password`). Mutually exclusive with `serve.users` |
| `--tls-cert` / `--tls-key` | empty | Supplying both enables HTTPS (`serve.tls-cert`/`serve.tls-key`) |
| `--backend-max-object-size` | `5TiB` | Declared backend per-object limit (S3 has no capability negotiation, it can't be probed) (`serve.backend-max-object-size`) |
| `--max-upload-size` | follows the flag above | Request body limit; over the limit returns 413 + actionable guidance before the body is fully read (`serve.max-upload-size`) |
| `--staging-dir` | system temp dir | Write staging directory; peak ≈ largest single file × concurrent uploads (`serve.staging-dir`) |
| `--chunked-upload` | `false` | Store files larger than `--chunk-size` as chunks + a manifest (off: 1 file = 1 object) (`serve.chunked-upload`) |
| `--chunk-size` | `4GiB` | Max physical chunk size, also the chunked-storage threshold (5MiB ~ 5GiB); requires `--chunked-upload` (`serve.chunk-size`) |
| `--dir-cache-ttl` | `60s` | How long a directory listing is cached (e.g. `60s`, `10m`); expired entries are served stale and refreshed in the background, so a warm directory never blocks; writes invalidate immediately, `0` disables. External bucket changes become visible after at most this long (`serve.dir-cache-ttl`) |
| `--prewarm` | empty | Directories to keep hot in the background (comma-separated logical paths, e.g. `/yiche,/modelImage`); each is listed once at startup then refreshed on a cycle, so the first visit does not pay the full listing cost. Intended for very large directories (100k+ entries, tens of seconds on first listing). In multi-user mode the same list applies inside each user's own space (`serve.prewarm`) |
| `--print-windows-setup` | — | Print the `.reg` content + PowerShell + a "you must restart the WebClient service" reminder, then exit |

The startup banner derives mount URLs from the bind address: for a wildcard bind (`:8080`/`0.0.0.0:8080`) it lists both `http://localhost:PORT/` (this machine) and each interface's LAN IP (other devices); for an explicit host it lists only that address.

> **Performance**: uploads/downloads stream via server-side Range reads without staging whole files in memory; the HTTP connection pool is tuned for high-concurrency S3, reusing long-lived connections when opening many files at once; directory listings are short-TTL cached. Opening a single file is 1 `HeadObject` + 1 `GetObject`.

> **Content-Type**: files uploaded through the mount are stored with a type inferred from the name (extension first, then the content signature) — so images/PDFs opened through a link render instead of downloading. A client that sends no Content-Type (macOS's built-in WebDAV client doesn't) or only the generic `application/octet-stream` counts as "no preference"; any other type the client sends is kept as-is.

### Multi-user (`serve.users`)

One gateway can serve multiple users, each with a private space. Write the user table into the
profile's `serve:` block — every user gets a Basic-auth identity and their own namespace:

```yaml
    serve:
      listen: ":8443"
      prefix: team/            # base prefix (cold zone: changes need a restart)
      users:
        - name: alice
          password: ${ALICE_PASSWORD}   # ${VAR} reference, same as other serve fields
          prefix: alice/                # relative to prefix; omitted = the base prefix itself
          quota: 10GB                   # per-user space limit (MB/GB/TB); hot-applies without restart
        - name: bob
          password: ${BOB_PASSWORD}
          prefix: shared/bob-data/      # any relative segment
```

- **Space quota (`quota`).** Each user's `quota` (e.g. `500MB`, `10GB`, `1TB`; units are decimal `MB`/`GB`/`TB`, and a plain number means bytes) caps the
  physical bytes stored under their prefix — the billable size, including `.sail/` chunk parts and
  manifests. Over-quota writes return **507** with actionable guidance: before the body is read when
  the request declares a Content-Length, or at the commit point for COPY (which has none); overwrites
  get the old object's size released from the arithmetic. Usage is a lazy snapshot (default TTL 5
  minutes) plus in-flight reservations — best-effort within the window, so writes made outside the
  gateway (e.g. `sail cp` directly to the bucket) become visible at the next refresh. An expired
  snapshot keeps serving the previous value while a background refresh runs (single-flight), so a
  large prefix never blocks writes; only the first write after startup may wait briefly (≤3s) for the
  initial snapshot. Changing `quota` in the config hot-applies without a restart.
  The quota is also reported to WebDAV clients (RFC 4331 `DAV:quota-available-bytes` /
  `DAV:quota-used-bytes` on directory PROPFIND), so Finder / Explorer show the remaining space.
  Deleting or renaming invalidates the snapshot immediately, so space freed through the gateway counts
  on the next write instead of waiting out the TTL.
- **Structural isolation.** A user's effective prefix is the base `prefix` + their `prefix`
  segment; every object key they touch (including `.sail/` chunk parts) lands inside it. A user's
  `/` is their own space — other users' objects are structurally unreachable, `..` traversal is
  rejected, and the access log attributes every request as `user=<name>`.
- **Hot reload.** The user table is watched: editing the config file (add/remove users, change
  passwords, prefixes or quotas) takes effect within seconds, no restart. `listen`, TLS certificates,
  `staging-dir`, chunked-upload settings, `dir-cache-ttl`, `prewarm` and the base `prefix` are cold
  zone — changing them logs a "restart required" warning and keeps the running values. A broken YAML keeps the previous
  user table (with a warning); deleting and recreating the config file does not kill the watcher,
  and the new content is picked up automatically.
- **Directory auto-create.** When a user comes into effect (startup or reload), sail asynchronously
  creates a 0-byte directory marker at the user's prefix so the folder is visible in S3 consoles
  and to `sail ls`. Creation is idempotent and best-effort: an S3 hiccup logs a warning and the
  user still works — the marker is visibility, not a mount prerequisite.
- **Fail-loud conflicts.** `users` and `user`/`password` are mutually exclusive (across sources
  too: a `--user`/`--password` flag plus a config `users` table is refused). Effective prefixes
  must be pairwise distinct and non-nested — `alice/` and `alice/logs/` cannot coexist, since the
  outer user would list the inner user's objects. Both checks run at startup and on every reload;
  violations are refused and the previous state is kept.
- **Single-user mode stays.** `--user`/`--password` (or `serve.user`/`serve.password`) behaves
  exactly as before: one implicit user on the base prefix. Credentials that come from flags
  disable hot reload — a warning is printed at startup.

### Mounting from clients

- **macOS Finder**: `Go → Connect to Server` (⌘K), enter `https://host:8443`, sign in with `--user`/`--password`.
- **Windows Explorer**: first run `sail serve webdav --print-windows-setup` to import the registry
  settings and restart the WebClient service, then
  `net use Z: \\host@SSL@8443\DavWWWRoot /user:alice`.

Windows caps a single upload at about 50MB by default. That gate lives in the **client registry**;
no server-side flag can move it. So sail deliberately does not offer a `--max-file-size` style fake
knob that looks like it could raise the gate — use `--print-windows-setup` for the correct
client-side procedure.

### Two limitations

- **LOCK is an in-process lock**: the locks required by the WebDAV protocol are implemented in
  memory, so they are lost on restart and are not shared across instances. That is enough for a
  single-instance, short-transaction network drive; with multiple instances the locks clients see
  are not shared.
- **The write path needs a staging disk**: uploads land in full under `--staging-dir`, and only on
  commit are they chunked and uploaded to S3. Disk peak ≈ largest single file × concurrent uploads;
  when space is short the write returns **507** before writing rather than failing midway. Point
  `--staging-dir` at a disk with enough room when large files are common.

Other trade-offs: directory-level `MOVE`/`COPY` returns **501**, leaving the client to fall back to
"copy + delete" (P1 only does object-level moves); `.sail/` is a reserved prefix — it is filtered out
of directory listings and any direct access to it is answered as "not found", so clients can neither
read nor damage the chunk parts.

### Chunked storage (`--chunked-upload`)

Off by default: one file is one object, and existing buckets plus third-party S3 tools see nothing
new. Turn it on when the backend has a small per-object ceiling (for example a gateway in front of
the bucket that rejects large objects): files larger than `--chunk-size` are then split into chunks
stored under the reserved `.sail/parts/<logical path>/<version>/` prefix, with a few-hundred-byte
JSON **manifest** at the logical key.

```bash
# Split anything over 100MiB; the pieces live under .sail/, the key holds a manifest
sail serve webdav --profile prod --user alice --password '***' \
  --chunked-upload --chunk-size 100MiB
```

Rules that hold once it is on:

- **The manifest is the commit point.** Chunks are uploaded first; the logical key is only
  overwritten once every chunk has landed, so a client never sees a half-written file. Reads follow
  the manifest and fetch only the chunk(s) a Range touches — no full-file buffering.
- **A chunk directory belongs to one logical path.** The chunks of `/a/big.bin` live under
  `.sail/parts/a/big.bin/<version>/`; another path stores its own copy even when the content is
  byte-identical. An overwrite therefore purges only its own old generation, and deleting a file or
  directory reclaims that path's chunks — never another path's.
- **Known limitation: an overwrite interrupts in-flight reads.** The old chunks are reclaimed as soon
  as the manifest switches, so a reader still streaming the previous content is cut off mid-transfer.
  The failure is **loud** (the response's `Content-Length` disagrees with the bytes delivered, and
  versions are never mixed within one response) and a retry succeeds; there is no delayed reclamation
  or in-flight reader registration today.
- **Chunks share the logical keys' root prefix** (`--prefix`). With a shared root prefix configured,
  chunks stay inside it too, so instances sharing one bucket cannot overwrite each other's data.
- `--chunk-size` must be between `5MiB` and `5GiB` (the S3 `PutObject` request ceiling) and must not
  exceed `--backend-max-object-size`; an out-of-range value refuses startup instead of failing later.
- **`sail presign` fails loud on chunked keys**: a presigned URL would hand out the manifest, not the
  file. Read those keys through `sail serve webdav` or `sail cp` (or pass `--allow-chunked` if you
  really want the manifest itself).
- **The bucket now contains `.sail/` objects.** They are filtered out of WebDAV listings, but
  `sail ls` and other clients will show them. If a delete or overwrite is interrupted, orphaned
  chunks may remain; they are invisible to listing and data-consistent. There is no `sail gc`
  command yet, but the **detection rule now exists**: the directory name records the owning logical
  path, so a single HEAD per candidate settles whether the generation is still referenced — a GC
  never has to scan the whole bucket.

## SMB gateway (`sail serve smb`)

Exports the whole bucket (or the prefix given by `--prefix`) as an SMB2 share, so Finder and
Explorer mount it as a network drive with nothing installed on the client:

```bash
# Start (defaults to a high port: 445 is the port clients dial by default but it needs root)
sail serve smb --profile prod --listen :1445 \
  --user alice --password '***' --share sail

# Share only a prefix inside the bucket (mapped to /, out-of-prefix paths are always rejected)
sail serve smb --profile prod --prefix shared --share sail

# Mount
#   macOS:   smb://host:1445/sail          (Finder → Go → Connect to Server)
#   Windows: \\host@1445\sail
#   Linux:   mount -t cifs //host:1445/sail /mnt -o username=alice,port=1445
```

The macOS form is the one that has been exercised end to end (native `mount_smbfs` against a
self-hosted gateway); the Linux and Windows commands above are the documented syntax for those
clients — run them once on your own platform before depending on them.

Which bucket is shared comes from the profile, exactly as in WebDAV mode. The port must be named at
mount time on every platform because the server deliberately does not require root for port 445.
Mounting a **non-standard port on Windows** has historically needed client-side configuration, and
that path is the one thing here that has not been verified end to end (the acceptance run had no
Windows machine): test it on your Windows version before rolling it out.

### Flags

Everything protocol-agnostic is shared with `serve webdav` and has identical semantics
(`--prefix`, `--user`/`--password`, `serve.users`, `--staging-dir`, `--backend-max-object-size`,
`--max-upload-size`, `--chunked-upload`, `--chunk-size`, `--dir-cache-ttl`, `--prewarm`).
The SMB-specific ones:

| Flag | Default | Notes (`serve.smb.*` config key) |
|---|---|---|
| `--listen` | `:1445` | Listen address. 445 needs root on every OS; clients name the port when mounting (`serve.smb.listen`) |
| `--share` | `sail` | Share name in single-user mode; in multi-user mode each user gets a share named after them instead (`serve.smb.share`) |
| `--server-name` | `SAIL` | The name this server calls itself in the NTLM challenge; clients display it (`serve.smb.server-name`) |

There is no `--tls-cert`/`--tls-key` here: SMB2 has no TLS layer — message signing and encryption
live inside the protocol and the library handles them.

`--max-upload-size` refuses over-limit writes, but the client sees "permission denied" rather than a
distinct error — see the differences below.

### Multi-user and quota

The user table is the same `serve.users` list, with the same per-user prefix and quota. One
difference follows from the protocol: **a share binds exactly one filesystem**, so there is no way to
give one share different content per user. In multi-user mode each user therefore gets their own
share, named after them:

```bash
sail serve smb --profile prod --listen :1445   # serve.users: alice, bob
# alice mounts smb://host:1445/alice, bob mounts smb://host:1445/bob
# bob cannot connect to alice's share at all (the share is restricted, not hidden)
```

Quota is enforced by the same protocol-independent decorator as in WebDAV mode: a write that would
exceed the limit is refused at commit time, and nothing is written. What differs is what the client
sees — see the silent-refusal bullet under "SMB-specific limitations" below.

### Differences from WebDAV mode

| | WebDAV | SMB |
|---|---|---|
| Transport security | `--tls-cert`/`--tls-key` (HTTPS) | SMB2 message signing/encryption inside the protocol; no TLS flags |
| Authentication | HTTP Basic, compared per request | NTLMv2 challenge/response (the server needs the plaintext password) |
| Default port | `:8080` | `:1445` (445 needs root) |
| Mount shape | `https://host:port/path` | `smb://host:port/share` (Windows: `\\host@port\share`) |
| Error reporting | HTTP status codes (404/405/413/507…) | NTSTATUS codes |
| Write model | PUT is already a sequential stream | Positional writes are staged locally, then uploaded on close |
| Config hot reload | user table hot-reloads on config change | **not supported** — the library can add shares and users but never remove them, so changing `serve.users` needs a restart |
| Quota / size-limit error | `507 Insufficient Storage` / `413` | refused at close; the client is not told (see below) |

### SMB-specific limitations

- **A failed commit is not reported to the client.** The SMB2 library closes the handle without
  checking the result, so a client whose upload could not be written still sees a successful CLOSE.
  Failures are logged on the server (`committing <path> failed`) — if a file looks unchanged after a
  write, check the server log. The window is narrow (the commit happens on close, in-process), but it
  is not zero, and it is the one place where a silent failure is possible.
- **An over-quota or over-limit write is refused, but the client is not told.** The check has to run
  at commit time (SMB2 carries no Content-Length up front), the commit happens when the client closes
  the handle, and the library ignores that return value — so the object is correctly *not* written
  (nothing over quota ever lands in the bucket), while the client sees a successful close and only
  finds out by looking at the file afterwards. The server log has a line for it. The library also
  maps filesystem errors to NTSTATUS codes itself with no hook to override, so `STATUS_DISK_FULL` and
  `STATUS_FILE_TOO_LARGE` are unreachable even where an error does reach the client.
- **Free-space reporting is fixed.** The library answers "how much room is there" with a constant
  (4 GiB volume, 2 GiB free) because the driver has no say in it; a client therefore cannot see the
  real quota, and `quota-available-bytes`-style reporting is WebDAV-only.
- **Timestamps are not persisted.** S3 stores only a modification time, so a client's
  creation/access/change times are accepted on writes and then read back as "now". Setting them is
  not refused (a refusal at the end of a copy reports failure after the bytes arrived), it is simply
  not stored.
- **Rename and delete cost what the backend costs — and on a slow backend that breaks clients.**
  On S3 a rename is a server-side copy plus a delete; on a gateway whose single DELETE takes ~27
  seconds (and whose batch endpoint answers 500, so there is no fast path), a rename takes that long,
  while clients treat rename as an instant operation. The sharper case: macOS `cp` creates an
  AppleDouble sidecar (`._name`) and deletes it when the copy finishes — that delete holds the
  share's write lock for its full latency, the client gives up waiting, and the copy reports
  `Bad file descriptor` **even though every byte committed correctly**. Measured on such a gateway
  (native macOS mount): a 1 MiB copy finishes in 32 s; an 8 MiB copy reports that error with the
  object perfect in the bucket. `internal/s3del` already parallelizes deletes; the remaining cost is
  the backend's, which makes this a gateway bug worth fixing rather than a sail one.
- **Directory listings are cached** (`--dir-cache-ttl`, default 60s) and a write invalidates its own
  directory immediately. Changes made by anyone else become visible after at most one TTL.
- **Renaming a file that is still open writes to the old name.** The shell's handle is bound to the
  path it was opened with, and the library's rename updates only its own record, so the commit at
  close goes to the pre-rename path: the old name reappears with the new content while the renamed
  file keeps its earlier bytes. File managers close before renaming in the normal case, so this is
  rare — but it is not detectable from inside the shell.
- Symbolic links, hard links, extended attributes and ACLs are not S3 concepts and answer
  "not supported".

The trade-off that makes all of this work is that `internal/smbfs/` is a thin shell over the same
protocol-independent kernel the WebDAV shell uses: quota, prefix isolation, chunked storage and the
delete path are the same code, and swapping the SMB library touches nothing below this package.

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
