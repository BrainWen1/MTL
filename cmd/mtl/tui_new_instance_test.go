package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"mtl/internal/config"
	"mtl/internal/fabric"
	"mtl/internal/instance"
)

func TestValidateNewInstanceName(t *testing.T) {
	root := t.TempDir()
	dir, err := validateNewInstanceName(root, "demo")
	if err != nil {
		t.Fatalf("合法实例名被拒绝: %v", err)
	}
	if dir != filepath.Join(root, "demo") {
		t.Fatalf("实例目录错误: %s", dir)
	}
	for _, name := range []string{"", "../evil", "a/b", "CON", strings.Repeat("a", 65)} {
		if _, err := validateNewInstanceName(root, name); err == nil {
			t.Fatalf("非法实例名 %q 应被拒绝", name)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "existing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := validateNewInstanceName(root, "existing"); err == nil {
		t.Fatal("重名实例应被拒绝")
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := validateNewInstanceName(root, "file"); err == nil {
		t.Fatal("同名文件应被拒绝")
	}
}

func TestDefaultInstanceNames(t *testing.T) {
	if got := defaultPackInstanceName(filepath.Join("packs", "Demo Pack.mrpack")); got != "Demo-Pack" {
		t.Fatalf("mrpack 默认名错误: %q", got)
	}
	if got := defaultVanillaInstanceName("1.20.1", "fabric"); got != "1.20.1-fabric" {
		t.Fatalf("Fabric 默认名错误: %q", got)
	}
	if got := defaultVanillaInstanceName("1.20.1", ""); got != "1.20.1-vanilla" {
		t.Fatalf("纯原版默认名错误: %q", got)
	}
	if got := sanitizeInstanceName("CON", "fallback"); got != "fallback" {
		t.Fatalf("Windows 保留名应回落到 fallback，实际 %q", got)
	}
}

func TestUniqueInstanceName(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := uniqueInstanceName(root, "demo")
	if got != "demo-2" {
		t.Fatalf("重名时应追加 -2，实际 %q", got)
	}
}

func TestPickMrpackCancelAndErrors(t *testing.T) {
	old := tuiPickMrpackFile
	t.Cleanup(func() { tuiPickMrpackFile = old })

	tuiPickMrpackFile = func() (string, error) { return "", nil }
	msg := pickMrpackCmd()().(tuiMrpackPickedMsg)
	if msg.err != nil || msg.path != "" {
		t.Fatalf("取消选择应优雅返回: %+v", msg)
	}

	tuiPickMrpackFile = func() (string, error) { return "", fmt.Errorf("对话框失败") }
	msg = pickMrpackCmd()().(tuiMrpackPickedMsg)
	if msg.err == nil {
		t.Fatal("对话框错误应原样返回")
	}

	dir := t.TempDir()
	txt := filepath.Join(dir, "pack.txt")
	if err := os.WriteFile(txt, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tuiPickMrpackFile = func() (string, error) { return txt, nil }
	msg = pickMrpackCmd()().(tuiMrpackPickedMsg)
	if msg.err == nil || !strings.Contains(msg.err.Error(), ".mrpack") {
		t.Fatalf("非 mrpack 文件应被拒绝: %+v", msg)
	}

	bad := filepath.Join(dir, "bad.mrpack")
	if err := os.WriteFile(bad, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	tuiPickMrpackFile = func() (string, error) { return bad, nil }
	msg = pickMrpackCmd()().(tuiMrpackPickedMsg)
	if msg.err == nil || !strings.Contains(msg.err.Error(), "解析 mrpack 失败") {
		t.Fatalf("损坏的 mrpack 应返回清晰错误: %+v", msg)
	}
}

func TestPickMrpackReadsInfo(t *testing.T) {
	dir := t.TempDir()
	pack := filepath.Join(dir, "demo.mrpack")
	index := `{"formatVersion":1,"game":"minecraft","versionId":"1.0.0","name":"Demo Pack",` +
		`"files":[{"path":"mods/example.jar","hashes":{"sha512":"` + strings.Repeat("0", 128) + `"},` +
		`"downloads":["https://example.com/example.jar"],"fileSize":1}],` +
		`"dependencies":{"minecraft":"1.20.1","fabric-loader":"0.15.11"}}`
	writeTestMrpack(t, pack, index)

	old := tuiPickMrpackFile
	t.Cleanup(func() { tuiPickMrpackFile = old })
	tuiPickMrpackFile = func() (string, error) { return pack, nil }

	msg := pickMrpackCmd()().(tuiMrpackPickedMsg)
	if msg.err != nil {
		t.Fatalf("读取 mrpack 失败: %v", msg.err)
	}
	if msg.info.Name != "Demo Pack" || msg.info.Minecraft != "1.20.1" ||
		msg.info.LoaderType != "fabric" || msg.info.LoaderVersion != "0.15.11" ||
		msg.info.Files != 1 || msg.info.Mods != 1 {
		t.Fatalf("mrpack 摘要错误: %+v", msg.info)
	}
}

func TestHandleMrpackPickedOpensNameModal(t *testing.T) {
	root := t.TempDir()
	m := newTUIModel(root)
	m.page = tuiPageNewInstance

	m, _ = updateTUIModel(t, m, tuiMrpackPickedMsg{
		path: filepath.Join(root, "Demo Pack.mrpack"),
		info: tuiPackInfo{
			Name: "Demo", VersionID: "1.0", Minecraft: "1.20.1",
			LoaderType: "fabric", LoaderVersion: "0.15.11", Files: 3,
		},
	})
	if !m.modal.active || m.modal.action != tuiModalNewPackName {
		t.Fatalf("选择整合包后应打开实例名弹窗: %+v", m.modal)
	}
	if m.modal.value != "Demo-Pack" {
		t.Fatalf("默认实例名错误: %q", m.modal.value)
	}
	if m.newInst.pendingPack == "" {
		t.Fatal("应记录待安装的整合包路径")
	}
}

func TestHandleMrpackPickedCancel(t *testing.T) {
	m := newTUIModel(t.TempDir())
	m.page = tuiPageNewInstance
	m, _ = updateTUIModel(t, m, tuiMrpackPickedMsg{})
	if m.modal.active {
		t.Fatal("取消选择不应打开弹窗")
	}
	if m.isErr || !strings.Contains(m.notice, "取消") {
		t.Fatalf("取消选择应给出非错误提示: %q err=%v", m.notice, m.isErr)
	}
}

func TestTUIWizardVanillaFlow(t *testing.T) {
	root := t.TempDir()
	m := newTUIModel(root)
	m.page = tuiPageNewInstance
	m.newInst.menuCursor = 1

	m, cmd := updateTUIModel(t, m, tuiKey(tea.KeyEnter))
	if m.page != tuiPageNewMinecraft || cmd == nil || !m.newInst.mcVersionLoading {
		t.Fatalf("应进入版本选择并加载清单: page=%v loading=%v cmd=%v", m.page, m.newInst.mcVersionLoading, cmd)
	}

	m, _ = updateTUIModel(t, m, tuiMCVersionsMsg{versions: []fabric.MinecraftVersion{
		{ID: "1.19.2", Type: "release"},
		{ID: "1.20.1", Type: "release"},
	}})
	if m.newInst.mcVersionLoading || len(m.newInst.mcVersions) != 2 {
		t.Fatalf("版本清单未载入: %+v", m.newInst)
	}

	m, _ = updateTUIModel(t, m, tuiKey(tea.KeyEnter))
	if m.page != tuiPageNewLoader || m.newInst.selectedMC != "1.19.2" {
		t.Fatalf("应选中 1.19.2 并进入加载器页: page=%v mc=%q", m.page, m.newInst.selectedMC)
	}

	m, _ = updateTUIModel(t, m, tuiKey(tea.KeyDown))
	m, _ = updateTUIModel(t, m, tuiKey(tea.KeyDown))
	m, _ = updateTUIModel(t, m, tuiKey(tea.KeyEnter))
	if m.modal.active || !m.isErr || !strings.Contains(m.notice, "1.20.1") {
		t.Fatalf("Forge 不支持 1.19.2 时应提示: modal=%v notice=%q err=%v", m.modal.active, m.notice, m.isErr)
	}

	m, _ = updateTUIModel(t, m, tuiKey(tea.KeyUp))
	m, _ = updateTUIModel(t, m, tuiKey(tea.KeyEnter))
	if !m.modal.active || m.modal.action != tuiModalNewMinecraftName {
		t.Fatalf("选择 Fabric 后应打开实例名弹窗: %+v", m.modal)
	}
	if m.modal.value != "1.19.2-fabric" {
		t.Fatalf("默认实例名错误: %q", m.modal.value)
	}

	m, cmd = updateTUIModel(t, m, tuiKey(tea.KeyEnter))
	if m.page != tuiPageNewProgress || !m.newInst.install.running || cmd == nil {
		t.Fatalf("提交实例名后应开始安装: page=%v running=%v cmd=%v", m.page, m.newInst.install.running, cmd)
	}
	if !strings.Contains(strings.Join(m.newInst.install.logs, "\n"), "Minecraft 版本: 1.19.2") {
		t.Fatalf("安装日志缺少版本信息: %v", m.newInst.install.logs)
	}
}

func TestInstallPackForTUIRejectsCorruptPack(t *testing.T) {
	t.Setenv(config.EnvGlobalConfig, filepath.Join(t.TempDir(), "config.json"))
	root := t.TempDir()
	pack := filepath.Join(t.TempDir(), "bad.mrpack")
	if err := os.WriteFile(pack, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg := installPackForTUI(root, "demo", pack, nil)()
	done, ok := msg.(tuiInstallDoneMsg)
	if !ok || done.err == nil {
		t.Fatalf("损坏整合包应返回错误: %#v", msg)
	}
	if _, err := os.Stat(filepath.Join(root, "demo")); !os.IsNotExist(err) {
		t.Fatalf("失败后不应留下实例目录: %v", err)
	}
}

func TestAppendInstallLogLimit(t *testing.T) {
	var m tuiModel
	for i := 0; i < tuiInstallLogLimit+100; i++ {
		m.appendInstallLog(fmt.Sprintf("line-%d", i))
	}
	logs := m.newInst.install.logs
	if len(logs) != tuiInstallLogLimit {
		t.Fatalf("日志应限制为 %d 行，实际 %d", tuiInstallLogLimit, len(logs))
	}
	if logs[0] != "line-100" || logs[len(logs)-1] != fmt.Sprintf("line-%d", tuiInstallLogLimit+99) {
		t.Fatalf("日志窗口错误: first=%q last=%q", logs[0], logs[len(logs)-1])
	}
}

func TestProgressWriterSplitsLines(t *testing.T) {
	var lines []string
	bridge := &tuiBridge{send: func(msg tea.Msg) {
		lines = append(lines, msg.(tuiInstallLogMsg).line)
	}}
	w := newTUIProgressWriter(bridge)
	if _, err := w.Write([]byte("a\nb\r\npart")); err != nil {
		t.Fatal(err)
	}
	w.Flush()
	want := []string{"a", "b", "part"}
	if len(lines) != len(want) {
		t.Fatalf("拆分结果错误: %v", lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("第 %d 行 = %q，期望 %q", i, lines[i], want[i])
		}
	}
}

func TestTUIInstanceNewRowRoutes(t *testing.T) {
	root := t.TempDir()
	m := newTUIModel(root)
	m.page = tuiPageInstances
	m.instMode = tuiInstanceManage
	m.instances = []instance.Entry{{Name: "alpha", Dir: filepath.Join(root, "alpha")}}
	m.instCursor = 0

	m, _ = updateTUIModel(t, m, tuiKey(tea.KeyEnter))
	if m.page != tuiPageNewInstance {
		t.Fatalf("首行 Enter 应打开新建实例向导，实际 %v", m.page)
	}
	m, _ = updateTUIModel(t, m, tuiRunes("q"))
	if m.page != tuiPageInstances {
		t.Fatalf("q 应返回实例列表，实际 %v", m.page)
	}
	m, _ = updateTUIModel(t, m, tuiKey(tea.KeyDown))
	m, _ = updateTUIModel(t, m, tuiKey(tea.KeyEnter))
	if m.page != tuiPageInstanceMenu || m.selected.Name != "alpha" {
		t.Fatalf("第二行应打开 alpha 操作菜单: page=%v selected=%q", m.page, m.selected.Name)
	}
}

func TestProgressViewShowsStatus(t *testing.T) {
	m := newTUIModel(t.TempDir())
	m.width, m.height = 80, 24
	m.page = tuiPageNewProgress
	m.newInst.install = tuiInstallRun{
		title: "正在导入",
		name:  "demo",
		dir:   "/tmp/demo",
		logs:  []string{"[Download] 下载进度：1/2（50%）", "[错误] boom"},
		done:  true,
		err:   "boom\nsecond line",
	}
	out := m.View()
	if !strings.Contains(out, "安装失败") || !strings.Contains(out, "boom / second line") {
		t.Fatalf("进度页缺少失败状态或未合并换行:\n%s", out)
	}
}

func writeTestMrpack(t *testing.T, path, index string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("modrinth.index.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, index); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
