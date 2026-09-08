package i18n

func init() {
	register(map[string]string{
		// ls.go
		"no bucket specified; use s3://bucket/prefix or set a default bucket in config": "未指定 bucket,请用 s3://bucket/prefix 或在配置中设置默认 bucket",
		"no bucket specified": "未指定 bucket",
		"list failed: %w":     "列举失败: %w",

		// du.go / find.go
		"--max-depth cannot be negative": "--max-depth 不能为负数",

		// find.go
		"invalid size spec: %q": "无效的大小规格: %q",
		"invalid size unit: %q": "无效的大小单位: %q",
		"cannot parse time %q, expected 2006-01-02 or 2006-01-02 15:04:05": "无法解析时间 %q,格式为 2006-01-02 或 2006-01-02 15:04:05",

		// tree.go
		"failed to read local path: %w": "读取本地路径失败: %w",
		"%s is not a directory":         "%s 不是目录",

		// stat.go
		"missing key; use s3://bucket/key": "缺少 key,需指定 s3://bucket/key",
		"object not found: s3://%s/%s":     "对象不存在: s3://%s/%s",
		"query failed: %w":                 "查询失败: %w",
		"warning: HeadObject returned 200 but metadata is empty; object may not exist: s3://%s/%s\n": "警告: HeadObject 返回 200 但元信息为空,对象可能不存在: s3://%s/%s\n",
		"failed to read local file: %w": "读取本地文件失败: %w",

		// view.go
		"view failed: %w":            "查看失败: %w",
		"unsupported format --as %q": "不支持的格式 --as %q",
	})
}
