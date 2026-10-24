package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mtl/internal/config"
	"mtl/internal/instance"
)

// defaultInstancesRoot 与项目惯例一致：从当前目录的 instances/ 扫描实例。
const defaultInstancesRoot = "instances"

// listCommand 实现 mtl list，只读扫描实例根目录并输出表格。
func listCommand(args []string) error {
	root, positional, err := parseInstanceArgs(args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("用法: mtl list [--dir <实例根目录>]")
	}

	entries, skipped, err := instance.Scan(root)
	if err != nil {
		return err
	}
	for _, s := range skipped {
		fmt.Fprintf(os.Stderr, "[Instance] 警告: 跳过 %s: %v\n", s.Name, s.Reason)
	}

	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	fmt.Printf("[Instance] 实例根目录: %s\n", abs)
	if len(entries) == 0 {
		fmt.Println("[Instance] 未发现可识别的实例")
		fmt.Printf("[Instance] 已跳过 %d 个无效目录\n", len(skipped))
		return nil
	}

	rows := make([][]string, 0, len(entries)+1)
	rows = append(rows, []string{"实例名", "加载器类型", "加载器版本", "默认账号", "Java 状态"})
	for _, e := range entries {
		rows = append(rows, []string{
			e.Name,
			orDash(e.Config.LoaderType),
			orDash(e.Config.LoaderVersion),
			orDash(e.Config.DefaultAccount),
			javaCell(e.JavaStatus),
		})
	}
	writeTable(os.Stdout, rows)

	abnormal := 0
	for _, e := range entries {
		if e.JavaStatus == instance.JavaMissing || e.JavaStatus == instance.JavaNotExecutable {
			abnormal++
		}
	}
	fmt.Printf("\n[Instance] 共 %d 个实例", len(entries))
	if abnormal > 0 {
		fmt.Printf("，其中 %d 个 Java 路径异常", abnormal)
	}
	if len(skipped) > 0 {
		fmt.Printf("；已跳过 %d 个无效目录", len(skipped))
	}
	fmt.Println()
	return nil
}

// infoCommand 实现 mtl info，只读展示完整配置与健康检查结果。
func infoCommand(args []string) error {
	root, positional, err := parseInstanceArgs(args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("用法: mtl info [--dir <实例根目录>] <实例名>")
	}

	e, err := instance.Load(root, positional[0])
	if err != nil {
		return err
	}

	fmt.Printf("[Instance] 实例: %s\n", e.Name)
	fmt.Printf("[Instance] 目录: %s\n", e.Dir)
	fmt.Printf("[Instance] 配置文件: %s\n", e.ConfigPath)

	fmt.Println("[Instance] 完整配置:")
	fmt.Println(config.Format(e.Config))

	fmt.Println("[Instance] 元信息:")
	meta := [][2]string{
		{"加载器类型", orDash(e.Config.LoaderType)},
		{"加载器版本", orDash(e.Config.LoaderVersion)},
		{"默认账号", orDash(e.Config.DefaultAccount)},
		{"Java 路径", orDash(e.Config.Java)},
		{"Java 状态", e.JavaStatus},
		{"内存模式", e.Config.MemoryMode},
		{"手动内存", orDash(e.Config.ManualMemory)},
		{"JVM 参数", fmt.Sprintf("%d 项", len(e.Config.JVMArgs))},
		{"游戏参数", fmt.Sprintf("%d 项", len(e.Config.GameArgs))},
		{"高性能显卡", fmt.Sprintf("%v", e.Config.PreferHighPerfGPU)},
	}
	writeKeyValue(os.Stdout, meta)

	checks := e.Health()
	fmt.Println("[Instance] 健康检查:")
	for _, c := range checks {
		fmt.Printf("  [%s] %s: %s\n", c.Level, c.Item, c.Detail)
	}

	failures, warnings := instance.CountHealth(checks)
	fmt.Println()
	switch {
	case failures > 0:
		fmt.Printf("[Instance] 结论: %d 项异常，%d 项警告\n", failures, warnings)
	case warnings > 0:
		fmt.Printf("[Instance] 结论: 健康，但有 %d 项警告\n", warnings)
	default:
		fmt.Println("[Instance] 结论: 健康")
	}
	return nil
}

// parseInstanceArgs 解析 list/info 共用的 --dir 参数。
// 手工解析而不是用 flag 包，保证 mtl info <实例名> --dir <根目录> 这种写法可用。
func parseInstanceArgs(args []string) (root string, positional []string, err error) {
	root = defaultInstancesRoot
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dir":
			if i+1 >= len(args) {
				return "", nil, errors.New("--dir 缺少参数值")
			}
			i++
			root = args[i]
		case strings.HasPrefix(a, "--dir="):
			root = strings.TrimPrefix(a, "--dir=")
		case strings.HasPrefix(a, "-"):
			return "", nil, fmt.Errorf("未知参数 %q", a)
		default:
			positional = append(positional, a)
		}
	}
	if strings.TrimSpace(root) == "" {
		return "", nil, errors.New("--dir 不能为空")
	}
	return root, positional, nil
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// javaCell 给异常状态加上醒目前缀，正常/自动状态保持简洁。
func javaCell(status string) string {
	switch status {
	case instance.JavaMissing, instance.JavaNotExecutable:
		return "!! " + status
	default:
		return status
	}
}

// writeTable 按显示宽度对齐输出简单文本表格，兼容中文占两个字符宽度。
func writeTable(w io.Writer, rows [][]string) {
	if len(rows) == 0 {
		return
	}
	cols := len(rows[0])
	widths := make([]int, cols)
	for _, row := range rows {
		for i := 0; i < cols && i < len(row); i++ {
			if n := displayWidth(row[i]); n > widths[i] {
				widths[i] = n
			}
		}
	}
	for _, row := range rows {
		var b strings.Builder
		for i := 0; i < cols && i < len(row); i++ {
			b.WriteString(row[i])
			if i < cols-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-displayWidth(row[i])+2))
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
}

func writeKeyValue(w io.Writer, pairs [][2]string) {
	width := 0
	for _, p := range pairs {
		if n := displayWidth(p[0]); n > width {
			width = n
		}
	}
	for _, p := range pairs {
		fmt.Fprintf(w, "  %s%s: %s\n", p[0], strings.Repeat(" ", width-displayWidth(p[0])+2), p[1])
	}
}

// displayWidth 近似计算终端显示宽度：CJK 与全角字符按 2 列处理。
func displayWidth(s string) int {
	width := 0
	for _, r := range s {
		if isWideRune(r) {
			width += 2
		} else {
			width++
		}
	}
	return width
}

func isWideRune(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0xA4CF, // CJK 部首、汉字、日韩文字等
		r >= 0xAC00 && r <= 0xD7A3, // Hangul Syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK Compatibility Ideographs
		r >= 0xFE10 && r <= 0xFE19,
		r >= 0xFE30 && r <= 0xFE6F,
		r >= 0xFF00 && r <= 0xFF60, // Fullwidth Forms
		r >= 0xFFE0 && r <= 0xFFE6:
		return true
	default:
		return false
	}
}
