package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// detectShell 返回当前用户的 shell 类型 (zsh/bash/fish)。
// 仅在 macOS 上启用补全引导 —— Linux 各发行版 bash-completion 路径不统一,
// Windows 无原生 POSIX shell。其他系统返回空,隐藏补全安装提示。
func detectShell() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		return ""
	}
	base := filepath.Base(shell)
	switch base {
	case "zsh", "bash", "fish":
		return base
	}
	return ""
}

// confirmDefault 默认 yes,用户输入 n 才拒绝。
func confirmDefault() bool {
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line != "n" && line != "no"
}

// installCompletion 为指定 shell 安装自动补全脚本。
func installCompletion(shell string) error {
	switch shell {
	case "zsh":
		return installZsh()
	case "bash":
		return installBash()
	case "fish":
		return installFish()
	}
	return fmt.Errorf("不支持的 shell: %s", shell)
}

func installZsh() error {
	dir := filepath.Join(os.Getenv("HOME"), ".zsh", "completion")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	target := filepath.Join(dir, "_sail")
	if err := genCompletionToFile("zsh", target); err != nil {
		return err
	}

	// 用 source + compdef 方式加载补全,兼容已安装 compinit 框架(如 Oh My Zsh)。
	// 单独 fpath 方式在 compinit 已被框架提前调用时无效;
	// 单独 source 不触发 compdef 注册(脚本自带的 guard 阻止)。
	rcPath := filepath.Join(os.Getenv("HOME"), ".zshrc")
	sourceLine := "source ~/.zsh/completion/_sail"
	compdefLine := "compdef _sail sail"

	needSource := !lineInFile(rcPath, sourceLine)
	needCompdef := !lineInFile(rcPath, compdefLine)
	if needSource || needCompdef {
		f, err := os.OpenFile(rcPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("写入 .zshrc 失败: %w", err)
		}
		defer f.Close()
		if needSource {
			fmt.Fprintln(f, sourceLine)
		}
		if needCompdef {
			fmt.Fprintln(f, compdefLine)
		}
	}

	fmt.Printf("补全已安装: %s\n", target)
	fmt.Printf("已写入 .zshrc: %s\n", sourceLine)
	fmt.Printf("请重新加载配置: source ~/.zshrc (或重新打开终端)\n")
	return nil
}

func installBash() error {
	// 按优先级查找 bash-completion 目录:已存在的目录优先,
	// 都不存在则用 XDG 兜底路径(创建)。
	var dir string
	candidates := []string{
		filepath.Join(os.Getenv("HOMEBREW_PREFIX"), "etc", "bash_completion.d"),
		"/usr/local/etc/bash_completion.d",    // Homebrew Intel
		"/opt/homebrew/etc/bash_completion.d", // Homebrew Apple Silicon
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			dir = c
			break
		}
	}
	if dir == "" {
		// XDG 兜底路径
		dir = filepath.Join(os.Getenv("HOME"), ".local", "share", "bash-completion", "completions")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建目录失败: %w", err)
		}
	}
	target := filepath.Join(dir, "sail")
	if err := genCompletionToFile("bash", target); err != nil {
		return err
	}
	fmt.Printf("补全已安装: %s\n", target)
	fmt.Println("重新打开终端即可生效(需已安装 bash-completion)。")
	// bash 版本提示
	if v := bashMajorVersion(); v > 0 && v < 4 {
		fmt.Printf("提示: 当前 bash 版本 %d.x,部分高级补全功能受限。建议升级: brew install bash\n", v)
	}
	return nil
}

func installFish() error {
	dir := filepath.Join(os.Getenv("HOME"), ".config", "fish", "completions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	target := filepath.Join(dir, "sail.fish")
	if err := genCompletionToFile("fish", target); err != nil {
		return err
	}
	fmt.Printf("补全已安装: %s\n", target)
	fmt.Println("重新打开终端即可生效。")
	return nil
}

// genCompletionToFile 调用 cobra 的补全生成逻辑写入文件。
// 直接用 rootCmd 生成,确保补全命令名正确(而非 completion 子命令的名字)。
func genCompletionToFile(shell, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("创建文件失败: %w", err)
	}
	defer f.Close()
	switch shell {
	case "zsh":
		return rootCmd.GenZshCompletion(f)
	case "bash":
		// 用 V1 生成器,兼容 macOS 默认 bash 3.2。
		// V2 要求 bash 4.4+,在 bash 3.2 上不工作。
		return rootCmd.GenBashCompletion(f)
	case "fish":
		return rootCmd.GenFishCompletion(f, true)
	}
	return fmt.Errorf("不支持的 shell: %s", shell)
}

// lineInFile 检查文件中是否已存在某行内容。
func lineInFile(path, target string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == target {
			return true
		}
	}
	return false
}

// bashMajorVersion 返回系统 bash 的主版本号,无法确定时返回 0。
func bashMajorVersion() int {
	out, err := exec.Command("bash", "--version").Output()
	if err != nil {
		return 0
	}
	// 输出形如 "GNU bash, version 3.2.57(1)-release"
	s := string(out)
	idx := strings.Index(s, "version ")
	if idx < 0 {
		return 0
	}
	rest := s[idx+8:]
	var major int
	fmt.Sscanf(rest, "%d", &major)
	return major
}
