package main

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"mtl/internal/backup"
	"mtl/internal/config"
	"mtl/internal/instance"
	"mtl/internal/runtime"
)

var tuiHomeItems = []string{
	"实例管理",
	"Mod 管理",
	"备份管理",
	"Java 运行时",
	"崩溃诊断",
}

var tuiHomeDescriptions = []string{
	"启动 / 配置 / 备份 / 删除实例",
	"启用 / 禁用 / 删除本地 mod",
	"查看本地备份包",
	"查看 / 设为默认 / 清理运行时",
	"查看最近一次运行诊断",
}

var tuiInstanceActions = []string{
	"启动游戏",
	"实例配置",
	"创建备份",
	"删除实例",
}

func (m tuiModel) View() string {
	var b strings.Builder
	b.WriteString("MTL · My Tiny Launcher")
	if title := tuiPageTitle(m.page); title != "" {
		b.WriteString("  /  " + title)
	}
	b.WriteString("\n")
	b.WriteString("实例根目录: " + m.root + "\n")
	b.WriteString(strings.Repeat("─", 48) + "\n")

	if m.modal.active {
		b.WriteString(m.viewModal())
	} else {
		switch m.page {
		case tuiPageHome:
			b.WriteString(m.viewHome())
		case tuiPageInstances:
			b.WriteString(m.viewInstances())
		case tuiPageInstanceMenu:
			b.WriteString(m.viewInstanceMenu())
		case tuiPageInstanceConfig:
			b.WriteString(m.viewInstanceConfig())
		case tuiPageMods:
			b.WriteString(m.viewMods())
		case tuiPageModMenu:
			b.WriteString(m.viewModMenu())
		case tuiPageBackups:
			b.WriteString(m.viewBackups())
		case tuiPageBackupDetail:
			b.WriteString(m.viewBackupDetail())
		case tuiPageJava:
			b.WriteString(m.viewJava())
		case tuiPageDiagnose:
			b.WriteString(m.viewDiagnose())
		}
	}

	if m.notice != "" {
		prefix := "[OK] "
		if m.isErr {
			prefix = "[错误] "
		}
		b.WriteString("\n" + prefix + m.notice + "\n")
	}
	b.WriteString("\n" + m.viewFooter())
	return limitDisplayWidth(b.String(), m.width)
}

func tuiPageTitle(page tuiPage) string {
	switch page {
	case tuiPageHome:
		return ""
	case tuiPageInstances:
		return "实例列表"
	case tuiPageInstanceMenu:
		return "实例操作"
	case tuiPageInstanceConfig:
		return "实例配置"
	case tuiPageMods:
		return "Mod 管理"
	case tuiPageModMenu:
		return "Mod 操作"
	case tuiPageBackups:
		return "备份列表"
	case tuiPageBackupDetail:
		return "备份详情"
	case tuiPageJava:
		return "Java 运行时"
	case tuiPageDiagnose:
		return "崩溃诊断"
	default:
		return ""
	}
}

func (m tuiModel) viewHome() string {
	var b strings.Builder
	switch {
	case m.instErr != nil:
		b.WriteString("实例扫描: 失败（" + m.instErr.Error() + "）\n")
	case len(m.instances) == 0:
		b.WriteString("实例扫描: 未发现实例\n")
	default:
		b.WriteString(fmt.Sprintf("实例扫描: %d 个实例\n", len(m.instances)))
	}
	if len(m.skipped) > 0 {
		b.WriteString(fmt.Sprintf("跳过目录: %d 个（缺少或损坏 .mtl/config.json）\n", len(m.skipped)))
	}
	b.WriteString("\n")
	for i, item := range tuiHomeItems {
		marker := "  "
		if i == m.homeCursor {
			marker = "> "
		}
		b.WriteString(marker + padDisplay(item, 14) + tuiHomeDescriptions[i] + "\n")
	}
	return b.String()
}

