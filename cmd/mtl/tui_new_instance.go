package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"mtl/internal/assets"
	"mtl/internal/fabric"
	"mtl/internal/forge"
	"mtl/internal/loader"
	"mtl/internal/mrpack"
	"mtl/internal/template"
)

const (
	tuiInstallLogLimit  = 1000
	tuiMaxLogLineRunes  = 1000
	tuiMaxInstanceRunes = 64
)

// tuiBridge 让后台安装 goroutine 把进度送回 Bubble Tea 事件循环。
// 模型会被 Bubble Tea 复制，因此这里必须使用共享指针。
type tuiBridge struct {
	send func(tea.Msg)
}

type tuiPackInfo struct {
	Name          string
	VersionID     string
	Minecraft     string
	LoaderType    string
	LoaderVersion string
	Files         int
	Mods          int
}

type tuiInstallRun struct {
	running bool
	done    bool
	title   string
	name    string
	dir     string
	kind    string // mrpack / vanilla
	logs    []string
	offset  int
	err     string
}

type tuiNewInstanceState struct {
	menuCursor  int
	pendingPack string

	mcVersions       []fabric.MinecraftVersion
	mcVersionErr     error
	mcVersionLoading bool
	mcVersionCursor  int
	selectedMC       string
	selectedLoader   string
	loaderCursor     int

	install tuiInstallRun
}

type tuiMrpackPickedMsg struct {
	path string
	info tuiPackInfo
	err  error
}

type tuiMCVersionsMsg struct {
	versions []fabric.MinecraftVersion
	err      error
}

type tuiInstallLogMsg struct {
	line string
}

type tuiInstallDoneMsg struct {
	name string
	dir  string
	kind string
	err  error
}

// tuiPickMrpackFile 便于单元测试替换；默认调用系统原生文件选择框。
var tuiPickMrpackFile = nativeOpenMrpackDialog

// pickMrpackCmd 先弹原生对话框，再复用 mrpack.Open 做格式校验与摘要解析。
func pickMrpackCmd() tea.Cmd {
	return func() tea.Msg {
		path, err := tuiPickMrpackFile()
		if err != nil {
			return tuiMrpackPickedMsg{err: err}
		}
		path = strings.TrimSpace(path)
		if path == "" {
			return tuiMrpackPickedMsg{}
		}
		if !strings.EqualFold(filepath.Ext(path), ".mrpack") {
			return tuiMrpackPickedMsg{path: path, err: fmt.Errorf("请选择 .mrpack 文件: %s", path)}
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return tuiMrpackPickedMsg{err: fmt.Errorf("解析整合包路径失败: %w", err)}
		}
		info, err := readPackInfo(abs)
		if err != nil {
			return tuiMrpackPickedMsg{path: abs, err: err}
		}
		return tuiMrpackPickedMsg{path: abs, info: info}
	}
}

func readPackInfo(path string) (tuiPackInfo, error) {
	p, err := mrpack.Open(path)
	if err != nil {
		return tuiPackInfo{}, fmt.Errorf("解析 mrpack 失败: %w", err)
	}
	defer p.Close()
	idx := p.Index
	loaderType, loaderVersion := idx.Loader()
	files := idx.ClientFiles()
	mods := 0
	for _, f := range files {
		if strings.HasPrefix(f.Path, "mods/") && strings.EqualFold(filepath.Ext(f.Path), ".jar") {
			mods++
		}
	}
	return tuiPackInfo{
		Name:          idx.Name,
		VersionID:     idx.VersionID,
		Minecraft:     idx.Dependencies[mrpack.DepMinecraft],
		LoaderType:    string(loaderType),
		LoaderVersion: loaderVersion,
		Files:         len(files),
		Mods:          mods,
	}, nil
}

