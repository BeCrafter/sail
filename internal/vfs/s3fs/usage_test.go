package s3fs_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

// Usage 是配额会计的物理字节口径:分页求和本核前缀下的全部对象,
// 与桶内真实字节一致。
func TestUsageSumsPhysicalBytesUnderPrefix(t *testing.T) {
	f, srv := newFS(t, "users/alice", 0)

	for _, name := range []string{"a.txt", "b.txt", "dir/c.txt"} {
		w, err := f.OpenWrite(context.Background(), "/"+name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(w, strings.NewReader(name)); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Commit(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}

	want := int64(0)
	for _, k := range srv.Keys(testBucket) {
		if strings.HasPrefix(k, "users/alice") {
			if obj, ok := srv.Get(testBucket, k); ok {
				want += int64(len(obj.Data))
			}
		}
	}
	got, err := f.Usage(context.Background())
	if err != nil {
		t.Fatalf("Usage 失败: %v", err)
	}
	if got != want {
		t.Errorf("Usage = %d, 桶内物理字节 = %d", got, want)
	}
	if want != int64(len("a.txt")+len("b.txt")+len("dir/c.txt")) {
		t.Errorf("前置校验:桶内应恰有 3 个对象的内容,实际 %d 字节", want)
	}
}

// 场景(P2-10 分片口径):分片文件按物理字节计入部件与 manifest,
// Usage 与逻辑大小解耦,与桶内真实字节一致。
func TestUsageCountsChunkPartsAndManifest(t *testing.T) {
	f, srv := newChunkedFSPrefixed(t, "", testChunkSize)

	payload := bytes.Repeat([]byte{0xA5}, threeChunkSize) // 15MiB = 3 片
	w, err := f.OpenWrite(context.Background(), "/big.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(w, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	physical := int64(0)
	for _, k := range srv.Keys(testBucket) {
		if strings.HasPrefix(k, ".sail/") || k == "big.bin" {
			if obj, ok := srv.Get(testBucket, k); ok {
				physical += int64(len(obj.Data))
			}
		}
	}
	got, err := f.Usage(context.Background())
	if err != nil {
		t.Fatalf("Usage 失败: %v", err)
	}
	if got != physical {
		t.Errorf("Usage = %d, 桶内物理字节(部件+manifest+逻辑 key)= %d", got, physical)
	}
	if got <= int64(len(payload)) {
		t.Errorf("分片存储的物理字节应含部件与 manifest(> 逻辑 %d),实际 %d", len(payload), got)
	}
}
