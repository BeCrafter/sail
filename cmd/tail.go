package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/view"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
)

var (
	tailLines int64 = 10
	tailBytes int64
)

var tailCmd = &cobra.Command{
	Use:   "tail [-n N | --bytes N] <s3://bucket/key|本地路径>",
	Short: "查看对象/文件结尾内容",
	Long: `读取对象/文件结尾内容。s3 路径通过 Range 只取尾部窗口,不下载全量;
Range 不可用时(服务不支持/对象过小)自动退化为全量流式读取。
-n 显示最后 N 行(默认 10);--bytes 显示最后 N 字节;两者互斥。

示例:
  sail tail -n 50 s3://bucket/logs/app.log
  sail tail --bytes 4096 s3://bucket/data.bin`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		nChanged := cmd.Flags().Changed("lines")
		cChanged := cmd.Flags().Changed("bytes")
		if nChanged && cChanged {
			return fmt.Errorf("-n 与 --bytes 不能同时使用")
		}
		if tailLines < 0 || tailBytes < 0 {
			return fmt.Errorf("-n 与 --bytes 不能为负数")
		}
		ctx := context.Background()
		arg := args[0]
		if strings.HasPrefix(arg, "s3://") {
			return tailS3(ctx, arg, cChanged)
		}
		return tailLocal(ctx, arg, cChanged)
	},
}

func tailS3(ctx context.Context, arg string, byBytes bool) error {
	r, _, err := loadResolved()
	if err != nil {
		return err
	}
	s3c, err := client.New(ctx, r)
	if err != nil {
		return err
	}
	p, err := parseS3(arg, r)
	if err != nil {
		return err
	}
	if p.Key == "" {
		return fmt.Errorf("缺少 key,需指定 s3://bucket/key")
	}
	// 探测大小用于窗口估算;Head 失败/无 Content-Length 时大小未知,走全量路径
	size := int64(-1)
	if h, err := s3c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &p.Bucket, Key: &p.Key}); err == nil && h.ContentLength != nil {
		size = *h.ContentLength
	}
	srcArg := "s3://" + p.Bucket + "/" + p.Key
	openWindow := func(w int64) (io.ReadCloser, int64, error) {
		src, err := view.OpenSourceRange(ctx, srcArg, s3c, r.Bucket, fmt.Sprintf("bytes=-%d", w))
		if err != nil {
			return nil, 0, err
		}
		return src.Reader, src.Size, nil
	}
	openFull := func() (io.ReadCloser, error) {
		src, err := view.OpenSource(ctx, srcArg, s3c, r.Bucket)
		if err != nil {
			return nil, err
		}
		return src.Reader, nil
	}
	return tailRun(size, byBytes, openWindow, openFull)
}

func tailLocal(ctx context.Context, arg string, byBytes bool) error {
	size := int64(-1)
	if info, err := os.Stat(arg); err == nil {
		size = info.Size()
	}
	openWindow := func(w int64) (io.ReadCloser, int64, error) {
		src, err := view.OpenSourceRange(ctx, arg, nil, "", fmt.Sprintf("bytes=-%d", w))
		if err != nil {
			return nil, 0, err
		}
		return src.Reader, src.Size, nil
	}
	openFull := func() (io.ReadCloser, error) {
		src, err := view.OpenSource(ctx, arg, nil, "")
		if err != nil {
			return nil, err
		}
		return src.Reader, nil
	}
	return tailRun(size, byBytes, openWindow, openFull)
}

// tailRun 统一执行尾部读取:优先窗口读取,窗口失败/不足时扩展或退化为全量流式。
func tailRun(size int64, byBytes bool, openWindow func(int64) (io.ReadCloser, int64, error), openFull func() (io.ReadCloser, error)) error {
	if size == 0 {
		return nil // 空对象:无输出
	}
	if size > 0 {
		if byBytes {
			return tailBytesWindow(size, tailBytes, openWindow, openFull)
		}
		return tailLinesWindow(size, tailLines, openWindow, openFull)
	}
	// 大小未知:全量流式
	if byBytes {
		return tailBytesFull(tailBytes, openFull)
	}
	return tailLinesFull(tailLines, openFull)
}

// tailBytesWindow 读尾部窗口并输出最后 N 字节(窗口=min(N,size) 一次命中)。
func tailBytesWindow(size, n int64, openWindow func(int64) (io.ReadCloser, int64, error), openFull func() (io.ReadCloser, error)) error {
	if n <= 0 {
		return nil
	}
	window := n
	if window > size {
		window = size
	}
	r, got, err := openWindow(window)
	if err != nil {
		return tailBytesFull(n, openFull) // Range 不可用(如 416/服务不支持):全量
	}
	defer r.Close()
	buf, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("读取失败: %w", err)
	}
	// 服务忽略 Range 返回了全量(got/长度超窗口):输出最后 N 字节
	if got > window || int64(len(buf)) > window {
		return writeTailBytes(buf, n)
	}
	_, err = os.Stdout.Write(buf)
	return err
}

