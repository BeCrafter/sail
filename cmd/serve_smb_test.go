package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/BeCrafter/sail/internal/i18n"
	"github.com/BeCrafter/sail/internal/smbfs"
)

func TestTranslateSMBStartError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
		omit string
	}{
		{
			name: "multi-user reserved name",
			err:  &smbfs.ShareNameError{Name: "IPC$", FromUserName: true, Reason: smbfs.ShareNameReserved},
			want: "SMB 用户名 \"IPC$\"",
			omit: "reserved by the SMB protocol",
		},
		{
			name: "single-user colon",
			err:  &smbfs.ShareNameError{Name: "sail:1445", Reason: smbfs.ShareNameColon},
			want: "SMB 共享名 \"sail:1445\"",
			omit: "clients may read it as a port",
		},
	}

	i18n.SetLang(i18n.Zh)
	defer i18n.SetLang(i18n.En)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := translateSMBStartError(tc.err)
			if !strings.Contains(got.Error(), tc.want) {
				t.Errorf("错误未翻译: %q, want substring %q", got, tc.want)
			}
			if strings.Contains(got.Error(), tc.omit) {
				t.Errorf("仍暴露英文错误: %q", got)
			}
		})
	}

	original := errors.New("unrelated")
	if got := translateSMBStartError(original); got != original {
		t.Fatal("非 SMB 校验错误不应被改写")
	}
}
