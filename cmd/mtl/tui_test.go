package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"mtl/internal/backup"
	"mtl/internal/config"
	"mtl/internal/diagnose"
	"mtl/internal/instance"
	"mtl/internal/mod"
	"mtl/internal/runtime"
)

func tuiKey(k tea.KeyType) tea.KeyMsg {
	return tea.KeyMsg{Type: k}
}

func tuiRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func updateTUIModel(t *testing.T, m tuiModel, msg tea.Msg) (tuiModel, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	updated, ok := next.(tuiModel)
	if !ok {
		t.Fatalf("Update 返回类型 %T，期望 tuiModel", next)
	}
	return updated, cmd
}

func TestTUIHomeRoutingAndQuit(t *testing.T) {
	m := newTUIModel(t.TempDir())

	m, _ = updateTUIModel(t, m, tuiKey(tea.KeyEnter))
	if m.page != tuiPageInstances || m.instMode != tuiInstanceManage {
		t.Fatalf("首页第一项应进入实例管理，实际 page=%v mode=%v", m.page, m.instMode)
	}

	m, _ = updateTUIModel(t, m, tuiRunes("q"))
	if m.page != tuiPageHome {
		t.Fatalf("实例页 q 应返回首页，实际 %v", m.page)
	}

	m, cmd := updateTUIModel(t, m, tuiRunes("q"))
	if !m.quit || cmd == nil {
		t.Fatalf("首页 q 应退出并返回 tea.Quit：quit=%v cmd=%v", m.quit, cmd)
	}
}

func TestTUIHomeMenuClampsAndRoutes(t *testing.T) {
	m := newTUIModel(t.TempDir())
	for i := 0; i < 10; i++ {
		m, _ = updateTUIModel(t, m, tuiKey(tea.KeyDown))
	}
	if m.homeCursor != len(tuiHomeItems)-1 {
		t.Fatalf("光标应停在最后一项，实际 %d", m.homeCursor)
	}
	m, cmd := updateTUIModel(t, m, tuiKey(tea.KeyEnter))
	if m.page != tuiPageInstances || m.instMode != tuiInstanceDiagnose {
		t.Fatalf("最后一项应进入崩溃诊断实例选择，实际 page=%v mode=%v", m.page, m.instMode)
	}
	if cmd != nil {
		t.Fatalf("进入实例选择不应立即执行命令，实际 cmd=%v", cmd)
	}
}

func TestTUIInstancesMissingRootIsFriendly(t *testing.T) {
	m := newTUIModel(filepath.Join(t.TempDir(), "missing"))
	m, _ = updateTUIModel(t, m, loadInstancesCmd(m.root)())
	if m.instErr != nil {
		t.Fatalf("缺失实例根目录不应显示为扫描错误: %v", m.instErr)
	}
	if !strings.Contains(m.notice, "不存在") {
		t.Fatalf("应提示实例根目录不存在，实际 %q", m.notice)
	}
}

func TestTUIInstanceCursorClampAndModes(t *testing.T) {
	m := newTUIModel(t.TempDir())
	m, _ = updateTUIModel(t, m, tuiInstancesMsg{entries: []instance.Entry{
		{Name: "alpha", Dir: filepath.Join(m.root, "alpha")},
		{Name: "beta", Dir: filepath.Join(m.root, "beta")},
	}})
	m.page = tuiPageInstances

	for i := 0; i < 5; i++ {
		m, _ = updateTUIModel(t, m, tuiKey(tea.KeyDown))
	}
	if m.instCursor != 1 {
		t.Fatalf("实例光标应被限制在 1，实际 %d", m.instCursor)
	}

	m.instMode = tuiInstanceMods
	m, cmd := updateTUIModel(t, m, tuiKey(tea.KeyEnter))
	if m.selected.Name != "beta" {
		t.Fatalf("应选中 beta，实际 %q", m.selected.Name)
	}
	if m.page != tuiPageMods || cmd == nil {
		t.Fatalf("Mod 模式应进入 Mod 页并触发加载：page=%v cmd=%v", m.page, cmd)
	}
}

