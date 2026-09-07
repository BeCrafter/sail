package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/i18n"
	"github.com/BeCrafter/sail/internal/s3path"
	"github.com/BeCrafter/sail/internal/uploader"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
)

var (
	cpRecursive bool
	cpDryRun    bool
)

var cpCmd = &cobra.Command{
	Use:     "cp <src> <dst>",
	Aliases: []string{"upload", "download"},
	Short:   "Copy objects/files (local↔s3, s3↔s3)",
	Long: `Copy objects/files between local and S3 (local↔s3) and S3 and S3 (s3↔s3; s3↔s3 prefers server-side CopyObject, falling back to download→re-upload).
upload/download are aliases of cp.

S3 source paths support wildcards (* matches any characters including /, ? matches a single character), expanding automatically into multiple objects,
the destination is treated as a directory/prefix and the source relative hierarchy is preserved (no -r needed):
  sail cp 's3://bucket/logs/*.log' s3://bucket/archive/
  sail cp 's3://bucket/*.json' ./download-dir/

Examples:
  sail cp ./local.txt s3://bucket/path/copied.txt
  sail cp ./local.txt s3://bucket/path/         # trailing / means into the directory
  sail cp s3://bucket/a.txt ./out.txt
  sail cp -r ./dir s3://bucket/mirror/          # mirror a local directory recursively
  sail cp -r s3://bucket/prefix/ s3://bucket/dest/   # recursive server-side copy
  sail cp --dry-run ./local.txt s3://bucket/x   # preview, without actually copying
  sail upload ./local.txt                       # 1 arg: upload to the default bucket, key is the file name
  sail download s3://bucket/a.txt               # 1 arg: download into the current directory
  cat file | sail upload - s3://bucket/key      # piped input
  sail cp ./a.txt ./b.txt                        # rejected: use the system cp for local→local`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		srcArg := args[0]
		hasDst := len(args) == 2
		var dstArg string
		if hasDst {
			dstArg = args[1]
		}

		srcIsS3 := strings.HasPrefix(srcArg, "s3://")
		isStdin := srcArg == "-"

		// 1 参推断:local → 上传默认 bucket(key 用文件名);s3:// → 下载到当前目录
		dstInferredBucket := false
		if !hasDst {
			if !isStdin && !srcIsS3 {
				dstArg = "" // 哨兵:用默认 bucket
				dstInferredBucket = true
			} else if srcIsS3 {
				dstArg = "."
			}
			// isStdin 1 参:dstArg 留空,后面 stdin 分支会报"必须指定目标"
		}

		dstIsS3 := dstInferredBucket || (dstArg != "" && strings.HasPrefix(dstArg, "s3://"))

		// 拒绝本地→本地
		if !isStdin && !srcIsS3 && hasDst && !dstIsS3 {
			return errors.New(i18n.T("local-to-local copy should use the system cp command"))
		}

		ctx := context.Background()
		var s3c *s3.Client
		r, _, err := loadResolved()
		if err != nil {
			return err
		}
		// 通配符来源需要列举,即使 dry-run 也要建客户端
		srcWildcard := false
		if srcIsS3 {
			if sp, perr := parseS3(srcArg, r); perr == nil {
				srcWildcard = hasWildcard(sp.Key)
			}
		}
		if srcWildcard || !cpDryRun {
			s3c, err = client.New(ctx, r)
			if err != nil {
				return err
			}
		}

		if srcWildcard {
			if !hasDst && srcIsS3 {
				dstArg = "."
			}
			if dstArg == "" {
				return errors.New(i18n.T("wildcard copy must specify a target directory or s3:// prefix"))
			}
			return cpWildcards(ctx, s3c, r, srcArg, dstArg, cpDryRun)
		}

		switch {
		case isStdin:
			// 管道 → s3
			if !dstIsS3 {
				return errors.New(i18n.T("stdin input must specify a target s3://bucket/key"))
			}
			dst, err := parseS3(dstArg, r)
			if err != nil {
				return err
			}
			if cpDryRun {
				fmt.Printf(i18n.T("would upload <stdin> -> s3://%s/%s\n"), dst.Bucket, dst.Key)
				return nil
			}
			u := uploader.New(s3c)
			fmt.Printf(i18n.T("uploading <stdin> -> s3://%s/%s\n"), dst.Bucket, dst.Key)
			return u.UploadStream(ctx, os.Stdin, dst.Bucket, dst.Key)

		case !srcIsS3 && dstIsS3:
			// 本地 → s3
			var dst *s3path.S3Path
			if dstInferredBucket {
				// 1 参:用默认 bucket + 文件名作 key
				if r == nil || r.Bucket == "" {
					if cpDryRun {
						fmt.Printf(i18n.T("would copy %s -> s3://<default bucket>/%s (no default bucket configured)\n"), srcArg, filepath.Base(srcArg))
						return nil
					}
					return errors.New(i18n.T("no target bucket specified, use s3://bucket/key or set a default bucket in config"))
				}
				dst = &s3path.S3Path{Bucket: r.Bucket} // Key 空 → cpLocalToS3 用 deriveDstKey 补 basename
			} else {
				var err error
				dst, err = parseS3(dstArg, r)
				if err != nil {
					return err
				}
			}
			return cpLocalToS3(ctx, s3c, srcArg, dst, cpRecursive, false, cpDryRun)

		case srcIsS3 && !dstIsS3:
			// s3 → 本地
			src, err := parseS3(srcArg, r)
			if err != nil {
				return err
			}
			if src.Key == "" {
				return errors.New(i18n.T("missing key, specify s3://bucket/key"))
			}
			return cpS3ToLocal(ctx, s3c, src, dstArg, cpRecursive, false, cpDryRun)

		default: // s3 → s3
			src, err := parseS3(srcArg, r)
			if err != nil {
				return err
			}
			if src.Key == "" {
				return errors.New(i18n.T("missing key, specify s3://bucket/key"))
			}
			dst, err := parseS3(dstArg, r)
			if err != nil {
				return err
			}
			return cpS3ToS3(ctx, s3c, src, dst, cpRecursive, false, cpDryRun)
		}
	},
}

