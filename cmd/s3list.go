package cmd

import (
	"context"
	"fmt"

	"github.com/BeCrafter/sail/internal/i18n"
	"github.com/BeCrafter/sail/internal/s3del"
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
			return nil, fmt.Errorf(i18n.T("list failed: %w"), err)
		}
		objs = append(objs, page.Contents...)
	}
	return objs, nil
}

// deleteObjectsBatch 批量删除对象,返回删除数量。
// 走 internal/s3del:批量端点优先,网关不支持时并发单删回退——串行回退会让
// 删除退化成「对象数 × 单次延迟」(实测某网关单次删除固定 ~28s)。
func deleteObjectsBatch(ctx context.Context, s3c *s3.Client, bucket string, keys []string) (int, error) {
	if err := s3del.New(s3c, bucket).DeleteKeys(ctx, keys); err != nil {
		return 0, fmt.Errorf(i18n.T("delete failed: %w"), err)
	}
	return len(keys), nil
}
