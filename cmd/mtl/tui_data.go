package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"mtl/internal/backup"
	"mtl/internal/config"
	"mtl/internal/diagnose"
	"mtl/internal/instance"
	"mtl/internal/lastrun"
	"mtl/internal/launch"
	"mtl/internal/mod"
	"mtl/internal/mrpack"
	"mtl/internal/runtime"
)

// TUI 消息：所有耗时操作都通过 tea.Cmd 异步执行，避免阻塞按键响应。
type tuiInstancesMsg struct {
	entries []instance.Entry
	skipped []instance.Skip
	err     error
}

type tuiModsMsg struct {
	mods     []mod.Mod
	warnings []string
	err      error
}

type tuiBackupsMsg struct {
	items []backup.Listed
	err   error
}

type tuiJavaMsg struct {
	runtimes    []runtime.Installed
	refs        map[string][]string
	globalID    string
	globalFound bool
	warnings    []string
	err         error
}

type tuiDiagnoseMsg struct {
	view tuiDiagnoseView
	err  error
}

type tuiConfigUpdatedMsg struct {
	cfg     config.Config
	text    string
	warning string
	err     error
}

type tuiModsActionMsg struct {
	text string
	err  error
}

type tuiJavaActionMsg struct {
	text string
	err  error
}

type tuiInstanceDeleteMsg struct {
	text string
	err  error
}

type tuiNoticeMsg struct {
	text string
	err  error
}

// loadInstancesCmd 只读扫描实例根目录，复用 instance.Scan。
func loadInstancesCmd(root string) tea.Cmd {
	return func() tea.Msg {
		entries, skipped, err := instance.Scan(root)
		return tuiInstancesMsg{entries: entries, skipped: skipped, err: err}
	}
}

// loadModsCmd 读取 mods 与 mods/disabled，复用 mod.Scan / mod.Check。
func loadModsCmd(dir, loader string) tea.Cmd {
	return func() tea.Msg {
		mods, err := mod.Scan(dir)
		if err != nil {
			return tuiModsMsg{err: err}
		}
		return tuiModsMsg{mods: mods, warnings: mod.Check(mods, loader)}
	}
}

func loadBackupsCmd() tea.Cmd {
	return func() tea.Msg {
		items, err := backup.List("")
		return tuiBackupsMsg{items: items, err: err}
	}
}

// loadJavaCmd 列出全局运行时，并统计实例与全局默认的引用关系。
func loadJavaCmd(root string) tea.Cmd {
	return func() tea.Msg {
		installed, err := runtime.List("")
		if err != nil {
			return tuiJavaMsg{err: err}
		}
		refs, warnings := tuiJavaReferences(root, installed)
		return tuiJavaMsg{
			runtimes:    installed,
			refs:        refs.byRuntime,
			globalID:    refs.globalID,
			globalFound: refs.globalFound,
			warnings:    warnings,
		}
	}
}

// tuiJavaReferences 与 collectJavaReferences 的判定规则保持一致，
// 但把告警收集起来交给 TUI 展示，避免直接写 stderr 破坏全屏渲染。
func tuiJavaReferences(root string, installed []runtime.Installed) (javaReferenceScan, []string) {
	scan := javaReferenceScan{byRuntime: make(map[string][]string)}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	scan.root = abs

	var warnings []string
	cfg, cfgErr := config.LoadGlobal()
	if cfgErr != nil {
		warnings = append(warnings, cfgErr.Error())
	}
	scan.globalID = strings.TrimSpace(cfg.DefaultJava)
	if scan.globalID != "" {
		if inst, ok := runtimeByID(installed, scan.globalID); ok {
			scan.globalFound = true
			addJavaRef(&scan, inst.ID, "全局默认")
		} else {
			warnings = append(warnings, fmt.Sprintf("全局默认运行时 %q 不存在，请重新设置", scan.globalID))
		}
	}

	entries, skipped, err := instance.Scan(abs)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			warnings = append(warnings, fmt.Sprintf("扫描实例目录失败: %v", err))
		}
		return scan, warnings
	}
	scan.instanceCount = len(entries)
	for _, s := range skipped {
		warnings = append(warnings, fmt.Sprintf("跳过实例 %s（%v）", s.Name, s.Reason))
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
	return scan, warnings
}