func TestTUIInstanceDeleteRequiresExactConfirmation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	m := newTUIModel(root)
	m.page = tuiPageInstanceMenu
	m.selected = instance.Entry{Name: "demo", Dir: dir}
	for i := 0; i < len(tuiInstanceActions)-1; i++ {
		m, _ = updateTUIModel(t, m, tuiKey(tea.KeyDown))
	}
	m, _ = updateTUIModel(t, m, tuiKey(tea.KeyEnter))
	if !m.modal.active || m.modal.action != tuiModalDeleteInstance || m.modal.requireText != "demo" {
		t.Fatalf("删除应打开输入式确认弹窗: %+v", m.modal)
	}

	m, _ = updateTUIModel(t, m, tuiRunes("wrong"))
	m, _ = updateTUIModel(t, m, tuiKey(tea.KeyEnter))
	if !m.modal.active || m.modal.err == "" {
		t.Fatalf("输入错误时应保留弹窗并提示错误: %+v", m.modal)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("未确认前不应删除实例目录: %v", err)
	}

	msg := deleteInstanceCmd(root, "demo")()
	if got, ok := msg.(tuiInstanceDeleteMsg); !ok || got.err != nil {
		t.Fatalf("删除命令失败: %#v", msg)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("实例目录应已删除，Stat 错误: %v", err)
	}
}

func TestDeleteInstanceRejectsTraversal(t *testing.T) {
	msg := deleteInstanceCmd(t.TempDir(), "../outside")()
	got, ok := msg.(tuiInstanceDeleteMsg)
	if !ok || got.err == nil {
		t.Fatalf("非法实例名应被拒绝，实际 %#v", msg)
	}
}

func TestTUIConfigManualMemoryPersists(t *testing.T) {
	dir := t.TempDir()
	m := newTUIModel(filepath.Dir(dir))
	m.selected = instance.Entry{Name: filepath.Base(dir), Dir: dir}
	m.configData = config.Default()

	next, _ := m.applyConfigAction(1)
	m = next.(tuiModel)
	if !m.modal.active || m.modal.action != tuiModalSetManualMemory {
		t.Fatalf("应打开手动内存输入弹窗: %+v", m.modal)
	}

	m, _ = updateTUIModel(t, m, tuiRunes("6G"))
	m, cmd := updateTUIModel(t, m, tuiKey(tea.KeyEnter))
	if m.modal.active {
		t.Fatalf("提交后弹窗应关闭")
	}
	if cmd == nil {
		t.Fatal("提交手动内存应返回保存命令")
	}
	msg := cmd()
	updated, ok := msg.(tuiConfigUpdatedMsg)
	if !ok || updated.err != nil {
		t.Fatalf("保存配置失败: %#v", msg)
	}
	if updated.cfg.MemoryMode != config.ModeManual || updated.cfg.ManualMemory != "6G" {
		t.Fatalf("内存配置不符合预期: %+v", updated.cfg)
	}
	stored, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if stored.MemoryMode != config.ModeManual || stored.ManualMemory != "6G" {
		t.Fatalf("磁盘配置不符合预期: %+v", stored)
	}
}

func TestSaveInstanceConfigAddAndRemoveArgs(t *testing.T) {
	dir := t.TempDir()
	msg := saveInstanceConfigCmd(dir, "已添加", func(cfg *config.Config) error {
		cfg.AddJVMArg("-XX:+UseG1GC")
		cfg.AddGameArg("--width")
		return nil
	})()
	if got := msg.(tuiConfigUpdatedMsg); got.err != nil {
		t.Fatalf("添加参数失败: %v", got.err)
	}
	stored, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.JVMArgs) != 1 || stored.JVMArgs[0] != "-XX:+UseG1GC" {
		t.Fatalf("JVM 参数未写入: %+v", stored.JVMArgs)
	}
	if len(stored.GameArgs) != 1 || stored.GameArgs[0] != "--width" {
		t.Fatalf("游戏参数未写入: %+v", stored.GameArgs)
	}

	msg = saveInstanceConfigCmd(dir, "已移除", func(cfg *config.Config) error {
		if !cfg.RemoveJVMArg("-XX:+UseG1GC") {
			return os.ErrNotExist
		}
		return nil
	})()
	if got := msg.(tuiConfigUpdatedMsg); got.err != nil {
		t.Fatalf("移除参数失败: %v", got.err)
	}
	stored, _ = config.Load(dir)
	if len(stored.JVMArgs) != 0 {
		t.Fatalf("JVM 参数应已移除: %+v", stored.JVMArgs)
	}
}

