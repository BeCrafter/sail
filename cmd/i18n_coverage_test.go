package cmd

import (
	"sort"
	"strings"
	"testing"

	"github.com/BeCrafter/sail/internal/i18n"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestTranslatableStringsAreRegistered 守住 i18n 的静默失效:译文表以英文
// 原文为 key,英文原文改了而译文表没跟着改时查询就 miss,用户看到的直接
// 退回英文——不报错、不 panic,不逐条比对根本发现不了。
//
// 本测试枚举命令树里所有可翻译的字符串(即 Apply 会走 T 查询的同一批:
// 各命令 Short/Long/Example、每个 flag 的 Usage),逐个要求译文表里有对应
// 条目。新增命令或 flag 时忘记补中文,这里会拦下。
//
// 不查的部分及原因:
//   - Use/Aliases:语言中立,cobra 靠它们定位命令;
//   - 隐藏命令(__commands):诊断用,不面向用户;
//   - 根命令自身:其 Short/Long 由 i18n.Apply 直接转换,不参与命令树遍历。
func TestTranslatableStringsAreRegistered(t *testing.T) {
	seen := map[string]string{} // 字符串 -> 首次出现的位置
	check := func(where, s string) {
		if s == "" {
			return
		}
		if _, ok := seen[s]; !ok {
			seen[s] = where
		}
	}

	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden {
			return
		}
		check(c.Name()+" Short", c.Short)
		// cobra 的 help 子命令正文内嵌宿主显示名("Simply type <host> help …"),
		// 每个宿主一份原文;i18n.Apply 按模板整段改写,这里还原成同一个 key。
		if c.Name() == "help" && strings.HasPrefix(c.Use, "help [command]") && c.Parent() != nil {
			check(c.Name()+" Long",
				"Help provides help for any command in the application.\nSimply type %s help [path to command] for full details.")
		} else {
			check(c.Name()+" Long", c.Long)
		}
		check(c.Name()+" Example", c.Example)
		visit := func(f *pflag.Flag) {
			// 帮助/版本开关的 Usage 由 cobra 拼上命令名("help for <cmd>" 等),
			// 无法用单一 key 覆盖,i18n.Apply 按模板整段改写。这里还原 Apply
			// 用的 key 再查表。
			//
			// Apply 是就地改写,同一个测试进程里跑了不止一次(其它测试驱动
			// rootCmd 时也会走 Execute),所以此处可能读到英文原文,也可能读到
			// 上一轮翻译后的中文;两种形态都还原成同一 key,断言与语言无关。
			for _, label := range []string{"help", "version"} {
				if strings.HasPrefix(f.Usage, label+" for ") {
					check(c.Name()+" --"+f.Name, label+" for %s")
					return
				}
			}
			check(c.Name()+" --"+f.Name, f.Usage)
		}
		// 与 i18n.Apply 同样先确保这些开关已由 cobra 创建:它们本应在 Apply
		// 的第一遍里物化,但其它测试可能已触发过 cobra 的惰性创建,形态不一。
		c.InitDefaultHelpFlag()
		c.InitDefaultVersionFlag()
		c.Flags().VisitAll(visit)
		c.PersistentFlags().VisitAll(visit)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)

	// 组标题:Title 在 Apply 时已被就地换成当前语言的文本,原始英文只存在于
	// 译文表里。这里反向核对——当前标题应能在译文表中找到(中文环境下是译文
	// 自身,英文环境下是英文 key),找不到即为未登记的标题。
	for _, g := range rootCmd.Groups() {
		if g.Title == "" {
			t.Errorf("命令组 %q 标题为空", g.ID)
			continue
		}
		if !i18n.CoveredByZh(g.Title) {
			t.Errorf("命令组 %q 的标题 %q 未登记进译文表(i18n 无法翻译)", g.ID, g.Title)
		}
	}

	var missing []string
	for s, where := range seen {
		if !i18n.HasZh(s) {
			missing = append(missing, s+"   ["+where+"]")
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("缺少中文译文: %s", m)
	}
	t.Logf("共校验 %d 条字符串,缺失 %d 条", len(seen), len(missing))
}
