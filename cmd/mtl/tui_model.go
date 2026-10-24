package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"mtl/internal/backup"
	"mtl/internal/config"
	"mtl/internal/diagnose"
	"mtl/internal/instance"
	"mtl/internal/mod"
	"mtl/internal/runtime"
)

// tuiPage 是 TUI 的页面路由。
type tuiPage int

const (
	tuiPageHome tuiPage = iota
	tuiPageInstances
	tuiPageInstanceMenu
	tuiPageInstanceConfig
	tuiPageMods
	tuiPageModMenu
	tuiPageBackups
	tuiPageBackupDetail
	tuiPageJava
	tuiPageDiagnose
	tuiPageNewInstance
	tuiPageNewMinecraft
	tuiPageNewLoader
	tuiPageNewProgress
)

// tuiInstanceMode 决定实例列表被哪个入口复用。
type tuiInstanceMode int

const (
	tuiInstanceManage tuiInstanceMode = iota
	tuiInstanceMods
	tuiInstanceDiagnose
)

// tuiModalAction 是输入/确认弹窗提交后要执行的动作。
type tuiModalAction int

const (
	tuiModalNone tuiModalAction = iota
	tuiModalSetManualMemory
	tuiModalSetAccount
	tuiModalSetJava
	tuiModalAddJVMArg
	tuiModalRemoveJVMArg
	tuiModalAddGameArg
	tuiModalRemoveGameArg
	tuiModalDeleteInstance
	tuiModalResetConfig
	tuiModalPruneJava
	tuiModalRemoveMod
	tuiModalNewPackName
	tuiModalNewMinecraftName
)

// tuiModal 是单行输入或输入式确认弹窗。
// requireText 非空时，必须输入完全一致的文本才能提交（用于删除、清理等危险操作）。
type tuiModal struct {
	active      bool
	title       string
	hint        string
	value       string
	action      tuiModalAction
	requireText string
	err         string
}

// tuiDiagnoseView 是一次诊断的展示数据。
type tuiDiagnoseView struct {
	name      string
	dir       string
	cfg       config.Config
	cfgErr    string
	report    diagnose.Report
	logPath   string
	crashPath string
	warnings  []string
}

// tuiModel 是 TUI 的唯一状态；所有页面共享同一个模型，通过 page 路由。
type tuiModel struct {
	root   string
	page   tuiPage
	width  int
	height int
	quit   bool

	// 首页
	homeCursor int

	// 实例列表
	instances  []instance.Entry
	skipped    []instance.Skip
	instErr    error
	instCursor int
	instMode   tuiInstanceMode

	// 当前选中的实例（实例操作、配置、Mod、诊断共用）
	selected instance.Entry

	// 实例操作菜单
	actionCursor int

	// 实例配置
	configCursor int
	configData   config.Config
	configHealth []instance.Check

	// Mod 管理
	mods            []mod.Mod
	modWarnings     []string
	modErr          error
	modCursor       int
	modActionCursor int
	modBusy         bool

	// 备份列表
	backups         []backup.Listed
	backupErr       error
	backupCursor    int
	backupSelected  *backup.Info
	backupDetailErr string

	// Java 运行时
	javaRuntimes    []runtime.Installed
	javaRefs        map[string][]string
	javaGlobalID    string
	javaGlobalFound bool
	javaWarnings    []string
	javaErr         error
	javaCursor      int
	javaBusy        bool

	// 崩溃诊断
	diag        *tuiDiagnoseView
	diagErr     error
	diagLoading bool
	diagOffset  int

	// 新建实例向导
	newInst   tuiNewInstanceState
	bridge    *tuiBridge
	forceQuit bool

	modal  tuiModal
	notice string
	isErr  bool
}

func newTUIModel(root string) tuiModel {
	return tuiModel{
		root:         root,
		page:         tuiPageHome,
		width:        100,
		height:       30,
		javaRefs:     map[string][]string{},
		backups:      nil,
		mods:         nil,
		instances:    nil,
		configHealth: nil,
		bridge:       &tuiBridge{},
	}
}

