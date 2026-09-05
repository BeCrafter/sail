package cmd

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/s3path"
	"github.com/BeCrafter/sail/internal/uploader"
	"github.com/BeCrafter/sail/internal/view"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
)

var (
	syncDelete   bool
	syncDryRun   bool
	syncExclude  []string
	syncInclude  []string
	syncChecksum bool
	syncUpdate   bool
)

// syncEntry 同步索引条目:大小 + 修改时间 + ETag(仅 s3 侧,已去引号)。
type syncEntry struct {
	size  int64
	mtime time.Time
	etag  string
}

// syncPath 同步的一端:s3 前缀或本地目录。
type syncPath struct {
	isS3     bool
	bucket   string // s3 桶
	keyBase  string // s3 基础前缀(trim 尾 /,可为空)
	localDir string // 本地根目录
}

func newSyncPath(r *config.Resolved, arg string) (*syncPath, error) {
	if strings.HasPrefix(arg, "s3://") {
		p, err := parseS3(arg, r)
		if err != nil {
			return nil, err
		}
		if p.Key == "" {
			return nil, fmt.Errorf("目标缺少 key,需指定 s3://bucket/prefix")
		}
		return &syncPath{isS3: true, bucket: p.Bucket, keyBase: strings.TrimSuffix(p.Key, "/")}, nil
	}
	return &syncPath{localDir: arg}, nil
}

func (sp *syncPath) index(ctx context.Context, s3c *s3.Client) (map[string]syncEntry, error) {
	if sp.isS3 {
		return indexS3(ctx, s3c, sp.bucket, sp.keyBase)
	}
	return indexLocal(sp.localDir)
}

func (sp *syncPath) uri(relKey string) string {
	if sp.isS3 {
		return "s3://" + sp.bucket + "/" + s3path.JoinKey(sp.keyBase, relKey)
	}
	return filepath.Join(sp.localDir, filepath.FromSlash(relKey))
}

func (sp *syncPath) display(relKey string) string {
	return sp.uri(relKey)
}

var syncCmd = &cobra.Command{
	Use:   "sync <src> <dst>",
	Short: "rsync 式增量同步(本地↔s3, s3↔s3)",
	Long: `rsync 式增量同步:默认以大小 + 修改时间比对,只传输有差异的条目。
支持本地→s3、s3→本地、s3→s3(服务端复制);本地↔本地请用系统 rsync。

比对模式(S3 LastModified 秒级精度,比对容差 1s):
  (默认)   目标缺失 / 大小不同 / 源修改时间晚于目标 1s 以上 → 传输
  --update 只传输比目标新的条目(目标较新时即使大小不同也跳过)
  --checksum 大小相同时按内容 md5 校验(ETag 单分片快路径,否则流式计算)

过滤(--exclude 与 --include 组合,被过滤条目双向不可见:既不传输也不被 --delete 删除):
  --exclude 通配符(可重复),同时匹配相对路径与文件名;尾 / 的目录模式排除整个目录
  --include 白名单(可重复);提供后只有命中的条目参与同步
  --delete 删除目标端多余条目(s3 侧批量删除,本地侧删除文件并清理空目录)

示例:
  sail sync ./dir s3://bucket/mirror/
  sail sync --exclude '*.tmp' --delete ./dir s3://bucket/mirror/
  sail sync --checksum ./dir s3://bucket/mirror/
  sail sync --include '*.json' s3://bucket/mirror/ ./dir2 --dry-run`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		srcArg, dstArg := args[0], args[1]
		if !strings.HasPrefix(srcArg, "s3://") && !strings.HasPrefix(dstArg, "s3://") {
			return fmt.Errorf("本地到本地的同步请使用系统 rsync")
		}
		ctx := context.Background()
		r, _, err := loadResolved()
		if err != nil {
			return err
		}
		s3c, err := client.New(ctx, r)
		if err != nil {
			return err
		}
		return runSync(ctx, s3c, r, srcArg, dstArg)
	},
}