// loadDiagnoseCmd 复用 diagnose 包的日志发现、尾部读取与关键词分析。
func loadDiagnoseCmd(dir, name string) tea.Cmd {
	return func() tea.Msg {
		view := tuiDiagnoseView{name: name, dir: dir}

		var rec *lastrun.Record
		if r, err := lastrun.Load(); err == nil && sameInstanceDir(r.InstanceDir, dir) {
			record := r
			rec = &record
		}

		logPath, err := instanceLogForDiagnose(dir, rec)
		if err != nil {
			view.warnings = append(view.warnings, "查找运行日志失败: "+err.Error())
		}
		view.logPath = logPath
		crashPath, err := diagnose.LatestCrashFile(dir)
		if err != nil {
			view.warnings = append(view.warnings, "查找崩溃报告失败: "+err.Error())
		}
		view.crashPath = crashPath

		logText, readErr := diagnose.ReadTextTail(logPath, diagnose.MaxLogBytes)
		if readErr != nil {
			view.warnings = append(view.warnings, "读取运行日志失败: "+readErr.Error())
			logText = ""
		}
		crashText, readErr := diagnose.ReadTextTail(crashPath, diagnose.MaxCrashBytes)
		if readErr != nil {
			view.warnings = append(view.warnings, "读取崩溃报告失败: "+readErr.Error())
			crashText = ""
		}

		var exitCode *int
		if rec != nil {
			exitCode = rec.ExitCode
		}
		view.report = diagnose.Analyze(diagnose.Input{
			InstanceName: name,
			InstanceDir:  dir,
			LogFile:      logPath,
			CrashFile:    crashPath,
			Log:          logText,
			Crash:        crashText,
			ExitCode:     exitCode,
			CrashStale:   crashPath != "" && !diagnose.CrashIsCurrent(crashPath, logPath, 0),
		})

		cfg, cfgErr := config.Load(dir)
		view.cfg = cfg
		if cfgErr != nil {
			view.cfgErr = cfgErr.Error()
		}
		return tuiDiagnoseMsg{view: view}
	}
}

// saveInstanceConfigCmd 读取、修改并原子保存实例配置。
// 修改函数返回错误时保持配置文件不变。
func saveInstanceConfigCmd(dir, text string, mutate func(*config.Config) error) tea.Cmd {
	return func() tea.Msg {
		cfg, loadErr := config.Load(dir)
		warning := ""
		if loadErr != nil {
			// 与 CLI 保持一致：配置损坏时基于默认值继续，但把原因带回到 TUI。
			warning = loadErr.Error()
		}
		if err := mutate(&cfg); err != nil {
			return tuiConfigUpdatedMsg{err: err}
		}
		if err := config.Save(dir, cfg); err != nil {
			return tuiConfigUpdatedMsg{err: err}
		}
		return tuiConfigUpdatedMsg{cfg: cfg, text: text, warning: warning}
	}
}

// setInstanceJavaPath 复用现有 Java 路径校验，校验通过后写入绝对路径。
func setInstanceJavaPath(cfg *config.Config, raw string) error {
	abs, err := launch.ValidateJavaPath(context.Background(), raw)
	if err != nil {
		return fmt.Errorf("Java 路径校验失败: %w", err)
	}
	cfg.Java = abs
	return nil
}

func accountNotice(name string) string {
	if name == "" {
		return "已解绑默认账号"
	}
	return "已绑定默认账号: " + name
}

// launchGameCmd 暂停 TUI，调用同一个二进制的 mtl run，完整复用现有启动链路。
func launchGameCmd(entry instance.Entry) tea.Cmd {
	exe, err := os.Executable()
	if err != nil {
		return func() tea.Msg {
			return tuiNoticeMsg{text: "无法定位 mtl 可执行文件: " + err.Error(), err: err}
		}
	}
	cmd := exec.Command(exe, "run", "--dir", entry.Dir)
	return tea.ExecProcess(cmd, func(runErr error) tea.Msg {
		if runErr != nil {
			return tuiNoticeMsg{text: "游戏进程已退出（" + runErr.Error() + "）"}
		}
		return tuiNoticeMsg{text: "游戏进程已退出"}
	})
}

// createBackupCmd 复用 backup.BuildPlan / WriteArchive，写入默认备份目录。
func createBackupCmd(instanceName, dir string) tea.Cmd {
	return func() tea.Msg {
		plan, err := backup.BuildPlan(dir)
		if err != nil {
			return tuiNoticeMsg{text: "创建备份失败: " + err.Error(), err: err}
		}
		res, err := backup.WriteArchive(plan, instanceName, backup.WriteOptions{Now: time.Now()})
		if err != nil {
			return tuiNoticeMsg{text: "创建备份失败: " + err.Error(), err: err}
		}
		return tuiNoticeMsg{text: fmt.Sprintf("备份完成: %s（%s）", res.Path, formatBytes(res.ArchiveSize))}
	}
}

