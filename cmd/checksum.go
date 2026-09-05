package cmd

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
)

var (
	checksumAlgo    = "md5"
	checksumCompare string
	checksumEtag    bool
)

var checksumCmd = &cobra.Command{
	Use:   "checksum [--algo md5|sha256] [--compare FILE] [--etag] <src>...",
	Short: "计算对象/文件校验和",
	Long: `流式计算对象/文件的 md5 或 sha256 校验和,输出 "<校验和>  <源>"。
--compare 与本地文件比对,匹配输出 OK、不匹配输出 FAILED(任一 FAILED 退出码 1)。
--etag 直接展示 S3 对象的原始 ETag(不读内容)。注意:分片上传对象的 ETag
不是对象内容的 md5,本命令不做 ETag 与内容 md5 的自动比对,--etag 仅作展示。

示例:
  sail checksum s3://bucket/data.bin
  sail checksum --algo sha256 --compare ./local.bin s3://bucket/data.bin
  sail checksum --etag s3://bucket/data.bin`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if checksumEtag {
			for _, arg := range args {
				if !strings.HasPrefix(arg, "s3://") {
					return fmt.Errorf("--etag 仅支持 s3:// 路径")
				}
			}
			if checksumCompare != "" || cmd.Flags().Changed("algo") {
				return fmt.Errorf("--etag 不能与 --algo/--compare 同时使用")
			}
			return etagValues(args)
		}
		newHash, err := newHasher(checksumAlgo)
		if err != nil {
			return err
		}
		ctx := context.Background()
		if checksumCompare != "" {
			want, err := hashFile(checksumCompare, newHash)
			if err != nil {
				return err
			}
			allOK := true
			for _, arg := range args {
				got, err := hashSource(ctx, arg, newHash)
				if err != nil {
					return err
				}
				if got == want {
					fmt.Printf("%s: OK\n", arg)
				} else {
					fmt.Printf("%s: FAILED\n", arg)
					allOK = false
				}
			}
			if !allOK {
				os.Exit(1)
			}
			return nil
		}
		for _, arg := range args {
			sum, err := hashSource(ctx, arg, newHash)
			if err != nil {
				return err
			}
			fmt.Printf("%s  %s\n", sum, arg)
		}
		return nil
	},
}

func newHasher(algo string) (func() hash.Hash, error) {
	switch algo {
	case "md5":
		return md5.New, nil
	case "sha256":
		return sha256.New, nil
	default:
		return nil, fmt.Errorf("仅支持 --algo md5 或 sha256,收到 %q", algo)
	}
}

// hashSource 对流式读取源并计算校验和,返回十六进制摘要。
func hashSource(ctx context.Context, arg string, newHash func() hash.Hash) (string, error) {
	src, err := openSourceArg(ctx, arg)
	if err != nil {
		return "", err
	}
	defer src.Close()
	h := newHash()
	if _, err := io.Copy(h, src.Reader); err != nil {
		return "", fmt.Errorf("读取 %s 失败: %w", arg, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashFile 计算本地文件校验和。
func hashFile(path string, newHash func() hash.Hash) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("读取本地文件失败: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("本地源必须是文件: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开本地文件失败: %w", err)
	}
	defer f.Close()
	h := newHash()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// etagValues 展示 S3 对象的原始 ETag。
func etagValues(args []string) error {
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
		if p.Key == "" {
			return fmt.Errorf("缺少 key,需指定 s3://bucket/key")
		}
		resp, err := s3c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &p.Bucket, Key: &p.Key})
		if err != nil {
			return fmt.Errorf("查询 %s 失败: %w", arg, err)
		}
		etag := "-"
		if resp.ETag != nil {
			etag = *resp.ETag
		}
		fmt.Printf("%s  %s\n", etag, arg)
	}
	return nil
}

func init() {
	checksumCmd.Flags().StringVar(&checksumAlgo, "algo", "md5", "校验算法: md5 或 sha256")
	checksumCmd.Flags().StringVar(&checksumCompare, "compare", "", "与本地文件比对(输出 OK/FAILED)")
	checksumCmd.Flags().BoolVar(&checksumEtag, "etag", false, "展示 S3 对象原始 ETag(不读内容)")
}