func runSync(ctx context.Context, s3c *s3.Client, r *config.Resolved, srcArg, dstArg string) error {
	src, err := newSyncPath(r, srcArg)
	if err != nil {
		return err
	}
	dst, err := newSyncPath(r, dstArg)
	if err != nil {
		return err
	}
	if !src.isS3 {
		info, err := os.Stat(srcArg)
		if err != nil {
			return fmt.Errorf("读取本地路径失败: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("源 %s 不是目录", srcArg)
		}
	}
	if src.isS3 && dst.isS3 && src.bucket == dst.bucket && prefixOverlap(src.keyBase, dst.keyBase) {
		return fmt.Errorf("源与目标前缀重叠,不能同步")
	}

	srcIndex, err := src.index(ctx, s3c)
	if err != nil {
		return err
	}
	dstIndex, err := dst.index(ctx, s3c)
	if err != nil {
		return err
	}

	transfer, skipped := 0, 0
	for relKey, se := range srcIndex {
		if !isVisible(relKey) {
			skipped++
			continue
		}
		de, ok := dstIndex[relKey]
		need, err := syncNeed(ctx, s3c, r, src, dst, relKey, se, de, ok)
		if err != nil {
			return err
		}
		if !need {
			skipped++
			continue
		}
		transfer++
		if syncDryRun {
			fmt.Printf("将同步 %s -> %s\n", src.display(relKey), dst.display(relKey))
			continue
		}
		if err := syncTransfer(ctx, s3c, src, dst, relKey, se); err != nil {
			return err
		}
	}

	deleted := 0
	if syncDelete {
		var extraKeys []string
		for relKey := range dstIndex {
			if !isVisible(relKey) {
				continue
			}
			if _, ok := srcIndex[relKey]; ok {
				continue
			}
			deleted++
			if syncDryRun {
				fmt.Printf("将删除 %s\n", dst.display(relKey))
				continue
			}
			if dst.isS3 {
				extraKeys = append(extraKeys, s3path.JoinKey(dst.keyBase, relKey))
			} else {
				localPath := filepath.Join(dst.localDir, filepath.FromSlash(relKey))
				if err := os.Remove(localPath); err != nil {
					return fmt.Errorf("删除 %s 失败: %w", localPath, err)
				}
				// 自底向上清理空目录(忽略非空/权限错误)
				dir := filepath.Dir(localPath)
				for strings.HasPrefix(dir, dst.localDir) && dir != dst.localDir {
					if err := os.Remove(dir); err != nil {
						break
					}
					dir = filepath.Dir(dir)
				}
			}
		}
		if !syncDryRun && dst.isS3 && len(extraKeys) > 0 {
			if _, err := deleteObjectsBatch(ctx, s3c, dst.bucket, extraKeys); err != nil {
				return err
			}
		}
	}
	fmt.Printf("同步完成: 传输 %d 个, 删除 %d 个, 跳过 %d 个\n", transfer, deleted, skipped)
	return nil
}

// syncNeed 判定单个条目是否需要传输。
func syncNeed(ctx context.Context, s3c *s3.Client, r *config.Resolved, src, dst *syncPath, relKey string, se, de syncEntry, dstExists bool) (bool, error) {
	if !dstExists {
		return true, nil
	}
	mtimeDiff := se.mtime.Sub(de.mtime)
	if de.size != se.size {
		// --update:大小不同时,仅当源较新才覆盖(目标较新则跳过)
		if syncUpdate {
			return mtimeDiff > time.Second, nil
		}
		return true, nil
	}
	if syncChecksum {
		// mtime 相差 <=1s 视为已同步(幂等快路径);否则按内容 md5 校验
		if mtimeDiff <= time.Second && mtimeDiff >= -time.Second {
			return false, nil
		}
		diff, err := checksumDiffer(ctx, s3c, r, src, dst, relKey, se, de)
		if err != nil {
			return false, err
		}
		return diff, nil
	}
	// 默认与 --update:源晚于目标 1s 以上才传输(容差吸收 S3 秒级截断,保证幂等)
	return mtimeDiff > time.Second, nil
}

// checksumDiffer 校验两侧内容 md5 是否不同。
// 快路径:两侧 ETag 均为单分片(32 位 hex)时直接比较;否则流式计算。
func checksumDiffer(ctx context.Context, s3c *s3.Client, r *config.Resolved, src, dst *syncPath, relKey string, se, de syncEntry) (bool, error) {
	if src.isS3 && dst.isS3 && isPlainETag(se.etag) && isPlainETag(de.etag) {
		return !strings.EqualFold(se.etag, de.etag), nil
	}
	srcSum, err := hashSyncSide(ctx, s3c, r, src, relKey)
	if err != nil {
		return false, err
	}
	dstSum, err := hashSyncSide(ctx, s3c, r, dst, relKey)
	if err != nil {
		return false, err
	}
	return srcSum != dstSum, nil
}

// hashSyncSide 计算同步一端单个条目的内容 md5(流式)。
func hashSyncSide(ctx context.Context, s3c *s3.Client, r *config.Resolved, side *syncPath, relKey string) (string, error) {
	if side.isS3 {
		src, err := view.OpenSource(ctx, side.uri(relKey), s3c, r.Bucket)
		if err != nil {
			return "", err
		}
		defer src.Close()
		h := md5.New()
		if _, err := io.Copy(h, src.Reader); err != nil {
			return "", fmt.Errorf("读取 %s 失败: %w", side.uri(relKey), err)
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	localPath := filepath.Join(side.localDir, filepath.FromSlash(relKey))
	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("打开 %s 失败: %w", localPath, err)
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("读取 %s 失败: %w", localPath, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// isPlainETag 判断 ETag 是否为单分片对象的内容 md5(32 位 hex,不含 "-")。
func isPlainETag(etag string) bool {
	if len(etag) != 32 {
		return false
	}
	_, err := hex.DecodeString(etag)
	return err == nil
}

// syncTransfer 执行单个条目的传输。
func syncTransfer(ctx context.Context, s3c *s3.Client, src, dst *syncPath, relKey string, se syncEntry) error {
	switch {
	case !src.isS3 && dst.isS3: // 本地 → s3
		localPath := filepath.Join(src.localDir, filepath.FromSlash(relKey))
		u := uploader.New(s3c)
		key := s3path.JoinKey(dst.keyBase, relKey)
		if err := u.UploadFile(ctx, localPath, dst.bucket, key); err != nil {
			return err
		}
		fmt.Printf("同步 %s -> s3://%s/%s\n", localPath, dst.bucket, key)
	case src.isS3 && !dst.isS3: // s3 → 本地
		key := s3path.JoinKey(src.keyBase, relKey)
		localPath := filepath.Join(dst.localDir, filepath.FromSlash(relKey))
		if err := syncDownload(ctx, s3c, src.bucket, key, localPath, se.mtime); err != nil {
			return err
		}
	default: // s3 → s3 服务端复制
		srcKey := s3path.JoinKey(src.keyBase, relKey)
		dstKey := s3path.JoinKey(dst.keyBase, relKey)
		u := uploader.New(s3c)
		if err := copyOneS3(ctx, s3c, u, src.bucket, srcKey, -1, dst.bucket, dstKey); err != nil {
			return err
		}
	}
	return nil
}

// prefixOverlap 判断两个 s3 基前缀是否互为祖先(空串表示桶根,与任何前缀重叠)。
func prefixOverlap(a, b string) bool {
	if a == b {
		return true
	}
	if a == "" || b == "" {
		return true
	}
	return strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// indexLocal 遍历本地目录构建索引(relKey 用 / 分隔)。
func indexLocal(dir string) (map[string]syncEntry, error) {
	entries := map[string]syncEntry{}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		e := syncEntry{size: info.Size()}
		if info.Mode()&os.ModeSymlink == 0 {
			e.mtime = info.ModTime()
		}
		entries[filepath.ToSlash(rel)] = e
		return nil
	})
	return entries, err
}

// indexS3 列举前缀构建索引,跳过目录占位对象。
func indexS3(ctx context.Context, s3c *s3.Client, bucket, prefix string) (map[string]syncEntry, error) {
	objs, err := collectAllObjects(ctx, s3c, bucket, prefix)
	if err != nil {
		return nil, err
	}
	entries := map[string]syncEntry{}
	base := strings.TrimSuffix(prefix, "/")
	for _, obj := range objs {
		key := *obj.Key
		if strings.HasSuffix(key, "/") && (obj.Size == nil || *obj.Size == 0) {
			continue
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(key, base), "/")
		e := syncEntry{}
		if obj.Size != nil {
			e.size = *obj.Size
		}
		if obj.LastModified != nil {
			e.mtime = *obj.LastModified
		}
		if obj.ETag != nil {
			e.etag = strings.Trim(*obj.ETag, `"`)
		}
		entries[rel] = e
	}
	return entries, nil
}

// isVisible 判断条目是否参与同步:exclude 命中则不可见(exclude 为空视为无排除);
// include 提供时白名单外的条目也不可见(include 为空视为无白名单)。
func isVisible(relKey string) bool {
	if len(syncExclude) > 0 && matchPatterns(syncExclude, relKey) {
		return false
	}
	if len(syncInclude) > 0 && !matchPatterns(syncInclude, relKey) {
		return false
	}
	return true
}

// matchPatterns 通配符匹配(同时匹配相对路径与文件名;尾 / 的目录模式匹配整个目录)。
// patterns 为空时视为全部命中。
func matchPatterns(patterns []string, relKey string) bool {
	if len(patterns) == 0 {
		return true
	}
	name := s3path.BaseName(relKey)
	for _, pattern := range patterns {
		if ok, _ := path.Match(pattern, relKey); ok {
			return true
		}
		if ok, _ := path.Match(pattern, name); ok {
			return true
		}
		if strings.HasSuffix(pattern, "/") && strings.HasPrefix(relKey, pattern) {
			return true
		}
	}
	return false
}

// syncDownload 下载对象到本地并固化修改时间(保证后续同步跳过)。
func syncDownload(ctx context.Context, s3c *s3.Client, bucket, key, localPath string, mtime time.Time) error {
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
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return fmt.Errorf("写入文件失败: %w", err)
	}
	f.Close()
	if !mtime.IsZero() {
		if err := os.Chtimes(localPath, mtime, mtime); err != nil {
			fmt.Fprintf(os.Stderr, "警告: 设置修改时间失败: %v\n", err)
		}
	}
	fmt.Printf("同步 s3://%s/%s -> %s\n", bucket, key, localPath)
	return nil
}

func init() {
	syncCmd.Flags().BoolVar(&syncDelete, "delete", false, "删除目标端多余的条目")
	syncCmd.Flags().BoolVar(&syncDryRun, "dry-run", false, "只显示将执行的操作,不实际同步")
	syncCmd.Flags().StringSliceVar(&syncExclude, "exclude", nil, "排除通配符(可重复,匹配相对路径或文件名)")
	syncCmd.Flags().StringSliceVar(&syncInclude, "include", nil, "包含白名单通配符(可重复,提供后只同步命中条目)")
	syncCmd.Flags().BoolVar(&syncChecksum, "checksum", false, "大小相同时按内容 md5 校验差异")
	syncCmd.Flags().BoolVar(&syncUpdate, "update", false, "只传输比目标新的条目")
}
