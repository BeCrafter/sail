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

var presignCmd = &cobra.Command{
	Use:   "presign s3://bucket/key",
	Short: "Generate a presigned download URL",
	Long: `Generate a presigned download URL (GET, valid for 1 hour by default) that can be accessed without credentials until it expires.
Note: some self-hosted S3-compatible services do not support query string authentication (returning "Authorization empty").
In that case, use a CDN domain to access a public object instead: sail url s3://bucket/key.

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

func init() {
	presignCmd.Flags().IntVar(&presignExpires, "expires", 3600, "URL lifetime in seconds")
}
