package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
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
	Short:   "复制对象/文件(本地↔s3, s3↔s3)",
	Long: `复制对象/文件,支持本地↔s3 与 s3↔s3(s3↔s3 优先走服务端 CopyObject,失败回退 download→re-upload)。
upload/download 为 cp 的别名。

s3 源路径支持通配符(* 匹配任意字符含 /,? 匹配单字符),自动展开为多个对象,
目标视为目录/前缀,源相对层级保留(无需 -r):
  sail cp 's3://bucket/logs/*.log' s3://bucket/archive/
  sail cp 's3://bucket/*.json' ./download-dir/

示例:
  sail cp ./local.txt s3://bucket/path/copied.txt
  sail cp ./local.txt s3://bucket/path/         # 尾 / 表示进目录
  sail cp s3://bucket/a.txt ./out.txt
  sail cp -r ./dir s3://bucket/mirror/          # 递归镜像本地目录
  sail cp -r s3://bucket/prefix/ s3://bucket/dest/   # 服务端递归复制
  sail cp --dry-run ./local.txt s3://bucket/x   # 预演,不实际复制
  sail upload ./local.txt                       # 1 参:上传到默认 bucket,key 用文件名
  sail download s3://bucket/a.txt               # 1 参:下载到当前目录
  cat file | sail upload - s3://bucket/key      # 管道输入
  sail cp ./a.txt ./b.txt                        # 拒绝:本地→本地用系统 cp`,
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
			return fmt.Errorf("本地到本地的复制请使用系统 cp 命令")
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
				return fmt.Errorf("通配符复制必须指定目标目录或 s3:// 前缀")
			}
			return cpWildcards(ctx, s3c, r, srcArg, dstArg, cpDryRun)
		}

		switch {
		case isStdin:
			// 管道 → s3
			if !dstIsS3 {
				return fmt.Errorf("管道输入必须指定目标 s3://bucket/key")
			}
			dst, err := parseS3(dstArg, r)
			if err != nil {
				return err
			}
			if cpDryRun {
				fmt.Printf("将上传 <stdin> -> s3://%s/%s\n", dst.Bucket, dst.Key)
				return nil
			}
			u := uploader.New(s3c)
			fmt.Printf("上传 <stdin> -> s3://%s/%s\n", dst.Bucket, dst.Key)
			return u.UploadStream(ctx, os.Stdin, dst.Bucket, dst.Key)

		case !srcIsS3 && dstIsS3:
			// 本地 → s3
			var dst *s3path.S3Path
			if dstInferredBucket {
				// 1 参:用默认 bucket + 文件名作 key
				if r == nil || r.Bucket == "" {
					if cpDryRun {
						fmt.Printf("将复制 %s -> s3://<默认桶>/%s (未配置默认 bucket)\n", srcArg, filepath.Base(srcArg))
						return nil
					}
					return fmt.Errorf("未指定目标 bucket,请用 s3://bucket/key 或在配置中设置默认 bucket")
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
				return fmt.Errorf("缺少 key,需指定 s3://bucket/key")
			}
			return cpS3ToLocal(ctx, s3c, src, dstArg, cpRecursive, false, cpDryRun)

		default: // s3 → s3
			src, err := parseS3(srcArg, r)
			if err != nil {
				return err
			}
			if src.Key == "" {
				return fmt.Errorf("缺少 key,需指定 s3://bucket/key")
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
		return fmt.Errorf("读取本地路径失败: %w", err)
	}
	if info.IsDir() {
		if !recursive {
			return fmt.Errorf("%s 是目录,请加 -r 递归复制", srcLocal)
		}
		if dryRun {
			fmt.Printf("将递归复制目录 %s -> s3://%s/%s\n", srcLocal, dst.Bucket, dst.Key)
			return nil
		}
		u := uploader.New(s3c)
		fmt.Printf("复制目录 %s -> s3://%s/%s\n", srcLocal, dst.Bucket, dst.Key)
		if err := u.UploadDir(ctx, srcLocal, dst.Bucket, dst.Key); err != nil {
			return fmt.Errorf("上传失败: %w", err)
		}
		if deleteSource {
			if err := os.RemoveAll(srcLocal); err != nil {
				fmt.Fprintf(os.Stderr, "警告: 源删除失败,数据已复制但源未清理: %v\n", err)
			}
		}
		return nil
	}
	// 单文件
	key := deriveDstKey(info.Name(), dst)
	if dryRun {
		fmt.Printf("将复制 %s -> s3://%s/%s\n", srcLocal, dst.Bucket, key)
		return nil
	}
	u := uploader.New(s3c)
	fmt.Printf("复制 %s -> s3://%s/%s\n", srcLocal, dst.Bucket, key)
	if err := u.UploadFile(ctx, srcLocal, dst.Bucket, key); err != nil {
		return fmt.Errorf("上传失败: %w", err)
	}
	if deleteSource {
		if err := os.Remove(srcLocal); err != nil {
			fmt.Fprintf(os.Stderr, "警告: 源删除失败,数据已复制但源未清理: %v\n", err)
		}
	}
	return nil
}

// cpS3ToLocal s3 -> 本地。deleteSource 为 true 时(由 mv 调用)每对象下载成功后删除源对象。
func cpS3ToLocal(ctx context.Context, s3c *s3.Client, src *s3path.S3Path, dstLocal string, recursive, deleteSource, dryRun bool) error {
	if recursive {
		base := strings.TrimSuffix(src.Key, "/")
		if dryRun {
			fmt.Printf("将递归复制 s3://%s/%s -> %s\n", src.Bucket, src.Key, dstLocal)
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
				return fmt.Errorf("列举失败: %w", err)
			}
			for _, obj := range page.Contents {
				relKey := strings.TrimPrefix(*obj.Key, base+"/")
				localPath := filepath.Join(dstLocal, relKey)
				if err := downloadOne(ctx, s3c, src.Bucket, *obj.Key, localPath); err != nil {
					return err
				}
				if deleteSource {
					if _, err := s3c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &src.Bucket, Key: obj.Key}); err != nil {
						fmt.Fprintf(os.Stderr, "警告: 源删除失败 s3://%s/%s: %v\n", src.Bucket, *obj.Key, err)
					}
				}
				count++
			}
		}
		fmt.Printf("共下载 %d 个对象\n", count)
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
		fmt.Printf("将复制 s3://%s/%s -> %s\n", src.Bucket, src.Key, localPath)
		return nil
	}
	if err := downloadOne(ctx, s3c, src.Bucket, src.Key, localPath); err != nil {
		return err
	}
	if deleteSource {
		if _, err := s3c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &src.Bucket, Key: &src.Key}); err != nil {
			fmt.Fprintf(os.Stderr, "警告: 源删除失败 s3://%s/%s: %v\n", src.Bucket, src.Key, err)
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
		return fmt.Errorf("下载 s3://%s/%s 失败: %w", bucket, key, err)
	}
	defer resp.Body.Close()
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	f, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("创建本地文件失败: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}
	fmt.Printf("复制 s3://%s/%s -> %s\n", bucket, key, localPath)
	return nil
}

