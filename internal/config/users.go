// users.go 是 serve.users 用户表的语义校验与生效前缀计算。
//
// 校验覆盖 MERC-11 冻结契约的不变量 I4(互斥,由 cmd 层在合并后调用)、
// I6(生效前缀互不嵌套/不相等)、密码非空与 quota 语法;热加载的 reload
// 校验复用同一入口,保证启动与 reload 语义一致。
package config

import (
	"errors"
	"strconv"
	"strings"

	"github.com/BeCrafter/sail/internal/i18n"
)

// EffectivePrefix 计算用户的生效前缀:base(serve.prefix)拼接相对段
// user.prefix,两侧均按 "/" 归一化。结果不含首尾 "/",空 base + 空段 = 桶根。
func EffectivePrefix(base, rel string) string {
	base = strings.Trim(base, "/")
	rel = strings.Trim(rel, "/")
	switch {
	case base == "":
		return rel
	case rel == "":
		return base
	default:
		return base + "/" + rel
	}
}

// ValidateUsers 校验用户表语义。basePrefix 是 serve.prefix 的归一化值,
// 所有前缀嵌套判定都在生效前缀(EffectivePrefix(basePrefix, u.Prefix))上做。
// 通过则返回 nil;任何失败都是 fail-loud,调用方必须拒绝启动/保留旧表。
func ValidateUsers(basePrefix string, users []UserConfig) error {
	names := map[string]bool{}
	effs := make([]string, 0, len(users))
	for i := range users {
		u := users[i]
		if u.Name == "" {
			return errors.New(i18n.Tf("serve.users[%d]: name is required", i))
		}
		if strings.Contains(u.Name, ":") {
			return errors.New(i18n.Tf("serve.users[%d] (%q): name must not contain \":\" (Basic auth userinfo separator)", i, u.Name))
		}
		if names[u.Name] {
			return errors.New(i18n.Tf("serve.users[%d]: duplicate name %q", i, u.Name))
		}
		names[u.Name] = true
		if u.Password == "" {
			return errors.New(i18n.Tf("serve.users[%d] (%q): password is required (it may reference an environment variable via ${VAR})", i, u.Name))
		}
		if err := validateUserPrefix(u.Prefix); err != nil {
			return errors.New(i18n.Tf("serve.users[%d] (%q): invalid prefix: %v", i, u.Name, err))
		}
		if u.Quota != "" {
			if _, err := ParseQuota(u.Quota); err != nil {
				return errors.New(i18n.Tf("serve.users[%d] (%q): invalid quota: %v", i, u.Name, err))
			}
		}
		effs = append(effs, EffectivePrefix(basePrefix, u.Prefix))
	}
	// I6:生效前缀互不嵌套/不相等。嵌套会让外层用户列举看到内层数据,
	// 相等会让两个用户共享同一命名空间——都属结构性越权,拒绝生效。
	for i := 0; i < len(effs); i++ {
		for j := i + 1; j < len(effs); j++ {
			if effs[i] == effs[j] {
				return errors.New(i18n.Tf("users %q and %q resolve to the same prefix %q; each user must own a distinct space", users[i].Name, users[j].Name, effs[i]))
			}
			if isNestedPrefix(effs[i], effs[j]) {
				return errors.New(i18n.Tf("prefix %q (user %q) nests inside %q (user %q): the outer user would list the inner user's objects; nested prefixes are rejected at startup", effs[j], users[j].Name, effs[i], users[i].Name))
			}
			if isNestedPrefix(effs[j], effs[i]) {
				return errors.New(i18n.Tf("prefix %q (user %q) nests inside %q (user %q): the outer user would list the inner user's objects; nested prefixes are rejected at startup", effs[i], users[i].Name, effs[j], users[j].Name))
			}
		}
	}
	return nil
}

// validateUserPrefix 校验用户相对前缀:拒绝绝对路径与 ".." 段。
// 前缀是配置声明,不是用户输入,但 fail-loud 仍优于让 S3 key 里出现字面 ".."。
func validateUserPrefix(p string) error {
	t := strings.Trim(p, "/")
	if strings.HasPrefix(p, "/") {
		return errors.New(i18n.Tf("prefix %q must be relative to serve.prefix (drop the leading \"/\")", p))
	}
	for _, seg := range strings.Split(t, "/") {
		if seg == ".." {
			return errors.New(i18n.Tf("prefix %q must not contain \"..\" segments", p))
		}
	}
	return nil
}

// isNestedPrefix 判定 a 是否是 b 的真前缀目录(b 在 a 目录内)。
// 两者均为归一化生效前缀(无首尾 "/")。
func isNestedPrefix(a, b string) bool {
	if a == "" {
		// a 是桶根:b 必然在桶内,除非 b 也是桶根(相等已另行判定)。
		return b != ""
	}
	return strings.HasPrefix(b, a+"/")
}

// quotaUnits 是配额语法支持的单位,只保留十进制 MB/GB/TB 三档
// (空单位 = 纯数字,按字节计)。刻意不收二进制单位(GiB/MiB 等)与其他量级,
// 避免十进制/二进制混用时把上限算错。
var quotaUnits = map[string]int64{
	"":   1,
	"MB": 1000 * 1000,
	"GB": 1000 * 1000 * 1000,
	"TB": 1000 * 1000 * 1000 * 1000,
}

// ParseQuota 解析配额字符串(如 "10GB"、"500MB"、"1024")为字节。
// 与 cmd.parseSize 不同,配额刻意只收 MB/GB/TB 三档单位(见 quotaUnits),
// 不接受二进制单位。空串不是合法配额——调用方以「非空才校验」表达
// 「省略 = 不限额」。
func ParseQuota(raw string) (int64, error) {
	t := strings.TrimSpace(raw)
	if t == "" {
		return 0, errors.New(i18n.T("quota is empty"))
	}
	i := 0
	for i < len(t) && (t[i] >= '0' && t[i] <= '9' || t[i] == '.') {
		i++
	}
	numPart := t[:i]
	unit := strings.ToUpper(strings.TrimSpace(t[i:]))
	if numPart == "" {
		return 0, errors.New(i18n.Tf("quota %q is missing a number", raw))
	}
	v, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return 0, errors.New(i18n.Tf("failed to parse quota %q: %v", raw, err))
	}
	mult, ok := quotaUnits[unit]
	if !ok {
		return 0, errors.New(i18n.Tf("unrecognized quota unit in %q (supported: MB/GB/TB; a plain number means bytes)", raw))
	}
	q := int64(v * float64(mult))
	if q <= 0 {
		return 0, errors.New(i18n.Tf("quota %q must be greater than zero", raw))
	}
	return q, nil
}
