package cmd

import "testing"

func TestBuildCDNURL(t *testing.T) {
	cases := []struct {
		name         string
		domain       string
		bucket       string
		key          string
		want         string
		shouldDedupe bool
	}{
		{
			name:   "域名不含 bucket,追加到路径",
			domain: "https://cdn.example.com",
			bucket: "mybucket",
			key:    "a/b.jpg",
			want:   "https://cdn.example.com/mybucket/a/b.jpg",
		},
		{
			name:   "域名路径已含 bucket,不重复拼接",
			domain: "https://cdn.example.com/mybucket",
			bucket: "mybucket",
			key:    "a/b.jpg",
			want:   "https://cdn.example.com/mybucket/a/b.jpg",
		},
		{
			name:   "域名路径含 bucket 且带尾斜杠",
			domain: "https://cdn.example.com/mybucket/",
			bucket: "mybucket",
			key:    "a/b.jpg",
			want:   "https://cdn.example.com/mybucket/a/b.jpg",
		},
		{
			name:   "virtual-hosted 子域含 bucket(不做子域推断,仍追加,保持 path 含 bucket)",
			domain: "https://mybucket.cdn.example.com",
			bucket: "mybucket",
			key:    "a/b.jpg",
			want:   "https://mybucket.cdn.example.com/mybucket/a/b.jpg",
		},
		{
			name:   "bucket 与域名首标签同名不作为已含(假阳性防护)",
			domain: "https://cdn.example.com",
			bucket: "cdn",
			key:    "a.jpg",
			want:   "https://cdn.example.com/cdn/a.jpg",
		},
		{
			name:   "bucket 是域名首标签子串不作为已含",
			domain: "https://data.example.com",
			bucket: "a",
			key:    "a.jpg",
			want:   "https://data.example.com/a/a.jpg",
		},
		{
			name:   "无 scheme 且不含 bucket",
			domain: "cdn.example.com",
			bucket: "mybucket",
			key:    "a/b.jpg",
			want:   "cdn.example.com/mybucket/a/b.jpg",
		},
		{
			name:   "无 scheme 且路径含 bucket",
			domain: "cdn.example.com/mybucket",
			bucket: "mybucket",
			key:    "a/b.jpg",
			want:   "cdn.example.com/mybucket/a/b.jpg",
		},
		{
			name:   "路径段等于 bucket 的子串不匹配(保守,仍追加)",
			domain: "https://cdn.example.com/mybucket-backup",
			bucket: "mybucket",
			key:    "a.jpg",
			want:   "https://cdn.example.com/mybucket-backup/mybucket/a.jpg",
		},
		{
			name:   "多重尾斜杠被清理",
			domain: "https://cdn.example.com//",
			bucket: "mybucket",
			key:    "a.jpg",
			want:   "https://cdn.example.com/mybucket/a.jpg",
		},
		{
			name:   "bucket 位于非首个路径段仍识别为已含",
			domain: "https://cdn.example.com/assets/mybucket",
			bucket: "mybucket",
			key:    "a.jpg",
			want:   "https://cdn.example.com/assets/mybucket/a.jpg",
		},
		{
			name:   "bucket 为空则始终不追加",
			domain: "https://cdn.example.com",
			bucket: "",
			key:    "a.jpg",
			want:   "https://cdn.example.com/a.jpg",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildCDNURL(c.domain, c.bucket, c.key, nil)
			if got != c.want {
				t.Errorf("buildCDNURL(%q,%q,%q) = %q, want %q", c.domain, c.bucket, c.key, got, c.want)
			}
		})
	}
}

func ptr(b bool) *bool { return &b }

func TestBuildCDNURLExplicit(t *testing.T) {
	cases := []struct {
		name         string
		domain       string
		bucket       string
		key          string
		bucketInPath *bool
		want         string
	}{
		{
			name:         "显式:域名已含 bucket,不追加",
			domain:       "https://cdn.example.com/mybucket",
			bucket:       "mybucket",
			key:          "a.jpg",
			bucketInPath: ptr(true),
			want:         "https://cdn.example.com/mybucket/a.jpg",
		},
		{
			name:         "显式:域名未含 bucket,仍追加(强制,跳过自动检测)",
			domain:       "https://cdn.example.com",
			bucket:       "mybucket",
			key:          "a.jpg",
			bucketInPath: ptr(false),
			want:         "https://cdn.example.com/mybucket/a.jpg",
		},
		{
			name:         "显式 true 覆盖:域名其实不含 bucket,但声明已含,直接拼 key",
			domain:       "https://cdn.example.com",
			bucket:       "mybucket",
			key:          "a.jpg",
			bucketInPath: ptr(true),
			want:         "https://cdn.example.com/a.jpg",
		},
		{
			name:         "显式 false 且域名已含 bucket,强制重复(用户声明错误,按声明走)",
			domain:       "https://cdn.example.com/mybucket",
			bucket:       "mybucket",
			key:          "a.jpg",
			bucketInPath: ptr(false),
			want:         "https://cdn.example.com/mybucket/mybucket/a.jpg",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildCDNURL(c.domain, c.bucket, c.key, c.bucketInPath)
			if got != c.want {
				t.Errorf("buildCDNURL(%q,%q,%q,%v) = %q, want %q", c.domain, c.bucket, c.key, *c.bucketInPath, got, c.want)
			}
		})
	}
}

func TestCDNAlreadyHasBucket(t *testing.T) {
	cases := []struct {
		domain string
		bucket string
		want   bool
	}{
		{"https://cdn.example.com", "mybucket", false},
		{"https://cdn.example.com/mybucket", "mybucket", true},
		{"https://cdn.example.com/a/mybucket", "mybucket", true},
		// 子域含 bucket 不做推断(避免与域名首标签同名造成假阳性)
		{"https://mybucket.cdn.example.com", "mybucket", false},
		{"https://cdn.example.com/mybucket-backup", "mybucket", false},
		// bucket 与域名首标签同名不作为已含
		{"https://cdn.example.com", "cdn", false},
		{"cdn.example.com", "mybucket", false},
		{"", "mybucket", false},
		{"https://cdn.example.com", "", false},
	}
	for _, c := range cases {
		if got := cdnAlreadyHasBucket(c.domain, c.bucket); got != c.want {
			t.Errorf("cdnAlreadyHasBucket(%q,%q) = %v, want %v", c.domain, c.bucket, got, c.want)
		}
	}
}
