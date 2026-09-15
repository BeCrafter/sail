package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"

	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/i18n"
	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// brokenXMLTimeRe 匹配非标准时间格式 "2022-08-12 16:15:54"(日期与时间为空格分隔)。
var brokenXMLTimeRe = regexp.MustCompile(`([0-9]{4}-[0-9]{2}-[0-9]{2}) ([0-9]{2}:[0-9]{2}:[0-9]{2})`)

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
		return nil, fmt.Errorf(i18n.T("failed to load AWS config: %w"), err)
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
		// 部分自建服务返回的 XML 时间格式不合 AWS 标准(空格分隔而非 ISO8601),
		// SDK 反序列化会整体失败。包装 HTTPClient 在传输层规范化时间格式,
		// 对后续 SDK 解析透明。
		o.HTTPClient = &xmlTimeNormalizer{base: newHTTPClient()}
	})
	return c, nil
}

// newHTTPClient 返回按 S3 高并发场景调优的 HTTP 客户端。
//
// 默认 http.DefaultTransport 的 MaxIdleConnsPerHost 是 0(即
// DefaultMaxIdleConnsPerHost=2):同一 host 只保留 2 条空闲连接,超出的会被关闭。
// serve webdav 下 Finder 会并发打开多个文件,并发度一旦超过 2,后续请求每次都
// 要重新建连(TCP + 跨区域 RTT),这正是「打开目录很慢」的主因之一。
// 调高每个 host 的空闲连接上限后,并发请求得以复用长连接。
func newHTTPClient() *http.Client {
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultClient
	}
	t := tr.Clone()
	// 并发上传/下载会同时占用多条连接;空闲池留足,避免复用前被回收。
	t.MaxIdleConns = 128
	t.MaxIdleConnsPerHost = 64
	t.MaxConnsPerHost = 0 // 0 = 不限,连接数由上层并发度决定
	return &http.Client{Transport: t}
}

// xmlTimeNormalizer 包装 HTTPClient:在传输层把响应 XML 中的
// "YYYY-MM-DD HH:MM:SS" 规范化为 "YYYY-MM-DDTHH:MM:SSZ"。
// 仅作用于 XML 文本,不会误改二进制响应(mime 判定为 XML 时才处理)。
type xmlTimeNormalizer struct {
	base aws.HTTPClient
}

func (n *xmlTimeNormalizer) Do(req *http.Request) (*http.Response, error) {
	resp, err := n.base.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.Body == nil || resp.StatusCode != http.StatusOK {
		return resp, nil
	}
	ct := resp.Header.Get("Content-Type")
	mt, _, _ := mime.ParseMediaType(ct)
	if mt != "application/xml" && mt != "text/xml" {
		return resp, nil
	}
	body, rerr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if rerr != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp, nil
	}
	fixed := brokenXMLTimeRe.ReplaceAll(body, []byte("${1}T${2}Z"))
	resp.Body = io.NopCloser(bytes.NewReader(fixed))
	resp.ContentLength = int64(len(fixed))
	return resp, nil
}

func coalesce(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
