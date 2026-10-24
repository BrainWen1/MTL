package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mtl/internal/config"
	"mtl/internal/mod"
)

// modCommand 处理 mtl mod 子命令。
// 所有操作都针对单个实例目录（--dir），不扫描实例根目录。
func modCommand(args []string) error {
	if len(args) == 0 {
		return modUsageError()
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return modList(rest)
	case "enable":
		return modSetEnabled(rest, true)
	case "disable":
		return modSetEnabled(rest, false)
	case "rm":
		return modRemove(rest)
	default:
		return fmt.Errorf("未知 mod 子命令 %q（可用: list、enable、disable、rm）", sub)
	}
}

func modUsageError() error {
	return errors.New("用法: mtl mod list --dir <实例目录> [--debug]；mtl mod enable|disable|rm <mod-id/文件名> --dir <实例目录>")
}

// modOptions 是 mod 子命令的公共参数。
type modOptions struct {
	dir        string
	debug      bool
	positional []string
}

// parseModOptions 手工解析参数，允许 --dir 出现在位置参数之后，
// 例如 `mtl mod disable sodium --dir ./instances/demo`。
func parseModOptions(args []string) (modOptions, error) {
	var opts modOptions
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dir":
			if i+1 >= len(args) {
				return opts, errors.New("--dir 缺少参数值")
			}
			i++
			opts.dir = args[i]
		case strings.HasPrefix(a, "--dir="):
			opts.dir = strings.TrimPrefix(a, "--dir=")
		case a == "--debug":
			opts.debug = true
		case strings.HasPrefix(a, "-"):
			return opts, fmt.Errorf("未知参数 %q", a)
		default:
			opts.positional = append(opts.positional, a)
		}
	}
	if strings.TrimSpace(opts.dir) == "" {
		return opts, errors.New("必须用 --dir 指定实例目录")
	}
	return opts, nil
}

// resolveModInstance 校验实例目录并读取实例配置。
// 配置缺失或损坏时回退默认配置并告警，不阻断 mod 管理（mods 目录仍可独立操作）。
func resolveModInstance(dir string) (string, config.Config, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", config.Config{}, errors.New("实例目录为空")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", config.Config{}, fmt.Errorf("解析实例目录失败: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", config.Config{}, fmt.Errorf("实例目录不存在: %s", abs)
		}
		return "", config.Config{}, fmt.Errorf("读取实例目录失败: %w", err)
	}
	if !info.IsDir() {
		return "", config.Config{}, fmt.Errorf("实例路径不是目录: %s", abs)
	}
	cfg, err := config.Load(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[Mod] 警告: %v；将按默认配置继续\n", err)
		cfg = config.Default()
	}
	return abs, cfg, nil
}

// modList 输出 mods 目录概况；默认表格，--debug 追加完整元数据。
func modList(args []string) error {
	opts, err := parseModOptions(args)
	if err != nil {
		return err
	}
	if len(opts.positional) != 0 {
		return errors.New("用法: mtl mod list --dir <实例目录> [--debug]")
	}
	dir, cfg, err := resolveModInstance(opts.dir)
	if err != nil {
		return err
	}
	mods, err := mod.Scan(dir)
	if err != nil {
		return err
	}

	// 缺少实例配置时不做类型猜测，避免把手工目录误标为 mrpack 实例。
	mode := "未识别实例类型（缺少 .mtl/config.json）"
	switch {
	case cfg.InstallMode == config.InstallModeTemplate:
		mode = "纯净模板实例"
	case cfg.LoaderType != "":
		mode = "mrpack 整合包实例"
	}
	loader := orDash(cfg.LoaderType)
	if cfg.LoaderVersion != "" {
		loader += " " + cfg.LoaderVersion
	}
	fmt.Printf("[Mod] 实例: %s（%s）\n", dir, mode)
	fmt.Printf("[Mod] 加载器: %s\n", loader)
	if len(mods) == 0 {
		fmt.Println("[Mod] 未发现 mod 文件（mods 目录为空或不存在）")
		return nil
	}

	rows := make([][]string, 0, len(mods)+1)
	rows = append(rows, []string{"状态", "MOD ID", "名称", "版本", "加载器", "依赖", "文件"})
	for _, m := range mods {
		rows = append(rows, []string{
			modStatus(m),
			orDash(m.ID),
			m.DisplayName(),
			orDash(m.Version),
			m.Loader.DisplayName(),
			formatDeps(m.Dependencies),
			m.File,
		})
	}
	writeTable(os.Stdout, rows)

	if opts.debug {
		fmt.Println("\n[Mod] 完整元数据:")
		for _, m := range mods {
			printModDetail(m)
		}
	}

	enabled, disabled, unknown := 0, 0, 0
	for _, m := range mods {
		if m.Disabled {
			disabled++
		} else {
			enabled++
		}
		if !m.Known {
			unknown++
		}
	}
	fmt.Printf("\n[Mod] 共 %d 个 mod（启用 %d，禁用 %d）", len(mods), enabled, disabled)
	if unknown > 0 {
		fmt.Printf("，其中 %d 个未知mod", unknown)
	}
	fmt.Println()

	for _, w := range mod.Check(mods, cfg.LoaderType) {
		fmt.Fprintf(os.Stderr, "[Mod] 警告: %s\n", w)
	}
	return nil
}

