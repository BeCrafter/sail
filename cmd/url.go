package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var urlCmd = &cobra.Command{
	Use:   "url s3://bucket/key",
	Short: "生成文件的 CDN 访问地址",
	Long: `根据配置的 cdn-domain 拼接文件的公开访问地址。

要求 bucket 为 public-read 权限,且配置中设置了 cdn-domain。

示例:
  sail url s3://mybucket/path/file.jpg
  sail url s3://mybucket/path/file.jpg --cdn https://<your-cdn-domain>`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		r, _, err := loadResolved()
		if err != nil {
			return err
		}
		p, err := parseS3(args[0], r)
		if err != nil {
			return err
		}
		if p.Key == "" {
			return fmt.Errorf("缺少 key,需指定 s3://bucket/key")
		}

		domain := r.CDNDomain
		if flagCDN != "" {
			domain = flagCDN
		}
		if domain == "" {
			return fmt.Errorf("未配置 cdn-domain,请在配置文件中设置或用 --cdn 指定")
		}
		domain = strings.TrimSuffix(domain, "/")
		fmt.Printf("%s/%s/%s\n", domain, p.Bucket, p.Key)
		return nil
	},
}

var flagCDN string

func init() {
	urlCmd.Flags().StringVar(&flagCDN, "cdn", "", "覆盖 CDN 域名 (如 https://<your-cdn-domain>)")
}