// deriveDstKey 解析目标 S3 key:dst.Key 为空或尾 / 表示进目录,追加 srcBase;否则用 dst.Key。
// 照搬 upload.go:81-83 的逻辑。
func deriveDstKey(srcBase string, dst *s3path.S3Path) string {
	if dst.Key == "" || strings.HasSuffix(dst.Key, "/") {
		return s3path.JoinKey(strings.TrimSuffix(dst.Key, "/"), srcBase)
	}
	return dst.Key
}

// cpLocalToS3 本地 -> s3。deleteSource 为 true 时(由 mv 调用)成功后删除本地源。
func cpLocalToS3(ctx context.Context, s3c *s3.Client, srcLocal string, dst *s3path.S3Path, recursive, deleteSource, dryRun bool) error {
	info, err := os.Stat(srcLocal)
	if err != nil {
		return fmt.Errorf(i18n.T("failed to read local path: %w"), err)
	}
	if info.IsDir() {
		if !recursive {
			return fmt.Errorf(i18n.T("%s is a directory, add -r to copy recursively"), srcLocal)
		}
		if dryRun {
			fmt.Printf(i18n.T("would recursively copy directory %s -> s3://%s/%s\n"), srcLocal, dst.Bucket, dst.Key)
			return nil
		}
		u := uploader.New(s3c)
		fmt.Printf(i18n.T("copying directory %s -> s3://%s/%s\n"), srcLocal, dst.Bucket, dst.Key)
		if err := u.UploadDir(ctx, srcLocal, dst.Bucket, dst.Key); err != nil {
			return fmt.Errorf(i18n.T("upload failed: %w"), err)
		}
		if deleteSource {
			if err := os.RemoveAll(srcLocal); err != nil {
				fmt.Fprintf(os.Stderr, i18n.T("warning: source deletion failed, data copied but source not cleaned up: %v\n"), err)
			}
		}
		return nil
	}
	// 单文件
	key := deriveDstKey(info.Name(), dst)
	if dryRun {
		fmt.Printf(i18n.T("would copy %s -> s3://%s/%s\n"), srcLocal, dst.Bucket, key)
		return nil
	}
	u := uploader.New(s3c)
	fmt.Printf(i18n.T("copying %s -> s3://%s/%s\n"), srcLocal, dst.Bucket, key)
	if err := u.UploadFile(ctx, srcLocal, dst.Bucket, key); err != nil {
		return fmt.Errorf(i18n.T("upload failed: %w"), err)
	}
	if deleteSource {
		if err := os.Remove(srcLocal); err != nil {
			fmt.Fprintf(os.Stderr, i18n.T("warning: source deletion failed, data copied but source not cleaned up: %v\n"), err)
		}
	}
	return nil
}