func (m tuiModel) Init() tea.Cmd {
	return loadInstancesCmd(m.root)
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			// 安装过程中需要二次确认，避免误触中断并留下未完成的目录。
			if m.newInst.install.running && !m.forceQuit {
				m.forceQuit = true
				m.setNotice("安装进行中：再次按 Ctrl+C 强制退出（会留下未完成的实例目录）", true)
				return m, nil
			}
			m.quit = true
			return m, tea.Quit
		}
		if m.modal.active {
			return m.updateModal(msg)
		}
		return m.updateKey(msg)
	case tuiInstancesMsg:
		m.instances, m.skipped, m.instErr = msg.entries, msg.skipped, msg.err
		// 默认的 instances/ 不存在属于常见初始状态，不应显示为扫描错误。
		if msg.err != nil && errors.Is(msg.err, os.ErrNotExist) {
			m.instances, m.skipped, m.instErr = nil, nil, nil
			m.setNotice("实例根目录不存在: "+m.root+"（可先用 mtl install 创建实例，或用 --dir 指定）", false)
		}
		m.instCursor = clampCursor(m.instCursor, m.instanceSelectableCount())
		return m, nil
	case tuiModsMsg:
		m.mods, m.modWarnings, m.modErr = msg.mods, msg.warnings, msg.err
		m.modCursor = clampCursor(m.modCursor, len(m.mods))
		m.modBusy = false
		return m, nil
	case tuiBackupsMsg:
		m.backups, m.backupErr = msg.items, msg.err
		m.backupCursor = clampCursor(m.backupCursor, len(m.backups))
		return m, nil
	case tuiJavaMsg:
		m.javaRuntimes = msg.runtimes
		m.javaRefs = msg.refs
		m.javaGlobalID = msg.globalID
		m.javaGlobalFound = msg.globalFound
		m.javaWarnings = msg.warnings
		m.javaErr = msg.err
		m.javaCursor = clampCursor(m.javaCursor, len(m.javaRuntimes))
		m.javaBusy = false
		return m, nil
	case tuiDiagnoseMsg:
		m.diagLoading = false
		m.diagOffset = 0
		if msg.err != nil {
			m.diagErr = msg.err
			m.diag = nil
			return m, nil
		}
		m.diagErr = nil
		m.diag = &msg.view
		return m, nil
	case tuiConfigUpdatedMsg:
		if msg.err != nil {
			m.setNotice(msg.err.Error(), true)
			return m, nil
		}
		m.configData = msg.cfg
		m.selected.Config = msg.cfg
		m.selected.JavaStatus, m.selected.JavaDetail = instance.CheckJava(msg.cfg.Java)
		m.configHealth = m.selected.Health()
		text := msg.text
		if msg.warning != "" {
			text += "（警告: " + msg.warning + "）"
		}
		m.setNotice(text, false)
		return m, nil
	case tuiModsActionMsg:
		m.setNotice(msg.text, msg.err != nil)
		if msg.err != nil {
			return m, nil
		}
		m.page = tuiPageMods
		m.modBusy = true
		return m, loadModsCmd(m.selected.Dir, m.selected.Config.LoaderType)
	case tuiJavaActionMsg:
		m.setNotice(msg.text, msg.err != nil)
		m.javaBusy = false
		return m, loadJavaCmd(m.root)
	case tuiInstanceDeleteMsg:
		m.setNotice(msg.text, msg.err != nil)
		if msg.err == nil {
			m.page = tuiPageInstances
			m.selected = instance.Entry{}
		}
		return m, loadInstancesCmd(m.root)
	case tuiNoticeMsg:
		m.setNotice(msg.text, msg.err != nil)
		return m, nil
	case tuiMrpackPickedMsg:
		return m.handleMrpackPicked(msg)
	case tuiMCVersionsMsg:
		m.newInst.mcVersionLoading = false
		m.newInst.mcVersions = msg.versions
		m.newInst.mcVersionErr = msg.err
		if msg.err == nil {
			m.newInst.mcVersionCursor = clampCursor(m.newInst.mcVersionCursor, len(msg.versions))
		}
		return m, nil
	case tuiInstallLogMsg:
		m.appendInstallLog(msg.line)
		return m, nil
	case tuiInstallDoneMsg:
		m.newInst.install.running = false
		m.newInst.install.done = true
		m.forceQuit = false
		if msg.err != nil {
			m.newInst.install.err = msg.err.Error()
			m.appendInstallLog("[错误] " + msg.err.Error())
			return m, nil
		}
		m.newInst.install.err = ""
		m.appendInstallLog("[完成] 实例目录已就绪: " + msg.dir)
		return m, loadInstancesCmd(m.root)
	}
	return m, nil
}