func TestTUIJavaReferenceScan(t *testing.T) {
	t.Setenv(config.EnvGlobalConfig, filepath.Join(t.TempDir(), "config.json"))
	root := t.TempDir()
	runtimeDir := filepath.Join(t.TempDir(), "java-runtime-gamma")
	javaPath := filepath.Join(runtimeDir, "bin", "java.exe")
	if err := os.MkdirAll(filepath.Dir(javaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(javaPath, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}

	instanceDir := filepath.Join(root, "demo")
	if err := config.Save(instanceDir, config.Config{MemoryMode: config.ModeAuto, Java: javaPath}); err != nil {
		t.Fatal(err)
	}

	scan, warnings := tuiJavaReferences(root, []runtime.Installed{{ID: "java-runtime-gamma", Dir: runtimeDir}})
	if len(warnings) != 0 {
		t.Fatalf("不应有警告: %v", warnings)
	}
	if scan.instanceCount != 1 {
		t.Fatalf("实例数量应为 1，实际 %d", scan.instanceCount)
	}
	refs := scan.byRuntime["java-runtime-gamma"]
	if len(refs) != 1 || refs[0] != "demo" {
		t.Fatalf("运行时引用统计错误: %+v", scan.byRuntime)
	}
}

func TestLoadDiagnoseCmdReadsLog(t *testing.T) {
	t.Setenv("MTL_LAST_RUN_PATH", filepath.Join(t.TempDir(), "missing-last-run.json"))
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "run-20260101-120000.log")
	content := "[12:00:00] [Render thread/ERROR]: java.lang.OutOfMemoryError: Java heap space\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	msg := loadDiagnoseCmd(dir, "demo")()
	got, ok := msg.(tuiDiagnoseMsg)
	if !ok || got.err != nil {
		t.Fatalf("诊断命令失败: %#v", msg)
	}
	if got.view.logPath == "" {
		t.Fatal("应发现运行日志")
	}
	if len(got.view.report.Findings) == 0 {
		t.Fatal("应识别出内存不足问题")
	}
	lines := strings.Join(tuiModel{diag: &got.view}.diagLines(), "\n")
	if !strings.Contains(lines, "内存不足") || !strings.Contains(lines, "摘要") {
		t.Fatalf("诊断展示缺少关键内容:\n%s", lines)
	}
}

func TestDiagLinesWithoutReport(t *testing.T) {
	var m tuiModel
	if len(m.diagLines()) != 0 {
		t.Fatal("没有诊断数据时应返回空列表")
	}
	view := tuiDiagnoseView{
		name:   "demo",
		dir:    "/tmp/demo",
		report: diagnose.Report{Reason: "正常退出", Confidence: 80},
	}
	m.diag = &view
	lines := strings.Join(m.diagLines(), "\n")
	if !strings.Contains(lines, "可疑问题: 未发现明显问题") {
		t.Fatalf("空诊断应给出友好提示:\n%s", lines)
	}
}

func TestWindowBounds(t *testing.T) {
	cases := []struct {
		cursor, total, capacity int
		start, end              int
	}{
		{cursor: 0, total: 0, capacity: 5, start: 0, end: 0},
		{cursor: 0, total: 3, capacity: 5, start: 0, end: 3},
		{cursor: 0, total: 10, capacity: 4, start: 0, end: 4},
		{cursor: 5, total: 10, capacity: 4, start: 3, end: 7},
		{cursor: 9, total: 10, capacity: 4, start: 6, end: 10},
	}
	for _, tc := range cases {
		start, end := windowBounds(tc.cursor, tc.total, tc.capacity)
		if start != tc.start || end != tc.end {
			t.Errorf("windowBounds(%d,%d,%d)=(%d,%d)，期望 (%d,%d)",
				tc.cursor, tc.total, tc.capacity, start, end, tc.start, tc.end)
		}
	}
}

func TestTruncateDisplay(t *testing.T) {
	if got := truncateDisplay("abcdef", 4); got != "abc…" {
		t.Fatalf("ASCII 截断错误: %q", got)
	}
	// 中文按 2 列计算：宽度 5 只能容纳 2 个汉字加省略号。
	if got := truncateDisplay("中文测试", 5); got != "中文…" {
		t.Fatalf("中文截断错误: %q", got)
	}
	if got := truncateDisplay("short", 10); got != "short" {
		t.Fatalf("未超宽不应截断: %q", got)
	}
}

func TestTUIViewPagesRenderWithoutPanic(t *testing.T) {
	m := newTUIModel("instances")
	m.width = 80
	m.height = 24
	entry := instance.Entry{
		Name:       "demo",
		Dir:        filepath.Join("instances", "demo"),
		Config:     config.Config{MemoryMode: config.ModeAuto, LoaderType: "fabric", LoaderVersion: "0.15.11"},
		JavaStatus: instance.JavaAuto,
	}
	m.instances = []instance.Entry{entry}
	m.selected = entry
	m.configData = entry.Config
	m.configHealth = entry.Health()
	m.mods = []mod.Mod{{File: "a.jar", ID: "a", Name: "Mod A", Version: "1.0", Known: true, Loader: mod.LoaderFabric}}
	m.backups = []backup.Listed{{Info: backup.Info{
		Path:    "demo.zip",
		Size:    1024,
		ModTime: time.Now(),
		Metadata: backup.Metadata{
			Instance:  "demo",
			CreatedAt: time.Now(),
			Name:      "test",
			Include:   []string{"mods", "saves"},
		},
	}}}
	m.javaRuntimes = []runtime.Installed{{ID: "java-runtime-gamma", Version: "21.0.7", OS: "windows", Arch: "x64", Valid: true}}
	m.javaGlobalID = "java-runtime-gamma"
	m.javaGlobalFound = true
	info := m.backups[0].Info
	m.backupSelected = &info
	m.diag = &tuiDiagnoseView{
		name:   "demo",
		dir:    entry.Dir,
		cfg:    entry.Config,
		report: diagnose.Report{Reason: "正常退出", Confidence: 80},
	}

	for _, page := range []tuiPage{
		tuiPageHome,
		tuiPageInstances,
		tuiPageInstanceMenu,
		tuiPageInstanceConfig,
		tuiPageMods,
		tuiPageModMenu,
		tuiPageBackups,
		tuiPageBackupDetail,
		tuiPageJava,
		tuiPageDiagnose,
	} {
		m.page = page
		if out := m.View(); strings.TrimSpace(out) == "" {
			t.Fatalf("页面 %v 渲染为空", page)
		}
	}

	m.modal = tuiModal{active: true, title: "测试弹窗", hint: "hint", value: "abc", requireText: "abc"}
	if out := m.View(); !strings.Contains(out, "测试弹窗") || !strings.Contains(out, "abc") {
		t.Fatalf("弹窗渲染缺少内容:\n%s", out)
	}
}

func TestTUIConfigActionLabelsFollowState(t *testing.T) {
	m := newTUIModel(t.TempDir())
	m.configData = config.Default()
	if actions := m.configActions(); !strings.Contains(actions[0], "手动") {
		t.Fatalf("自动模式应提供切换为手动内存: %v", actions)
	}
	m.configData.MemoryMode = config.ModeManual
	m.configData.ManualMemory = "4G"
	if actions := m.configActions(); !strings.Contains(actions[0], "自动") {
		t.Fatalf("手动模式应提供切换为自动内存: %v", actions)
	}
	m.configData.PreferHighPerfGPU = true
	if actions := m.configActions(); !strings.Contains(actions[2], "关闭") {
		t.Fatalf("已开启时应提供关闭选项: %v", actions)
	}
}
