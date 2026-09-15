package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/i18n"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
)

var presignExpires int

// metaManifestKey 是 s3fs 写在分片文件 manifest 上的用户元数据键(SDK 已去
// x-amz-meta- 前缀)。命中也意味着预签名 URL 只会给到那几百字节的 manifest。
const metaManifestKey = "sail-manifest-key"

var presignCmd = &cobra.Command{
	GroupID: "verify",
	Use:     "presign s3://bucket/key",
	Short:   "Generate a presigned download URL",
	Long: `Generate a presigned download URL (GET, valid for 1 hour by default) that can be accessed without credentials until it expires.
Note: some self-hosted S3-compatible services do not support query string authentication (returning "Authorization empty").
In that case, use a CDN domain to access a public object instead: sail url s3://bucket/key.

A key stored as chunks (written with "sail serve webdav --chunked-upload") cannot be presigned:
the URL would hand out the small manifest instead of the file, so this command fails loud and
points at "sail serve webdav" or "sail cp" instead.

Examples:
  sail presign s3://bucket/data.bin --expires 3600`,
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
		ctx := context.Background()
		s3c, err := client.New(ctx, r)
		if err != nil {
			return err
		}

		if !presignAllowChunked {
			// 分片文件 fail-loud:预签名 URL 给不出可用的文件下载地址。
			// HEAD 失败(如无 HeadObject 权限)不阻断——按 P1 行为继续。
			if h, herr := s3c.HeadObject(ctx, &s3.HeadObjectInput{
				Bucket: &p.Bucket,
				Key:    &p.Key,
			}); herr == nil && h.Metadata[metaManifestKey] != "" {
				return fmt.Errorf(i18n.T("s3://%s/%s is stored as chunks; a presigned URL would return the manifest, not the file. Read it through 'sail serve webdav' or 'sail cp' instead (pass --allow-chunked to presign the manifest anyway)"),
					p.Bucket, p.Key)
			}
		}

		dur := time.Duration(presignExpires) * time.Second
		psClient := s3.NewPresignClient(s3c)
		req, err := psClient.PresignGetObject(ctx, &s3.GetObjectInput{
			Bucket: &p.Bucket,
			Key:    &p.Key,
		}, s3.WithPresignExpires(dur))
		if err != nil {
			return fmt.Errorf(i18n.T("failed to generate presigned URL: %w"), err)
		}
		fmt.Println(req.URL)
		return nil
	},
}

var presignAllowChunked bool

func init() {
	presignCmd.Flags().IntVar(&presignExpires, "expires", 3600, "URL lifetime in seconds")
	presignCmd.Flags().BoolVar(&presignAllowChunked, "allow-chunked", false, "presign the manifest of a chunked key anyway (the URL will not return the file content)")
}