func (m tuiModel) viewInstances() string {
	var b strings.Builder
	switch m.instMode {
	case tuiInstanceMods:
		b.WriteString("选择要管理 Mod 的实例\n\n")
	case tuiInstanceDiagnose:
		b.WriteString("选择要诊断的实例\n\n")
	default:
		b.WriteString("选择实例后打开操作菜单\n\n")
	}
	if m.instErr != nil {
		b.WriteString("扫描实例失败: " + m.instErr.Error() + "\n")
		return b.String()
	}
	if len(m.instances) == 0 {
		b.WriteString("未发现可识别的实例。请先使用 mtl install 创建实例，或用 --dir 指定实例根目录。\n")
		return b.String()
	}

	rows := [][]string{{"  实例名", "加载器", "版本", "默认账号", "Java"}}
	start, end := windowBounds(m.instCursor, len(m.instances), m.listCapacity())
	for i := start; i < end; i++ {
		e := m.instances[i]
		marker := "  "
		if i == m.instCursor {
			marker = "> "
		}
		rows = append(rows, []string{
			marker + e.Name,
			orDash(e.Config.LoaderType),
			orDash(e.Config.LoaderVersion),
			orDash(e.Config.DefaultAccount),
			javaCell(e.JavaStatus),
		})
	}
	writeTable(&b, rows)
	if end < len(m.instances) || start > 0 {
		b.WriteString(fmt.Sprintf("\n显示 %d-%d / 共 %d 个实例\n", start+1, end, len(m.instances)))
	}
	return b.String()
}

func (m tuiModel) viewInstanceMenu() string {
	var b strings.Builder
	b.WriteString("实例: " + m.selected.Name + "\n")
	b.WriteString("目录: " + m.selected.Dir + "\n")
	b.WriteString("加载器: " + loaderCell(m.selected.Config) + "\n")
	b.WriteString("默认账号: " + orDash(m.selected.Config.DefaultAccount) + "\n")
	b.WriteString("Java: " + m.selected.JavaStatus + "\n\n")
	for i, item := range tuiInstanceActions {
		marker := "  "
		if i == m.actionCursor {
			marker = "> "
		}
		b.WriteString(marker + item + "\n")
	}
	b.WriteString("\n提示: 启动游戏会暂停 TUI，直接复用 mtl run 的完整启动链路。\n")
	return b.String()
}

func (m tuiModel) configActions() []string {
	memoryLabel := "切换为手动内存"
	if m.configData.MemoryMode == config.ModeManual {
		memoryLabel = "切换为自动内存"
	}
	gpuLabel := "开启高性能显卡"
	if m.configData.PreferHighPerfGPU {
		gpuLabel = "关闭高性能显卡"
	}
	return []string{
		memoryLabel,
		"设置手动内存值",
		gpuLabel,
		"设置默认账号",
		"设置 Java 路径",
		"添加 JVM 参数",
		"移除 JVM 参数",
		"添加游戏参数",
		"移除游戏参数",
		"重置实例配置",
	}
}

func (m tuiModel) viewInstanceConfig() string {
	var b strings.Builder
	cfg := m.configData
	b.WriteString("实例: " + m.selected.Name + "\n")
	b.WriteString("加载器: " + loaderCell(cfg) + "\n")
	memory := "自动"
	if cfg.MemoryMode == config.ModeManual {
		memory = "手动 " + orDash(cfg.ManualMemory)
	}
	b.WriteString("内存: " + memory + "\n")
	b.WriteString("高性能显卡: " + boolLabel(cfg.PreferHighPerfGPU) + "\n")
	b.WriteString("默认账号: " + orDash(cfg.DefaultAccount) + "\n")
	b.WriteString("Java: " + javaCell(m.selected.JavaStatus) + "\n")
	b.WriteString(fmt.Sprintf("JVM 参数（%d）: %s\n", len(cfg.JVMArgs), listOrDash(cfg.JVMArgs)))
	b.WriteString(fmt.Sprintf("游戏参数（%d）: %s\n", len(cfg.GameArgs), listOrDash(cfg.GameArgs)))

	if len(m.configHealth) > 0 {
		failures, warnings := instance.CountHealth(m.configHealth)
		b.WriteString(fmt.Sprintf("\n健康检查: %d 项异常，%d 项警告\n", failures, warnings))
		for _, c := range m.configHealth {
			b.WriteString(fmt.Sprintf("  [%s] %s: %s\n", c.Level, c.Item, c.Detail))
		}
	}

	b.WriteString("\n配置操作:\n")
	actions := m.configActions()
	for i, item := range actions {
		marker := "  "
		if i == m.configCursor {
			marker = "> "
		}
		b.WriteString(marker + item + "\n")
	}
	return b.String()
}