// cpS3ToLocal s3 -> 本地。deleteSource 为 true 时(由 mv 调用)每对象下载成功后删除源对象。
func cpS3ToLocal(ctx context.Context, s3c *s3.Client, src *s3path.S3Path, dstLocal string, recursive, deleteSource, dryRun bool) error {
	if recursive {
		base := strings.TrimSuffix(src.Key, "/")
		if dryRun {
			fmt.Printf(i18n.T("would recursively copy s3://%s/%s -> %s\n"), src.Bucket, src.Key, dstLocal)
			return nil
		}
		paginator := s3.NewListObjectsV2Paginator(s3c, &s3.ListObjectsV2Input{
			Bucket: &src.Bucket,
			Prefix: &src.Key,
		})
		count := 0
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(ctx)
			if err != nil {
				return fmt.Errorf(i18n.T("list failed: %w"), err)
			}
			for _, obj := range page.Contents {
				relKey := strings.TrimPrefix(*obj.Key, base+"/")
				localPath := filepath.Join(dstLocal, relKey)
				if err := downloadOne(ctx, s3c, src.Bucket, *obj.Key, localPath); err != nil {
					return err
				}
				if deleteSource {
					if _, err := s3c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &src.Bucket, Key: obj.Key}); err != nil {
						fmt.Fprintf(os.Stderr, i18n.T("warning: source deletion failed s3://%s/%s: %v\n"), src.Bucket, *obj.Key, err)
					}
				}
				count++
			}
		}
		fmt.Printf(i18n.T("downloaded %d objects\n"), count)
		return nil
	}
	// 单对象
	localPath := dstLocal
	if info, err := os.Stat(dstLocal); err == nil && info.IsDir() {
		localPath = dstLocal + "/" + s3path.BaseName(src.Key)
	} else if strings.HasSuffix(dstLocal, "/") {
		localPath = dstLocal + s3path.BaseName(src.Key)
	}
	if dryRun {
		fmt.Printf(i18n.T("would copy s3://%s/%s -> %s\n"), src.Bucket, src.Key, localPath)
		return nil
	}
	if err := downloadOne(ctx, s3c, src.Bucket, src.Key, localPath); err != nil {
		return err
	}
	if deleteSource {
		if _, err := s3c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &src.Bucket, Key: &src.Key}); err != nil {
			fmt.Fprintf(os.Stderr, i18n.T("warning: source deletion failed s3://%s/%s: %v\n"), src.Bucket, src.Key, err)
		}
	}
	return nil
}

