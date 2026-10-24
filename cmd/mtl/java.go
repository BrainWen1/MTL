package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"

	"mtl/internal/config"
	"mtl/internal/instance"
	"mtl/internal/runtime"
)

// javaCommand 处理 mtl java 子命令。
// 所有操作针对全局 runtime 缓存（全实例共享），不针对单个实例。
func javaCommand(args []string) error {
	if len(args) == 0 {
		return javaUsageError()
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return javaList(rest)
	case "use":
		return javaUse(rest)
	case "prune":
		return javaPrune(rest)
	default:
		return fmt.Errorf("未知 java 子命令 %q（可用: list、use、prune）", sub)
	}
}

func javaUsageError() error {
	return errors.New("用法: mtl java list [--debug]；mtl java use <runtime-id>；mtl java prune")
}

// verifyJavaExecutable 允许单元测试替换实际执行 `java -version` 的检查；
// 生产路径始终使用 runtime.VerifyExecutable。
var verifyJavaExecutable = runtime.VerifyExecutable

// javaReferenceScan 汇总 runtime 被哪些实例与全局配置引用。
type javaReferenceScan struct {
	// root 是实例根目录绝对路径。
	root string
	// instanceCount 是成功读取配置的实例数量。
	instanceCount int
	// byRuntime 是 runtime ID -> 引用方名称（实例名或“全局默认”）。
	byRuntime map[string][]string
	// globalID 是全局配置中的默认 runtime 标识；globalFound 表示该 runtime 是否仍存在。
	globalID    string
	globalFound bool
}

// javaList 输出全局 runtime 缓存清单与引用情况。
func javaList(args []string) error {
	debug, positional, err := parseJavaFlags(args, true)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("用法: mtl java list [--debug]")
	}

	root, err := runtime.DefaultDir()
	if err != nil {
		return err
	}
	installed, err := runtime.List(root)
	if err != nil {
		return err
	}
	refs, err := collectJavaReferences(defaultInstancesRoot, installed)
	if err != nil {
		return err
	}

	fmt.Printf("[Java] 运行时缓存: %s\n", root)
	fmt.Printf("[Java] 实例引用扫描: %s（%d 个实例）\n", refs.root, refs.instanceCount)
	if len(installed) == 0 {
		fmt.Println("[Java] 未发现已缓存的 Java 运行时")
		printGlobalJava(refs)
		return nil
	}

	rows := make([][]string, 0, len(installed)+1)
	rows = append(rows, []string{"标识", "版本", "操作系统", "架构", "状态", "Java 路径", "引用"})
	referenced := 0
	for _, inst := range installed {
		list := refs.byRuntime[inst.ID]
		if len(list) > 0 {
			referenced++
		}
		status := "正常"
		if !inst.Valid {
			status = "异常"
		}
		rows = append(rows, []string{
			inst.ID,
			orDash(inst.Version),
			orDash(inst.OS),
			orDash(inst.Arch),
			status,
			orDash(inst.JavaPath),
			orDash(strings.Join(list, ", ")),
		})
	}
	writeTable(os.Stdout, rows)

	if debug {
		fmt.Println("\n[Java] 完整元信息:")
		for _, inst := range installed {
			printJavaDetail(inst, refs.byRuntime[inst.ID])
		}
	}

	for _, inst := range installed {
		if !inst.Valid {
			fmt.Fprintf(os.Stderr, "[Java] 警告: 运行时 %s 校验失败: %s\n", inst.ID, inst.Err)
		}
	}
	fmt.Printf("\n[Java] 共 %d 个 Java 运行时，%d 个被引用\n", len(installed), referenced)
	printGlobalJava(refs)
	return nil
}

// javaUse 校验并写入全局默认 runtime；校验失败拒绝设置。
func javaUse(args []string) error {
	_, positional, err := parseJavaFlags(args, false)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("用法: mtl java use <runtime-id>")
	}

	inst, err := runtime.Find("", positional[0])
	if err != nil {
		return err
	}
	if !inst.Valid {
		return fmt.Errorf("无法设置全局默认 Java 运行时 %q: %s", inst.ID, inst.Err)
	}
	// 除了文件校验，再实际执行一次 java -version，确认运行时能正常启动。
	if err := verifyJavaExecutable(context.Background(), inst.JavaPath); err != nil {
		return fmt.Errorf("Java 运行时 %q 不可用，拒绝设置: %w", inst.ID, err)
	}

	cfg, loadErr := config.LoadGlobal()
	if loadErr != nil {
		fmt.Fprintf(os.Stderr, "[Java] 警告: %v；将基于可用字段保存\n", loadErr)
	}
	cfg.DefaultJava = inst.ID
	if err := config.SaveGlobal(cfg); err != nil {
		return err
	}
	path, _ := config.GlobalPath()
	fmt.Printf("[Java] 已设置全局默认 Java 运行时: %s（%s）\n", inst.ID, orDash(inst.Version))
	fmt.Printf("[Java] java: %s\n", inst.JavaPath)
	fmt.Printf("[Java] 全局配置: %s\n", path)
	return nil
}

