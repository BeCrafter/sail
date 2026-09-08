package i18n

func init() {
	register(map[string]string{
		// config.go
		"read config %s failed: %w":                "读取配置 %s 失败: %w",
		"parse config failed: %w":                  "解析配置失败: %w",
		"profile %q not found in config file":      "profile %q 不存在于配置文件中",
		"profile %q missing endpoint":              "profile %q 缺少 endpoint",
		"profile %q missing access-key/secret-key": "profile %q 缺少 access-key/secret-key",

		// s3path.go
		"empty path":                    "路径为空",
		"path %q must start with s3://": "路径 %q 必须以 s3:// 开头",
		"missing bucket":                "缺少 bucket",

		// client.go
		"failed to load AWS config: %w": "加载 AWS 配置失败: %w",

		// uploader.go
		"failed to open file: %w":        "打开文件失败: %w",
		"failed to read file info: %w":   "读取文件信息失败: %w",
		"failed to read input: %w":       "读取输入失败: %w",
		"uploading %s -> s3://%s/%s\n":   "上传 %s -> s3://%s/%s\n",
		"\r\033[K%s / %s  %.1f%% done\n": "\r\033[K%s / %s  %.1f%% 完成\n",
	})
}
