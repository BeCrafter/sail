package cmd

import "testing"

func TestNewHasher(t *testing.T) {
	md5h, err := newHasher("md5")
	if err != nil || md5h == nil {
		t.Errorf("md5 应可用,err=%v", err)
	}
	sha, err := newHasher("sha256")
	if err != nil || sha == nil {
		t.Errorf("sha256 应可用,err=%v", err)
	}
	if _, err := newHasher("crc32"); err == nil {
		t.Errorf("非法算法应报错")
	}
	if _, err := newHasher(""); err == nil {
		t.Errorf("空算法应报错")
	}
}