// loadMCVersionsCmd 读取 Mojang 版本清单，只保留正式版供用户选择。
func loadMCVersionsCmd() tea.Cmd {
	return func() tea.Msg {
		settings, err := tuiInstallDownloadSettings()
		if err != nil {
			return tuiMCVersionsMsg{err: fmt.Errorf("读取下载配置失败: %w", err)}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		versions, err := fabric.MinecraftVersions(ctx, tuiFabricOptions(settings, nil))
		if err != nil {
			return tuiMCVersionsMsg{err: err}
		}
		releases := make([]fabric.MinecraftVersion, 0, len(versions))
		for _, v := range versions {
			if strings.EqualFold(v.Type, "release") {
				releases = append(releases, v)
			}
		}
		if len(releases) == 0 {
			return tuiMCVersionsMsg{err: errors.New("Mojang 版本清单中没有可用的正式版")}
		}
		return tuiMCVersionsMsg{versions: releases}
	}
}

// installPackForTUI 在进程内复用 installPack，把原有下载/加载器进度实时送进 TUI。
func installPackForTUI(root, name, packPath string, bridge *tuiBridge) tea.Cmd {
	return func() tea.Msg {
		dir, err := validateNewInstanceName(root, name)
		if err != nil {
			return tuiInstallDoneMsg{name: name, kind: "mrpack", err: err}
		}
		settings, err := tuiInstallDownloadSettings()
		if err != nil {
			return tuiInstallDoneMsg{name: name, dir: dir, kind: "mrpack", err: fmt.Errorf("读取下载配置失败: %w", err)}
		}
		w := newTUIProgressWriter(bridge)
		err = installPack(packPath, dir, installOptions{download: settings}, w)
		w.Flush()
		if err != nil {
			return tuiInstallDoneMsg{name: name, dir: dir, kind: "mrpack",
				err: tuiInstallFailure(w, dir, "导入 mrpack 失败", err)}
		}
		return tuiInstallDoneMsg{name: name, dir: dir, kind: "mrpack"}
	}
}

// installVanillaForTUI 自动解析加载器版本，再复用 template.Install 创建纯净实例。
func installVanillaForTUI(root, name, mcVersion, loaderType string, bridge *tuiBridge) tea.Cmd {
	return func() tea.Msg {
		dir, err := validateNewInstanceName(root, name)
		if err != nil {
			return tuiInstallDoneMsg{name: name, kind: "vanilla", err: err}
		}
		settings, err := tuiInstallDownloadSettings()
		if err != nil {
			return tuiInstallDoneMsg{name: name, dir: dir, kind: "vanilla", err: fmt.Errorf("读取下载配置失败: %w", err)}
		}
		w := newTUIProgressWriter(bridge)

		loaderVersion := ""
		if loaderType != "" {
			fmt.Fprintf(w, "[New] 正在解析 %s 加载器版本…\n", tuiLoaderLabel(loaderType))
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			switch loaderType {
			case loader.TypeFabric:
				loaderVersion, err = fabric.LatestLoaderVersion(ctx, mcVersion, tuiFabricOptions(settings, w))
			case loader.TypeForge:
				loaderVersion, err = forge.LatestVersion(ctx, mcVersion, tuiForgeOptions(settings, w))
			default:
				err = fmt.Errorf("不支持的加载器: %s", loaderType)
			}
			cancel()
			if err != nil {
				w.Flush()
				return tuiInstallDoneMsg{name: name, dir: dir, kind: "vanilla",
					err: tuiInstallFailure(w, dir, "解析加载器版本失败", err)}
			}
			fmt.Fprintf(w, "[New] 使用 %s %s\n", tuiLoaderLabel(loaderType), loaderVersion)
		}

		err = installTemplate(dir, tuiTemplateOptions(mcVersion, loaderType, loaderVersion, settings, w), w)
		w.Flush()
		if err != nil {
			return tuiInstallDoneMsg{name: name, dir: dir, kind: "vanilla",
				err: tuiInstallFailure(w, dir, "创建实例失败", err)}
		}
		return tuiInstallDoneMsg{name: name, dir: dir, kind: "vanilla"}
	}
}

// tuiInstallFailure 清理本次新建但未完成的实例目录，避免下次同名创建被误判为重名。
// 实例目录在安装前已通过 validateNewInstanceName 确认不存在，因此删除范围可控。
func tuiInstallFailure(w *tuiProgressWriter, dir, prefix string, err error) error {
	if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			return fmt.Errorf("%s: %w（清理未完成目录失败: %v）", prefix, err, rmErr)
		}
		if w != nil {
			w.WriteLine("[清理] 已删除未完成的实例目录")
		}
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

func tuiInstallDownloadSettings() (downloadSettings, error) {
	fs := flag.NewFlagSet("tui-install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dl := addDownloadFlags(fs)
	return resolveDownloadSettingsQuiet(fs, dl, 0, false)
}

func tuiFabricOptions(settings downloadSettings, out io.Writer) fabric.Options {
	opts := fabric.Options{
		Concurrency: settings.Concurrency,
		Retries:     settings.Retries,
		Timeout:     settings.Timeout,
		Mirrors:     settings.Mirrors,
		LogMode:     settings.LogMode,
	}
	if out != nil {
		opts.Progress = func(msg string) {
			fmt.Fprintf(out, "[Fabric] %s\n", msg)
		}
	}
	return opts
}

func tuiForgeOptions(settings downloadSettings, out io.Writer) forge.Options {
	opts := forge.Options{
		Concurrency: settings.Concurrency,
		Retries:     settings.Retries,
		Timeout:     settings.Timeout,
		Mirrors:     settings.Mirrors,
		LogMode:     settings.LogMode,
	}
	if out != nil {
		opts.Progress = func(msg string) {
			fmt.Fprintf(out, "[Forge] %s\n", msg)
		}
	}
	return opts
}

func tuiAssetsOptions(settings downloadSettings, out io.Writer) assets.Options {
	opts := assets.Options{
		Concurrency: settings.Concurrency,
		Retries:     settings.Retries,
		Timeout:     settings.Timeout,
		Mirrors:     settings.Mirrors,
		LogMode:     settings.LogMode,
	}
	if out != nil {
		opts.Progress = func(msg string) {
			fmt.Fprintf(out, "[Assets] %s\n", msg)
		}
	}
	return opts
}

func tuiTemplateOptions(mcVersion, loaderType, loaderVersion string, settings downloadSettings, out io.Writer) template.Options {
	return template.Options{
		MinecraftVersion: mcVersion,
		LoaderType:       loaderType,
		LoaderVersion:    loaderVersion,
		Fabric:           tuiFabricOptions(settings, out),
		Forge:            tuiForgeOptions(settings, out),
		Assets:           tuiAssetsOptions(settings, out),
		Progress: func(msg string) {
			fmt.Fprintf(out, "[Template] %s\n", msg)
		},
	}
}

// tuiProgressWriter 按行拆分流式输出，保证进度文字不会和 TUI 光标混在一起。
type tuiProgressWriter struct {
	bridge *tuiBridge
	mu     sync.Mutex
	buf    []byte
}

func newTUIProgressWriter(bridge *tuiBridge) *tuiProgressWriter {
	return &tuiProgressWriter{bridge: bridge}
}

func (w *tuiProgressWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	w.drainLocked()
	return len(p), nil
}

func (w *tuiProgressWriter) WriteLine(line string) {
	_, _ = w.Write([]byte(line + "\n"))
}

func (w *tuiProgressWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	line := strings.TrimRight(string(w.buf), "\r\n")
	w.buf = nil
	w.emitLocked(line)
}

func (w *tuiProgressWriter) drainLocked() {
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf[:i]), "\r")
		w.buf = w.buf[i+1:]
		w.emitLocked(line)
	}
	// 防御没有换行的超长输出，避免缓冲区无限增长。
	if len(w.buf) > 64<<10 {
		line := string(w.buf)
		w.buf = nil
		w.emitLocked(line)
	}
}

