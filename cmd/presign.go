package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
	"github.com/BeCrafter/sail/internal/client"
)

var presignExpires int

var presignCmd = &cobra.Command{
	Use:   "presign s3://bucket/key",
	Short: "生成预签名下载 URL",
	Args:  cobra.ExactArgs(1),
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
			return fmt.Errorf("生成预签名失败: %w", err)
		}
		fmt.Println(req.URL)
		return nil
	},
}

func init() {
	presignCmd.Flags().IntVar(&presignExpires, "expires", 3600, "URL 有效期(秒)")
}
