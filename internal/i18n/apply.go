package i18n

import (
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
func Apply(root *cobra.Command) {
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		c.Short = T(c.Short)
		c.Long = T(c.Long)
		if c.Example != "" {
			c.Example = T(c.Example)
		}
		c.Flags().VisitAll(func(f *pflag.Flag) { f.Usage = T(f.Usage) })
		c.PersistentFlags().VisitAll(func(f *pflag.Flag) { f.Usage = T(f.Usage) })
		for _, g := range c.Groups() {
			g.Title = T(g.Title)
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}