// javaPrune 清理没有被任何实例或全局默认引用的 runtime。
func javaPrune(args []string) error {
	_, positional, err := parseJavaFlags(args, false)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("用法: mtl java prune")
	}

	root, err := runtime.DefaultDir()
	if err != nil {
		return err
	}
	installed, err := runtime.List(root)
	if err != nil {
		return err
	}
	refs, err := collectJavaReferences(defaultInstancesRoot, installed)
	if err != nil {
		return err
	}
	keep := make(map[string]string, len(refs.byRuntime))
	for id, reasons := range refs.byRuntime {
		keep[id] = strings.Join(reasons, "、")
	}

	res, err := runtime.Prune(runtime.PruneOptions{Keep: keep})
	if err != nil {
		return err
	}

	fmt.Printf("[Java] 运行时缓存: %s\n", root)
	for _, k := range res.Kept {
		fmt.Printf("[Java] 跳过 %s（%s）\n", k.ID, k.Reason)
	}
	if len(res.Kept) == 0 && len(res.Removed) == 0 && len(res.Failed) == 0 {
		fmt.Println("[Java] 未发现已缓存的 Java 运行时")
		return nil
	}
	for _, r := range res.Removed {
		fmt.Printf("[Java] 已删除 %s %s（%s）\n", r.ID, orDash(r.Version), formatBytes(r.SizeBytes))
	}
	for _, f := range res.Failed {
		fmt.Fprintf(os.Stderr, "[Java] 警告: 清理 %s 失败: %v\n", f.ID, f.Err)
	}
	if len(res.Removed) == 0 {
		fmt.Println("[Java] 没有可清理的 Java 运行时")
	} else {
		fmt.Printf("[Java] 共清理 %d 个运行时，释放 %s\n", len(res.Removed), formatBytes(res.FreedBytes))
	}
	return nil
}

// parseJavaFlags 解析 java 子命令参数；除 list 外不接受 --debug。
func parseJavaFlags(args []string, allowDebug bool) (debug bool, positional []string, err error) {
	for _, a := range args {
		switch {
		case a == "--debug" && allowDebug:
			debug = true
		case strings.HasPrefix(a, "-"):
			return false, nil, fmt.Errorf("未知参数 %q", a)
		default:
			positional = append(positional, a)
		}
	}
	return debug, positional, nil
}

// collectJavaReferences 扫描全局配置与 instances 目录，统计 runtime 引用。
// 损坏或格式错误的实例配置会被跳过并告警，不影响其他实例。
func collectJavaReferences(instancesRoot string, installed []runtime.Installed) (javaReferenceScan, error) {
	abs, err := filepath.Abs(instancesRoot)
	if err != nil {
		abs = instancesRoot
	}
	scan := javaReferenceScan{root: abs, byRuntime: make(map[string][]string)}

	// 全局默认也视为一种引用：prune 不能删除 mtl java use 选中的运行时。
	cfg, cfgErr := config.LoadGlobal()
	if cfgErr != nil {
		fmt.Fprintf(os.Stderr, "[Java] 警告: %v\n", cfgErr)
	}
	scan.globalID = strings.TrimSpace(cfg.DefaultJava)
	if scan.globalID != "" {
		if inst, ok := runtimeByID(installed, scan.globalID); ok {
			scan.globalFound = true
			addJavaRef(&scan, inst.ID, "全局默认")
		}
	}

	entries, skipped, err := instance.Scan(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return scan, nil
		}
		return scan, fmt.Errorf("扫描实例目录失败: %w", err)
	}
	scan.instanceCount = len(entries)
	for _, s := range skipped {
		fmt.Fprintf(os.Stderr, "[Java] 警告: 跳过实例 %s（%v）\n", s.Name, s.Reason)
	}
	for _, e := range entries {
		javaPath := strings.TrimSpace(e.Config.Java)
		if javaPath == "" {
			continue
		}
		if id, ok := runtimeForPath(installed, javaPath); ok {
			addJavaRef(&scan, id, e.Name)
		}
	}
	return scan, nil
}