// updateKey 处理列表页面的按键。弹窗和 Ctrl+C 已在 Update 中优先处理。
func (m tuiModel) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch m.page {
	case tuiPageHome:
		return m.updateHome(key)
	case tuiPageInstances:
		return m.updateInstances(key)
	case tuiPageInstanceMenu:
		return m.updateInstanceMenu(key)
	case tuiPageInstanceConfig:
		return m.updateInstanceConfig(key)
	case tuiPageMods:
		return m.updateMods(key)
	case tuiPageModMenu:
		return m.updateModMenu(key)
	case tuiPageBackups:
		return m.updateBackups(key)
	case tuiPageBackupDetail:
		if isBackKey(key) {
			m.page = tuiPageBackups
		}
		return m, nil
	case tuiPageJava:
		return m.updateJava(key)
	case tuiPageDiagnose:
		return m.updateDiagnose(key)
	case tuiPageNewInstance:
		return m.updateNewInstance(key)
	case tuiPageNewMinecraft:
		return m.updateNewMinecraft(key)
	case tuiPageNewLoader:
		return m.updateNewLoader(key)
	case tuiPageNewProgress:
		return m.updateNewProgress(key)
	}
	return m, nil
}

func (m tuiModel) updateHome(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		m.homeCursor = clampCursor(m.homeCursor-1, len(tuiHomeItems))
	case "down", "j":
		m.homeCursor = clampCursor(m.homeCursor+1, len(tuiHomeItems))
	case "q", "esc":
		m.quit = true
		return m, tea.Quit
	case "enter":
		m.notice, m.isErr = "", false
		switch m.homeCursor {
		case 0:
			m.instMode = tuiInstanceManage
			m.page = tuiPageInstances
		case 1:
			m.instMode = tuiInstanceMods
			m.page = tuiPageInstances
		case 2:
			m.page = tuiPageBackups
			return m, loadBackupsCmd()
		case 3:
			m.page = tuiPageJava
			return m, loadJavaCmd(m.root)
		case 4:
			m.instMode = tuiInstanceDiagnose
			m.page = tuiPageInstances
		}
	}
	return m, nil
}

func (m tuiModel) updateInstances(key string) (tea.Model, tea.Cmd) {
	total := m.instanceSelectableCount()
	switch key {
	case "up", "k":
		m.instCursor = clampCursor(m.instCursor-1, total)
	case "down", "j":
		m.instCursor = clampCursor(m.instCursor+1, total)
	case "n":
		if m.instMode == tuiInstanceManage {
			return m.openNewInstanceMenu()
		}
	case "r":
		m.setNotice("正在刷新实例列表…", false)
		return m, loadInstancesCmd(m.root)
	case "q", "esc":
		m.page = tuiPageHome
	case "enter":
		if total == 0 {
			return m, nil
		}
		if m.instMode == tuiInstanceManage && m.instCursor == 0 {
			return m.openNewInstanceMenu()
		}
		index := m.instCursor
		if m.instMode == tuiInstanceManage {
			index--
		}
		index = clampCursor(index, len(m.instances))
		if len(m.instances) == 0 {
			return m, nil
		}
		m.selected = m.instances[index]
		m.notice, m.isErr = "", false
		switch m.instMode {
		case tuiInstanceManage:
			m.actionCursor = 0
			m.page = tuiPageInstanceMenu
		case tuiInstanceMods:
			m.modCursor, m.modBusy = 0, true
			m.page = tuiPageMods
			return m, loadModsCmd(m.selected.Dir, m.selected.Config.LoaderType)
		case tuiInstanceDiagnose:
			m.diag, m.diagErr, m.diagOffset = nil, nil, 0
			m.diagLoading = true
			m.page = tuiPageDiagnose
			return m, loadDiagnoseCmd(m.selected.Dir, m.selected.Name)
		}
	}
	return m, nil
}

// instanceSelectableCount 在实例管理模式下把首行“新建实例”也计入光标范围。
func (m tuiModel) instanceSelectableCount() int {
	if m.instMode == tuiInstanceManage {
		return len(m.instances) + 1
	}
	return len(m.instances)
}

// openNewInstanceMenu 打开新建实例向导，不触碰任何已有实例。
func (m tuiModel) openNewInstanceMenu() (tea.Model, tea.Cmd) {
	m.page = tuiPageNewInstance
	m.newInst.menuCursor = 0
	m.notice, m.isErr = "", false
	return m, nil
}