func (m tuiModel) modActions() []string {
	if len(m.mods) == 0 {
		return []string{"启用/禁用 Mod", "删除 Mod"}
	}
	target := m.mods[m.modCursor]
	if target.Disabled {
		return []string{"启用 Mod", "删除 Mod"}
	}
	return []string{"禁用 Mod", "删除 Mod"}
}

func (m tuiModel) viewMods() string {
	var b strings.Builder
	b.WriteString("实例: " + m.selected.Name + "\n")
	if m.modBusy {
		b.WriteString("正在读取 mod 元数据…\n")
		return b.String()
	}
	if m.modErr != nil {
		b.WriteString("读取 Mod 失败: " + m.modErr.Error() + "\n")
		return b.String()
	}
	enabled, disabled := 0, 0
	for _, item := range m.mods {
		if item.Disabled {
			disabled++
		} else {
			enabled++
		}
	}
	b.WriteString(fmt.Sprintf("共 %d 个 mod（启用 %d，禁用 %d）\n\n", len(m.mods), enabled, disabled))
	if len(m.mods) == 0 {
		b.WriteString("未发现 mod 文件（mods 目录为空或不存在）。\n")
	} else {
		rows := [][]string{{"  状态", "MOD ID", "名称", "版本", "文件"}}
		start, end := windowBounds(m.modCursor, len(m.mods), m.listCapacity())
		for i := start; i < end; i++ {
			item := m.mods[i]
			marker := "  "
			if i == m.modCursor {
				marker = "> "
			}
			status := "启用"
			if item.Disabled {
				status = "禁用"
			}
			rows = append(rows, []string{
				marker + status,
				orDash(item.ID),
				item.DisplayName(),
				orDash(item.Version),
				item.File,
			})
		}
		writeTable(&b, rows)
		if end < len(m.mods) || start > 0 {
			b.WriteString(fmt.Sprintf("\n显示 %d-%d / 共 %d 个 mod\n", start+1, end, len(m.mods)))
		}
	}
	if len(m.modWarnings) > 0 {
		b.WriteString("\n警告:\n")
		for _, w := range m.modWarnings {
			b.WriteString("  - " + w + "\n")
		}
	}
	return b.String()
}

func (m tuiModel) viewModMenu() string {
	var b strings.Builder
	if len(m.mods) == 0 {
		b.WriteString("未选择 mod\n")
		return b.String()
	}
	item := m.mods[m.modCursor]
	b.WriteString("Mod: " + item.DisplayName() + "\n")
	b.WriteString("ID: " + orDash(item.ID) + "\n")
	b.WriteString("版本: " + orDash(item.Version) + "\n")
	b.WriteString("文件: " + item.Path + "\n")
	status := "启用"
	if item.Disabled {
		status = "禁用"
	}
	b.WriteString("状态: " + status + "\n\n")
	for i, action := range m.modActions() {
		marker := "  "
		if i == m.modActionCursor {
			marker = "> "
		}
		b.WriteString(marker + action + "\n")
	}
	return b.String()
}

