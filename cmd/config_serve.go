package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/i18n"
)

// collectServeConfig 交互式收集 profile 的 serve 块(WebDAV 网关参数)。
//
// 保留语义(避免重配时丢数据):已有非空 serve 块时给出选择
// 「回车=原样保留 / a=追加用户(仅多用户表存在时)/ r=重新配置 / d=删除」;
// 选保留则逐字返回 existing,任何字段都不经手 —— 这是 serve.users 曾被静默
// 丢弃缺陷的根治方式。追加只动用户表,其余字段逐字保留;重新配置时也只覆盖
// 被提问的字段,backend-max-object-size / max-upload-size / chunk-size /
// dir-cache-ttl / prewarm 原样保留。
func collectServeConfig(r *bufio.Reader, existing config.ServeConfig) (config.ServeConfig, error) {
	if serveBlockEmpty(existing) {
		label := i18n.T(`configure the serve block for "sail serve webdav" (listen/prefix/auth/TLS/chunked-upload/staging-dir)?`)
		if !promptBoolReader(r, label, false) {
			return config.ServeConfig{}, nil
		}
		return collectServeParams(r, config.ServeConfig{})
	}
	fmt.Println(serveDigest(existing))
	canAppend := len(existing.Users) > 0
	label := i18n.T("serve block: Enter=keep, r=reconfigure, d=remove")
	if canAppend {
		label = i18n.T("serve block: Enter=keep, a=append users, r=reconfigure, d=remove")
	}
	for i := 0; i < 3; i++ {
		switch promptReaderDisplay(r, label, "", "") {
		case "d", "D":
			fmt.Println(i18n.T("serve block removed; sail serve webdav will fall back to flags (edit the config file to add one later)"))
			return config.ServeConfig{}, nil
		case "r", "R":
			return collectServeParams(r, existing)
		case "a", "A":
			if !canAppend {
				fmt.Println(i18n.T("no users table to append to; choose r to configure multi-user mode"))
				continue
			}
			return collectServeAppendUsers(r, existing)
		default:
			fmt.Println(i18n.T("serve block kept as-is"))
			return existing, nil
		}
	}
	fmt.Println(i18n.T("serve block kept as-is"))
	return existing, nil
}

// collectServeAppendUsers 在现有用户表基础上追加用户:其余 serve 字段逐字保留。
// 先列出已有用户(不含密码)供核对,再进入与新建多用户相同的录入循环。
func collectServeAppendUsers(r *bufio.Reader, existing config.ServeConfig) (config.ServeConfig, error) {
	fmt.Println(i18n.T("append users to the existing table:"))
	printServeUsers(existing.Users)
	users, err := collectServeUsers(r, corePrefix(existing.Prefix), existing.Users)
	if err != nil {
		return config.ServeConfig{}, err
	}
	existing.Users = users
	fmt.Println(i18n.T("serve block updated (users appended); all other serve fields are kept as-is"))
	return existing, nil
}

// printServeUsers 列出用户表概要供核对;绝不显示密码。
func printServeUsers(users []config.UserConfig) {
	for _, u := range users {
		quota := u.Quota
		if quota == "" {
			quota = i18n.T("unlimited")
		}
		prefix := u.Prefix
		if prefix == "" {
			prefix = i18n.T("(base prefix)")
		}
		fmt.Printf(i18n.T("  - name=%s prefix=%s quota=%s\n"), u.Name, prefix, quota)
	}
}

// collectServeParams 逐字段收集 serve 参数:默认值取 existing 同名字段
// (回车即保留),留空 = 不写值(启动时落回 flag 默认)。只返回用户表收集
// 的放弃类错误;标量字段不做校验(与 bucket/region 一致),缺失由摘要标注、
// 启动时 fail-loud。
func collectServeParams(r *bufio.Reader, existing config.ServeConfig) (config.ServeConfig, error) {
	fmt.Println(i18n.T(`serve parameters (for "sail serve webdav"; Enter keeps the shown value, empty = flag default)`))
	s := existing
	s.Listen = promptListen(r, existing.Listen)
	s.Prefix = promptReader(r, i18n.T("shared prefix mapped to / (empty = the whole bucket)"), existing.Prefix)
	if err := collectServeAuth(r, &s, existing); err != nil {
		return config.ServeConfig{}, err
	}
	s.TLSCert = promptPath(r, i18n.T("tls-cert (path to the certificate; empty = serve over HTTP)"), existing.TLSCert, true)
	if s.TLSCert == "" {
		// 无证书则私钥无意义:清掉「有 key 无 cert」的半配置态。
		s.TLSKey = ""
	} else {
		s.TLSKey = promptPath(r, i18n.T("tls-key (path to the private key; required together with tls-cert)"), existing.TLSKey, true)
	}
	s.ChunkedUpload = promptBoolReader(r, i18n.T("chunked-upload (store files over chunk-size as chunks + a manifest)?"), existing.ChunkedUpload)
	s.StagingDir = promptPath(r, i18n.T("staging-dir (write staging directory; empty = the system temp dir)"), existing.StagingDir, false)
	return s, nil
}

