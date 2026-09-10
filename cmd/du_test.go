package cmd

import "testing"

func TestDisplayRoot(t *testing.T) {
	cases := []struct {
		bucket, base string
		want         string
	}{
		{"bucket", "", "s3://bucket"},
		{"bucket", "logs", "s3://bucket/logs"},
		{"bucket", "logs/2026", "s3://bucket/logs/2026"},
	}
	for _, c := range cases {
		if got := displayRoot(c.bucket, c.base); got != c.want {
			t.Errorf("displayRoot(%q,%q) = %q,期望 %q", c.bucket, c.base, got, c.want)
		}
	}
}