func (m tuiModel) viewBackups() string {
	var b strings.Builder
	if dir, err := backup.DefaultDir(); err == nil {
		b.WriteString("备份目录: " + dir + "\n\n")
	}
	if m.backupErr != nil {
		b.WriteString("读取备份失败: " + m.backupErr.Error() + "\n")
		return b.String()
	}
	if len(m.backups) == 0 {
		b.WriteString("没有本地备份。可在「实例管理 → 创建备份」生成。\n")
		return b.String()
	}

	rows := [][]string{{"  备份文件", "备份时间", "来源实例", "备注", "大小"}}
	start, end := windowBounds(m.backupCursor, len(m.backups), m.listCapacity())
	for i := start; i < end; i++ {
		item := m.backups[i]
		marker := "  "
		if i == m.backupCursor {
			marker = "> "
		}
		when := item.Info.ModTime.Local().Format("2006-01-02 15:04:05")
		source, note := "(无效)", "-"
		if item.Err == "" {
			when = item.Info.Metadata.CreatedAt.Local().Format("2006-01-02 15:04:05")
			source = orDash(item.Info.Metadata.Instance)
			note = orDash(item.Info.Metadata.Name)
		}
		rows = append(rows, []string{
			marker + filepath.Base(item.Info.Path),
			when,
			source,
			note,
			formatBytes(item.Info.Size),
		})
	}
	writeTable(&b, rows)
	if end < len(m.backups) || start > 0 {
		b.WriteString(fmt.Sprintf("\n显示 %d-%d / 共 %d 个备份\n", start+1, end, len(m.backups)))
	}
	return b.String()
}

func (m tuiModel) viewBackupDetail() string {
	var b strings.Builder
	if m.backupSelected == nil {
		b.WriteString("未选择备份\n")
		return b.String()
	}
	info := *m.backupSelected
	b.WriteString("文件: " + info.Path + "\n")
	b.WriteString("大小: " + formatBytes(info.Size) + "\n")
	b.WriteString("修改时间: " + info.ModTime.Local().Format("2006-01-02 15:04:05") + "\n")
	if m.backupDetailErr != "" {
		b.WriteString("状态: 不是有效的 mtl 备份（" + m.backupDetailErr + "）\n")
		return b.String()
	}
	b.WriteString("来源实例: " + orDash(info.Metadata.Instance) + "\n")
	b.WriteString("备份时间: " + info.Metadata.CreatedAt.Local().Format("2006-01-02 15:04:05") + "\n")
	b.WriteString("备注: " + orDash(info.Metadata.Name) + "\n")
	b.WriteString("包含: " + joinOrDash(info.Metadata.Include) + "\n")
	b.WriteString("\n还原请使用: mtl restore \"" + info.Path + "\" --target <实例名> [--force]\n")
	return b.String()
}

func (m tuiModel) viewJava() string {
	var b strings.Builder
	if dir, err := runtime.DefaultDir(); err == nil {
		b.WriteString("运行时缓存: " + dir + "\n")
	}
	switch {
	case m.javaGlobalID == "":
		b.WriteString("全局默认: 未设置\n")
	case m.javaGlobalFound:
		b.WriteString("全局默认: " + m.javaGlobalID + "\n")
	default:
		b.WriteString("全局默认: " + m.javaGlobalID + "（不存在）\n")
	}
	if m.javaBusy {
		b.WriteString("\n正在处理…\n")
		return b.String()
	}
	if m.javaErr != nil {
		b.WriteString("\n读取运行时失败: " + m.javaErr.Error() + "\n")
		return b.String()
	}
	if len(m.javaRuntimes) == 0 {
		b.WriteString("\n未发现已缓存的 Java 运行时。\n")
	} else {
		b.WriteString("\n")
		rows := [][]string{{"  标识", "版本", "平台", "状态", "默认", "引用"}}
		start, end := windowBounds(m.javaCursor, len(m.javaRuntimes), m.listCapacity())
		for i := start; i < end; i++ {
			item := m.javaRuntimes[i]
			marker := "  "
			if i == m.javaCursor {
				marker = "> "
			}
			status := "正常"
			if !item.Valid {
				status = "异常"
			}
			isDefault := "-"
			if strings.EqualFold(item.ID, m.javaGlobalID) {
				isDefault = "*"
			}
			refs := "-"
			if list := m.javaRefs[item.ID]; len(list) > 0 {
				refs = strings.Join(list, ", ")
			}
			rows = append(rows, []string{
				marker + item.ID,
				orDash(item.Version),
				orDash(item.OS) + "/" + orDash(item.Arch),
				status,
				isDefault,
				refs,
			})
		}
		writeTable(&b, rows)
		if end < len(m.javaRuntimes) || start > 0 {
			b.WriteString(fmt.Sprintf("\n显示 %d-%d / 共 %d 个运行时\n", start+1, end, len(m.javaRuntimes)))
		}
	}
	if len(m.javaWarnings) > 0 {
		b.WriteString("\n警告:\n")
		for _, w := range m.javaWarnings {
			b.WriteString("  - " + w + "\n")
		}
	}
	b.WriteString("\nEnter 设为全局默认，p 清理未引用运行时。\n")
	return b.String()
}