// promptListen 读取监听地址并宽容纠正(纯数字补冒号、去掉误写的 scheme);
// 端口缺失或越界则说明原因后重问(至多 3 次,避免管道输入死循环)。
func promptListen(r *bufio.Reader, def string) string {
	label := fmt.Sprintf(i18n.T("listen address (empty = flag default %s)"), serveWebdavOpts.listen)
	for i := 0; i < 3; i++ {
		raw := promptReader(r, label, def)
		norm, changed, err := normalizeListen(raw)
		if err != nil {
			fmt.Printf(i18n.T("invalid listen address: %v\n"), err)
			continue
		}
		if changed {
			fmt.Printf(i18n.T("note: normalized listen address to %s\n"), norm)
		}
		return norm
	}
	return def
}

// normalizeListen 归一化监听地址:纯数字视为端口补冒号(8443 → :8443)、
// 去掉误写的 http:// / https:// 前缀,并校验端口存在且落在 1-65535。
// 返回归一化值、是否发生纠正、错误(错误时调用方应重问)。
func normalizeListen(v string) (string, bool, error) {
	trimmed := strings.TrimSpace(v)
	changed := trimmed != v // 首尾空白也算被纠正
	v = trimmed
	if v == "" {
		return "", changed, nil
	}
	if s := strings.TrimPrefix(strings.TrimPrefix(v, "http://"), "https://"); s != v {
		v = s
		changed = true
	}
	if isDigits(v) {
		v = ":" + v
		changed = true
	}
	i := strings.LastIndex(v, ":")
	if i < 0 || !isDigits(v[i+1:]) {
		return v, changed, fmt.Errorf(i18n.T("%q has no port; use [host]:port, e.g. :8443"), v)
	}
	n, err := strconv.Atoi(v[i+1:])
	if err != nil || n < 1 || n > 65535 {
		return v, changed, fmt.Errorf(i18n.T("port %q must be a number between 1 and 65535"), v[i+1:])
	}
	return v, changed, nil
}

// isDigits 判定是否全为数字(端口号形态)。
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// promptPath 读取路径类配置(证书/私钥/暂存盘):展开开头的 ~/(Go 不认
// shell 的 ~);mustExist 时对不存在的文件给出提醒但不阻塞——文件可能稍后
// 才放到位,启动时仍会 fail-loud。
func promptPath(r *bufio.Reader, label, def string, mustExist bool) string {
	v := promptReader(r, label, def)
	if norm := expandTilde(v); norm != v {
		fmt.Printf(i18n.T("note: expanded ~ to %s\n"), norm)
		v = norm
	}
	if mustExist && v != "" {
		if _, err := os.Stat(v); err != nil {
			fmt.Printf(i18n.T("warning: %s does not exist; sail serve webdav will fail to start until the file is in place\n"), v)
		}
	}
	return v
}

// expandTilde 展开路径开头的 ~/(Go 不认 shell 的 ~)。
func expandTilde(v string) string {
	if v == "~" || strings.HasPrefix(v, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return v
		}
		if v == "~" {
			return home
		}
		return filepath.Join(home, v[2:])
	}
	return v
}

// collectServeAuth 收集认证配置:先问单用户/多用户,再按选择引导。
// 两种模式互斥(I4):选定一侧即清空另一侧(用户显式选择,不算静默丢弃)。
func collectServeAuth(r *bufio.Reader, s *config.ServeConfig, existing config.ServeConfig) error {
	if len(existing.Users) > 0 && (existing.User != "" || existing.Password != "") {
		fmt.Println(i18n.T("warning: this profile sets both serve.users and user/password (mutually exclusive); the mode you pick below clears the other side"))
	}
	multi := promptBoolReader(r, i18n.T("multi-user mode (serve.users: one private space per user)?"), len(existing.Users) > 0)
	if !multi {
		if len(existing.Users) > 0 {
			fmt.Println(i18n.T("note: switching to single-user clears the serve.users table; switching to multi-user clears user/password"))
		}
		s.Users = nil
		s.User = promptReader(r, i18n.T("user (Basic auth username; required by sail serve webdav, empty = configure later)"), existing.User)
		s.Password = promptSecretReader(r, i18n.T("password for the Basic auth user (plaintext or ${VAR}; empty = configure later)"), existing.Password)
		return nil
	}
	// 多用户:user/password 与 users 互斥,清掉单用户半边。
	s.User, s.Password = "", ""
	seed := existing.Users
	if len(seed) > 0 {
		if !promptBoolReader(r, fmt.Sprintf(i18n.T("keep the %d existing user(s)?"), len(seed)), true) {
			seed = nil
		}
	}
	for round := 0; round < 3; round++ {
		users, err := collectServeUsers(r, corePrefix(s.Prefix), seed)
		if err != nil {
			return err
		}
		if len(users) > 0 {
			s.Users = users
			return nil
		}
		// 空表:回退单用户(默认是),否则重新录入。
		if promptBoolReader(r, i18n.T("no users added; fall back to single-user authentication?"), true) {
			s.User = promptReader(r, i18n.T("user (Basic auth username; required by sail serve webdav, empty = configure later)"), existing.User)
			s.Password = promptSecretReader(r, i18n.T("password for the Basic auth user (plaintext or ${VAR}; empty = configure later)"), existing.Password)
			return nil
		}
	}
	return errors.New(i18n.T("no users added after 3 attempts; re-run sail config setup or edit the config file manually"))
}

