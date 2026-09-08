package i18n

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		in   string
		want Lang
	}{
		{"zh", Zh}, {"ZH", Zh}, {"zh-CN", Zh}, {"zh_CN", Zh}, {"zh-Hans", Zh}, {"cn", Zh}, {" Cn ", Zh},
		{"en", En}, {"en-US", En}, {"", En}, {"jp", En}, {"--lang", En},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestT(t *testing.T) {
	SetLang(En)
	defer SetLang(En)

	// English fallback: key missing or language is En returns msgID verbatim.
	if got := T("Copy objects"); got != "Copy objects" {
		t.Errorf("En T() = %q, want verbatim", got)
	}

	SetLang(Zh)
	// Existing zh key returns Chinese.
	if got := T("config file path (default ~/.sail/config.yaml)"); got != "配置文件路径 (默认 ~/.sail/config.yaml)" {
		t.Errorf("Zh T() = %q, want Chinese", got)
	}
	// Missing key degrades to English.
	if got := T("Not a real key 中文测试"); got != "Not a real key 中文测试" {
		t.Errorf("Zh missing key = %q, want verbatim English", got)
	}

	SetLang(En)
	if got := T("config file path (default ~/.sail/config.yaml)"); got != "config file path (default ~/.sail/config.yaml)" {
		t.Errorf("En fallback = %q, want verbatim", got)
	}
}
