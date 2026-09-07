package i18n

func init() {
	register(map[string]string{
		// ls.go
		"List objects or buckets": "列举对象或桶",
		`List objects or buckets. With no arguments, lists the default bucket
(s3://bucket is accepted but pointless);
-l long format (size + last-modified time), combinable with -t to sort by time,
-S by size, -r to reverse, and --human for human-readable sizes;
-d lists sub-directories only (comma-separated prefixes, no files), mirroring ls -d;
--buckets lists all buckets.
Without sorting flags, output is streamed (low memory on large buckets);
with sorting flags, all objects are collected first and then printed.

Examples:
  sail ls s3://bucket/prefix/
  sail ls -l -t --human s3://bucket/
  sail ls -d s3://bucket/prefix/
  sail ls --buckets`: `列举对象或桶。0 个参数时列举默认桶(s3://bucket 合法但无谓);
-l 长格式(大小+修改时间),可叠加 -t 按时间、-S 按大小、-r 逆序、--human 人类可读;
-d 只列子目录(逗号分隔前缀,不含文件),对齐 ls -d;--buckets 列出所有桶。
不带排序 flag 时流式输出(大桶低内存),带排序 flag 时全量收集后打印。

示例:
  sail ls s3://bucket/prefix/
  sail ls -l -t --human s3://bucket/
  sail ls -d s3://bucket/prefix/
  sail ls --buckets`,
		"show size and last-modified time":          "显示大小和修改时间",
		"list sub-directories only (no files)":      "只列目录(子前缀,不含文件)",
		"sort by last-modified time (newest first)": "按修改时间排序(新→旧)",
		"sort by size (largest first)":              "按大小排序(大→小)",
		"reverse output order":                      "逆序输出",
		"human-readable sizes (with -l)":            "人类可读大小(配合 -l)",
		"list all buckets (ListBuckets)":            "列出所有桶(ListBuckets)",

		// tree.go
		"Show object/file tree": "树形查看对象/文件树",
		`Show a tree of S3 objects or a local file tree.

Flags:
  -L N            maximum depth (0 = unlimited)
  -d              show directories only (no file leaves)
  -s              show file size (in bytes)
      --human     human-readable sizes (implies -s)

Examples:
  sail tree s3://bucket/prefix/
  sail tree -L 2 s3://bucket/prefix/
  sail tree -d -s --human s3://bucket/prefix/
  sail tree ./cmd`: `树形查看 S3 对象或本地文件树。

Flags:
  -L N            最大深度(0=不限)
  -d              只显目录(不含文件叶子)
  -s              显文件大小(字节数)
      --human     人类可读大小(隐含 -s)

示例:
  sail tree s3://bucket/prefix/
  sail tree -L 2 s3://bucket/prefix/
  sail tree -d -s --human s3://bucket/prefix/
  sail tree ./cmd`,
		"maximum depth (0 = unlimited)":     "最大深度(0=不限)",
		"show directories only":             "只显目录",
		"show file size (in bytes)":         "显文件大小(字节数)",
		"human-readable sizes (implies -s)": "人类可读大小(隐含 -s)",

		// find.go
		"Find objects by name/size/time": "按名称/大小/时间查找对象",
		`Find objects under a prefix by criteria, printing s3://bucket/key one per line by default.

Filter criteria are combinable (AND):
  --name    filename glob (repeatable, OR-ed together), e.g. '*.log' / 'data_*'
  --size    +1M greater than / -500K less than / 1024 exact; unit B/K/M/G is case-insensitive
  --newer   last-modified time after the given time (2006-01-02 or 2006-01-02 15:04:05)
  --max-depth  maximum depth, 0 means unlimited

Examples:
  sail find s3://bucket/logs --name '*.log' --size +1M -l
  sail find s3://bucket --newer 2026-01-01`: `按条件查找前缀下的对象,默认打印 s3://bucket/key 每行一个。

过滤条件可组合(AND 关系):
  --name    文件名通配符(可重复,多个之间 OR),如 '*.log' / 'data_*'
  --size    +1M 大于 / -500K 小于 / 1024 精确;单位 B/K/M/G 不区分大小写
  --newer   修改时间晚于指定时刻(2006-01-02 或 2006-01-02 15:04:05)
  --max-depth  最大层级深度,0 表示不限

示例:
  sail find s3://bucket/logs --name '*.log' --size +1M -l
  sail find s3://bucket --newer 2026-01-01`,
		"filename glob (repeatable, OR-ed together)":             "文件名通配符(可重复,多个之间 OR)",
		"filter by size (+1M greater / -500K less / 1024 exact)": "按大小过滤(+1M 大于 / -500K 小于 / 1024 精确)",
		"last-modified time after the given time":                "修改时间晚于指定时刻",
		"maximum depth, 0 means unlimited":                       "最大层级深度,0 表示不限",

		// du.go
		"Summarize object size under a prefix": "统计前缀下的对象占用大小",
		`Sum the size of objects under a prefix, broken down by directory level
(each level is cumulative; the root is the grand total).
With no arguments, sums the default bucket; --max-depth limits the printed levels;
-s prints only the total.

Examples:
  sail du -h s3://bucket/logs
  sail du -h --max-depth 1 s3://bucket`: `按目录层级统计前缀下对象的大小总和(各层级为累计值,根为总计行)。
0 个参数时统计默认桶;--max-depth 限制打印层级;-s 只打印总计。

示例:
  sail du -h s3://bucket/logs
  sail du -h --max-depth 1 s3://bucket`,
		"print only the total":                   "只打印总计",
		"human-readable sizes":                   "人类可读大小",
		"maximum print depth, 0 means unlimited": "最大打印层级,0 表示不限",

		// stat.go
		"Show object/file metadata": "查看对象/文件元信息",
		`Show metadata for an object or file: s3 paths use HeadObject, local paths use os.Stat.
Outputs key, size, content-type, last-modified time, etag, storage class,
version-id and custom metadata.
For local paths, prints file name, size, mode bits, mod time and whether it is a directory.

Examples:
  sail stat s3://bucket/config.json
  sail stat ./local.log`: `查看对象/文件的元信息:s3 路径走 HeadObject,本地路径走 os.Stat。
输出 key、大小、content-type、最后修改时间、etag、存储类型、版本号与自定义 metadata。
本地路径列出文件名、大小、权限位、修改时间与是否目录。

示例:
  sail stat s3://bucket/config.json
  sail stat ./local.log`,
	})
}