func (w *tuiProgressWriter) emitLocked(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	runes := []rune(line)
	if len(runes) > tuiMaxLogLineRunes {
		line = string(runes[:tuiMaxLogLineRunes]) + "…"
	}
	if w.bridge != nil && w.bridge.send != nil {
		w.bridge.send(tuiInstallLogMsg{line: line})
	}
}

func (m *tuiModel) appendInstallLog(line string) {
	line = strings.TrimRight(line, "\r\n")
	line = strings.ReplaceAll(line, "\r", "")
	line = strings.ReplaceAll(line, "\n", " / ")
	if strings.TrimSpace(line) == "" {
		return
	}
	runes := []rune(line)
	if len(runes) > tuiMaxLogLineRunes {
		line = string(runes[:tuiMaxLogLineRunes]) + "…"
	}
	run := &m.newInst.install
	run.logs = append(run.logs, line)
	if run.offset > 0 {
		run.offset++
	}
	if len(run.logs) > tuiInstallLogLimit {
		drop := len(run.logs) - tuiInstallLogLimit
		run.logs = append([]string(nil), run.logs[drop:]...)
		if run.offset > 0 {
			run.offset -= drop
			if run.offset < 0 {
				run.offset = 0
			}
		}
	}
	if run.offset > len(run.logs) {
		run.offset = len(run.logs)
	}
}

