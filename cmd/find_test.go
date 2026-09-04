package cmd

import (
	"testing"
	"time"
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