// deleteInstanceCmd 只允许删除实例根目录下的一级目录，且要求名称合法。
func deleteInstanceCmd(root, name string) tea.Cmd {
	return func() tea.Msg {
		if err := validateSingleInstanceName(name); err != nil {
			return tuiInstanceDeleteMsg{text: "删除实例失败: " + err.Error(), err: err}
		}
		dir, err := mrpack.SafeJoin(root, name)
		if err != nil {
			return tuiInstanceDeleteMsg{text: "删除实例失败: " + err.Error(), err: err}
		}
		info, err := os.Stat(dir)
		if err != nil {
			return tuiInstanceDeleteMsg{text: "删除实例失败: " + err.Error(), err: err}
		}
		if !info.IsDir() {
			return tuiInstanceDeleteMsg{text: "删除实例失败: 目标不是目录", err: fmt.Errorf("目标不是目录: %s", dir)}
		}
		if err := os.RemoveAll(dir); err != nil {
			return tuiInstanceDeleteMsg{text: "删除实例失败: " + err.Error(), err: err}
		}
		return tuiInstanceDeleteMsg{text: "已删除实例: " + name}
	}
}

// applyModEnabledCmd 复用 mod.SetEnabled；操作完成后重新扫描 mod 列表。
func applyModEnabledCmd(dir, file string, enabled bool) tea.Cmd {
	return func() tea.Msg {
		m, changed, err := mod.SetEnabled(dir, file, enabled)
		if err != nil {
			return tuiModsActionMsg{text: "操作失败: " + err.Error(), err: err}
		}
		verb := "禁用"
		if enabled {
			verb = "启用"
		}
		if !changed {
			return tuiModsActionMsg{text: fmt.Sprintf("%s 已处于%s状态", m.File, verb)}
		}
		return tuiModsActionMsg{text: fmt.Sprintf("已%s %s", verb, m.File)}
	}
}

// removeModCmd 复用 mod.Remove。
func removeModCmd(dir, file string) tea.Cmd {
	return func() tea.Msg {
		m, err := mod.Remove(dir, file)
		if err != nil {
			return tuiModsActionMsg{text: "删除 Mod 失败: " + err.Error(), err: err}
		}
		return tuiModsActionMsg{text: "已删除 " + m.File}
	}
}

// setDefaultJavaCmd 与 mtl java use 保持相同的校验和写入流程。
func setDefaultJavaCmd(id string) tea.Cmd {
	return func() tea.Msg {
		inst, err := runtime.Find("", id)
		if err != nil {
			return tuiJavaActionMsg{text: "设置失败: " + err.Error(), err: err}
		}
		if !inst.Valid {
			return tuiJavaActionMsg{text: "设置失败: " + inst.Err, err: fmt.Errorf("%s", inst.Err)}
		}
		if err := runtime.VerifyExecutable(context.Background(), inst.JavaPath); err != nil {
			return tuiJavaActionMsg{text: "Java 运行时不可用: " + err.Error(), err: err}
		}
		cfg, loadErr := config.LoadGlobal()
		warning := ""
		if loadErr != nil {
			warning = "（警告: " + loadErr.Error() + "）"
		}
		cfg.DefaultJava = inst.ID
		if err := config.SaveGlobal(cfg); err != nil {
			return tuiJavaActionMsg{text: "设置失败: " + err.Error(), err: err}
		}
		return tuiJavaActionMsg{text: fmt.Sprintf("已设置全局默认 Java: %s（%s）", inst.ID, orDash(inst.Version)) + warning}
	}
}

// pruneJavaCmd 与 mtl java prune 保持相同的引用保护规则。
func pruneJavaCmd(root string) tea.Cmd {
	return func() tea.Msg {
		installed, err := runtime.List("")
		if err != nil {
			return tuiJavaActionMsg{text: "清理失败: " + err.Error(), err: err}
		}
		refs, _ := tuiJavaReferences(root, installed)
		keep := make(map[string]string, len(refs.byRuntime))
		for id, reasons := range refs.byRuntime {
			keep[id] = strings.Join(reasons, "、")
		}
		res, err := runtime.Prune(runtime.PruneOptions{Keep: keep})
		if err != nil {
			return tuiJavaActionMsg{text: "清理失败: " + err.Error(), err: err}
		}
		switch {
		case len(res.Removed) > 0:
			return tuiJavaActionMsg{text: fmt.Sprintf("已清理 %d 个运行时，释放 %s",
				len(res.Removed), formatBytes(res.FreedBytes))}
		case len(res.Kept) > 0:
			return tuiJavaActionMsg{text: fmt.Sprintf("没有可清理的运行时（%d 个被引用，已跳过）", len(res.Kept))}
		default:
			return tuiJavaActionMsg{text: "没有可清理的 Java 运行时"}
		}
	}
}