func validateNewInstanceName(root, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("实例名不能为空")
	}
	if utf8.RuneCountInString(name) > tuiMaxInstanceRunes {
		return "", fmt.Errorf("实例名过长（最多 %d 个字符）", tuiMaxInstanceRunes)
	}
	if err := validateSingleInstanceName(name); err != nil {
		return "", err
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("实例根目录为空")
	}
	if info, err := os.Stat(root); err == nil && !info.IsDir() {
		return "", fmt.Errorf("实例根目录不是目录: %s", root)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("检查实例根目录失败: %w", err)
	}
	dir, err := mrpack.SafeJoin(root, name)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(dir); err == nil {
		if info.IsDir() {
			return "", fmt.Errorf("实例已存在: %s", name)
		}
		return "", fmt.Errorf("目标路径已存在同名文件: %s", dir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("检查目标路径失败: %w", err)
	}
	return dir, nil
}

func defaultPackInstanceName(packPath string) string {
	base := filepath.Base(packPath)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	return sanitizeInstanceName(base, "mrpack-instance")
}

func defaultVanillaInstanceName(mcVersion, loaderType string) string {
	suffix := loader.TypeVanilla
	if loaderType != "" {
		suffix = loaderType
	}
	return sanitizeInstanceName(mcVersion+"-"+suffix, "mc-instance")
}