func (m tuiModel) viewDiagnose() string {
	var b strings.Builder
	if m.diagLoading {
		b.WriteString("正在读取运行日志与崩溃报告…\n")
		return b.String()
	}
	if m.diagErr != nil {
		b.WriteString("诊断失败: " + m.diagErr.Error() + "\n")
		return b.String()
	}
	lines := m.diagLines()
	if len(lines) == 0 {
		b.WriteString("暂无可展示的诊断结果。\n")
		return b.String()
	}
	page := m.diagnosePageSize()
	start := clampCursor(m.diagOffset, len(lines))
	end := start + page
	if end > len(lines) {
		end = len(lines)
	}
	for _, line := range lines[start:end] {
		b.WriteString(line + "\n")
	}
	if start > 0 || end < len(lines) {
		b.WriteString(fmt.Sprintf("\n[%d-%d / %d]  ↑/↓ 滚动  PgUp/PgDn 翻页  Home/End 跳转\n",
			start+1, end, len(lines)))
	}
	return b.String()
}

func (m tuiModel) diagLines() []string {
	if m.diag == nil {
		return nil
	}
	v := m.diag
	var lines []string
	lines = append(lines, "实例: "+v.name)
	lines = append(lines, "目录: "+v.dir)
	if v.cfg.LoaderType != "" {
		lines = append(lines, "版本: "+v.cfg.LoaderType+" "+v.cfg.LoaderVersion)
	}
	if v.cfgErr != "" {
		lines = append(lines, "配置警告: "+v.cfgErr)
	}
	lines = append(lines, "运行日志: "+displayPath(v.logPath))
	lines = append(lines, "崩溃报告: "+displayPath(v.crashPath))
	lines = append(lines, "退出码: "+displayExitCode(v.report.ExitCode))
	status := "未崩溃 / 正常结束"
	if v.report.Crashed {
		status = "已崩溃 / 异常退出"
	}
	lines = append(lines, fmt.Sprintf("摘要: %s；退出原因预判：%s；置信度 %d%%",
		status, v.report.Reason, v.report.Confidence))
	for _, note := range v.report.Notes {
		lines = append(lines, "提示: "+note)
	}
	for _, w := range v.warnings {
		lines = append(lines, "警告: "+w)
	}

	if len(v.report.Findings) == 0 {
		lines = append(lines, "可疑问题: 未发现明显问题")
	} else {
		lines = append(lines, fmt.Sprintf("可疑问题（%d 项）:", len(v.report.Findings)))
		for i, f := range v.report.Findings {
			lines = append(lines, fmt.Sprintf("  %d. [%s] %s（置信度 %d%%）", i+1, f.Severity, f.Title, f.Confidence))
			if f.Detail != "" {
				lines = append(lines, "     说明: "+f.Detail)
			}
			if len(f.Advice) > 0 {
				lines = append(lines, "     建议:")
				for _, a := range f.Advice {
					lines = append(lines, "       - "+a)
				}
			}
			if len(f.Evidence) > 0 {
				lines = append(lines, "     关键行:")
				for _, evidence := range f.Evidence {
					lines = append(lines, "       > "+evidence)
				}
			}
		}
	}
	if len(v.report.Highlights) > 0 && v.report.Crashed {
		lines = append(lines, "崩溃栈关键行:")
		for _, line := range v.report.Highlights {
			lines = append(lines, "  > "+line)
		}
	}
	lines = append(lines, "说明: 诊断仅基于本地日志关键词匹配，复杂问题请查看完整日志或崩溃报告")
	return lines
}

