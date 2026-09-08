package i18n

func init() {
	register(map[string]string{
		"Copy objects/files (local↔s3, s3↔s3)": "复制对象/文件(本地↔s3, s3↔s3)",
		`Copy objects/files between local and S3 (local↔s3) and S3 and S3 (s3↔s3; s3↔s3 prefers server-side CopyObject, falling back to download→re-upload).
upload/download are aliases of cp.

S3 source paths support wildcards (* matches any characters including /, ? matches a single character), expanding automatically into multiple objects,
the destination is treated as a directory/prefix and the source relative hierarchy is preserved (no -r needed):
  sail cp 's3://bucket/logs/*.log' s3://bucket/archive/
  sail cp 's3://bucket/*.json' ./download-dir/

Examples:
  sail cp ./local.txt s3://bucket/path/copied.txt
  sail cp ./local.txt s3://bucket/path/         # trailing / means into the directory
  sail cp s3://bucket/a.txt ./out.txt
  sail cp -r ./dir s3://bucket/mirror/          # mirror a local directory recursively
  sail cp -r s3://bucket/prefix/ s3://bucket/dest/   # recursive server-side copy
  sail cp --dry-run ./local.txt s3://bucket/x   # preview, without actually copying
  sail upload ./local.txt                       # 1 arg: upload to the default bucket, key is the file name
  sail download s3://bucket/a.txt               # 1 arg: download into the current directory
  cat file | sail upload - s3://bucket/key      # piped input
  sail cp ./a.txt ./b.txt                        # rejected: use the system cp for local→local`: `复制对象/文件,支持本地↔s3 与 s3↔s3(s3↔s3 优先走服务端 CopyObject,失败回退 download→re-upload)。
upload/download 为 cp 的别名。

s3 源路径支持通配符(* 匹配任意字符含 /,? 匹配单字符),自动展开为多个对象,
目标视为目录/前缀,源相对层级保留(无需 -r):
  sail cp 's3://bucket/logs/*.log' s3://bucket/archive/
  sail cp 's3://bucket/*.json' ./download-dir/

示例:
  sail cp ./local.txt s3://bucket/path/copied.txt
  sail cp ./local.txt s3://bucket/path/         # 尾 / 表示进目录
  sail cp s3://bucket/a.txt ./out.txt
  sail cp -r ./dir s3://bucket/mirror/          # 递归镜像本地目录
  sail cp -r s3://bucket/prefix/ s3://bucket/dest/   # 服务端递归复制
  sail cp --dry-run ./local.txt s3://bucket/x   # 预演,不实际复制
  sail upload ./local.txt                       # 1 参:上传到默认 bucket,key 用文件名
  sail download s3://bucket/a.txt               # 1 参:下载到当前目录
  cat file | sail upload - s3://bucket/key      # 管道输入
  sail cp ./a.txt ./b.txt                        # 拒绝:本地→本地用系统 cp`,
		"recurse into subdirectories":                      "递归复制",
		"show what would be done without actually copying": "只显示将执行的操作,不实际复制",
		"Delete objects":                                   "删除对象",
		`Delete objects; accepts multiple arguments. -r recursively deletes every object under the prefix (batch deletes of up to 1000 at a time).
Arguments with wildcards (*, ?) are matched against patterns (listing + client-side matching), e.g. s3://bucket/logs/*.log.

When an argument is "-", keys are read line by line from stdin (one per line; s3://bucket/key or a bare key are accepted,
bare keys use the default bucket, and wildcards are supported). This composes with -r into piped batch operations:
  sail ls s3://bucket/prefix/ | sail rm -r -
  sail ls s3://bucket | sail rm 's3://bucket/*.tmp' -`: `删除对象,支持多个参数;-r 递归删除前缀下所有对象(批量删除,每次最多 1000 个)。
参数含通配符(*、?)时按模式匹配删除(基于列举 + 客户端匹配),如 s3://bucket/logs/*.log。

参数为 "-" 时从 stdin 逐行读取 key(每行一个,支持 s3://bucket/key 或裸 key,
裸 key 使用默认桶,也支持通配符),可与 -r 组合成管道式批量操作:
  sail ls s3://bucket/prefix/ | sail rm -r -
  sail ls s3://bucket | sail rm 's3://bucket/*.tmp' -`,
		"recursively delete all objects under the prefix": "递归删除前缀下所有对象",
		"Create directory placeholder objects":            "创建目录占位对象",
		`Create directory placeholder objects (zero bytes, key ending in /).
S3 has no real directories; placeholder objects are the conventional directory marker. mkdir is naturally idempotent — rerunning simply overwrites the placeholder.

Examples:
  sail mkdir s3://bucket/videos/2026/
  sail mkdir s3://bucket/a s3://bucket/b`: `创建目录占位对象(0 字节、key 以 / 结尾)。
S3 没有真实目录,占位对象是约定俗成的目录标记;mkdir 天然幂等,重复执行只是覆盖占位对象。

示例:
  sail mkdir s3://bucket/videos/2026/
  sail mkdir s3://bucket/a s3://bucket/b`,
		"Delete empty directory placeholder objects": "删除空目录占位对象",
		`Delete empty directory placeholder objects (no recursive deletion). Errors if the directory contains other objects; use sail rm -r instead.
A directory with no placeholder object is treated as already deleted (idempotent).

Examples:
  sail rmdir s3://bucket/videos/2026/`: `删除空目录占位对象(不做递归删除)。目录下有其他对象时报错,请改用 sail rm -r。
目录不存在占位对象时视为已删除(幂等)。

示例:
  sail rmdir s3://bucket/videos/2026/`,
		"Create buckets":       "创建桶",
		"Delete empty buckets": "删除空桶",
		`Create buckets (CreateBucket). Bucket names must follow the naming rules of the S3 service you connect to.
Recreating the same bucket may be rejected by the service (implementation-dependent); on failure you are prompted to use the existing bucket instead.

Examples:
  sail mb s3://my-new-bucket`: `创建桶(CreateBucket)。桶名需符合所接入 S3 服务的命名规则。
重复创建同一桶可能被服务拒绝(取决于服务实现),失败时提示改用已存在的桶。

示例:
  sail mb s3://my-new-bucket`,
		`Delete empty buckets (DeleteBucket). The service rejects the call when the bucket still holds objects or placeholder objects;
empty it first (e.g. sail rm -r s3://bucket/). Recreation after deletion usually has a delay — wait and retry.

Examples:
  sail rb s3://my-old-bucket`: `删除空桶(DeleteBucket)。桶内仍有对象或占位对象时服务会拒绝,
需先清空(如 sail rm -r s3://bucket/)。删除后再次创建通常有延迟,请稍候重试。

示例:
  sail rb s3://my-old-bucket`,
		"rsync-style incremental sync (local↔s3, s3↔s3)": "rsync 式增量同步(本地↔s3, s3↔s3)",
		`rsync-style incremental sync: by default it compares by size + mtime and transfers only entries that differ.
Supports local→s3, s3→local, and s3→s3 (server-side copy); for local↔local use the system rsync.

Comparison modes (S3 LastModified has second precision, with a 1s tolerance):
  (default)    destination missing / different size / source mtime newer than destination by more than 1s → transfer
  --update transfer only entries newer than the destination (skip when the destination is newer even if the size differs)
  --checksum verify content by md5 when sizes match (ETag single-part fast path, otherwise streaming)

Filtering (--exclude and --include combine; filtered entries are invisible in both directions — neither transferred nor removed by --delete):
  --exclude wildcard (repeatable), matches both the relative path and the file name; a trailing-/ directory pattern excludes the whole directory
  --include allowlist (repeatable); once provided, only matching entries take part in the sync
  --delete delete extraneous entries on the destination side (batch deletes on the s3 side; deletes files and cleans empty dirs on the local side)

Examples:
  sail sync ./dir s3://bucket/mirror/
  sail sync --exclude '*.tmp' --delete ./dir s3://bucket/mirror/
  sail sync --checksum ./dir s3://bucket/mirror/
  sail sync --include '*.json' s3://bucket/mirror/ ./dir2 --dry-run`: `rsync 式增量同步:默认以大小 + 修改时间比对,只传输有差异的条目。
支持本地→s3、s3→本地、s3→s3(服务端复制);本地↔本地请用系统 rsync。

比对模式(S3 LastModified 秒级精度,比对容差 1s):
  (默认)   目标缺失 / 大小不同 / 源修改时间晚于目标 1s 以上 → 传输
  --update 只传输比目标新的条目(目标较新时即使大小不同也跳过)
  --checksum 大小相同时按内容 md5 校验(ETag 单分片快路径,否则流式计算)

过滤(--exclude 与 --include 组合,被过滤条目双向不可见:既不传输也不被 --delete 删除):
  --exclude 通配符(可重复),同时匹配相对路径与文件名;尾 / 的目录模式排除整个目录
  --include 白名单(可重复);提供后只有命中的条目参与同步
  --delete 删除目标端多余条目(s3 侧批量删除,本地侧删除文件并清理空目录)

示例:
  sail sync ./dir s3://bucket/mirror/
  sail sync --exclude '*.tmp' --delete ./dir s3://bucket/mirror/
  sail sync --checksum ./dir s3://bucket/mirror/
  sail sync --include '*.json' s3://bucket/mirror/ ./dir2 --dry-run`,
		"delete extraneous entries on the destination side":                                       "删除目标端多余的条目",
		"show what would be done without actually syncing":                                        "只显示将执行的操作,不实际同步",
		"exclude wildcard (repeatable, matches relative path or file name)":                       "排除通配符(可重复,匹配相对路径或文件名)",
		"include allowlist wildcard (repeatable, only matching entries are synced once provided)": "包含白名单通配符(可重复,提供后只同步命中条目)",
		"verify content by md5 when sizes match":                                                  "大小相同时按内容 md5 校验差异",
		"transfer only entries newer than the destination":                                        "只传输比目标新的条目",
	})
}
