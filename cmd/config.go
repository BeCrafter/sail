package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/BeCrafter/sail/internal/config"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "配置管理",
}

var configInitCmd = &cobra.Command{
	Use:   "init",
	Short: "交互式生成配置文件 ~/.sail/config.yaml",
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := config.ConfigPath()
		if err != nil {
			return err
		}
		if _, err := os.Stat(path); err == nil {
			fmt.Printf("配置文件已存在: %s\n是否覆盖? [y/N] ", path)
			if !confirm() {
				fmt.Println("已取消")
				return nil
			}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("创建目录失败: %w", err)
		}

		reader := bufio.NewReader(os.Stdin)
		prof := promptReader(reader, "profile 名称", "prod")
		endpoint := promptReader(reader, "endpoint (如 https://<your-s3-endpoint>/)", "")
		accessKey := promptReader(reader, "access-key (可留空,用环境变量 SAIL_ACCESS_KEY)", "")
		secretKey := promptReader(reader, "secret-key (可留空,用环境变量 SAIL_SECRET_KEY)", "")
		bucket := promptReader(reader, "默认 bucket (可留空)", "")
		cdnDomain := promptReader(reader, "CDN 域名 (用于 url 命令,可留空)", "")
		region := promptReader(reader, "region (云厂商填如 us-east-1,自建服务留空)", "")
		pathStyle := promptBoolReader(reader, "path-style (自建/MinIO 选 y,AWS S3 选 n)", true)

		content := renderConfig(prof, endpoint, accessKey, secretKey, bucket, cdnDomain, region, pathStyle)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return fmt.Errorf("写入配置失败: %w", err)
		}
		fmt.Printf("\n配置已写入: %s\n", path)
		fmt.Println("提示: 如使用 ${VAR} 占位符,可避免在文件中明文保存密钥。")

		// 引导安装 shell 自动补全 (仅 macOS)
		if shell := detectShell(); shell != "" {
			fmt.Printf("\n检测到当前 shell: %s\n", shell)
			fmt.Print("是否安装命令自动补全? [Y/n] ")
			if confirmDefault() {
				if err := installCompletion(shell); err != nil {
					fmt.Printf("安装补全失败: %v\n", err)
				}
			} else {
				fmt.Println("跳过补全安装,之后可手动运行: sail completion " + shell)
			}
		} else if runtime.GOOS == "darwin" {
			fmt.Println("\n未检测到支持的 shell,可手动安装补全: sail completion <zsh|bash|fish>")
		}
		return nil
	},
}

func init() {
	configCmd.AddCommand(configInitCmd)
}

// promptReader 从共享 reader 读取一行,空输入返回默认值。
func promptReader(r *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// promptBoolReader 从共享 reader 读取 y/n,返回布尔值。默认 yes(def=true)。
func promptBoolReader(r *bufio.Reader, label string, def bool) bool {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	fmt.Printf("%s [%s]: ", label, hint)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	if line == "" {
		return def
	}
	return line == "y" || line == "yes"
}

// confirm 询问 y/n,默认 no。用于覆盖确认等场景。
func confirm() bool {
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes"
}

func renderConfig(prof, endpoint, accessKey, secretKey, bucket, cdnDomain, region string, pathStyle bool) string {
	ak := accessKey
	if ak == "" {
		ak = "${SAIL_ACCESS_KEY}"
	}
	sk := secretKey
	if sk == "" {
		sk = "${SAIL_SECRET_KEY}"
	}
	bk := bucket
	if bk == "" {
		bk = ""
	}
	ps := "false"
	if pathStyle {
		ps = "true"
	}
	return fmt.Sprintf(`# S3 兼容存储 CLI 配置
# 密钥可用 ${VAR} 引用环境变量,避免明文。
default-profile: %s
profiles:
  %s:
    endpoint: %s
    access-key: %s
    secret-key: %s
    bucket: "%s"
    region: "%s"
    path-style: %s
    cdn-domain: "%s"
`, prof, prof, endpoint, ak, sk, bk, region, ps, cdnDomain)
}
