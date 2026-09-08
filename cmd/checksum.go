package cmd

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/i18n"
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
	Short: "Compute object/file checksums",
	Long: `Stream-compute the md5 or sha256 checksum of an object/file and print "<checksum>  <source>".
--compare checks against a local file, printing OK on match and FAILED on mismatch (exit code 1 if any FAILED).
--etag shows the raw ETag of the S3 object without reading its content. Note: for multipart uploads
the ETag is not the md5 of the object content; this command does not compare ETag against content md5, --etag only displays it.

Examples:
  sail checksum s3://bucket/data.bin
  sail checksum --algo sha256 --compare ./local.bin s3://bucket/data.bin
  sail checksum --etag s3://bucket/data.bin`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if checksumEtag {
			for _, arg := range args {
				if !strings.HasPrefix(arg, "s3://") {
					return errors.New(i18n.T("--etag only supports s3:// paths"))
				}
			}
			if checksumCompare != "" || cmd.Flags().Changed("algo") {
				return errors.New(i18n.T("--etag cannot be used together with --algo/--compare"))
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
		return nil, fmt.Errorf(i18n.T("only --algo md5 or sha256 is supported, got %q"), algo)
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
		return "", fmt.Errorf(i18n.T("failed to read %s: %w"), arg, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashFile 计算本地文件校验和。
func hashFile(path string, newHash func() hash.Hash) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf(i18n.T("failed to read local file: %w"), err)
	}
	if info.IsDir() {
		return "", fmt.Errorf(i18n.T("local source must be a file: %s"), path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf(i18n.T("failed to open local file: %w"), err)
	}
	defer f.Close()
	h := newHash()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf(i18n.T("failed to read %s: %w"), path, err)
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
			return errors.New(i18n.T("missing key; specify s3://bucket/key"))
		}
		resp, err := s3c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &p.Bucket, Key: &p.Key})
		if err != nil {
			return fmt.Errorf(i18n.T("failed to query %s: %w"), arg, err)
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
	checksumCmd.Flags().StringVar(&checksumAlgo, "algo", "md5", "checksum algorithm (md5|sha256)")
	checksumCmd.Flags().StringVar(&checksumCompare, "compare", "", "compare against a local file (prints OK/FAILED)")
	checksumCmd.Flags().BoolVar(&checksumEtag, "etag", false, "show the raw ETag of the S3 object (without reading content)")
}