// collectServeUsers 循环收集用户表,空名字结束。每追加一名候选立即对
// 「累计表 + 候选」跑 config.ValidateUsers(重名/非法前缀/生效前缀相等或
// 嵌套/密码非空/quota 语法),失败打印错误并重问当前用户,连续 3 次失败
// 返回错误(与 endpoint 的 3 次守卫同风格,防管道输入死循环)。
// 返回的表保证通过校验;空表合法(由调用方决定回退策略)。
func collectServeUsers(r *bufio.Reader, basePrefix string, seed []config.UserConfig) ([]config.UserConfig, error) {
	fmt.Println(i18n.T("each user has: name (login username, unique), password (Basic auth), prefix (their private space under the shared prefix), quota (space limit for their objects)"))
	fmt.Println(i18n.T("quota units: MB / GB / TB (e.g. 500MB, 10GB, 1TB; a plain number means bytes)"))
	users := append([]config.UserConfig(nil), seed...)
	fails := 0
	for {
		name := promptReader(r, i18n.T("name (login username; Enter on empty to finish adding users)"), "")
		if name == "" {
			return users, nil
		}
		pw := promptSecretReader(r, fmt.Sprintf(i18n.T("password for user %s (Basic auth password; plaintext or ${VAR} env reference)"), name), "")
		prefix := promptReader(r, fmt.Sprintf(i18n.T("prefix for user %s (this user's private space, relative to serve.prefix, e.g. alice/; empty = the base prefix itself)"), name), "")
		if norm := normalizeUserPrefix(prefix); norm != prefix {
			// 手滑写成 /alice/ 时直接纠正,而不是让 ValidateUsers 报错重来。
			fmt.Printf(i18n.T("note: user prefix is relative to serve.prefix; using %q\n"), norm)
			prefix = norm
		}
		quota := promptServeQuota(r, name)
		// 拷贝后追加,避免与 users 共享底层数组。
		probe := append(append([]config.UserConfig(nil), users...), config.UserConfig{
			Name: name, Password: pw, Prefix: prefix, Quota: quota,
		})
		if err := config.ValidateUsers(basePrefix, probe); err != nil {
			fmt.Printf(i18n.T("user not added: %v\n"), err)
			fails++
			if fails >= 3 {
				return nil, fmt.Errorf(i18n.T("user table is still invalid after 3 attempts (%v); re-run sail config setup or edit the config file manually"), err)
			}
			continue
		}
		users = probe
		fails = 0
	}
}

// normalizeUserPrefix 归一化用户前缀为规范形态「去首尾斜杠 + 单个尾斜杠」
// (如 "/alice" → "alice/"):ValidateUsers 要求相对路径,手滑写成 /alice/
// 时由调用方纠正而非报错重来;空串仍是「base 前缀本身」。
func normalizeUserPrefix(v string) string {
	t := strings.Trim(strings.TrimSpace(v), "/")
	if t == "" {
		return ""
	}
	return t + "/"
}

// quotaShortUnitRe 匹配单字母单位写法(如 "10G"、"1.5T"),用于补全为 GB/TB。
var quotaShortUnitRe = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([MmGgTt])$`)

// normalizeQuotaShortUnit 补全单字母单位:M/G/T → MB/GB/TB。只补这三档,
// 其余单位(KB/KiB/B 等)不猜,交给 ParseQuota 报错重问。
func normalizeQuotaShortUnit(v string) string {
	if m := quotaShortUnitRe.FindStringSubmatch(strings.TrimSpace(v)); m != nil {
		return m[1] + strings.ToUpper(m[2]) + "B"
	}
	return v
}

// promptServeQuota 读取并就地校验配额:空 = 不限额;单字母单位自动补全;
// 合法时回显折算的字节数便于核对。连续 3 次仍非法则原样返回,
// 交由 ValidateUsers 拒绝并走既有的候选重试路径。
func promptServeQuota(r *bufio.Reader, name string) string {
	label := fmt.Sprintf(i18n.T("quota for user %s (space limit for this user's objects, e.g. 500MB or 10GB; empty = unlimited)"), name)
	raw := ""
	for i := 0; i < 3; i++ {
		raw = promptReader(r, label, "")
		if raw == "" {
			return ""
		}
		if norm := normalizeQuotaShortUnit(raw); norm != raw {
			fmt.Printf(i18n.T("note: quota unit normalized to %s\n"), norm)
			raw = norm
		}
		n, err := config.ParseQuota(raw)
		if err == nil {
			fmt.Printf(i18n.T("quota %s = %d bytes\n"), raw, n)
			return raw
		}
		fmt.Printf(i18n.T("invalid quota: %v\n"), err)
	}
	return raw
}
