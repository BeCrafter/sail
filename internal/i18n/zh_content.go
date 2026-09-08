package i18n

func init() {
	register(map[string]string{
		// view
		"View object/file contents": "查看对象/文件内容",
		`Smart-render an S3 object or local file based on its format:
  text/code -> output as-is
  JSON      -> pretty-print
  YAML      -> reformat
  CSV       -> aligned table
  XML       -> pretty-print
  image     -> terminal ASCII art (half-block characters, visible in any terminal, no graphics protocol needed)
  binary    -> metadata + first 256 bytes hex dump

Examples:
  sail view s3://bucket/config.json
  sail view ./local.log
  sail view s3://bucket/data.json --raw | jq .
  sail view s3://bucket/photo.png --width 60`: `智能查看 S3 对象或本地文件,按格式渲染:
  文本/代码 → 原文输出
  JSON → 缩进美化
  YAML → 重新格式化
  CSV → 表格对齐
  XML → 缩进美化
  图片 → 终端字符画(半块字符,任何终端可见,不依赖终端图形协议)
  二进制 → 元信息 + 前 256 字节 hex dump

示例:
  sail view s3://bucket/config.json
  sail view ./local.log
  sail view s3://bucket/data.json --raw | jq .
  sail view s3://bucket/photo.png --width 60`,
		"force format: text|json|yaml|csv|xml|image|binary":           "强制格式: text|json|yaml|csv|xml|image|binary",
		"raw output (skip formatting and ASCII art; good for piping)": "原样输出(跳过格式化和字符画,适合管道)",
		"skip size limit":                        "跳过大小限制",
		"ASCII art column width (0=auto-detect)": "字符画列宽(0=自动探测)",

		// head
		"Output the first part of object/file": "查看对象/文件开头内容",
		`Stream-read the head of an object/file without downloading it to disk.
-n shows the first N lines (default 10); --bytes shows the first N bytes; the two are mutually exclusive.

Examples:
  sail head -n 20 s3://bucket/logs/app.log
  sail head --bytes 4096 s3://bucket/data.bin`: `流式读取对象/文件开头内容,不落盘。
-n 显示前 N 行(默认 10);--bytes 显示前 N 字节;两者互斥。

示例:
  sail head -n 20 s3://bucket/logs/app.log
  sail head --bytes 4096 s3://bucket/data.bin`,
		"show first N lines": "显示前 N 行",
		"show first N bytes": "显示前 N 字节",

		// tail
		"Output the last part of object/file": "查看对象/文件结尾内容",
		`Read the tail of an object/file. For s3 paths only the trailing window is fetched via Range, so the whole object is not downloaded;
when Range is unavailable (unsupported service or tiny object) it automatically falls back to a full streaming read.
-n shows the last N lines (default 10); --bytes shows the last N bytes; the two are mutually exclusive.

Examples:
  sail tail -n 50 s3://bucket/logs/app.log
  sail tail --bytes 4096 s3://bucket/data.bin`: `读取对象/文件结尾内容。s3 路径通过 Range 只取尾部窗口,不下载全量;
Range 不可用时(服务不支持/对象过小)自动退化为全量流式读取。
-n 显示最后 N 行(默认 10);--bytes 显示最后 N 字节;两者互斥。

示例:
  sail tail -n 50 s3://bucket/logs/app.log
  sail tail --bytes 4096 s3://bucket/data.bin`,
		"show last N lines": "显示最后 N 行",
		"show last N bytes": "显示最后 N 字节",

		// wc
		"Count lines, words, and bytes": "统计行数/单词数/字节数",
		`Stream-count lines, words, and bytes of an object/file.
With no options, print three columns (lines words bytes, GNU wc order); with options, print only the selected columns.

Examples:
  sail wc -l s3://bucket/logs/app.log
  sail wc s3://bucket/a.json ./b.txt`: `流式统计对象/文件的行、词、字节数。
未给选项时输出三列(行 词 字节,GNU wc 顺序);给选项则只输出所选列。

示例:
  sail wc -l s3://bucket/logs/app.log
  sail wc s3://bucket/a.json ./b.txt`,
		"count lines": "统计行数",
		"count words": "统计单词数",
		"count bytes": "统计字节数",

		// grep
		"Search object/file contents": "在对象/文件内容中搜索",
		`Stream-search object/file contents line by line with a regex, without downloading to disk.
-i ignore case; -v invert (print non-matching lines); -l list only sources with matches; -c print match count; -n show line numbers.
A single source prints bare matching lines; multiple sources prefix each line with "source:line". Exit code 1 when no source matches (GNU grep convention).

Examples:
  sail grep -n "ERROR" s3://bucket/logs/app.log
  sail grep -ic "timeout" s3://bucket/a.json ./b.txt`: `流式按行正则搜索对象/文件内容,不落盘。
-i 忽略大小写;-v 反向(输出不匹配的行);-l 只列出有匹配的源;-c 输出匹配行数;-n 显示行号。
单源输出裸匹配行;多源带 "源:行" 前缀。全部源无匹配时退出码为 1(GNU grep 惯例)。

示例:
  sail grep -n "ERROR" s3://bucket/logs/app.log
  sail grep -ic "timeout" s3://bucket/a.json ./b.txt`,
		"ignore case": "忽略大小写",
		"invert match (print non-matching lines)": "反向匹配(输出不匹配的行)",
		"list only sources with matches":          "只列出有匹配的源",
		"print match count":                       "输出匹配行数",
		"show line numbers":                       "显示行号",

		// checksum
		"Compute object/file checksums": "计算对象/文件校验和",
		`Stream-compute the md5 or sha256 checksum of an object/file and print "<checksum>  <source>".
--compare checks against a local file, printing OK on match and FAILED on mismatch (exit code 1 if any FAILED).
--etag shows the raw ETag of the S3 object without reading its content. Note: for multipart uploads
the ETag is not the md5 of the object content; this command does not compare ETag against content md5, --etag only displays it.

Examples:
  sail checksum s3://bucket/data.bin
  sail checksum --algo sha256 --compare ./local.bin s3://bucket/data.bin
  sail checksum --etag s3://bucket/data.bin`: `流式计算对象/文件的 md5 或 sha256 校验和,输出 "<校验和>  <源>"。
--compare 与本地文件比对,匹配输出 OK、不匹配输出 FAILED(任一 FAILED 退出码 1)。
--etag 直接展示 S3 对象的原始 ETag(不读内容)。注意:分片上传对象的 ETag
不是对象内容的 md5,本命令不做 ETag 与内容 md5 的自动比对,--etag 仅作展示。

示例:
  sail checksum s3://bucket/data.bin
  sail checksum --algo sha256 --compare ./local.bin s3://bucket/data.bin
  sail checksum --etag s3://bucket/data.bin`,
		"checksum algorithm (md5|sha256)":                              "校验算法: md5 或 sha256",
		"compare against a local file (prints OK/FAILED)":              "与本地文件比对(输出 OK/FAILED)",
		"show the raw ETag of the S3 object (without reading content)": "展示 S3 对象原始 ETag(不读内容)",

		// presign
		"Generate a presigned download URL": "生成预签名下载 URL",
		`Generate a presigned download URL (GET, valid for 1 hour by default) that can be accessed without credentials until it expires.
Note: some self-hosted S3-compatible services do not support query string authentication (returning "Authorization empty").
In that case, use a CDN domain to access a public object instead: sail url s3://bucket/key.

Examples:
  sail presign s3://bucket/data.bin --expires 3600`: `生成预签名下载 URL(GET,默认有效 1 小时),无需凭证即可在有效期内访问。
注意:部分自建 S3 兼容服务不支持 query string 认证(返回 "Authorization empty"),
此时请改用 CDN 域名访问公开对象:sail url s3://bucket/key。

示例:
  sail presign s3://bucket/data.bin --expires 3600`,
		"URL lifetime in seconds": "URL 有效期(秒)",

		// url
		"Generate a CDN access URL for a file": "生成文件的 CDN 访问地址",
		`Build a public access URL for a file from the configured cdn-domain.

Requires the bucket to be public-read and a cdn-domain set in the config.

Examples:
  sail url s3://mybucket/path/file.jpg
  sail url s3://mybucket/path/file.jpg --cdn https://<your-cdn-domain>`: `根据配置的 cdn-domain 拼接文件的公开访问地址。

要求 bucket 为 public-read 权限,且配置中设置了 cdn-domain。

示例:
  sail url s3://mybucket/path/file.jpg
  sail url s3://mybucket/path/file.jpg --cdn https://<your-cdn-domain>`,
		"override CDN domain (e.g. https://<your-cdn-domain>)":                  "覆盖 CDN 域名 (如 https://<your-cdn-domain>)",
		"CDN domain already includes the bucket path; do not append the bucket": "CDN 域名已含 bucket 路径,不再追加 bucket",
	})
}
