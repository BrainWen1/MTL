package main

import (
	"fmt"
	"strings"

	"mtl/internal/forge"
)

func (m tuiModel) viewNewInstance() string {
	var b strings.Builder
	b.WriteString("选择新建方式\n\n")
	for i, item := range tuiNewInstanceItems {
		marker := "  "
		if i == m.newInst.menuCursor {
			marker = "> "
		}
		b.WriteString(marker + padDisplay(item, 27) + tuiNewInstanceDescriptions[i] + "\n")
	}
	b.WriteString("\n提示: mrpack 导入会使用系统文件选择框；原版向导会列出 Mojang 正式版。\n")
	return b.String()
}

func (m tuiModel) viewNewMinecraft() string {
	var b strings.Builder
	b.WriteString("选择 Minecraft 版本（仅正式版）\n")
	if m.newInst.mcVersionLoading {
		b.WriteString("\n正在获取 Mojang 版本清单…\n")
		return b.String()
	}
	if m.newInst.mcVersionErr != nil {
		b.WriteString("\n获取版本清单失败: " + m.newInst.mcVersionErr.Error() + "\n")
		b.WriteString("按 r 重试，或按 q 返回。\n")
		return b.String()
	}
	if len(m.newInst.mcVersions) == 0 {
		b.WriteString("\n没有可用的正式版，按 r 重试。\n")
		return b.String()
	}
	b.WriteString(fmt.Sprintf("共 %d 个正式版，新版本在前\n\n", len(m.newInst.mcVersions)))
	capacity := maxInt(3, m.listCapacity()-2)
	start, end := windowBounds(m.newInst.mcVersionCursor, len(m.newInst.mcVersions), capacity)
	for i := start; i < end; i++ {
		marker := "  "
		if i == m.newInst.mcVersionCursor {
			marker = "> "
		}
		b.WriteString(marker + m.newInst.mcVersions[i].ID + "\n")
	}
	if end < len(m.newInst.mcVersions) || start > 0 {
		b.WriteString(fmt.Sprintf("\n显示 %d-%d / 共 %d 个版本\n", start+1, end, len(m.newInst.mcVersions)))
	}
	return b.String()
}

func (m tuiModel) viewNewLoader() string {
	var b strings.Builder
	b.WriteString("Minecraft: " + orDash(m.newInst.selectedMC) + "\n")
	b.WriteString("选择加载器\n\n")
	for i, name := range tuiLoaderNames {
		marker := "  "
		if i == m.newInst.loaderCursor {
			marker = "> "
		}
		desc := tuiLoaderDescriptions[i]
		if tuiLoaderChoices[i] == "forge" && !forge.SupportsMinecraftVersion(m.newInst.selectedMC) {
			desc += "（当前版本不支持）"
		}
		b.WriteString(marker + padDisplay(name, 18) + desc + "\n")
	}
	b.WriteString("\nFabric 自动使用最新稳定 Loader；Forge 自动使用官方 recommended 版本。\n")
	return b.String()
}

func (m tuiModel) viewNewProgress() string {
	run := m.newInst.install
	title := run.title
	if title == "" {
		title = "新建实例"
	}
	var b strings.Builder
	b.WriteString(title + "\n")
	if run.name != "" {
		b.WriteString("实例名: " + run.name + "\n")
	}
	if run.dir != "" {
		b.WriteString("目标目录: " + run.dir + "\n")
	}
	switch {
	case run.running:
		b.WriteString("状态: 正在安装…（下方实时显示下载与安装进度）\n")
	case run.err != "":
		b.WriteString("状态: 安装失败\n")
		b.WriteString("原因: " + oneLine(run.err) + "\n")
	case run.done:
		b.WriteString("状态: 安装完成，实例已写入实例列表\n")
	default:
		b.WriteString("状态: 等待开始\n")
	}
	b.WriteString(strings.Repeat("─", 48) + "\n")

	logs := run.logs
	total := len(logs)
	if total == 0 {
		b.WriteString("（等待安装输出…）\n")
		return b.String()
	}
	capacity := m.installLogPageSize()
	end := total - run.offset
	if end < 0 {
		end = 0
	}
	if end > total {
		end = total
	}
	start := end - capacity
	if start < 0 {
		start = 0
	}
	for _, line := range logs[start:end] {
		b.WriteString("  " + line + "\n")
	}
	b.WriteString(fmt.Sprintf("\n显示 %d-%d / 共 %d 行\n", start+1, end, total))
	return b.String()
}

func (m tuiModel) installLogPageSize() int {
	return maxInt(5, m.height-16)
}

func oneLine(text string) string {
	text = strings.ReplaceAll(text, "\r", "")
	return strings.ReplaceAll(text, "\n", " / ")
}