// downloadOne 下载单个对象到本地路径(直读 GetObject 非 manager,避 checksum-trailer),照搬 download.go 模式。
func downloadOne(ctx context.Context, s3c *s3.Client, bucket, key, localPath string) error {
	resp, err := s3c.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &key,
	})
	if err != nil {
		return fmt.Errorf(i18n.T("download s3://%s/%s failed: %w"), bucket, key, err)
	}
	defer resp.Body.Close()
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf(i18n.T("failed to create directory: %w"), err)
	}
	f, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf(i18n.T("failed to create local file: %w"), err)
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf(i18n.T("failed to write file: %w"), err)
	}
	fmt.Printf(i18n.T("copying s3://%s/%s -> %s\n"), bucket, key, localPath)
	return nil
}

// cpS3ToS3 s3 -> s3(优先服务端 CopyObject,不可靠时回退 download→re-upload)。deleteSource 为 true 时(由 mv 调用)复制后删源。
func cpS3ToS3(ctx context.Context, s3c *s3.Client, src, dst *s3path.S3Path, recursive, deleteSource, dryRun bool) error {
	u := uploader.New(s3c)
	if recursive {
		srcBase := strings.TrimSuffix(src.Key, "/")
		dstBase := strings.TrimSuffix(dst.Key, "/")
		if dryRun {
			fmt.Printf(i18n.T("would recursively copy s3://%s/%s -> s3://%s/%s\n"), src.Bucket, src.Key, dst.Bucket, dst.Key)
			return nil
		}
		paginator := s3.NewListObjectsV2Paginator(s3c, &s3.ListObjectsV2Input{
			Bucket: &src.Bucket,
			Prefix: &src.Key,
		})
		count := 0
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(ctx)
			if err != nil {
				return fmt.Errorf(i18n.T("list failed: %w"), err)
			}
			for _, obj := range page.Contents {
				relKey := strings.TrimPrefix(*obj.Key, srcBase+"/")
				dstKey := s3path.JoinKey(dstBase, relKey)
				srcSize := int64(-1)
				if obj.Size != nil {
					srcSize = *obj.Size
				}
				if err := copyOneS3(ctx, s3c, u, src.Bucket, *obj.Key, srcSize, dst.Bucket, dstKey); err != nil {
					return err
				}
				if deleteSource {
					if _, err := s3c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &src.Bucket, Key: obj.Key}); err != nil {
						fmt.Fprintf(os.Stderr, i18n.T("warning: source deletion failed s3://%s/%s: %v\n"), src.Bucket, *obj.Key, err)
					}
				}
				count++
			}
		}
		fmt.Printf(i18n.T("copied %d objects\n"), count)
		return nil
	}
	// 单对象
	dstKey := deriveDstKey(s3path.BaseName(src.Key), dst)
	if dryRun {
		fmt.Printf(i18n.T("would copy s3://%s/%s -> s3://%s/%s\n"), src.Bucket, src.Key, dst.Bucket, dstKey)
		return nil
	}
	// 取源大小用于 CopyObject 后校验(检测部分服务 0 字节 quirk)
	srcSize := int64(-1)
	if h, err := s3c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &src.Bucket, Key: &src.Key}); err == nil && h.ContentLength != nil {
		srcSize = *h.ContentLength
	}
	if err := copyOneS3(ctx, s3c, u, src.Bucket, src.Key, srcSize, dst.Bucket, dstKey); err != nil {
		return err
	}
	if deleteSource {
		if _, err := s3c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &src.Bucket, Key: &src.Key}); err != nil {
			fmt.Fprintf(os.Stderr, i18n.T("warning: source deletion failed s3://%s/%s: %v\n"), src.Bucket, src.Key, err)
		}
	}
	return nil
}