// modSetEnabled 启用或禁用目标 mod；已是目标状态时幂等返回成功。
func modSetEnabled(args []string, enable bool) error {
	opts, err := parseModOptions(args)
	if err != nil {
		return err
	}
	if len(opts.positional) != 1 {
		return errors.New("用法: mtl mod enable|disable <mod-id/文件名> --dir <实例目录>")
	}
	dir, _, err := resolveModInstance(opts.dir)
	if err != nil {
		return err
	}

	verb := "禁用"
	if enable {
		verb = "启用"
	}
	m, changed, err := mod.SetEnabled(dir, opts.positional[0], enable)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Printf("[Mod] %s 已处于%s状态: %s\n", m.File, verb, m.Path)
		return nil
	}
	fmt.Printf("[Mod] 已%s %s: %s\n", verb, m.File, m.Path)
	return nil
}

// modRemove 删除目标 mod 文件，启用或禁用状态均可删除。
func modRemove(args []string) error {
	opts, err := parseModOptions(args)
	if err != nil {
		return err
	}
	if len(opts.positional) != 1 {
		return errors.New("用法: mtl mod rm <mod-id/文件名> --dir <实例目录>")
	}
	dir, _, err := resolveModInstance(opts.dir)
	if err != nil {
		return err
	}
	m, err := mod.Remove(dir, opts.positional[0])
	if err != nil {
		return err
	}
	fmt.Printf("[Mod] 已删除 %s（%s）\n", m.Path, m.DisplayName())
	return nil
}

func modStatus(m mod.Mod) string {
	if m.Disabled {
		return "禁用"
	}
	return "启用"
}

// formatDeps 把依赖压缩成表格单元格：可选依赖带 ? 后缀。
func formatDeps(deps []mod.Dependency) string {
	if len(deps) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(deps))
	for _, d := range deps {
		s := d.ID
		if !d.Required {
			s += "?"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

// printModDetail 输出单个 mod 的完整元数据，供 --debug 使用。
func printModDetail(m mod.Mod) {
	fmt.Printf("\n[Mod] %s\n", m.Path)
	fmt.Printf("  状态: %s\n", modStatus(m))
	fmt.Printf("  加载器: %s\n", m.Loader.DisplayName())
	fmt.Printf("  MOD ID: %s\n", orDash(m.ID))
	fmt.Printf("  名称: %s\n", m.DisplayName())
	fmt.Printf("  版本: %s\n", orDash(m.Version))
	if len(m.Provides) > 0 {
		fmt.Printf("  附带模块: %s\n", strings.Join(m.Provides, ", "))
	}
	if !m.Known {
		fmt.Printf("  错误: %s\n", m.Err)
	}
	if len(m.Dependencies) == 0 {
		fmt.Println("  依赖: -")
		return
	}
	fmt.Println("  依赖:")
	for _, d := range m.Dependencies {
		kind := "必需"
		if !d.Required {
			kind = "可选"
		}
		version := d.VersionRange
		if strings.TrimSpace(version) == "" {
			version = "任意版本"
		}
		fmt.Printf("    - %s（%s，%s）\n", d.ID, kind, version)
	}
}
