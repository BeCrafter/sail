package client

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/BeCrafter/sail/internal/config"
)

// New 根据已解析的配置构造 path-style S3 client。
func New(ctx context.Context, r *config.Resolved) (*s3.Client, error) {
	creds := credentials.NewStaticCredentialsProvider(r.AccessKey, r.SecretKey, "")

	// 部分 S3 服务 region 为空,但 AWS SDK v2 的 endpoint 规则要求 region 非空。
	// endpoint 已被 BaseEndpoint 覆盖,region 用任意值即可,这里用 us-east-1。
	region := r.Region
	if region == "" {
		region = "us-east-1"
	}

	cfg, err := awscfg.LoadDefaultConfig(ctx,
		awscfg.WithCredentialsProvider(creds),
		awscfg.WithRegion(region),
	)
	if err != nil {
		return nil, fmt.Errorf("加载 AWS 配置失败: %w", err)
	}

	c := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(r.Endpoint)
		o.UsePathStyle = r.PathStyle
		o.Region = region
		// 部分 S3 兼容服务在收到 x-amz-checksum-mode 请求头时会返回
		// aws-chunked + trailing checksum 响应体,Go HTTP client 无法
		// 正确解码,导致下载内容被 chunk 元数据污染。设为 WhenRequired
		// 使 SDK 不主动请求 checksum,保持响应体干净。
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
	return c, nil
}

func coalesce(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
