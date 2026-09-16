package config

import (
	"strings"
	"testing"
)

func TestEffectivePrefix(t *testing.T) {
	cases := []struct{ base, rel, want string }{
		{"team", "alice", "team/alice"},
		{"team/", "/alice/", "team/alice"},
		{"team", "", "team"},
		{"", "alice", "alice"},
		{"", "", ""},
		{"/team/", "a/b/", "team/a/b"},
	}
	for _, c := range cases {
		if got := EffectivePrefix(c.base, c.rel); got != c.want {
			t.Errorf("EffectivePrefix(%q, %q) = %q, want %q", c.base, c.rel, got, c.want)
		}
	}
}

// TestValidateUsers 覆盖用户表校验:必填项、重复、前缀合法性与 I6 嵌套/相等。
func TestValidateUsers(t *testing.T) {
	ok := []UserConfig{
		{Name: "alice", Password: "p1", Prefix: "alice/"},
		{Name: "bob", Password: "p2", Prefix: "shared/bob-data/"},
		{Name: "carol", Password: "p3", Prefix: "docs/carol-2026/"},
	}
	if err := ValidateUsers("team", ok); err != nil {
		t.Fatalf("合法表被拒绝: %v", err)
	}
	// 单用户省略前缀 = base 本身,无同表冲突时合法。
	if err := ValidateUsers("team", []UserConfig{{Name: "root", Password: "p"}}); err != nil {
		t.Fatalf("单用户省略前缀被拒绝: %v", err)
	}

	cases := []struct {
		name  string
		base  string
		users []UserConfig
		want  string // 期望错误信息包含的片段
	}{
		{"缺名字", "team", []UserConfig{{Password: "p"}}, "name is required"},
		{"名字含冒号", "team", []UserConfig{{Name: "a:b", Password: "p"}}, `must not contain ":"`},
		{"名字重复", "team", []UserConfig{{Name: "alice", Password: "p"}, {Name: "alice", Password: "q"}}, "duplicate name"},
		{"缺密码", "team", []UserConfig{{Name: "alice"}}, "password is required"},
		{"前缀绝对路径", "team", []UserConfig{{Name: "alice", Password: "p", Prefix: "/abs"}}, "must be relative"},
		{"前缀含 ..", "team", []UserConfig{{Name: "alice", Password: "p", Prefix: "../x"}}, `".."`},
		{"前缀相等", "team", []UserConfig{{Name: "alice", Password: "p", Prefix: "a"}, {Name: "bob", Password: "p", Prefix: "a/"}}, "same prefix"},
		{"与 base 相等", "team", []UserConfig{{Name: "alice", Password: "p"}, {Name: "bob", Password: "p", Prefix: ""}}, "same prefix"},
		{"嵌套(后者在前者内)", "team", []UserConfig{{Name: "alice", Password: "p", Prefix: "a"}, {Name: "bob", Password: "p", Prefix: "a/b"}}, "nests inside"},
		{"嵌套(桶根在前者外)", "", []UserConfig{{Name: "alice", Password: "p"}, {Name: "bob", Password: "p", Prefix: "b"}}, "nests inside"},
		{"quota 非法", "team", []UserConfig{{Name: "alice", Password: "p", Quota: "10XB"}}, "invalid quota"},
		{"quota 零", "team", []UserConfig{{Name: "alice", Password: "p", Quota: "0"}}, "invalid quota"},
		{"quota 负值不支持", "team", []UserConfig{{Name: "alice", Password: "p", Quota: "-5GB"}}, "invalid quota"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateUsers(c.base, c.users)
			if err == nil {
				t.Fatalf("期望被拒绝(含 %q),实际通过", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误信息 %q 不含 %q", err.Error(), c.want)
			}
		})
	}
}

func TestParseQuota(t *testing.T) {
	cases := []struct {
		raw  string
		want int64
		ok   bool
	}{
		// 只收十进制 MB/GB/TB 三档;纯数字 = 字节数。
		{"500MB", 500 * 1000 * 1000, true},
		{"10GB", 10 * 1000 * 1000 * 1000, true},
		{"1TB", 1000 * 1000 * 1000 * 1000, true},
		{"1.5GB", int64(1.5 * 1e9), true},
		{"10 gb", 10 * 1000 * 1000 * 1000, true},
		{"1024", 1024, true},
		// 刻意不支持的单位与量级:一律 fail-loud,避免十进制/二进制混用把上限算错。
		{"10GiB", 0, false},
		{"500MiB", 0, false},
		{"1KiB", 0, false},
		{"1KB", 0, false},
		{"1TiB", 0, false},
		{"1PB", 0, false},
		{"1G", 0, false},
		{"1M", 0, false},
		{"1B", 0, false},
		{"", 0, false},
		{"GiB", 0, false},
		{"10XB", 0, false},
		{"0", 0, false},
	}
	for _, c := range cases {
		got, err := ParseQuota(c.raw)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("ParseQuota(%q) = %d, %v; want %d, nil", c.raw, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("ParseQuota(%q) 期望失败,实际 %d", c.raw, got)
		}
	}
}