// runtimeByID 按标识查找已安装运行时，精确匹配优先，其次忽略大小写。
func runtimeByID(installed []runtime.Installed, id string) (runtime.Installed, bool) {
	for _, inst := range installed {
		if inst.ID == id {
			return inst, true
		}
	}
	for _, inst := range installed {
		if strings.EqualFold(inst.ID, id) {
			return inst, true
		}
	}
	return runtime.Installed{}, false
}

// runtimeForPath 判断实例配置的 Java 路径是否位于某个 runtime 目录内。
func runtimeForPath(installed []runtime.Installed, javaPath string) (string, bool) {
	abs, err := filepath.Abs(javaPath)
	if err != nil {
		return "", false
	}
	for _, inst := range installed {
		root, target := inst.Dir, abs
		// Windows 路径大小写不敏感，统一小写后再做相对路径判断。
		if goruntime.GOOS == "windows" {
			root, target = strings.ToLower(root), strings.ToLower(target)
		}
		rel, err := filepath.Rel(root, target)
		if err != nil || filepath.IsAbs(rel) || rel == ".." ||
			strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return inst.ID, true
	}
	return "", false
}

func addJavaRef(scan *javaReferenceScan, id, who string) {
	if slices.Contains(scan.byRuntime[id], who) {
		return
	}
	scan.byRuntime[id] = append(scan.byRuntime[id], who)
}

// printGlobalJava 输出全局默认 runtime 状态；未设置时给出操作提示。
func printGlobalJava(refs javaReferenceScan) {
	switch {
	case refs.globalID == "":
		fmt.Println("[Java] 全局默认: 未设置（可执行 mtl java use <runtime-id>）")
	case refs.globalFound:
		fmt.Printf("[Java] 全局默认: %s\n", refs.globalID)
	default:
		fmt.Fprintf(os.Stderr, "[Java] 警告: 全局默认运行时 %q 不存在，请重新执行 mtl java use\n", refs.globalID)
	}
}

// printJavaDetail 输出 --debug 的完整元信息与目录结构。
func printJavaDetail(inst runtime.Installed, refs []string) {
	fmt.Printf("\n[Java] %s\n", inst.ID)
	fmt.Printf("  目录: %s\n", inst.Dir)
	fmt.Printf("  版本: %s\n", orDash(inst.Version))
	fmt.Printf("  平台: %s（%s / %s）\n", orDash(inst.Platform), orDash(inst.OS), orDash(inst.Arch))
	fmt.Printf("  java: %s\n", orDash(inst.JavaPath))
	if inst.Valid {
		fmt.Println("  状态: 正常")
	} else {
		fmt.Printf("  状态: 异常（%s）\n", inst.Err)
	}
	fmt.Printf("  引用: %s\n", orDash(strings.Join(refs, ", ")))
	fmt.Println("  目录结构（最多 2 层）:")
	printDirTree(inst.Dir, 2)
}

// printDirTree 打印有限深度的目录结构。
// 运行时 lib/ 等目录文件较多，超过上限时截断，避免 --debug 输出失控。
func printDirTree(root string, maxDepth int) {
	const maxEntries = 120
	printed := 0
	var walk func(dir, indent string, depth int)
	walk = func(dir, indent string, depth int) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			fmt.Printf("%s<读取失败: %v>\n", indent, err)
			return
		}
		for _, de := range entries {
			if printed >= maxEntries {
				fmt.Printf("%s...（条目过多，已省略）\n", indent)
				return
			}
			printed++
			name := de.Name()
			if de.IsDir() {
				name += "/"
			}
			fmt.Printf("%s%s\n", indent, name)
			if de.IsDir() && depth < maxDepth {
				walk(filepath.Join(dir, de.Name()), indent+"  ", depth+1)
			}
		}
	}
	walk(root, "    ", 1)
}

// formatBytes 把字节数格式化为便于阅读的二进制单位。
func formatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	value := float64(n)
	for i, unit := range units {
		value /= 1024
		if value < 1024 || i == len(units)-1 {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
	}
	return fmt.Sprintf("%d B", n)
}
