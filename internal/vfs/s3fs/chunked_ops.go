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

// cleanupVersion 删除某个逻辑路径某一代的片目录。幂等、尽力而为:失败只留孤儿片
// (对列目录不可见),不影响逻辑文件的一致性,由 GC 收口。
//
// 作用域是 (logical, version) 这一对,因此不会碰到其它逻辑路径的数据 ——
// 回收是「删自己名下的前缀」,不需要读 manifest,也不需要知道谁引用过这些片。
func (f *FS) cleanupVersion(ctx context.Context, logical, version string) {
	if version == "" {
		return
	}
	f.cleanupPrefix(ctx, f.partsDir(logical, version)+"/", "旧分片")
}

// cleanupPrefix 列举并批量删掉一个前缀下的全部对象,供片回收的两处调用共用。
// label 是告警里对这批对象的称呼(如「旧分片」/「分片」),便于定位是哪条路径失败。
func (f *FS) cleanupPrefix(ctx context.Context, prefix, label string) {
	paginator := s3.NewListObjectsV2Paginator(f.client, &s3.ListObjectsV2Input{
		Bucket:  aws.String(f.bucket),
		Prefix:  aws.String(prefix),
		MaxKeys: aws.Int32(deleteBatchSize),
	})
	var keys []string
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "警告: 列举待清理的%s %s 失败: %v\n", label, prefix, err)
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
		fmt.Fprintf(os.Stderr, "警告: 清理%s %s 失败(留作孤儿待 GC): %v\n", label, prefix, err)
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
//  3. 删本路径的片目录(幂等,失败只留孤儿)。
func (f *FS) removeChunked(ctx context.Context, logical, key string, m *manifest) error {
	if err := f.deleteObjects(ctx, []string{key}); err != nil {
		return err
	}
	f.cleanupVersion(ctx, logical, m.Version)
	return nil
}

// renameChunked 重命名一个分片文件。分片不能只 CopyObject manifest —— manifest
// 引用的片目录按逻辑路径归属,删掉旧 manifest 就会连带把片清掉,新路径随即读不出。
// 因此:先把片复制到新路径自己的版本目录,再写新 manifest,最后删旧 manifest 与旧片。
func (f *FS) renameChunked(ctx context.Context, oldLogical, newLogical, oldKey, newKey string, m *manifest) error {
	version, err := f.copyParts(ctx, oldLogical, newLogical, m.Chunks, m.Version)
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
	f.cleanupVersion(ctx, oldLogical, m.Version)
	return nil
}

// copyParts 把一组分片复制到新逻辑路径自己的版本目录下,返回新版本号。
// 新版本号取自 (源版本, 新逻辑路径, 逻辑大小):掺进新路径保证与目标路径
// 已有代际不同名,否则复制会覆盖目标同名的片对象,让持有目标旧 manifest 的
// 在途读读到不同边界的数据。
func (f *FS) copyParts(ctx context.Context, oldLogical, newLogical string, parts []manifestChunk, srcVersion string) (string, error) {
	var total int64
	for _, c := range parts {
		total += c.Length
	}
	version := newVersion(srcVersion, newLogical, total)

	// 并发复制:逐片串行会让大文件重命名退化成「片数 × 单次 RTT」。复制是
	// 服务端内部搬字节,服务端可并行处理(与删除回退同理)。
	indexes := make([]int, len(parts))
	for i := range parts {
		indexes[i] = i
	}
	if err := runConcurrently(indexes, func(i int) error {
		src := f.partKey(oldLogical, srcVersion, i)
		dst := f.partKey(newLogical, version, i)
		if _, err := f.client.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket:     aws.String(f.bucket),
			Key:        aws.String(dst),
			CopySource: aws.String(f.bucket + "/" + src),
		}); err != nil {
			return fmt.Errorf("复制片 %d 失败: %w", i, err)
		}
		return nil
	}); err != nil {
		return "", err
	}
	return version, nil
}

// newVersion 为「复制到新路径」的片目录派生版本号。版本号只是片目录内的代际
// 隔离命名,不需要是内容哈希(manifest 里已记有事实验证信息);这里用源版本 +
// 新逻辑路径 + 逻辑大小派生,保证与目标路径已有代际不同名。
func newVersion(srcVersion, newLogical string, size int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("sail-rename\x00%s\x00%s\x00%d", srcVersion, newLogical, size)))
	return hex.EncodeToString(sum[:])
}

// removeChunkedPath 是 Remove 的分片分支入口:先 HEAD 定论,再决定是否走分片删除。
// handled=true 表示已按分片处理(调用方不必再删);exists 表示逻辑 key 确实存在
// (调用方可据此跳过注定空转的删除请求)。
func (f *FS) removeChunkedPath(ctx context.Context, logical, key string) (handled, exists bool, err error) {
	h, err := f.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(f.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return false, false, nil
		}
		return false, false, fmt.Errorf("s3fs: 读取 %s 元信息失败: %w", logical, err)
	}
	m, ok, err := f.detectManifest(ctx, key, h)
	if err != nil {
		return false, true, err
	}
	if !ok {
		return false, true, nil
	}
	return true, true, f.removeChunked(ctx, logical, key, m)
}