func (m tuiModel) updateInstanceMenu(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		m.actionCursor = clampCursor(m.actionCursor-1, len(tuiInstanceActions))
	case "down", "j":
		m.actionCursor = clampCursor(m.actionCursor+1, len(tuiInstanceActions))
	case "q", "esc":
		m.page = tuiPageInstances
	case "enter":
		switch m.actionCursor {
		case 0:
			return m, launchGameCmd(m.selected)
		case 1:
			cfg, cfgErr := config.Load(m.selected.Dir)
			m.configData = cfg
			m.selected.Config = cfg
			m.selected.JavaStatus, m.selected.JavaDetail = instance.CheckJava(cfg.Java)
			m.configHealth = m.selected.Health()
			m.configCursor = 0
			m.page = tuiPageInstanceConfig
			if cfgErr != nil {
				m.setNotice("读取实例配置失败，已回退默认值: "+cfgErr.Error(), true)
			} else {
				m.notice, m.isErr = "", false
			}
		case 2:
			m.setNotice("正在创建备份…", false)
			return m, createBackupCmd(m.selected.Name, m.selected.Dir)
		case 3:
			m.modal = tuiModal{
				active:      true,
				title:       "删除实例",
				hint:        "将永久删除实例目录（不含全局 assets / runtime 缓存）。",
				action:      tuiModalDeleteInstance,
				requireText: m.selected.Name,
			}
		}
	}
	return m, nil
}

func (m tuiModel) updateInstanceConfig(key string) (tea.Model, tea.Cmd) {
	actions := m.configActions()
	switch key {
	case "up", "k":
		m.configCursor = clampCursor(m.configCursor-1, len(actions))
	case "down", "j":
		m.configCursor = clampCursor(m.configCursor+1, len(actions))
	case "q", "esc":
		m.page = tuiPageInstanceMenu
	case "enter":
		return m.applyConfigAction(m.configCursor)
	}
	return m, nil
}

func (m tuiModel) updateMods(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		m.modCursor = clampCursor(m.modCursor-1, len(m.mods))
	case "down", "j":
		m.modCursor = clampCursor(m.modCursor+1, len(m.mods))
	case "r":
		m.modBusy = true
		return m, loadModsCmd(m.selected.Dir, m.selected.Config.LoaderType)
	case "q", "esc":
		m.page = tuiPageInstances
	case "enter":
		if len(m.mods) == 0 {
			return m, nil
		}
		m.modActionCursor = 0
		m.page = tuiPageModMenu
	}
	return m, nil
}

func (m tuiModel) updateModMenu(key string) (tea.Model, tea.Cmd) {
	actions := m.modActions()
	switch key {
	case "up", "k":
		m.modActionCursor = clampCursor(m.modActionCursor-1, len(actions))
	case "down", "j":
		m.modActionCursor = clampCursor(m.modActionCursor+1, len(actions))
	case "q", "esc":
		m.page = tuiPageMods
	case "enter":
		if len(m.mods) == 0 {
			return m, nil
		}
		target := m.mods[m.modCursor]
		if m.modActionCursor == 0 {
			m.modBusy = true
			return m, applyModEnabledCmd(m.selected.Dir, target.File, !target.Disabled)
		}
		m.modal = tuiModal{
			active:      true,
			title:       "删除 Mod",
			hint:        "文件将被直接删除，无法从 MTL 恢复。",
			action:      tuiModalRemoveMod,
			requireText: target.File,
		}
	}
	return m, nil
}

func (m tuiModel) updateBackups(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		m.backupCursor = clampCursor(m.backupCursor-1, len(m.backups))
	case "down", "j":
		m.backupCursor = clampCursor(m.backupCursor+1, len(m.backups))
	case "r":
		m.setNotice("正在刷新备份列表…", false)
		return m, loadBackupsCmd()
	case "q", "esc":
		m.page = tuiPageHome
	case "enter":
		if len(m.backups) == 0 {
			return m, nil
		}
		item := m.backups[m.backupCursor]
		info := item.Info
		m.backupSelected = &info
		m.backupDetailErr = item.Err
		m.page = tuiPageBackupDetail
	}
	return m, nil
}

func (m tuiModel) updateJava(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		m.javaCursor = clampCursor(m.javaCursor-1, len(m.javaRuntimes))
	case "down", "j":
		m.javaCursor = clampCursor(m.javaCursor+1, len(m.javaRuntimes))
	case "r":
		m.javaBusy = true
		return m, loadJavaCmd(m.root)
	case "q", "esc":
		m.page = tuiPageHome
	case "p":
		m.modal = tuiModal{
			active:      true,
			title:       "清理未引用的 Java 运行时",
			hint:        "只会删除没有被任何实例或全局默认引用的运行时。",
			action:      tuiModalPruneJava,
			requireText: "prune",
		}
	case "enter":
		if len(m.javaRuntimes) == 0 {
			return m, nil
		}
		target := m.javaRuntimes[m.javaCursor]
		m.javaBusy = true
		m.setNotice("正在校验并设置全局默认 Java…", false)
		return m, setDefaultJavaCmd(target.ID)
	}
	return m, nil
}

