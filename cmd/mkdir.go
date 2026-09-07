package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/i18n"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
)

var mkdirCmd = &cobra.Command{
	Use:   "mkdir <s3://bucket/prefix/>...",
	Short: "Create directory placeholder objects",
	Long: `Create directory placeholder objects (zero bytes, key ending in /).
S3 has no real directories; placeholder objects are the conventional directory marker. mkdir is naturally idempotent — rerunning simply overwrites the placeholder.

Examples:
  sail mkdir s3://bucket/videos/2026/
  sail mkdir s3://bucket/a s3://bucket/b`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		r, _, err := loadResolved()
		if err != nil {
			return err
		}
		ctx := context.Background()
		s3c, err := client.New(ctx, r)
		if err != nil {
			return err
		}
		for _, arg := range args {
			p, err := parseS3(arg, r)
			if err != nil {
				return err
			}
			key := strings.TrimSuffix(p.Key, "/") + "/"
			if key == "/" {
				return errors.New(i18n.T("missing directory name; specify s3://bucket/prefix/"))
			}
			_, err = s3c.PutObject(ctx, &s3.PutObjectInput{
				Bucket:        &p.Bucket,
				Key:           &key,
				Body:          bytes.NewReader(nil),
				ContentLength: aws.Int64(0),
				ContentType:   aws.String("application/x-directory"),
			})
			if err != nil {
				return fmt.Errorf(i18n.T("failed to create s3://%s/%s: %w"), p.Bucket, key, err)
			}
			fmt.Printf(i18n.T("created s3://%s/%s\n"), p.Bucket, key)
		}
		return nil
	},
}