// sanitizeInstanceName 只做“生成合法默认名”，最终仍由 validateNewInstanceName 校验。
func sanitizeInstanceName(raw, fallback string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		switch {
		case r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	name := strings.Join(strings.Fields(b.String()), "-")
	name = strings.Trim(name, ".- ")
	runes := []rune(name)
	if len(runes) > tuiMaxInstanceRunes {
		name = strings.Trim(string(runes[:tuiMaxInstanceRunes]), ".- ")
	}
	if name == "" || mrpack.ValidateRelPath(name) != nil {
		return fallback
	}
	return name
}

// uniqueInstanceName 为默认值追加 -2、-3…，避免弹窗一打开就带着重名。
func uniqueInstanceName(root, base string) string {
	if _, err := validateNewInstanceName(root, base); err == nil {
		return base
	}
	baseRunes := []rune(base)
	for i := 2; i <= 999; i++ {
		suffix := fmt.Sprintf("-%d", i)
		keep := tuiMaxInstanceRunes - len([]rune(suffix))
		if keep < 1 {
			break
		}
		prefix := base
		if len(baseRunes) > keep {
			prefix = string(baseRunes[:keep])
		}
		candidate := strings.Trim(prefix, ".- ") + suffix
		if _, err := validateNewInstanceName(root, candidate); err == nil {
			return candidate
		}
	}
	return base
}

func tuiLoaderLabel(loaderType string) string {
	switch strings.ToLower(strings.TrimSpace(loaderType)) {
	case "", loader.TypeVanilla:
		return "纯原版"
	case loader.TypeFabric:
		return "Fabric"
	case loader.TypeForge:
		return "Forge"
	default:
		return loaderType
	}
}

func (m tuiModel) handleMrpackPicked(msg tuiMrpackPickedMsg) (tea.Model, tea.Cmd) {
	if m.page != tuiPageNewInstance {
		return m, nil
	}
	if msg.err != nil {
		m.setNotice(msg.err.Error(), true)
		return m, nil
	}
	if msg.path == "" {
		m.setNotice("已取消选择 mrpack 整合包", false)
		return m, nil
	}
	if msg.info.LoaderType != "" &&
		msg.info.LoaderType != loader.TypeFabric &&
		msg.info.LoaderType != loader.TypeForge {
		m.setNotice(fmt.Sprintf("暂不支持 %s 加载器的整合包", msg.info.LoaderType), true)
		return m, nil
	}
	m.newInst.pendingPack = msg.path
	m.modal = tuiModal{
		active: true,
		title:  "新建实例（mrpack）",
		hint: fmt.Sprintf("整合包: %s %s\nMinecraft %s / %s %s\n客户端文件 %d 个（其中 Mod %d 个）\n文件: %s\n将创建到实例根目录下。",
			msg.info.Name, msg.info.VersionID,
			msg.info.Minecraft, tuiLoaderLabel(msg.info.LoaderType), orDash(msg.info.LoaderVersion),
			msg.info.Files, msg.info.Mods, msg.path),
		value:  uniqueInstanceName(m.root, defaultPackInstanceName(msg.path)),
		action: tuiModalNewPackName,
	}
	m.notice, m.isErr = "", false
	return m, nil
}

func (m tuiModel) updateNewInstance(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		m.newInst.menuCursor = clampCursor(m.newInst.menuCursor-1, len(tuiNewInstanceItems))
	case "down", "j":
		m.newInst.menuCursor = clampCursor(m.newInst.menuCursor+1, len(tuiNewInstanceItems))
	case "q", "esc":
		m.page = tuiPageInstances
		m.instMode = tuiInstanceManage
		m.notice, m.isErr = "", false
	case "enter":
		m.notice, m.isErr = "", false
		switch m.newInst.menuCursor {
		case 0:
			m.setNotice("正在打开系统文件选择框…", false)
			return m, pickMrpackCmd()
		case 1:
			m.newInst.selectedMC = ""
			m.newInst.selectedLoader = ""
			m.newInst.loaderCursor = 0
			m.page = tuiPageNewMinecraft
			if len(m.newInst.mcVersions) > 0 && m.newInst.mcVersionErr == nil {
				m.newInst.mcVersionCursor = clampCursor(m.newInst.mcVersionCursor, len(m.newInst.mcVersions))
				return m, nil
			}
			m.newInst.mcVersionLoading = true
			m.newInst.mcVersionErr = nil
			m.newInst.mcVersionCursor = 0
			return m, loadMCVersionsCmd()
		}
	}
	return m, nil
}

func (m tuiModel) updateNewMinecraft(key string) (tea.Model, tea.Cmd) {
	total := len(m.newInst.mcVersions)
	page := m.listCapacity()
	switch key {
	case "up", "k":
		if !m.newInst.mcVersionLoading {
			m.newInst.mcVersionCursor = clampCursor(m.newInst.mcVersionCursor-1, total)
		}
	case "down", "j":
		if !m.newInst.mcVersionLoading {
			m.newInst.mcVersionCursor = clampCursor(m.newInst.mcVersionCursor+1, total)
		}
	case "pgup":
		if !m.newInst.mcVersionLoading {
			m.newInst.mcVersionCursor = clampCursor(m.newInst.mcVersionCursor-page, total)
		}
	case "pgdown":
		if !m.newInst.mcVersionLoading {
			m.newInst.mcVersionCursor = clampCursor(m.newInst.mcVersionCursor+page, total)
		}
	case "home":
		m.newInst.mcVersionCursor = 0
	case "end":
		m.newInst.mcVersionCursor = clampCursor(total-1, total)
	case "r":
		if m.newInst.mcVersionLoading {
			return m, nil
		}
		m.newInst.mcVersionLoading = true
		m.newInst.mcVersionErr = nil
		m.newInst.mcVersionCursor = 0
		return m, loadMCVersionsCmd()
	case "q", "esc":
		m.page = tuiPageNewInstance
	case "enter":
		if m.newInst.mcVersionLoading || m.newInst.mcVersionErr != nil || total == 0 {
			return m, nil
		}
		m.newInst.selectedMC = m.newInst.mcVersions[m.newInst.mcVersionCursor].ID
		m.newInst.selectedLoader = ""
		m.newInst.loaderCursor = 0
		m.notice, m.isErr = "", false
		m.page = tuiPageNewLoader
	}
	return m, nil
}