func (m tuiModel) updateDiagnose(key string) (tea.Model, tea.Cmd) {
	page := m.diagnosePageSize()
	switch key {
	case "up", "k":
		m.diagOffset = clampCursor(m.diagOffset-1, len(m.diagLines()))
	case "down", "j":
		m.diagOffset = clampCursor(m.diagOffset+1, len(m.diagLines()))
	case "pgup":
		m.diagOffset = clampCursor(m.diagOffset-page, len(m.diagLines()))
	case "pgdown":
		m.diagOffset = clampCursor(m.diagOffset+page, len(m.diagLines()))
	case "home":
		m.diagOffset = 0
	case "end":
		m.diagOffset = clampCursor(len(m.diagLines())-1, len(m.diagLines()))
	case "r":
		if m.diagLoading {
			return m, nil
		}
		m.diagLoading = true
		m.diagErr = nil
		m.diagOffset = 0
		return m, loadDiagnoseCmd(m.selected.Dir, m.selected.Name)
	case "q", "esc":
		m.page = tuiPageInstances
		m.diag = nil
		m.diagErr = nil
		m.diagLoading = false
	}
	return m, nil
}

// updateModal 处理输入/确认弹窗。所有危险操作都要求输入指定文本后才会执行。
func (m tuiModel) updateModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		if m.modal.action == tuiModalNewPackName {
			m.newInst.pendingPack = ""
		}
		m.modal = tuiModal{}
		return m, nil
	case "enter":
		return m.submitModal()
	case "backspace":
		m.modal.value = trimLastRune(m.modal.value)
		m.modal.err = ""
		return m, nil
	case "space", " ":
		m.modal.value += " "
		m.modal.err = ""
		return m, nil
	}
	if msg.Type == tea.KeyRunes {
		for _, r := range msg.Runes {
			if r >= 0x20 && r != 0x7f {
				m.modal.value += string(r)
			}
		}
		m.modal.err = ""
	}
	return m, nil
}