func (m tuiModel) viewModal() string {
	var b strings.Builder
	modal := m.modal
	b.WriteString(modal.title + "\n")
	if modal.hint != "" {
		b.WriteString(modal.hint + "\n")
	}
	b.WriteString("\n> " + modal.value + "_\n")
	if modal.err != "" {
		b.WriteString("\n[错误] " + modal.err + "\n")
	}
	b.WriteString("\n")
	if modal.requireText != "" {
		b.WriteString(fmt.Sprintf("输入 %q 后按 Enter 确认，Esc 取消\n", modal.requireText))
	} else {
		b.WriteString("Enter 确认，Esc 取消\n")
	}
	return b.String()
}

func (m tuiModel) viewFooter() string {
	switch m.page {
	case tuiPageHome:
		return "↑/↓ 选择  Enter 确认  q 退出"
	case tuiPageInstances:
		if m.instMode == tuiInstanceManage {
			return "↑/↓ 选择  Enter 打开操作菜单  r 刷新  q 返回"
		}
		return "↑/↓ 选择  Enter 确认  r 刷新  q 返回"
	case tuiPageInstanceMenu:
		return "↑/↓ 选择  Enter 确认  q 返回"
	case tuiPageInstanceConfig:
		return "↑/↓ 选择  Enter 修改  q 返回"
	case tuiPageMods:
		return "↑/↓ 选择  Enter 操作  r 刷新  q 返回"
	case tuiPageModMenu:
		return "↑/↓ 选择  Enter 确认  q 返回"
	case tuiPageBackups:
		return "↑/↓ 选择  Enter 查看详情  r 刷新  q 返回"
	case tuiPageBackupDetail:
		return "q 返回"
	case tuiPageJava:
		return "↑/↓ 选择  Enter 设为默认  p 清理  r 刷新  q 返回"
	case tuiPageDiagnose:
		return "↑/↓ 滚动  PgUp/PgDn 翻页  r 重新诊断  q 返回"
	}
	return "q 返回"
}

func (m tuiModel) listCapacity() int {
	return maxInt(3, m.height-12)
}

func (m tuiModel) diagnosePageSize() int {
	return maxInt(5, m.height-10)
}

func loaderCell(cfg config.Config) string {
	value := orDash(cfg.LoaderType)
	if cfg.LoaderVersion != "" {
		value += " " + cfg.LoaderVersion
	}
	return value
}

func boolLabel(v bool) string {
	if v {
		return "开启"
	}
	return "关闭"
}

func padDisplay(s string, width int) string {
	n := displayWidth(s)
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}

func listOrDash(items []string) string {
	if len(items) == 0 {
		return "-"
	}
	return strings.Join(items, " ")
}