// tailLinesWindow 尾部窗口读行:行数不足时窗口翻倍重取,直至够 N 行或全量。
func tailLinesWindow(size, n int64, openWindow func(int64) (io.ReadCloser, int64, error), openFull func() (io.ReadCloser, error)) error {
	if n <= 0 {
		return nil
	}
	guess := n * 256
	if guess < 4096 {
		guess = 4096
	}
	window := size
	if guess < window {
		window = guess
	}
	for {
		r, got, err := openWindow(window)
		if err != nil {
			return tailLinesFull(n, openFull) // Range 不可用:全量
		}
		buf, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			return fmt.Errorf("读取失败: %w", err)
		}
		if got > window || int64(len(buf)) > window {
			return tailLinesFromBuffer(buf, n) // 服务忽略 Range,拿到的是全量
		}
		lines := splitLines(buf)
		// 严格大于才可直接输出:窗口起点可能截断了最旧的行,行数恰好等于 N 时
		// 无法保证最后一行的完整,须继续扩窗直至窗口为全量。
		if int64(len(lines)) > n {
			printLines(lines[int64(len(lines))-n:])
			return nil
		}
		if window >= size {
			printLines(lines)
			return nil
		}
		window *= 2
		if window > size {
			window = size
		}
	}
}

// splitLines 切分窗口内容为行(保留原文本,去除末尾空元素)。
func splitLines(buf []byte) []string {
	if len(buf) == 0 {
		return nil
	}
	lines := strings.Split(string(buf), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func printLines(lines []string) {
	for _, l := range lines {
		fmt.Println(l)
	}
}

// tailLinesFromBuffer 从全量缓冲取最后 n 行输出(服务忽略 Range 的降级路径)。
func tailLinesFromBuffer(buf []byte, n int64) error {
	lines := splitLines(buf)
	if int64(len(lines)) > n {
		lines = lines[int64(len(lines))-n:]
	}
	printLines(lines)
	return nil
}

// writeTailBytes 输出 buf 的最后 n 字节。
func writeTailBytes(buf []byte, n int64) error {
	if int64(len(buf)) > n {
		_, err := os.Stdout.Write(buf[int64(len(buf))-n:])
		return err
	}
	_, err := os.Stdout.Write(buf)
	return err
}

// tailLinesFull 全量流式环形缓冲,保留最后 n 行。
func tailLinesFull(n int64, openFull func() (io.ReadCloser, error)) error {
	if n <= 0 {
		return nil
	}
	r, err := openFull()
	if err != nil {
		return err
	}
	defer r.Close()
	ring := make([]string, int(n))
	br := bufio.NewReader(r)
	idx := 0
	filled := 0
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			ring[idx%len(ring)] = line
			idx++
			if filled < len(ring) {
				filled++
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("读取失败: %w", err)
		}
	}
	start := idx - filled
	for i := 0; i < filled; i++ {
		fmt.Print(ring[(start+i)%len(ring)])
	}
	return nil
}

// tailBytesFull 全量流式环形缓冲,保留最后 n 字节。
func tailBytesFull(n int64, openFull func() (io.ReadCloser, error)) error {
	if n <= 0 {
		return nil
	}
	r, err := openFull()
	if err != nil {
		return err
	}
	defer r.Close()
	buf := make([]byte, int(n))
	idx := 0
	filled := 0
	tmp := make([]byte, 64*1024)
	for {
		rn, err := r.Read(tmp)
		for i := 0; i < rn; i++ {
			buf[idx%len(buf)] = tmp[i]
			idx++
			if filled < len(buf) {
				filled++
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("读取失败: %w", err)
		}
	}
	out := make([]byte, 0, filled)
	if filled == len(buf) {
		p := idx % len(buf)
		out = append(out, buf[p:]...)
		out = append(out, buf[:p]...)
	} else {
		out = append(out, buf[:filled]...)
	}
	_, err = os.Stdout.Write(out)
	return err
}

func init() {
	tailCmd.Flags().Int64VarP(&tailLines, "lines", "n", 10, "显示最后 N 行")
	tailCmd.Flags().Int64Var(&tailBytes, "bytes", 0, "显示最后 N 字节")
}