// cpS3ToS3 s3 -> s3(优先服务端 CopyObject,不可靠时回退 download→re-upload)。deleteSource 为 true 时(由 mv 调用)复制后删源。
func cpS3ToS3(ctx context.Context, s3c *s3.Client, src, dst *s3path.S3Path, recursive, deleteSource, dryRun bool) error {
	u := uploader.New(s3c)
	if recursive {
		srcBase := strings.TrimSuffix(src.Key, "/")
		dstBase := strings.TrimSuffix(dst.Key, "/")
		if dryRun {
			fmt.Printf("将递归复制 s3://%s/%s -> s3://%s/%s\n", src.Bucket, src.Key, dst.Bucket, dst.Key)
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
				return fmt.Errorf("列举失败: %w", err)
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
						fmt.Fprintf(os.Stderr, "警告: 源删除失败 s3://%s/%s: %v\n", src.Bucket, *obj.Key, err)
					}
				}
				count++
			}
		}
		fmt.Printf("共复制 %d 个对象\n", count)
		return nil
	}
	// 单对象
	dstKey := deriveDstKey(s3path.BaseName(src.Key), dst)
	if dryRun {
		fmt.Printf("将复制 s3://%s/%s -> s3://%s/%s\n", src.Bucket, src.Key, dst.Bucket, dstKey)
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
			fmt.Fprintf(os.Stderr, "警告: 源删除失败 s3://%s/%s: %v\n", src.Bucket, src.Key, err)
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
			fmt.Printf("复制 s3://%s/%s -> s3://%s/%s\n", srcBucket, srcKey, dstBucket, dstKey)
			return nil
		}
		// 大小不一致:CopyObject 不可靠(部分服务),回退
	}
	resp, gErr := s3c.GetObject(ctx, &s3.GetObjectInput{Bucket: &srcBucket, Key: &srcKey})
	if gErr != nil {
		return fmt.Errorf("复制 s3://%s/%s -> s3://%s/%s 失败(CopyObject 不可靠且回退读取源失败): %w", srcBucket, srcKey, dstBucket, dstKey, gErr)
	}
	defer resp.Body.Close()
	if uErr := u.UploadStream(ctx, resp.Body, dstBucket, dstKey); uErr != nil {
		return fmt.Errorf("复制 s3://%s/%s -> s3://%s/%s 失败(回退 re-upload): %w", srcBucket, srcKey, dstBucket, dstKey, uErr)
	}
	fmt.Printf("复制 s3://%s/%s -> s3://%s/%s (回退 download→upload)\n", srcBucket, srcKey, dstBucket, dstKey)
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
				fmt.Printf("将复制 s3://%s/%s -> s3://%s/%s\n", bucket, *obj.Key, dstBucket, dstKey)
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
				fmt.Printf("将复制 s3://%s/%s -> %s\n", bucket, *obj.Key, localPath)
				count++
				continue
			}
			if err := downloadOne(ctx, s3c, bucket, *obj.Key, localPath); err != nil {
				return err
			}
		}
		count++
	}
	fmt.Printf("共复制 %d 个对象\n", count)
	return nil
}

func init() {
	cpCmd.Flags().BoolVarP(&cpRecursive, "recursive", "r", false, "递归复制")
	cpCmd.Flags().BoolVar(&cpDryRun, "dry-run", false, "只显示将执行的操作,不实际复制")
}