// windowBounds 根据当前光标与可视行数计算列表窗口，保证光标始终可见。
func windowBounds(cursor, total, capacity int) (int, int) {
	if total <= 0 {
		return 0, 0
	}
	if capacity <= 0 || total <= capacity {
		return 0, total
	}
	cursor = clampCursor(cursor, total)
	start := cursor - capacity/2
	if start < 0 {
		start = 0
	}
	if start+capacity > total {
		start = total - capacity
	}
	return start, start + capacity
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// limitDisplayWidth 把每行截断到终端宽度，避免长路径或长日志行破坏布局。
func limitDisplayWidth(text string, width int) string {
	if width <= 0 {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = truncateDisplay(line, width)
	}
	return strings.Join(lines, "\n")
}

func truncateDisplay(s string, width int) string {
	if width <= 0 || displayWidth(s) <= width {
		return s
	}
	var b strings.Builder
	used := 0
	limit := width - 1
	for _, r := range s {
		w := 1
		if isWideRune(r) {
			w = 2
		}
		if used+w > limit {
			break
		}
		b.WriteRune(r)
		used += w
	}
	b.WriteString("…")
	return b.String()
}

// applyConfigAction 把配置页的菜单动作转成弹窗或配置写入命令。
func (m tuiModel) applyConfigAction(index int) (tea.Model, tea.Cmd) {
	dir := m.selected.Dir
	switch index {
	case 0:
		if m.configData.MemoryMode == config.ModeManual {
			return m, saveInstanceConfigCmd(dir, "已切换为自动内存", func(cfg *config.Config) error {
				cfg.MemoryMode = config.ModeAuto
				return nil
			})
		}
		if value := strings.TrimSpace(m.configData.ManualMemory); value != "" {
			return m, saveInstanceConfigCmd(dir, "已切换为手动内存: "+value, func(cfg *config.Config) error {
				cfg.MemoryMode = config.ModeManual
				cfg.ManualMemory = value
				return nil
			})
		}
		m.modal = tuiModal{
			active: true,
			title:  "设置手动内存",
			hint:   "输入最大堆大小，例如 4G、512M。输入空值不会切换模式。",
			action: tuiModalSetManualMemory,
		}
		return m, nil
	case 1:
		m.modal = tuiModal{
			active: true,
			title:  "设置手动内存值",
			hint:   "输入最大堆大小，例如 4G、6G。",
			value:  m.configData.ManualMemory,
			action: tuiModalSetManualMemory,
		}
		return m, nil
	case 2:
		enabled := !m.configData.PreferHighPerfGPU
		text := "已开启高性能显卡调度（仅 Windows 生效）"
		if !enabled {
			text = "已关闭高性能显卡调度"
		}
		return m, saveInstanceConfigCmd(dir, text, func(cfg *config.Config) error {
			cfg.PreferHighPerfGPU = enabled
			return nil
		})
	case 3:
		m.modal = tuiModal{
			active: true,
			title:  "设置默认账号",
			hint:   "输入离线账号名；留空表示解绑。只保存名字，不校验账号是否存在。",
			action: tuiModalSetAccount,
		}
		return m, nil
	case 4:
		m.modal = tuiModal{
			active: true,
			title:  "设置 Java 路径",
			hint:   "输入 java 可执行文件完整路径；留空表示清空并恢复自动匹配。",
			action: tuiModalSetJava,
		}
		return m, nil
	case 5:
		m.modal = tuiModal{active: true, title: "添加 JVM 参数", hint: "参数会排在 -Xms/-Xmx 之后。", action: tuiModalAddJVMArg}
		return m, nil
	case 6:
		m.modal = tuiModal{active: true, title: "移除 JVM 参数", hint: "输入要移除的完整参数，当前值可用 mtl config show 查看。", action: tuiModalRemoveJVMArg}
		return m, nil
	case 7:
		m.modal = tuiModal{active: true, title: "添加游戏参数", hint: "参数只拼在主类之后。", action: tuiModalAddGameArg}
		return m, nil
	case 8:
		m.modal = tuiModal{active: true, title: "移除游戏参数", hint: "输入要移除的完整参数。", action: tuiModalRemoveGameArg}
		return m, nil
	case 9:
		m.modal = tuiModal{
			active:      true,
			title:       "重置实例配置",
			hint:        "会恢复默认配置（包括解绑默认账号、清空加载器字段），但不会删除游戏文件。",
			action:      tuiModalResetConfig,
			requireText: "reset",
		}
		return m, nil
	}
	return m, nil
}
