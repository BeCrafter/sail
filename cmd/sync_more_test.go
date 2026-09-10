package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSyncNeed 覆盖同步决策核心:目标缺失 / 大小不同 / update / checksum 幂等 / mtime 容差。
// 非 checksum 路径不触网络,s3c 传 nil 即可。
func TestSyncNeed(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	se := syncEntry{size: 100, mtime: base}
	src := &syncPath{isS3: true, bucket: "b", keyBase: "src"}

	reset := func() {
		syncUpdate = false
		syncChecksum = false
	}
	reset()
	defer reset()

	// 目标不存在 -> 需要传输
	if need, err := syncNeed(ctx, nil, nil, src, src, "a.txt", se, syncEntry{}, false); err != nil || !need {
		t.Errorf("目标缺失应需要传输,got need=%v err=%v", need, err)
	}

	// 大小不同(默认):需要传输
	de := syncEntry{size: 200, mtime: base}
	if need, _ := syncNeed(ctx, nil, nil, src, src, "a.txt", se, de, true); !need {
		t.Errorf("大小不同应需要传输")
	}

	// 大小不同 + --update:源较新(>1s)才传输
	syncUpdate = true
	newer := syncEntry{size: 200, mtime: base.Add(5 * time.Second)}  // 目标更新
	older := syncEntry{size: 200, mtime: base.Add(-5 * time.Second)} // 目标更旧
	if need, _ := syncNeed(ctx, nil, nil, src, src, "a.txt", se, newer, true); need {
		t.Errorf("--update 目标较新应跳过")
	}
	if need, _ := syncNeed(ctx, nil, nil, src, src, "a.txt", se, older, true); !need {
		t.Errorf("--update 源较新应传输")
	}
	syncUpdate = false

	// 大小相同 + 默认:mtime 差 >1s 才传输(容差吸收 S3 秒级截断)
	sameSize := syncEntry{size: 100, mtime: base}
	if need, _ := syncNeed(ctx, nil, nil, src, src, "a.txt", se, sameSize, true); need {
		t.Errorf("mtime 相同应跳过(幂等)")
	}
	tinyDiff := syncEntry{size: 100, mtime: base.Add(500 * time.Millisecond)}
	if need, _ := syncNeed(ctx, nil, nil, src, src, "a.txt", se, tinyDiff, true); need {
		t.Errorf("mtime 差 ≤1s 应视为已同步而跳过")
	}
	bigDiff := syncEntry{size: 100, mtime: base.Add(-5 * time.Second)}
	if need, _ := syncNeed(ctx, nil, nil, src, src, "a.txt", se, bigDiff, true); !need {
		t.Errorf("源较目标新 >1s 应传输")
	}

	// --checksum 大小相同且 mtime 差 ≤1s:幂等快路径直接跳过(不触网络)
	syncChecksum = true
	if need, _ := syncNeed(ctx, nil, nil, src, src, "a.txt", se, sameSize, true); need {
		t.Errorf("checksum 且 mtime 差 ≤1s 应跳过(快路径)")
	}
	syncChecksum = false
}

// TestChecksumDifferFastPath 覆盖 checksum 快路径:两侧 ETag 均为单分片直接比较。
func TestChecksumDifferFastPath(t *testing.T) {
	ctx := context.Background()
	src := &syncPath{isS3: true, bucket: "a", keyBase: "s"}
	dst := &syncPath{isS3: true, bucket: "a", keyBase: "d"}

	// 相同 ETag -> 无差异
	diff, err := checksumDiffer(ctx, nil, nil, src, dst, "x",
		syncEntry{etag: "df31ab9d4881a1a91ab8be84e6186d6a"},
		syncEntry{etag: "df31ab9d4881a1a91ab8be84e6186d6a"})
	if err != nil || diff {
		t.Errorf("相同 ETag 应无差异,got diff=%v err=%v", diff, err)
	}

	// 不同 ETag -> 有差异
	diff, err = checksumDiffer(ctx, nil, nil, src, dst, "x",
		syncEntry{etag: "df31ab9d4881a1a91ab8be84e6186d6a"},
		syncEntry{etag: "11111111111111111111111111111111"})
	if err != nil || !diff {
		t.Errorf("不同 ETag 应有差异,got diff=%v err=%v", diff, err)
	}

	// ETag 大小写不敏感
	diff, err = checksumDiffer(ctx, nil, nil, src, dst, "x",
		syncEntry{etag: "DF31AB9D4881A1A91AB8BE84E6186D6A"},
		syncEntry{etag: "df31ab9d4881a1a91ab8be84e6186d6a"})
	if err != nil || diff {
		t.Errorf("ETag 应大小写不敏感比较")
	}
}

func TestNewSyncPathLocal(t *testing.T) {
	sp, err := newSyncPath(nil, "./mydir")
	if err != nil {
		t.Fatalf("newSyncPath 报错: %v", err)
	}
	if sp.isS3 || sp.localDir != "./mydir" {
		t.Errorf("本地分支解析错误: %+v", sp)
	}
}

func TestSyncPathURI(t *testing.T) {
	// s3 分支
	s3sp := &syncPath{isS3: true, bucket: "bucket", keyBase: "base"}
	if got := s3sp.uri("a/b.txt"); got != "s3://bucket/base/a/b.txt" {
		t.Errorf("s3 uri = %q,期望 s3://bucket/base/a/b.txt", got)
	}
	// keyBase 空
	root := &syncPath{isS3: true, bucket: "bucket"}
	if got := root.uri("a.txt"); got != "s3://bucket/a.txt" {
		t.Errorf("空 keyBase uri = %q", got)
	}
	// 本地分支
	local := &syncPath{localDir: "/tmp/d"}
	if got := local.uri("x/y.txt"); got != filepath.Join("/tmp/d", "x", "y.txt") {
		t.Errorf("local uri = %q", got)
	}
	if got := local.display("x"); got != local.uri("x") {
		t.Errorf("display 应等于 uri")
	}
}

// TestIndexLocal 校验本地目录索引:relKey 用 / 分隔、目录跳过。
func TestIndexLocal(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a/b.txt", "hello")
	write("c.log", "x")

	entries, err := indexLocal(dir)
	if err != nil {
		t.Fatalf("indexLocal 报错: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("索引条目 = %d,期望 2", len(entries))
	}
	e, ok := entries["a/b.txt"]
	if !ok {
		t.Fatalf("缺少 a/b.txt,got %v", entries)
	}
	if e.size != 5 {
		t.Errorf("a/b.txt size = %d,期望 5", e.size)
	}
	if _, ok := entries["c.log"]; !ok {
		t.Errorf("缺少 c.log")
	}
}
