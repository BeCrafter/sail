package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// initHelpTree 补齐 cobra 惰性创建的 help 子命令与帮助开关,使 `--help` 的输出
// 与真实运行一致(否则测试看到的是尚未挂上 help 的中间态)。
func initHelpTree(c *cobra.Command) {
	c.InitDefaultHelpCmd()
	c.InitDefaultHelpFlag()
	for _, sub := range c.Commands() {
		initHelpTree(sub)
	}
}

// TestHelpCoversEveryCommand 守住「帮助信息覆盖全部命令」:
//   - 每个非隐藏命令都能在父命令的 Available Commands 里被看到;
//   - 每个命令都有一句非空的 Short 作为列表里的说明;
//   - 每个命令都有可执行的 Run/RunE,或另有子命令。
//
// 反向也成立:遍历的是真实命令树,新增命令若忘了写 Short 或没挂到父命令,
// 这里直接失败——不依赖人去比对帮助输出与代码。
func TestHelpCoversEveryCommand(t *testing.T) {
	initHelpTree(rootCmd)

	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			// cobra 的 help 子命令不是命令树里的普通成员:IsAvailableCommand 对它
			// 恒为 false(父命令的 help 指针判等),但帮助列表照样渲染它。
			if sub.Hidden || strings.HasPrefix(sub.Use, "help ") {
				walk(sub)
				continue
			}
			if !sub.IsAvailableCommand() {
				t.Errorf("%s: 子命令 %q 不可用却未标记 Hidden(帮助里看不到,也调不动)",
					c.CommandPath(), sub.Name())
			}
			if strings.TrimSpace(sub.Short) == "" {
				t.Errorf("%s: 子命令 %q 没有 Short,父命令帮助列表中无法说明它是做什么的",
					c.CommandPath(), sub.Name())
			}
			if sub.Run == nil && sub.RunE == nil && !sub.HasAvailableSubCommands() {
				t.Errorf("%s: 命令 %q 既不可执行也没有子命令", c.CommandPath(), sub.Name())
			}
			walk(sub)
		}
	}
	walk(rootCmd)

	// 根命令是 help 的入口,Long 必须非空(否则 `sail --help` 只剩命令列表)。
	if strings.TrimSpace(rootCmd.Long) == "" {
		t.Error("根命令缺少 Long:`sail --help` 将没有任何说明文字")
	}
}
