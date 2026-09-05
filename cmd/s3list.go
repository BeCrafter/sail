package cmd

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// collectAllObjects 全量分页收集 bucket 下 prefix 的所有对象。
// find/du/sync/ls 排序共用;CLI 场景全量进内存可接受。
func collectAllObjects(ctx context.Context, s3c *s3.Client, bucket, prefix string) ([]types.Object, error) {
	var objs []types.Object
	paginator := s3.NewListObjectsV2Paginator(s3c, &s3.ListObjectsV2Input{
		Bucket: &bucket,
		Prefix: &prefix,
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("列举失败: %w", err)
		}
		objs = append(objs, page.Contents...)
	}
	return objs, nil
}

// deleteObjectsBatch 批量删除对象(DeleteObjects 每次最多 1000 个)。
// 部分 S3 兼容服务对批量删除接口支持不佳(500/NotImplemented 等),
// 失败时自动回退为逐对象 DeleteObject 组合(幂等),保证删除一定完成。
func deleteObjectsBatch(ctx context.Context, s3c *s3.Client, bucket string, keys []string) (int, error) {
	const batchSize = 1000
	count := 0
	for i := 0; i < len(keys); i += batchSize {
		end := i + batchSize
		if end > len(keys) {
			end = len(keys)
		}
		ids := make([]types.ObjectIdentifier, end-i)
		for j, k := range keys[i:end] {
			ids[j] = types.ObjectIdentifier{Key: aws.String(k)}
		}
		out, err := s3c.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: &bucket,
			Delete: &types.Delete{Objects: ids},
		})
		if err != nil {
			n, ferr := deleteKeysOneByOne(ctx, s3c, bucket, keys[i:])
			return count + n, ferr
		}
		if len(out.Errors) > 0 {
			n, ferr := deleteKeysOneByOne(ctx, s3c, bucket, keys[i:])
			return count + n, ferr
		}
		count += len(ids)
	}
	return count, nil
}

// deleteKeysOneByOne 逐对象删除(组合式回退,对已删除对象幂等)。
// 返回删除数量;失败即终止。
func deleteKeysOneByOne(ctx context.Context, s3c *s3.Client, bucket string, keys []string) (int, error) {
	count := 0
	for _, k := range keys {
		_, err := s3c.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: &bucket,
			Key:    &k,
		})
		if err != nil {
			return count, fmt.Errorf("删除 s3://%s/%s 失败: %w", bucket, k, err)
		}
		count++
	}
	return count, nil
}
