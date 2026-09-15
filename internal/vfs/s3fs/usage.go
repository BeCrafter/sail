// usage.go 提供按前缀的物理字节用量统计,供配额会计(quotafs)消费。
// 仅新增能力,不改变本包既有方法的语义。
package s3fs

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Usage 按物理字节统计本核前缀下的全部对象:ListObjectsV2 分页求和,
// 含目录标记、.sail/ 分片部件与 manifest——与桶内真实字节一致,即账单口径。
// 前缀为空(桶根)时统计整桶。失败返回错误,由调用方决定降级策略。
func (f *FS) Usage(ctx context.Context) (int64, error) {
	var total int64
	paginator := s3.NewListObjectsV2Paginator(f.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(f.bucket),
		Prefix: aws.String(f.prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return 0, fmt.Errorf("s3fs: 统计前缀 %q 用量失败: %w", f.prefix, err)
		}
		for _, obj := range page.Contents {
			if obj.Size != nil {
				total += *obj.Size
			}
		}
	}
	return total, nil
}
