package i18n

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Apply rewrites the help-bearing fields of root and every subcommand in the
// current language: each command's Short/Long/Example, every flag's Usage, and
// the Title of any command groups.
//
// pflag stores *Flag pointers (AddFlag keeps the same object), so LocalFlags/
// InheritedFlags alias the same flags as Flags()/PersistentFlags(). Visiting
// Flags()+PersistentFlags() per command therefore covers every flag exactly
// once, inherited ones included.
//
// Use/Aliases/GroupID/VersionTemplate are language-neutral and left untouched.
// Cobra renders help lazily (inside Execute), so calling Apply first is
// sufficient. Apply is idempotent: under Zh a re-apply looks up a Chinese
// string against the English-keyed table, misses, and returns it unchanged.
//
// The help flag gets special treatment. Cobra creates it lazily — on the first
// Execute or help render, which is *after* Apply has run — and its usage is
// "help for <name>", a string that varies per command. Left alone it would stay
// English forever, so Apply materializes the flag on every command before the
// translation pass and rewrites the usage from the "help for %s" template.
func Apply(root *cobra.Command) {
	var walk func(*cobra.Command, func(*cobra.Command))
	walk = func(c *cobra.Command, fn func(*cobra.Command)) {
		fn(c)
		for _, sub := range c.Commands() {
			walk(sub, fn)
		}
	}

	// 先物化所有帮助开关、版本开关与 help 子命令,再统一翻译:三者都由 cobra
	// 惰性创建(首次执行或渲染帮助时,晚于 Apply),不提前物化就赶不上这轮
	// 翻译;三个初始化都幂等,重复调用无副作用。
	walk(root, func(c *cobra.Command) {
		c.InitDefaultHelpFlag()
		c.InitDefaultVersionFlag()
		c.InitDefaultHelpCmd()
	})

	walk(root, func(c *cobra.Command) {
		c.Short = T(c.Short)
		c.Long = T(c.Long)
		if c.Example != "" {
			c.Example = T(c.Example)
		}
		// cobra 的 help 子命令正文里内嵌了宿主的显示名("Simply type sail help …"),
		// 每个宿主一份原文,固定 key 覆盖不了,同样按模板整段改写。
		if c.Name() == "help" && strings.HasPrefix(c.Use, "help [command]") {
			c.Long = Tf("Help provides help for any command in the application.\nSimply type %s help [path to command] for full details.", c.Parent().DisplayName())
		}
		// cobra 给这两个开关的 Usage 拼的是 "help for <名>"/"version for <名>",
		// 随命令名变化,固定 key 覆盖不了,故识别原文后按模板整段改写。
		visit := func(f *pflag.Flag) {
			name := c.DisplayName()
			switch f.Usage {
			case "help for " + name:
				f.Usage = Tf("help for %s", name)
			case "version for " + name:
				f.Usage = Tf("version for %s", name)
			default:
				f.Usage = T(f.Usage)
			}
		}
		c.Flags().VisitAll(visit)
		c.PersistentFlags().VisitAll(visit)
		for _, g := range c.Groups() {
			g.Title = T(g.Title)
		}
	})
}
