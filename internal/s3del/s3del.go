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
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	awshttp "github.com/aws/smithy-go/transport/http"
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
}

// probe 是「批量端点不可用」的探测结论。按 bucket 存在包级表里(而不是挂在
// Deleter 上):CLI 每次命令都会 New 一个 Deleter,挂在实例上的结论跨命令就
// 失效了 —— 网关不支持批量删除时,每次 rm 都要白撞一轮(含 SDK 重试)。
type probe struct {
	// unsupported 为真时跳过批量端点;until 是重新探测的放行时刻(UnixNano)。
	unsupported atomic.Bool
	until       atomic.Int64

	// 5xx 类失败的同签名连击计数:见 unsupportedBatchErr。
	mu           sync.Mutex
	lastFailSig  string
	lastFailSeen int
}

var probes sync.Map // bucket -> *probe

func probeFor(bucket string) *probe {
	if p, ok := probes.Load(bucket); ok {
		return p.(*probe)
	}
	p, _ := probes.LoadOrStore(bucket, &probe{})
	return p.(*probe)
}

// ResetProbes 清空进程级探测结论(测试用;生产代码不应调用)。
func ResetProbes() {
	probes.Range(func(k, _ any) bool {
		probes.Delete(k)
		return true
	})
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
	// 单键直删:批量端点对一个 key 没有任何收益(同样是一次请求),而在
	// 「不支持批量端点」的网关上还要多付一次注定失败的探测。多键删除仍优先
	// 批量端点,并顺带把探测结论缓存下来。
	if len(keys) == 1 {
		if _, err := d.client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(keys[0]),
		}); err != nil {
			return fmt.Errorf("s3del: 删除 %s 失败: %w", keys[0], err)
		}
		return nil
	}
	for i := 0; i < len(keys); i += batchSize {
		end := i + batchSize
		if end > len(keys) {
			end = len(keys)
		}
		if !batchUsable(bucket) {
			if err := d.deleteConcurrently(ctx, bucket, keys[i:end]); err != nil {
				return err
			}
			continue
		}
		if err := d.deleteBatch(ctx, bucket, keys[i:end]); err != nil {
			// 只有确证「批量端点不支持」才记结论并进入冷却期;瞬态失败
			// (抖动/超时/被取消)只让当前批回退单删,不污染后续批次。
			if unsupportedBatchErr(bucket, err) {
				p := probeFor(bucket)
				p.unsupported.Store(true)
				p.until.Store(time.Now().Add(ProbeTTL).UnixNano())
			}
			if derr := d.deleteConcurrently(ctx, bucket, keys[i:end]); derr != nil {
				return derr
			}
		}
	}
	return nil
}

// batchUsable 返回此刻是否可以尝试批量端点:从未失败过,或冷却期已过。
func batchUsable(bucket string) bool {
	p := probeFor(bucket)
	if !p.unsupported.Load() {
		return true
	}
	return time.Now().UnixNano() >= p.until.Load()
}

// unsupportedBatchErr 判定一次批量删除失败是否**确证**「端点不支持批量删除」。
// 只有确证才值得缓存结论并冷却 5 分钟;误判的代价是此后 5 分钟全部降级为
// 并发单删(放大后端压力),所以判据取严:
//   - 405/501 与 NotImplemented 类错误码:立即确证;
//   - 5xx:网关常用 500 代答「不支持」(实测某网关即如此),但单次 500 更可能
//     是抖动 —— 要求**同签名连续两次**才确证;
//   - 取消/超时:客户端已放弃,结论无意义,永不确证;
//   - 其余(含批量端点返回的逐 key 错误):不确证,当前批回退单删即可。
func unsupportedBatchErr(bucket string, err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "NotImplemented", "MethodNotAllowed", "XNotImplemented":
			return true
		}
	}
	var re *awshttp.ResponseError
	if errors.As(err, &re) {
		switch re.HTTPStatusCode() {
		case http.StatusNotImplemented, http.StatusMethodNotAllowed:
			return true
		case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			sig := fmt.Sprintf("%d", re.HTTPStatusCode())
			if ae != nil {
				sig += "/" + ae.ErrorCode()
			}
			p := probeFor(bucket)
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.lastFailSig == sig {
				p.lastFailSeen++
			} else {
				p.lastFailSig, p.lastFailSeen = sig, 1
			}
			return p.lastFailSeen >= 2
		}
	}
	return false
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
