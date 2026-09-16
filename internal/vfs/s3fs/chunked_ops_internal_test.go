package s3fs

import (
	"context"
	"testing"
	"time"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/fakes3"
)

func newChunkedFSForInternal(t *testing.T, prefix string, chunk int64) (*FS, *fakes3.Server) {
	t.Helper()
	srv := fakes3.New()
	t.Cleanup(srv.Close)
	s3c, err := client.New(context.Background(), &config.Resolved{
		Endpoint:  srv.URL(),
		AccessKey: "ak",
		SecretKey: "sk",
		Region:    "us-east-1",
		PathStyle: true,
	})
	if err != nil {
		t.Fatalf("构造 S3 client 失败: %v", err)
	}
	fs, err := New(Config{
		Client:        s3c,
		Bucket:        "b",
		Prefix:        prefix,
		StagingDir:    t.TempDir(),
		ChunkedUpload: true,
		ChunkSize:     chunk,
	})
	if err != nil {
		t.Fatalf("构造 s3fs 失败: %v", err)
	}
	return fs, srv
}

// 片复制必须是并发的:串行实现的总耗时是片数 × 单次延迟,并发后接近单次。
func TestCopyPartsIsConcurrent(t *testing.T) {
	const chunk = 5 << 20
	fs, srv := newChunkedFSForInternal(t, "", chunk)
	ctx := context.Background()
	srv.CopyDelay.Store(int64(150 * time.Millisecond))

	parts := make([]manifestChunk, objectOpsConcurrency)
	version := "v1"
	for i := range parts {
		parts[i] = manifestChunk{Length: chunk}
		srv.Put("b", fs.partKey("/big.bin", version, i), []byte("chunk"), "application/octet-stream")
	}

	start := time.Now()
	if _, err := fs.copyParts(ctx, "/big.bin", "/moved.bin", parts, version); err != nil {
		t.Fatalf("copyParts 失败: %v", err)
	}
	elapsed := time.Since(start)

	if serial := time.Duration(len(parts)) * 150 * time.Millisecond; elapsed >= serial {
		t.Fatalf("复制片似乎是串行的:%d 片耗时 %v,串行下限 %v", len(parts), elapsed, serial)
	}
}