func (m tuiModel) updateNewLoader(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		m.newInst.loaderCursor = clampCursor(m.newInst.loaderCursor-1, len(tuiLoaderChoices))
	case "down", "j":
		m.newInst.loaderCursor = clampCursor(m.newInst.loaderCursor+1, len(tuiLoaderChoices))
	case "q", "esc":
		m.page = tuiPageNewMinecraft
	case "enter":
		loaderType := tuiLoaderChoices[m.newInst.loaderCursor]
		if loaderType == loader.TypeForge && !forge.SupportsMinecraftVersion(m.newInst.selectedMC) {
			m.setNotice("Forge 仅支持 Minecraft 1.20.1 及以上，请返回选择其他加载器", true)
			return m, nil
		}
		m.newInst.selectedLoader = loaderType
		defaultName := uniqueInstanceName(m.root, defaultVanillaInstanceName(m.newInst.selectedMC, loaderType))
		m.modal = tuiModal{
			active: true,
			title:  fmt.Sprintf("新建实例（%s）", tuiLoaderLabel(loaderType)),
			hint: fmt.Sprintf("Minecraft: %s\n加载器: %s\n将创建到实例根目录下，默认不安装 Mod；Java 运行时会在首次启动时自动匹配或下载。",
				m.newInst.selectedMC, tuiLoaderLabel(loaderType)),
			value:  defaultName,
			action: tuiModalNewMinecraftName,
		}
		m.notice, m.isErr = "", false
	}
	return m, nil
}

func (m tuiModel) updateNewProgress(key string) (tea.Model, tea.Cmd) {
	run := m.newInst.install
	page := m.installLogPageSize()
	if run.running {
		switch key {
		case "up", "k":
			m.scrollInstallLog(1)
		case "down", "j":
			m.scrollInstallLog(-1)
		case "pgup":
			m.scrollInstallLog(page)
		case "pgdown":
			m.scrollInstallLog(-page)
		case "home":
			m.scrollInstallLog(len(run.logs))
		case "end":
			m.newInst.install.offset = 0
		case "q", "esc":
			m.setNotice("安装仍在进行中，请等待完成；如需强制退出请按两次 Ctrl+C", false)
		}
		return m, nil
	}
	switch key {
	case "up", "k":
		m.scrollInstallLog(1)
	case "down", "j":
		m.scrollInstallLog(-1)
	case "pgup":
		m.scrollInstallLog(page)
	case "pgdown":
		m.scrollInstallLog(-page)
	case "home":
		m.scrollInstallLog(len(run.logs))
	case "end":
		m.newInst.install.offset = 0
	case "enter", "q", "esc":
		return m.leaveInstall()
	}
	return m, nil
}

func (m *tuiModel) scrollInstallLog(delta int) {
	logs := len(m.newInst.install.logs)
	if logs == 0 {
		m.newInst.install.offset = 0
		return
	}
	offset := m.newInst.install.offset + delta
	if offset < 0 {
		offset = 0
	}
	if offset > logs-1 {
		offset = logs - 1
	}
	m.newInst.install.offset = offset
}

func (m tuiModel) leaveInstall() (tea.Model, tea.Cmd) {
	run := m.newInst.install
	if run.running {
		m.setNotice("安装仍在进行中，请等待完成", false)
		return m, nil
	}
	m.newInst.install = tuiInstallRun{}
	m.forceQuit = false
	if run.err != "" {
		m.page = tuiPageNewInstance
		if run.kind == "vanilla" {
			m.newInst.menuCursor = 1
		} else {
			m.newInst.menuCursor = 0
		}
		m.setNotice("创建实例失败: "+run.err, true)
		return m, nil
	}
	m.page = tuiPageInstances
	m.instMode = tuiInstanceManage
	m.instCursor = 1
	m.setNotice("实例已创建: "+run.name, false)
	return m, loadInstancesCmd(m.root)
}