// copyOneS3 服务端 CopyObject;若失败或目标大小与源不一致(部分服务会产出 0 字节),
// 回退到 download→re-upload 以保证数据正确。
func copyOneS3(ctx context.Context, s3c *s3.Client, u *uploader.Uploader, srcBucket, srcKey string, srcSize int64, dstBucket, dstKey string) error {
	_, err := s3c.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     &dstBucket,
		Key:        &dstKey,
		CopySource: aws.String(srcBucket + "/" + srcKey),
	})
	if err == nil {
		if h, hErr := s3c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &dstBucket, Key: &dstKey}); hErr == nil && h.ContentLength != nil && srcSize >= 0 && *h.ContentLength == srcSize {
			fmt.Printf(i18n.T("copying s3://%s/%s -> s3://%s/%s\n"), srcBucket, srcKey, dstBucket, dstKey)
			return nil
		}
		// 大小不一致:CopyObject 不可靠(部分服务),回退
	}
	resp, gErr := s3c.GetObject(ctx, &s3.GetObjectInput{Bucket: &srcBucket, Key: &srcKey})
	if gErr != nil {
		return fmt.Errorf(i18n.T("copy s3://%s/%s -> s3://%s/%s failed (CopyObject unreliable and fallback source read failed): %w"), srcBucket, srcKey, dstBucket, dstKey, gErr)
	}
	defer resp.Body.Close()
	if uErr := u.UploadStream(ctx, resp.Body, dstBucket, dstKey); uErr != nil {
		return fmt.Errorf(i18n.T("copy s3://%s/%s -> s3://%s/%s failed (fallback re-upload): %w"), srcBucket, srcKey, dstBucket, dstKey, uErr)
	}
	fmt.Printf(i18n.T("copy s3://%s/%s -> s3://%s/%s (fallback download→upload)\n"), srcBucket, srcKey, dstBucket, dstKey)
	return nil
}

// cpWildcards 通配符来源复制:s3 -> s3 逐对象服务端复制,下载逐对象落盘。
// 对象相对静态前缀的层级关系被保留(与 cp -r 语义一致)。
func cpWildcards(ctx context.Context, s3c *s3.Client, r *config.Resolved, srcArg, dstArg string, dryRun bool) error {
	objs, staticBase, bucket, err := expandWildcards(ctx, s3c, r, srcArg)
	if err != nil {
		return err
	}
	dstIsS3 := strings.HasPrefix(dstArg, "s3://")
	dstBucket, dstBase, dstLocal := "", "", ""
	if dstIsS3 {
		dp, err := parseS3(dstArg, r)
		if err != nil {
			return err
		}
		dstBucket, dstBase = dp.Bucket, strings.TrimSuffix(dp.Key, "/")
	} else {
		dstLocal = dstArg
	}
	count := 0
	u := uploader.New(s3c)
	for _, obj := range objs {
		rel := relKeyOf(*obj.Key, staticBase)
		if dstIsS3 {
			dstKey := s3path.JoinKey(dstBase, rel)
			if dryRun {
				fmt.Printf(i18n.T("would copy s3://%s/%s -> s3://%s/%s\n"), bucket, *obj.Key, dstBucket, dstKey)
				count++
				continue
			}
			srcSize := int64(-1)
			if obj.Size != nil {
				srcSize = *obj.Size
			}
			if err := copyOneS3(ctx, s3c, u, bucket, *obj.Key, srcSize, dstBucket, dstKey); err != nil {
				return err
			}
		} else {
			localPath := filepath.Join(dstLocal, filepath.FromSlash(rel))
			if dryRun {
				fmt.Printf(i18n.T("would copy s3://%s/%s -> %s\n"), bucket, *obj.Key, localPath)
				count++
				continue
			}
			if err := downloadOne(ctx, s3c, bucket, *obj.Key, localPath); err != nil {
				return err
			}
		}
		count++
	}
	fmt.Printf(i18n.T("copied %d objects\n"), count)
	return nil
}

func init() {
	cpCmd.Flags().BoolVarP(&cpRecursive, "recursive", "r", false, "recurse into subdirectories")
	cpCmd.Flags().BoolVar(&cpDryRun, "dry-run", false, "show what would be done without actually copying")
}
