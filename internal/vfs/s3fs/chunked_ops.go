package s3fs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// 本文件承载分片对象的「非读写」操作:删除、重命名、旧版本清理。
// 与读写共享同一条不变量:逻辑 key 上的 manifest 是唯一提交点。

// cleanupVersion 删除某个版本的整个片目录。幂等、尽力而为:失败只留孤儿片
// (对列目录不可见),不影响逻辑文件的一致性,由 GC 收口。
func (f *FS) cleanupVersion(ctx context.Context, version string) {
	if version == "" {
		return
	}
	prefix := partsPrefixOf(version)
	paginator := s3.NewListObjectsV2Paginator(f.client, &s3.ListObjectsV2Input{
		Bucket:  aws.String(f.bucket),
		Prefix:  aws.String(prefix),
		MaxKeys: aws.Int32(deleteBatchSize),
	})
	var keys []string
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "警告: 列举待清理分片 %s 失败: %v\n", prefix, err)
			return
		}
		for _, o := range page.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
	}
	if len(keys) == 0 {
		return
	}
	if err := f.deleteObjects(ctx, keys); err != nil {
		fmt.Fprintf(os.Stderr, "警告: 清理旧分片 %s 失败(留作孤儿待 GC): %v\n", prefix, err)
	}
}

// oldVersion 读逻辑 key 上现有对象的 sail-manifest-key 元数据;不是分片对象
// 或不存在时返回空串。
func (w *writeFile) oldVersion(ctx context.Context) (string, error) {
	h, err := w.fs.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(w.fs.bucket),
		Key:    aws.String(w.key),
	})
	if err != nil {
		if isNotFound(err) {
			return "", nil
		}
		return "", err
	}
	return h.Metadata["sail-manifest-key"], nil
}

// removeChunked 删除一个分片文件:
//  1. 读 manifest 拿版本号;
//  2. 删逻辑 key —— 逻辑文件立刻不可见(提交点);
//  3. 列片目录并批量删(幂等,失败只留孤儿)。
func (f *FS) removeChunked(ctx context.Context, key string, m *manifest) error {
	if err := f.deleteObjects(ctx, []string{key}); err != nil {
		return err
	}
	f.cleanupVersion(ctx, m.Version)
	return nil
}

// renameChunked 重命名一个分片文件。分片不能只 CopyObject manifest —— manifest
// 引用的片目录名取自内容哈希,删掉旧 manifest 就会连带把片清掉,新路径随即读不出。
// 因此:先把片复制到新路径自己的版本目录,再写新 manifest,最后删旧 manifest 与旧片。
func (f *FS) renameChunked(ctx context.Context, oldLogical, newLogical, oldKey, newKey string, m *manifest) error {
	version, err := f.copyParts(ctx, m.Chunks, m.Version)
	if err != nil {
		return fmt.Errorf("s3fs: 复制 %s 的分片失败: %w", oldLogical, err)
	}
	next := *m
	next.Version = version
	for i := range next.Chunks {
		next.Chunks[i].ETag = ""
	}
	body, err := next.encode()
	if err != nil {
		return err
	}
	contentType := m.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if _, err := f.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(f.bucket),
		Key:         aws.String(newKey),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
		Metadata: map[string]string{
			"sail-manifest-key": next.Version,
			"sail-total-size":   fmt.Sprintf("%d", next.Size),
		},
	}); err != nil {
		return fmt.Errorf("s3fs: 写入 %s 的 manifest 失败: %w", newLogical, err)
	}
	// 新路径已可读,再删旧物件。
	if err := f.deleteObjects(ctx, []string{oldKey}); err != nil {
		return fmt.Errorf("s3fs: 删除源 manifest %s 失败: %w", oldLogical, err)
	}
	f.cleanupVersion(ctx, m.Version)
	return nil
}

// copyParts 把一组分片复制到新的版本目录下,返回新版本号。新版本号取自
// 新路径的逻辑大小与源版本(迁移后仍可自证来源),与源目录隔离,避免
// 重命名与删除互相清理对方的片。
func (f *FS) copyParts(ctx context.Context, parts []manifestChunk, srcVersion string) (string, error) {
	var total int64
	for _, c := range parts {
		total += c.Length
	}
	version := newVersion(srcVersion, total)
	for i := range parts {
		src := partKey(srcVersion, i)
		dst := partKey(version, i)
		if _, err := f.client.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket:     aws.String(f.bucket),
			Key:        aws.String(dst),
			CopySource: aws.String(f.bucket + "/" + src),
		}); err != nil {
			return "", fmt.Errorf("复制片 %d 失败: %w", i, err)
		}
	}
	return version, nil
}

// newVersion 为「复制到新路径」的片目录派生版本号。版本号只是片目录的隔离
// 命名,不需要是内容哈希(manifest 里已记有事实验证信息);这里用源版本 +
// 逻辑大小派生,保证与源目录不同、且同一源重复迁移得到同一目录(幂等)。
func newVersion(srcVersion string, size int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("sail-rename\x00%s\x00%d", srcVersion, size)))
	return hex.EncodeToString(sum[:])
}

// removeChunkedPath 是 Remove 的分片分支入口:先 HEAD 定论,再决定是否走分片删除。
// 返回 (true, err) 表示已按分片处理。
func (f *FS) removeChunkedPath(ctx context.Context, logical, key string) (bool, error) {
	h, err := f.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(f.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("s3fs: 读取 %s 元信息失败: %w", logical, err)
	}
	m, ok, err := f.detectManifest(ctx, key, h)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return true, f.removeChunked(ctx, key, m)
}