func (m tuiModel) submitModal() (tea.Model, tea.Cmd) {
	modal := m.modal
	value := strings.TrimSpace(modal.value)
	if modal.requireText != "" && value != modal.requireText {
		m.modal.err = fmt.Sprintf("请输入 %q 以确认", modal.requireText)
		return m, nil
	}
	m.modal = tuiModal{}

	switch modal.action {
	case tuiModalSetManualMemory:
		if value == "" {
			m.setNotice("内存值不能为空，例如 4G", true)
			return m, nil
		}
		return m, saveInstanceConfigCmd(m.selected.Dir, "已设置手动内存: "+value, func(cfg *config.Config) error {
			cfg.MemoryMode = config.ModeManual
			cfg.ManualMemory = value
			return nil
		})
	case tuiModalSetAccount:
		return m, saveInstanceConfigCmd(m.selected.Dir, accountNotice(value), func(cfg *config.Config) error {
			if err := config.ValidateDefaultAccount(value); err != nil {
				return err
			}
			cfg.DefaultAccount = value
			return nil
		})
	case tuiModalSetJava:
		if value == "" {
			return m, saveInstanceConfigCmd(m.selected.Dir, "已清空 Java 路径，恢复自动匹配", func(cfg *config.Config) error {
				cfg.Java = ""
				return nil
			})
		}
		m.setNotice("正在校验 Java 路径…", false)
		return m, saveInstanceConfigCmd(m.selected.Dir, "已设置实例 Java 路径", func(cfg *config.Config) error {
			return setInstanceJavaPath(cfg, value)
		})
	case tuiModalAddJVMArg:
		if value == "" {
			m.setNotice("JVM 参数不能为空", true)
			return m, nil
		}
		return m, saveInstanceConfigCmd(m.selected.Dir, "已添加 JVM 参数: "+value, func(cfg *config.Config) error {
			if !cfg.AddJVMArg(value) {
				return fmt.Errorf("JVM 参数已存在: %s", value)
			}
			return nil
		})
	case tuiModalRemoveJVMArg:
		return m, saveInstanceConfigCmd(m.selected.Dir, "已移除 JVM 参数: "+value, func(cfg *config.Config) error {
			if !cfg.RemoveJVMArg(value) {
				return fmt.Errorf("未找到 JVM 参数: %s", value)
			}
			return nil
		})
	case tuiModalAddGameArg:
		if value == "" {
			m.setNotice("游戏参数不能为空", true)
			return m, nil
		}
		return m, saveInstanceConfigCmd(m.selected.Dir, "已添加游戏参数: "+value, func(cfg *config.Config) error {
			if !cfg.AddGameArg(value) {
				return fmt.Errorf("游戏参数已存在: %s", value)
			}
			return nil
		})
	case tuiModalRemoveGameArg:
		return m, saveInstanceConfigCmd(m.selected.Dir, "已移除游戏参数: "+value, func(cfg *config.Config) error {
			if !cfg.RemoveGameArg(value) {
				return fmt.Errorf("未找到游戏参数: %s", value)
			}
			return nil
		})
	case tuiModalDeleteInstance:
		return m, deleteInstanceCmd(m.root, m.selected.Name)
	case tuiModalResetConfig:
		return m, saveInstanceConfigCmd(m.selected.Dir, "实例配置已重置", func(cfg *config.Config) error {
			*cfg = config.Default()
			return nil
		})
	case tuiModalPruneJava:
		m.javaBusy = true
		m.setNotice("正在清理未引用的 Java 运行时…", false)
		return m, pruneJavaCmd(m.root)
	case tuiModalRemoveMod:
		if len(m.mods) == 0 {
			return m, nil
		}
		target := m.mods[m.modCursor]
		m.modBusy = true
		return m, removeModCmd(m.selected.Dir, target.File)
	case tuiModalNewPackName:
		if m.newInst.pendingPack == "" {
			m.setNotice("整合包路径已丢失，请重新选择", true)
			return m, nil
		}
		dir, err := validateNewInstanceName(m.root, value)
		if err != nil {
			modal.err = err.Error()
			m.modal = modal
			return m, nil
		}
		pack := m.newInst.pendingPack
		m.newInst.pendingPack = ""
		m.page = tuiPageNewProgress
		m.newInst.install = tuiInstallRun{
			running: true,
			title:   "正在导入 mrpack 整合包",
			name:    value,
			dir:     dir,
			kind:    "mrpack",
		}
		m.forceQuit = false
		m.notice, m.isErr = "", false
		m.appendInstallLog("[New] 开始导入 mrpack: " + pack)
		m.appendInstallLog("[New] 实例名: " + value)
		m.appendInstallLog("[New] 目标目录: " + dir)
		return m, installPackForTUI(m.root, value, pack, m.bridge)
	case tuiModalNewMinecraftName:
		mcVersion := m.newInst.selectedMC
		loaderType := m.newInst.selectedLoader
		if mcVersion == "" {
			m.setNotice("Minecraft 版本已丢失，请重新选择", true)
			return m, nil
		}
		dir, err := validateNewInstanceName(m.root, value)
		if err != nil {
			modal.err = err.Error()
			m.modal = modal
			return m, nil
		}
		m.page = tuiPageNewProgress
		m.newInst.install = tuiInstallRun{
			running: true,
			title:   fmt.Sprintf("正在创建 Minecraft %s 实例", mcVersion),
			name:    value,
			dir:     dir,
			kind:    "vanilla",
		}
		m.forceQuit = false
		m.notice, m.isErr = "", false
		m.appendInstallLog(fmt.Sprintf("[New] 开始创建实例: %s", tuiLoaderLabel(loaderType)))
		m.appendInstallLog("[New] Minecraft 版本: " + mcVersion)
		m.appendInstallLog("[New] 实例名: " + value)
		m.appendInstallLog("[New] 目标目录: " + dir)
		return m, installVanillaForTUI(m.root, value, mcVersion, loaderType, m.bridge)
	}
	return m, nil
}

func (m *tuiModel) setNotice(text string, isErr bool) {
	// 错误可能来自 installer 输出，去掉换行，避免破坏单行通知布局。
	text = strings.ReplaceAll(text, "\r", "")
	text = strings.ReplaceAll(text, "\n", " / ")
	m.notice, m.isErr = text, isErr
}

func clampCursor(cursor, total int) int {
	if total <= 0 {
		return 0
	}
	if cursor < 0 {
		return 0
	}
	if cursor >= total {
		return total - 1
	}
	return cursor
}

func trimLastRune(s string) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return s
	}
	return string(runes[:len(runes)-1])
}

func isBackKey(key string) bool {
	return key == "q" || key == "esc"
}
