// Package s3del 是 S3 对象删除的统一入口:批量端点优先,不支持的网关自动
// 降级为**并发**单删,并把探测结论缓存下来避免重复试错。
//
// 为什么需要它:部分 S3 兼容服务的 DeleteObjects(批量端点)不可用,而单对象
// DELETE 的延迟可能高得离谱且是服务端处理耗时而非带宽——实测某网关单次删除
// 固定 ~28s(连不存在的 key 也一样)。串行回退会让 N 个对象的删除退化成
// N×28s;并发化后实测 6 个对象从 163s 降到 27s。
package s3del

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const (
	// batchSize 是 DeleteObjects 的单批上限(S3 协议硬上限 1000)。
	batchSize = 1000
	// concurrency 是单删回退的并发上限。
	//
	// 为什么定得高:单次删除的延迟可能是服务端处理耗时(实测某网关固定 ~28s),
	// 此时总耗时 ≈ ⌈对象数/并发度⌉ × 单次延迟。并发度 10 时,100 个对象要跑
	// 11 轮 ≈ 5 分钟;并发度 128 时一轮就跑完(实测该网关 100 路并发无衰减)。
	// 对象数少于并发度时按对象数开 worker,不会平白占用连接。
	concurrency = 128
)

// ProbeTTL 是「批量端点不可用」结论的缓存时长:到期后允许再试一次,网关修复
// 后无需重启进程即可自愈。是变量而非常量,以便测试缩短冷却期;生产代码不应
// 修改它。
var ProbeTTL = 5 * time.Minute

// Deleter 用某个 S3 客户端删除对象。零值不可用,请用 New 构造。
// 可安全并发使用。
type Deleter struct {
	client *s3.Client
	bucket string

	// batchUnsupported 记住「批量端点不可用」的探测结论,避免每次删除都白撞
	// 一次(该结论稳定,不该按请求重复付出)。probeAt 是重新探测的放行时刻
	// (UnixNano),到期后允许再试一次。
	batchUnsupported atomic.Bool
	probeAt          atomic.Int64
}

// New 构造删除器。bucket 为空时每次调用需自行给出(见 DeleteKeysIn)。
func New(client *s3.Client, bucket string) *Deleter {
	return &Deleter{client: client, bucket: bucket}
}

// DeleteKeys 删除一批 key(可含不存在的 key,幂等)。任一 key 失败即返回错误,
// 但会等其余 key 跑完——部分完成的中间态与批量端点「部分成功」的语义一致。
func (d *Deleter) DeleteKeys(ctx context.Context, keys []string) error {
	return d.DeleteKeysIn(ctx, d.bucket, keys)
}

// DeleteKeysIn 同 DeleteKeys,但显式指定 bucket(CLI 可能跨 bucket 操作)。
func (d *Deleter) DeleteKeysIn(ctx context.Context, bucket string, keys []string) error {
	for i := 0; i < len(keys); i += batchSize {
		end := i + batchSize
		if end > len(keys) {
			end = len(keys)
		}
		if !d.batchUsable() {
			if err := d.deleteConcurrently(ctx, bucket, keys[i:end]); err != nil {
				return err
			}
			continue
		}
		if err := d.deleteBatch(ctx, bucket, keys[i:end]); err != nil {
			// 批量端点不支持:记下结论并进入冷却期,后续批次不再白撞。
			d.batchUnsupported.Store(true)
			d.probeAt.Store(time.Now().Add(ProbeTTL).UnixNano())
			if derr := d.deleteConcurrently(ctx, bucket, keys[i:end]); derr != nil {
				return derr
			}
		}
	}
	return nil
}

// batchUsable 返回此刻是否可以尝试批量端点:从未失败过,或冷却期已过。
func (d *Deleter) batchUsable() bool {
	if !d.batchUnsupported.Load() {
		return true
	}
	return time.Now().UnixNano() >= d.probeAt.Load()
}

func (d *Deleter) deleteBatch(ctx context.Context, bucket string, keys []string) error {
	ids := make([]types.ObjectIdentifier, 0, len(keys))
	for _, k := range keys {
		ids = append(ids, types.ObjectIdentifier{Key: aws.String(k)})
	}
	out, err := d.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(bucket),
		Delete: &types.Delete{Objects: ids},
	})
	if err != nil {
		return err
	}
	if len(out.Errors) > 0 {
		return fmt.Errorf("s3del: 批量删除返回 %d 个错误: %s", len(out.Errors), aws.ToString(out.Errors[0].Message))
	}
	return nil
}

// deleteConcurrently 以 concurrency 为上限并发单删。
//
// 用工作池而非信号量:信号量下第 N+1 个任务要等整批跑完才启动。当单次删除
// 延迟高达数十秒时,这一等就是完整的第二轮——实测 11 个 key、并发度 10 时,
// 信号量模式 54s,工作池 27s(剩余任务在前一个完成时立刻补位)。
func (d *Deleter) deleteConcurrently(ctx context.Context, bucket string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	jobs := make(chan string, len(keys))
	for _, k := range keys {
		jobs <- k
	}
	close(jobs)

	errCh := make(chan error, len(keys))
	var wg sync.WaitGroup
	workers := min(concurrency, len(keys))
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range jobs {
				if _, err := d.client.DeleteObject(ctx, &s3.DeleteObjectInput{
					Bucket: aws.String(bucket),
					Key:    aws.String(k),
				}); err != nil {
					errCh <- fmt.Errorf("s3del: 删除 %s 失败: %w", k, err)
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}
