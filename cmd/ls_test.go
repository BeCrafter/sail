package cmd

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TestObjSize(t *testing.T) {
	var o types.Object
	if got := objSize(o); got != 0 {
		t.Errorf("Size nil 应返回 0,got %d", got)
	}
	n := int64(42)
	o.Size = &n
	if got := objSize(o); got != 42 {
		t.Errorf("Size=42 应返回 42,got %d", got)
	}
}

func TestObjTime(t *testing.T) {
	var o types.Object
	if got := objTime(o); !got.IsZero() {
		t.Errorf("LastModified nil 应返回零值,got %v", got)
	}
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	o.LastModified = &ts
	if got := objTime(o); !got.Equal(ts) {
		t.Errorf("objTime = %v,期望 %v", got, ts)
	}
}
