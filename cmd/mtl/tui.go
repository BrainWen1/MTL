package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// tuiCommand 实现 mtl tui：基于 Bubble Tea 的终端交互外壳。
// TUI 不包含任何业务实现，所有数据读取与写操作都复用现有内部包和 CLI 辅助函数。
func tuiCommand(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	root := fs.String("dir", defaultInstancesRoot, "实例根目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("用法: mtl tui [--dir <实例根目录>]")
	}
	rootValue := strings.TrimSpace(*root)
	if rootValue == "" {
		return errors.New("--dir 不能为空")
	}

	program := tea.NewProgram(newTUIModel(rootValue), tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("TUI 启动失败（请在支持交互的终端中运行，或改用普通子命令）: %w", err)
	}
	return nil
}
