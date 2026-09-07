package cmd

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/BeCrafter/sail/internal/i18n"
	"github.com/spf13/cobra"
)

var urlCmd = &cobra.Command{
	Use:   "url s3://bucket/key",
	Short: "Generate a CDN access URL for a file",
	Long: `Build a public access URL for a file from the configured cdn-domain.

Requires the bucket to be public-read and a cdn-domain set in the config.

Examples:
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
			return errors.New(i18n.T("missing key; specify s3://bucket/key"))
		}

		domain := r.CDNDomain
		if flagCDN != "" {
			domain = flagCDN
		}
		if domain == "" {
			return errors.New(i18n.T("cdn-domain is not configured; set it in the config file or pass --cdn"))
		}

		// bucketInPath 优先级:--no-bucket flag > 配置 cdn-bucket-path > 自动检测。
		// 注意:--cdn 覆盖域名时,配置的 cdn-bucket-path 描述的是原 cdn-domain 与
		// bucket 的关系,对新域名不适用,故重置为自动检测;--no-bucket 仍为最高覆盖。
		bucketInPath := r.CDNBucketPath
		if flagCDN != "" {
			bucketInPath = nil
		}
		if flagNoBucket {
			v := true
			bucketInPath = &v
		}
		fmt.Println(buildCDNURL(domain, p.Bucket, p.Key, bucketInPath))
		return nil
	},
}

var (
	flagCDN      string
	flagNoBucket bool
)

func init() {
	urlCmd.Flags().StringVar(&flagCDN, "cdn", "", "override CDN domain (e.g. https://<your-cdn-domain>)")
	urlCmd.Flags().BoolVar(&flagNoBucket, "no-bucket", false, "CDN domain already includes the bucket path; do not append the bucket")
}

// buildCDNURL 拼接 CDN 域名与 bucket/key 生成公开访问地址。
// bucketInPath 控制是否认为域名已包含 bucket,nil 时自动检测(仅路径段,即 path-style)。
//   - nil   自动检测:若域名路径已含 bucket 段则不重复拼接
//   - true  视为已含,仅追加 key(不追加 bucket)
//   - false 视为未含,总是追加 bucket
//
// 避免产生 https://cdn.example.com/bucket/bucket/key 这类重复 bucket 的失效链接。
// 虚拟主机式(子域)或域名直接映射 bucket(URL 不含 bucket)的场景自动检测无法区分,
// 但域名首标签与 bucket 同名(如 bucket=cdn、域名 cdn.example.com)不会被误判,
// 这类域名请用 cdn-bucket-path 显式声明。
func buildCDNURL(domain, bucket, key string, bucketInPath *bool) string {
	domain = strings.TrimRight(domain, "/")
	inPath := cdnAlreadyHasBucket(domain, bucket)
	if bucketInPath != nil {
		inPath = *bucketInPath
	}
	if bucket != "" && !inPath {
		return fmt.Sprintf("%s/%s/%s", domain, bucket, key)
	}
	return fmt.Sprintf("%s/%s", domain, key)
}

// cdnAlreadyHasBucket 判断 domain 的路径是否已包含 bucket 段(仅 path-style 寻址)。
// 只按路径段整段精确匹配;不做子域(host 前缀)推断,避免 bucket 名与域名首标签
// 同名时(如 bucket=cdn、域名 cdn.example.com)被误判为虚拟主机式而错误剥除 path bucket。
// 无 scheme 的域名(如 cdn.example.com)视为 https,仅供解析判断,不影响输出格式。
func cdnAlreadyHasBucket(domain, bucket string) bool {
	if domain == "" || bucket == "" {
		return false
	}
	u := domain
	if !strings.Contains(u, "://") {
		u = "https://" + u
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	b := strings.ToLower(bucket)

	// path-style 方式:https://cdn.example.com/bucket/...
	for _, seg := range strings.Split(strings.Trim(parsed.Path, "/"), "/") {
		if seg == b {
			return true
		}
	}
	return false
}
