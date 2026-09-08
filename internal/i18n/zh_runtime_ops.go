package i18n

func init() {
	register(map[string]string{
		// cp.go
		"local-to-local copy should use the system cp command":                              "本地到本地的复制请使用系统 cp 命令",
		"wildcard copy must specify a target directory or s3:// prefix":                     "通配符复制必须指定目标目录或 s3:// 前缀",
		"stdin input must specify a target s3://bucket/key":                                 "管道输入必须指定目标 s3://bucket/key",
		"would upload <stdin> -> s3://%s/%s\n":                                              "将上传 <stdin> -> s3://%s/%s\n",
		"uploading <stdin> -> s3://%s/%s\n":                                                 "上传 <stdin> -> s3://%s/%s\n",
		"would copy %s -> s3://<default bucket>/%s (no default bucket configured)\n":        "将复制 %s -> s3://<默认桶>/%s (未配置默认 bucket)\n",
		"no target bucket specified, use s3://bucket/key or set a default bucket in config": "未指定目标 bucket,请用 s3://bucket/key 或在配置中设置默认 bucket",
		"missing key, specify s3://bucket/key":                                              "缺少 key,需指定 s3://bucket/key",
		"failed to read local path: %w":                                                     "读取本地路径失败: %w",
		"%s is a directory, add -r to copy recursively":                                     "%s 是目录,请加 -r 递归复制",
		"would recursively copy directory %s -> s3://%s/%s\n":                               "将递归复制目录 %s -> s3://%s/%s\n",
		"copying directory %s -> s3://%s/%s\n":                                              "复制目录 %s -> s3://%s/%s\n",
		"upload failed: %w":                                                                 "上传失败: %w",
		"warning: source deletion failed, data copied but source not cleaned up: %v\n":      "警告: 源删除失败,数据已复制但源未清理: %v\n",
		"would copy %s -> s3://%s/%s\n":                                                     "将复制 %s -> s3://%s/%s\n",
		"copying %s -> s3://%s/%s\n":                                                        "复制 %s -> s3://%s/%s\n",
		"would recursively copy s3://%s/%s -> %s\n":                                         "将递归复制 s3://%s/%s -> %s\n",
		"list failed: %w": "列举失败: %w",
		"warning: source deletion failed s3://%s/%s: %v\n":  "警告: 源删除失败 s3://%s/%s: %v\n",
		"downloaded %d objects\n":                           "共下载 %d 个对象\n",
		"would copy s3://%s/%s -> %s\n":                     "将复制 s3://%s/%s -> %s\n",
		"download s3://%s/%s failed: %w":                    "下载 s3://%s/%s 失败: %w",
		"failed to create directory: %w":                    "创建目录失败: %w",
		"failed to create local file: %w":                   "创建本地文件失败: %w",
		"failed to write file: %w":                          "写入文件失败: %w",
		"copying s3://%s/%s -> %s\n":                        "复制 s3://%s/%s -> %s\n",
		"would recursively copy s3://%s/%s -> s3://%s/%s\n": "将递归复制 s3://%s/%s -> s3://%s/%s\n",
		"copied %d objects\n":                               "共复制 %d 个对象\n",
		"would copy s3://%s/%s -> s3://%s/%s\n":             "将复制 s3://%s/%s -> s3://%s/%s\n",
		"copying s3://%s/%s -> s3://%s/%s\n":                "复制 s3://%s/%s -> s3://%s/%s\n",
		"copy s3://%s/%s -> s3://%s/%s failed (CopyObject unreliable and fallback source read failed): %w": "复制 s3://%s/%s -> s3://%s/%s 失败(CopyObject 不可靠且回退读取源失败): %w",
		"copy s3://%s/%s -> s3://%s/%s failed (fallback re-upload): %w":                                    "复制 s3://%s/%s -> s3://%s/%s 失败(回退 re-upload): %w",
		"copy s3://%s/%s -> s3://%s/%s (fallback download→upload)\n":                                       "复制 s3://%s/%s -> s3://%s/%s (回退 download→upload)\n",

		// sync.go
		"destination missing key, specify s3://bucket/prefix":     "目标缺少 key,需指定 s3://bucket/prefix",
		"local-to-local sync should use the system rsync":         "本地到本地的同步请使用系统 rsync",
		"source %s is not a directory":                            "源 %s 不是目录",
		"source and destination prefixes overlap, cannot sync":    "源与目标前缀重叠,不能同步",
		"would sync %s -> %s\n":                                   "将同步 %s -> %s\n",
		"would delete %s\n":                                       "将删除 %s\n",
		"failed to delete %s: %w":                                 "删除 %s 失败: %w",
		"sync complete: transferred %d, deleted %d, skipped %d\n": "同步完成: 传输 %d 个, 删除 %d 个, 跳过 %d 个\n",
		"failed to read %s: %w":                                   "读取 %s 失败: %w",
		"failed to open %s: %w":                                   "打开 %s 失败: %w",
		"syncing %s -> s3://%s/%s\n":                              "同步 %s -> s3://%s/%s\n",
		"warning: failed to set modification time: %v\n":          "警告: 设置修改时间失败: %v\n",
		"syncing s3://%s/%s -> %s\n":                              "同步 s3://%s/%s -> %s\n",

		// mv.go (help metadata)
		"Move objects/files (copy then delete source)": "移动对象/文件(复制后删除源)",
		`Move objects/files: copy then delete the source. s3-to-s3 uses server-side CopyObject + Delete with zero bandwidth.

Examples:
  sail mv s3://bucket/a.txt s3://bucket/moved.txt     # single object, no confirm
  sail mv ./local.txt s3://bucket/uploaded.txt
  sail mv s3://bucket/file.txt ./retrieved.txt
  sail mv -r s3://bucket/src/ s3://bucket/dst/         # recursive, interactive confirm
  sail mv -r --yes s3://bucket/src/ s3://bucket/dst/   # skip confirm
  sail mv -r --yes ./dir s3://bucket/mirror/`: `移动对象/文件,等于复制后删除源。s3↔s3 走服务端 CopyObject+Delete,零带宽。

示例:
  sail mv s3://bucket/a.txt s3://bucket/moved.txt     # 单对象,无确认
  sail mv ./local.txt s3://bucket/uploaded.txt
  sail mv s3://bucket/file.txt ./retrieved.txt
  sail mv -r s3://bucket/src/ s3://bucket/dst/         # 递归,交互确认
  sail mv -r --yes s3://bucket/src/ s3://bucket/dst/   # 跳过确认
  sail mv -r --yes ./dir s3://bucket/mirror/`,
		"move recursively":                                "递归移动",
		"skip the confirmation prompt":                    "跳过确认提示",
		"show what would be done without actually moving": "只显示将执行的操作,不实际移动",

		// mv.go
		"local-to-local move should use the system mv command":                                "本地到本地的移动请使用系统 mv 命令",
		"recursive move is destructive, add --yes to confirm in non-interactive environments": "递归移动有破坏性,非交互环境请加 --yes 确认",
		"would recursively move %s -> %s, confirm? [y/N] ":                                    "将递归移动 %s -> %s,确认? [y/N] ",
		"canceled": "已取消",
	})
}
