package i18n

func init() {
	register(map[string]string{
		// render.go
		"file too large: %s exceeds the %s limit; use --force to override": "文件过大: %s 超过 %s 限制,使用 --force 跳过",
		"failed to parse JSON: %w":           "解析 JSON 失败: %w",
		"failed to format JSON: %w":          "格式化 JSON 失败: %w",
		"failed to parse YAML: %w":           "解析 YAML 失败: %w",
		"failed to format YAML: %w":          "格式化 YAML 失败: %w",
		"failed to parse XML: %w":            "解析 XML 失败: %w",
		"failed to parse CSV: %w":            "解析 CSV 失败: %w",
		"size: unknown":                      "size: 未知",
		"--- hex dump (first 256 bytes) ---": "--- hex dump (前 256 字节) ---",

		// source.go
		"missing S3 client (local paths do not need s3://)": "缺少 S3 客户端(本地路径无需 s3://)",
		"failed to read object: %w":                         "读取对象失败: %w",
		"%s is a directory":                                 "%s 是目录",
		"failed to seek file: %w":                           "定位文件失败: %w",
		"only suffix Range is supported: %q":                "仅支持后缀 Range: %q",
		"invalid Range: %q":                                 "无效的 Range: %q",
		"failed to sniff content type: %w":                  "嗅探内容类型失败: %w",

		// image.go
		"failed to decode image: %w": "解码图片失败: %w",
		"unknown":                    "未知",
	})
}
