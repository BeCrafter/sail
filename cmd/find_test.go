package cmd

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TestParseSizeSpec(t *testing.T) {
	cases := []struct {
		in      string
		wantOp  int64
		wantN   int64
		wantErr bool
	}{
		{in: "", wantOp: 0, wantN: 0},
		{in: "+1M", wantOp: '+', wantN: 1024 * 1024},
		{in: "-500K", wantOp: '-', wantN: 500 * 1024},
		{in: "1024", wantOp: '=', wantN: 1024},
		{in: "512B", wantOp: '=', wantN: 512},
		{in: "2g", wantOp: '=', wantN: 2 * 1024 * 1024 * 1024},
		{in: "+1.5M", wantErr: true},
		{in: "+", wantErr: true},
		{in: "10X", wantErr: true},
	}
	for _, c := range cases {
		got, err := parseSizeSpec(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseSizeSpec(%q) 期望报错,实际返回 %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSizeSpec(%q) 报错: %v", c.in, err)
			continue
		}
		if c.in == "" {
			if got != nil {
				t.Errorf("parseSizeSpec(\"\") 期望 nil,实际 %v", got)
			}
			continue
		}
		if got[0] != c.wantOp || got[1] != c.wantN {
			t.Errorf("parseSizeSpec(%q) = [%d,%d],期望 [%d,%d]", c.in, got[0], got[1], c.wantOp, c.wantN)
		}
	}
}

func TestParseTimeArg(t *testing.T) {
	t1, err := parseTimeArg("2026-01-02")
	if err != nil {
		t.Fatalf("parseTimeArg(2026-01-02) 报错: %v", err)
	}
	want := time.Date(2026, 1, 2, 0, 0, 0, 0, time.Local)
	if !t1.Equal(want) {
		t.Errorf("parseTimeArg(2026-01-02) = %v,期望 %v", t1, want)
	}
	t2, err := parseTimeArg("2026-01-02 15:04:05")
	if err != nil {
		t.Fatalf("parseTimeArg 带时刻报错: %v", err)
	}
	want2 := time.Date(2026, 1, 2, 15, 4, 5, 0, time.Local)
	if !t2.Equal(want2) {
		t.Errorf("parseTimeArg 带时刻 = %v,期望 %v", t2, want2)
	}
	if _, err := parseTimeArg("2026/01/02"); err == nil {
		t.Errorf("parseTimeArg(2026/01/02) 期望报错")
	}
}

func strPtr(s string) *string { return &s }

func sizePtr(n int64) *int64 { return &n }

func timePtr(t time.Time) *time.Time { return &t }

// findObj 构造一个 types.Object 用于 matchFind。
func findObj(size int64, mod time.Time) types.Object {
	o := types.Object{Key: strPtr("unused")}
	if size >= 0 {
		o.Size = sizePtr(size)
	}
	if !mod.IsZero() {
		o.LastModified = timePtr(mod)
	}
	return o
}

// TestMatchFind 校验 find 四类过滤的组合匹配。
func TestMatchFind(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	old := findObj(100, base.Add(-24*time.Hour)) // 对象本身尺寸/时间
	new := findObj(500, base.Add(24*time.Hour))

	reset := func() {
		findMaxDepth = 0
		findNames = nil
		findSize = ""
		findNewer = ""
	}
	reset()
	defer reset()

	t.Run("无过滤全命中", func(t *testing.T) {
		if !matchFind(old, "logs/a.log", "", nil, time.Time{}) {
			t.Errorf("无过滤应命中")
		}
	})

	t.Run("name 通配按 basename", func(t *testing.T) {
		findNames = []string{"*.log"}
		defer reset()
		if !matchFind(old, "logs/a.log", "", nil, time.Time{}) {
			t.Errorf("*.log 应命中 a.log")
		}
		if matchFind(old, "logs/a.txt", "", nil, time.Time{}) {
			t.Errorf("*.log 不应命中 a.txt")
		}
	})

	t.Run("size 精确/大于/小于", func(t *testing.T) {
		cases := []struct {
			spec string
			obj  types.Object
			want bool
		}{
			{"100", old, true},
			{"101", old, false},
			{"+200", new, true},
			{"+200", old, false},
			{"-200", old, true},
			{"-200", new, false},
		}
		for _, c := range cases {
			spec, err := parseSizeSpec(c.spec)
			if err != nil {
				t.Fatalf("parseSizeSpec(%q): %v", c.spec, err)
			}
			if got := matchFind(c.obj, "a.log", "", spec, time.Time{}); got != c.want {
				t.Errorf("size %q 对 obj(size=%d) = %v,期望 %v", c.spec, *c.obj.Size, got, c.want)
			}
		}
	})

	t.Run("size 过滤中 Size 为 nil 视为 0", func(t *testing.T) {
		noSize := types.Object{Key: strPtr("x")}
		if !matchFind(noSize, "x", "", []int64{'=', 0}, time.Time{}) {
			t.Errorf("Size nil 应视为 0,=0 应命中")
		}
	})

	t.Run("newer 过滤", func(t *testing.T) {
		if !matchFind(new, "a.log", "", nil, base) {
			t.Errorf("对象晚于 newer 应命中")
		}
		if matchFind(old, "a.log", "", nil, base) {
			t.Errorf("对象早于 newer 应被拒")
		}
		noMod := types.Object{Key: strPtr("x")} // LastModified nil
		if matchFind(noMod, "x", "", nil, base) {
			t.Errorf("LastModified nil 且 newer 非零应被拒")
		}
	})

	t.Run("max-depth 过滤", func(t *testing.T) {
		findMaxDepth = 2
		defer reset()
		// base="logs",rel 层级数
		obj := findObj(1, base)
		if matchFind(obj, "logs/a/b/c.txt", "logs", nil, time.Time{}) {
			t.Errorf("深度 ≥2 应被拒")
		}
		if !matchFind(obj, "logs/a.txt", "logs", nil, time.Time{}) {
			t.Errorf("深度 0 应命中")
		}
	})
}
