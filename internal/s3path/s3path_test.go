package s3path

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantBucket string
		wantKey    string
		wantErr    bool
	}{
		{name: "空串报错", in: "", wantErr: true},
		{name: "无 s3:// 前缀报错", in: "bucket/key", wantErr: true},
		{name: "仅前缀无 bucket 报错", in: "s3://", wantErr: true},
		{name: "仅 bucket(key 空)", in: "s3://bucket", wantBucket: "bucket"},
		{name: "bucket + key", in: "s3://bucket/key", wantBucket: "bucket", wantKey: "key"},
		{name: "多级 key", in: "s3://bucket/a/b/c.txt", wantBucket: "bucket", wantKey: "a/b/c.txt"},
		{name: "尾斜杠 key 为空", in: "s3://bucket/", wantBucket: "bucket", wantKey: ""},
		{name: "bucket 内含连字符", in: "s3://my-bucket.1/x", wantBucket: "my-bucket.1", wantKey: "x"},
		{name: "空 key 段保留点", in: "s3://b//c", wantBucket: "b", wantKey: "/c"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := Parse(c.in)
			if c.wantErr {
				if err == nil {
					t.Errorf("Parse(%q) 期望报错,实际 %v", c.in, p)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) 报错: %v", c.in, err)
			}
			if p.Bucket != c.wantBucket || p.Key != c.wantKey {
				t.Errorf("Parse(%q) = {%q,%q},期望 {%q,%q}", c.in, p.Bucket, p.Key, c.wantBucket, c.wantKey)
			}
		})
	}
}

func TestFormat(t *testing.T) {
	cases := []struct {
		name string
		in   S3Path
		want string
	}{
		{name: "仅 bucket", in: S3Path{Bucket: "bucket"}, want: "s3://bucket"},
		{name: "bucket + key", in: S3Path{Bucket: "bucket", Key: "a/b.txt"}, want: "s3://bucket/a/b.txt"},
		{name: "空 key 段", in: S3Path{Bucket: "b", Key: "/c"}, want: "s3://b//c"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.in.Format(); got != c.want {
				t.Errorf("(%v).Format() = %q,期望 %q", c.in, got, c.want)
			}
		})
	}
}

// TestParseFormatRoundTrip 校验 Parse 与 Format 往返一致。
func TestParseFormatRoundTrip(t *testing.T) {
	for _, s := range []string{"s3://bucket", "s3://bucket/key", "s3://bucket/a/b/c.txt"} {
		p, err := Parse(s)
		if err != nil {
			t.Fatalf("Parse(%q) 报错: %v", s, err)
		}
		if got := p.Format(); got != s {
			t.Errorf("Parse(%q).Format() = %q,往返不一致", s, got)
		}
	}
}

func TestBaseName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"a", "a"},
		{"a/b/c", "c"},
		{"a/b/", ""}, // 尾斜杠:末段为空
		{"/a", "a"},  // 前导斜杠
		{"a.txt", "a.txt"},
	}
	for _, c := range cases {
		if got := BaseName(c.in); got != c.want {
			t.Errorf("BaseName(%q) = %q,期望 %q", c.in, got, c.want)
		}
	}
}

func TestJoinKey(t *testing.T) {
	cases := []struct {
		base, name string
		want       string
	}{
		{"", "b", "b"},            // base 空:直接用 name
		{"a", "b", "a/b"},         // base 无尾斜杠:补 /
		{"a/", "b", "a/b"},        // base 已带尾斜杠:不重复
		{"a/b", "c/d", "a/b/c/d"}, // name 可含多级
		{"a/", "", "a/"},          // name 空:保留 base
	}
	for _, c := range cases {
		if got := JoinKey(c.base, c.name); got != c.want {
			t.Errorf("JoinKey(%q, %q) = %q,期望 %q", c.base, c.name, got, c.want)
		}
	}
}
