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
sail config setup                               # interactively generate/update ~/.config/sail/config.yaml
sail cp local.txt s3://mybucket/path/local.txt  # upload
sail ls s3://mybucket/                          # list
sail cp s3://mybucket/key local.txt             # download
sail cp --content-type text/markdown NOTES.md s3://mybucket/notes   # pin the type for this upload (s3→s3 copies unaffected)
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

All `sail serve webdav` flags can also be pinned in a profile's `serve:` block, so startup does not need them on the command line (explicit flags still override):

```yaml
profiles:
  prod:
    # ...connection settings above...
    bucket: mybucket
    serve:
      listen: ":8443"
      user: alice
      password: ${SAIL_PROD_SERVE_PASSWORD}   # plaintext or ${VAR}
      # users: …                              # multi-user mode — see "Multi-user" below; mutually exclusive with user/password
      # prefix, tls-cert, tls-key, staging-dir, chunked-upload, dir-cache-ttl, prewarm, ... also supported
```

`sail config setup` guides these fields interactively (including generating and validating the multi-user
table); fields it does not ask are kept as written in the config file.

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
| Server | `sail serve webdav` | Share a bucket as a mountable network drive (macOS Finder / Windows Explorer) |
| Config | `sail config` | Manage configuration (`config setup` wizard) |

All commands support `--help` for detailed usage and examples.

## WebDAV gateway (`sail serve webdav`)

Mount a bucket (or the prefix given by `--prefix`) as a network drive: clients read and write
directly through the WebDAV support built into the OS, with no software to install. Listing,
uploading, downloading, dragging the progress bar with Range requests, renaming, and deleting
all behave like an ordinary network drive.

```bash
# Start (HTTPS recommended; supplying both --tls-cert/--tls-key enables it)
sail serve webdav --bucket mybucket --listen :8443 \
  --user alice --password '***' --tls-cert cert.pem --tls-key key.pem

# Share only a prefix inside the bucket (mapped to /, out-of-prefix paths are always rejected)
sail serve webdav --bucket mybucket --prefix tenant-a --user alice --password '***'

# Omit --bucket: resolved like every other command (--bucket > SAIL_BUCKET > profile.bucket)
sail serve webdav --profile prod --user alice --password '***'

# Print the one-time Windows client registry setup and mount command, then exit
sail serve webdav --print-windows-setup
```

The bucket is taken from the same resolution chain every other command uses: `--bucket` >
`SAIL_BUCKET` > `profile.bucket`. Startup is refused when none of the three yields a bucket;
the startup banner prints `bucket=`, `profile=`, `prefix=`, and the mountable addresses.

| Flag | Default | Description |
|---|---|---|
| `--listen` | `:8080` | Listen address (`serve.listen`) |
| `--prefix` | empty | Shared root prefix (mapped to `/`); out-of-prefix paths are always rejected (`serve.prefix`) |
| `--user` / `--password` | empty | Basic auth; startup is refused when empty, anonymous sharing is not allowed. Mutually exclusive with `serve.users` |
| `--tls-cert` / `--tls-key` | empty | Supplying both enables HTTPS |
| `--staging-dir` | system temp dir | Write staging directory; peak ≈ largest single file × concurrent uploads |
| `--chunked-upload` / `--chunk-size` | `false` / `4GiB` | Store files larger than `--chunk-size` as chunks + a manifest (off: 1 file = 1 object) |
| `--dir-cache-ttl` | `60s` | Directory listing cache; expired entries are served stale and refreshed in the background (`serve.dir-cache-ttl`) |
| `--prewarm` | empty | Directories to keep hot in the background (comma-separated, e.g. `/bigdir`) (`serve.prewarm`) |
| `--print-windows-setup` | — | Print the Windows client registry setup and mount command, then exit |

Mount with **macOS Finder** (⌘K, `https://host:8443`) or **Windows Explorer** (run
`--print-windows-setup` first to lift the ~50MB WebClient registry gate, then `net use Z: \\host@SSL@8443\DavWWWRoot`).

Uploaded files are stored with a Content-Type inferred from the extension, then the content
signature — so images/PDFs opened through a public link render instead of downloading. A client
that sends no Content-Type (macOS's built-in WebDAV client doesn't) or only the generic
`application/octet-stream` counts as "no preference"; any other type the client sends is kept as-is.

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

- **Structural isolation.** Each user's effective prefix is the base `prefix` + their `prefix`
  segment; every object key they touch (including `.sail/` chunk parts) lands inside it. A user's
  `/` is their own space — other users' objects are structurally unreachable, `..` traversal is
  rejected, and the access log attributes every request as `user=<name>`.
- **Space quota (`quota`).** Caps the physical bytes stored under the user's prefix — the billable
  size, including `.sail/` chunk parts and manifests. `quota` units are `MB`/`GB`/`TB`
  (a plain number means bytes). Over-quota writes return **507** with
  actionable guidance; overwrites release the old object's size from the arithmetic. Usage is a
  lazy snapshot (default TTL 5 minutes) plus in-flight reservations, so writes made outside the
  gateway (e.g. `sail cp` directly to the bucket) become visible at the next refresh; expired
  snapshots keep serving the old value while a background refresh runs, so a large prefix never
  blocks writes. The quota is also reported to clients (RFC 4331 `DAV:quota-available-bytes`), so
  Finder / Explorer show the remaining space. Changing `quota` in the config hot-applies without a
  restart.
- **Hot reload.** The user table is watched: add/remove users or change passwords, prefixes or
  quotas by editing the config file — effective within seconds, no restart. `listen`, TLS
  certificates, `staging-dir`, chunked-upload settings and the base `prefix` are cold zone
  (changing them logs a "restart required" warning). A broken YAML keeps the previous user table
  with a warning.
- **Directory auto-create.** On startup/reload, sail asynchronously creates a 0-byte directory
  marker at each user's prefix so the folder is visible in S3 consoles and to `sail ls`.
  Idempotent and best-effort: an S3 hiccup only logs a warning.
- **Fail-loud conflicts.** `users` and `user`/`password` are mutually exclusive (across sources
  too: `--user`/`--password` flags plus a config `users` table is refused). Effective prefixes
  must be pairwise distinct and non-nested; violations are refused and the previous state is kept.
- **Single-user mode stays.** `--user`/`--password` behaves exactly as before; credentials from
  flags disable hot reload (a warning is printed at startup).

Full details in the [repo README](https://github.com/BeCrafter/sail#multi-user-serveusers).

See the [repo README](https://github.com/BeCrafter/sail#webdav-gateway-sail-serve-webdav) for the
full flag table, design boundaries (in-process LOCK, 501 on directory MOVE/COPY), and chunked storage.

## Documentation

Full documentation — config guide, path syntax, AWS CLI cross-check, implementation details, and more — see the [repo README](https://github.com/BeCrafter/sail#readme).

- Source: <https://github.com/BeCrafter/sail>
- Issues: <https://github.com/BeCrafter/sail/issues>

## License

MIT
