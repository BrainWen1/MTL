package mod

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jarBytes 生成一个包含指定条目的 jar 内容，便于构造嵌套 jar。
func jarBytes(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for entry, body := range entries {
		w, err := zw.Create(entry)
		if err != nil {
			t.Fatalf("创建 zip 条目 %s: %v", entry, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("写入 zip 条目 %s: %v", entry, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip: %v", err)
	}
	return buf.Bytes()
}

// writeJar 在 dir/name 生成一个包含指定条目的 jar 文件。
func writeJar(t *testing.T, dir, name string, entries map[string]string) string {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, jarBytes(t, entries), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

func findMod(t *testing.T, mods []Mod, file string) Mod {
	t.Helper()
	for _, m := range mods {
		if m.File == file {
			return m
		}
	}
	t.Fatalf("未找到 mod %q: %+v", file, mods)
	return Mod{}
}

func fabricJSON(id, name, version string, depends string) string {
	body := `{"schemaVersion":1,"id":"` + id + `","name":"` + name + `","version":"` + version + `"`
	if depends != "" {
		body += `,"depends":` + depends
	}
	return body + "}"
}

func TestScanMetadata(t *testing.T) {
	dir := t.TempDir()
	writeJar(t, dir, "mods/fabric.jar", map[string]string{
		"fabric.mod.json": fabricJSON("fabric-api", "Fabric API", "0.92.0+1.20.1",
			`{"fabricloader":">=0.14","minecraft":"~1.20.1","java":">=17"}`),
	})
	writeJar(t, dir, "mods/forge.jar", map[string]string{
		"META-INF/mods.toml": `
modLoader="javafml"
loaderVersion="[47,)"
license="MIT"

[[mods]]
modId="examplemod"
version="${file.jarVersion}"
displayName="Example Mod"
description="A test mod"

[[dependencies.examplemod]]
    modId="forge"
    mandatory=true
    versionRange="[47,)"
[[dependencies.examplemod]]
    modId="minecraft"
    mandatory=true
    versionRange="[1.20.1,1.21)"
[[dependencies.examplemod]]
    modId="optional_lib"
    mandatory=false
    versionRange="*"
`,
	})
	writeJar(t, dir, "mods/disabled/old.jar", map[string]string{
		"fabric.mod.json": fabricJSON("oldmod", "Old Mod", "1.0.0", ""),
	})
	if err := os.WriteFile(filepath.Join(dir, "mods", "broken.jar"), []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mods", "readme.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}

	mods, err := Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(mods) != 4 {
		t.Fatalf("mod 数量 = %d, want 4: %+v", len(mods), mods)
	}

	fabric := findMod(t, mods, "fabric.jar")
	if fabric.Loader != LoaderFabric || fabric.ID != "fabric-api" || fabric.Name != "Fabric API" {
		t.Errorf("Fabric 元数据错误: %+v", fabric)
	}
	if fabric.Version != "0.92.0+1.20.1" || len(fabric.Dependencies) != 3 {
		t.Errorf("Fabric 版本/依赖错误: %+v", fabric)
	}
	if fabric.Disabled || !fabric.Known {
		t.Errorf("Fabric mod 状态错误: %+v", fabric)
	}

	forge := findMod(t, mods, "forge.jar")
	if forge.Loader != LoaderForge || forge.ID != "examplemod" || forge.Name != "Example Mod" {
		t.Errorf("Forge 元数据错误: %+v", forge)
	}
	if forge.Version != "${file.jarVersion}" {
		t.Errorf("Forge 版本应保留原始占位符，got %q", forge.Version)
	}
	if len(forge.Dependencies) != 3 {
		t.Fatalf("Forge 依赖数量 = %d, want 3: %+v", len(forge.Dependencies), forge.Dependencies)
	}
	for _, dep := range forge.Dependencies {
		if dep.ID == "optional_lib" && dep.Required {
			t.Errorf("mandatory=false 应标记为可选: %+v", dep)
		}
	}

	broken := findMod(t, mods, "broken.jar")
	if broken.Known || broken.DisplayName() != "未知mod" || broken.Err == "" {
		t.Errorf("损坏 jar 应标记为未知mod: %+v", broken)
	}

	old := findMod(t, mods, "old.jar")
	if !old.Disabled || old.Path != "mods/disabled/old.jar" {
		t.Errorf("禁用 mod 路径错误: %+v", old)
	}

	// 启用中的 mod 排在禁用 mod 之前。
	if mods[len(mods)-1].File != "old.jar" {
		t.Errorf("禁用 mod 应排在最后: %+v", mods)
	}
}

func TestScanMissingModsDir(t *testing.T) {
	mods, err := Scan(t.TempDir())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(mods) != 0 {
		t.Fatalf("空实例应返回空列表: %+v", mods)
	}
}

func TestScanInvalidJSONMetadata(t *testing.T) {
	dir := t.TempDir()
	writeJar(t, dir, "mods/badjson.jar", map[string]string{
		"fabric.mod.json": "{not json",
	})
	mods, err := Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	m := findMod(t, mods, "badjson.jar")
	if m.Known || !strings.Contains(m.Err, "fabric.mod.json") {
		t.Errorf("非法 JSON 应标记为未知mod: %+v", m)
	}
}

func TestResolve(t *testing.T) {
	mods := []Mod{
		{File: "Sodium.jar", Path: "mods/Sodium.jar", ID: "sodium", Known: true},
		{File: "sodium-extra.jar", Path: "mods/sodium-extra.jar", ID: "sodium-extra", Known: true},
		{File: "a.jar", Path: "mods/disabled/a.jar", ID: "alpha", Disabled: true, Known: true},
		{File: "dup1.jar", Path: "mods/dup1.jar", ID: "dup", Known: true},
		{File: "dup2.jar", Path: "mods/dup2.jar", ID: "dup", Known: true},
	}

	cases := map[string]string{
		"sodium.jar": "Sodium.jar",
		"SODIUM.JAR": "Sodium.jar",
		"sodium":     "Sodium.jar",
		"ALPHA":      "a.jar",
		"a":          "a.jar",
	}
	for target, want := range cases {
		m, err := Resolve(mods, target)
		if err != nil {
			t.Errorf("Resolve(%q): %v", target, err)
			continue
		}
		if m.File != want {
			t.Errorf("Resolve(%q) = %q, want %q", target, m.File, want)
		}
	}

	for _, target := range []string{"", "../evil", `mods\evil.jar`, "."} {
		if _, err := Resolve(mods, target); err == nil {
			t.Errorf("Resolve(%q) 应报错", target)
		}
	}
	if _, err := Resolve(mods, "dup"); err == nil || !strings.Contains(err.Error(), "多个") {
		t.Errorf("重复 mod id 应返回歧义错误, got %v", err)
	}
	if _, err := Resolve(mods, "nope"); err == nil {
		t.Error("不存在的目标应报错")
	}
}

func TestSetEnabledRoundTrip(t *testing.T) {
	dir := t.TempDir()
	jar := writeJar(t, dir, "mods/testmod.jar", map[string]string{
		"fabric.mod.json": fabricJSON("testmod", "Test Mod", "1.0.0", ""),
	})
	body, err := os.ReadFile(jar)
	if err != nil {
		t.Fatal(err)
	}

	m, changed, err := SetEnabled(dir, "testmod", false)
	if err != nil || !changed {
		t.Fatalf("禁用失败: changed=%v err=%v", changed, err)
	}
	if !m.Disabled || m.Path != "mods/disabled/testmod.jar" {
		t.Errorf("禁用后状态错误: %+v", m)
	}
	if _, err := os.Stat(jar); !os.IsNotExist(err) {
		t.Error("禁用后原文件应不存在")
	}
	disabledPath := filepath.Join(dir, "mods", "disabled", "testmod.jar")
	got, err := os.ReadFile(disabledPath)
	if err != nil {
		t.Fatalf("读取禁用文件: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Error("禁用不应修改 jar 内容")
	}

	if _, changed, err := SetEnabled(dir, "testmod", false); err != nil || changed {
		t.Errorf("重复禁用应幂等: changed=%v err=%v", changed, err)
	}

	m, changed, err = SetEnabled(dir, "TESTMOD.JAR", true)
	if err != nil || !changed {
		t.Fatalf("启用失败: changed=%v err=%v", changed, err)
	}
	if m.Disabled || m.Path != "mods/testmod.jar" {
		t.Errorf("启用后状态错误: %+v", m)
	}
	got, err = os.ReadFile(jar)
	if err != nil {
		t.Fatalf("读取启用文件: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Error("启用不应修改 jar 内容")
	}
	if _, changed, err := SetEnabled(dir, "testmod", true); err != nil || changed {
		t.Errorf("重复启用应幂等: changed=%v err=%v", changed, err)
	}
}

func TestSetEnabledDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	writeJar(t, dir, "mods/a.jar", map[string]string{
		"fabric.mod.json": fabricJSON("a", "A", "1", ""),
	})
	writeJar(t, dir, "mods/disabled/a.jar", map[string]string{
		"fabric.mod.json": fabricJSON("b", "B", "1", ""),
	})
	if _, _, err := SetEnabled(dir, "a.jar", false); err == nil {
		t.Fatal("目标已存在时应报错，不能覆盖已禁用的 mod")
	}
	if _, err := os.Stat(filepath.Join(dir, "mods", "a.jar")); err != nil {
		t.Error("失败时不应移动源文件")
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	writeJar(t, dir, "mods/a.jar", map[string]string{
		"fabric.mod.json": fabricJSON("a", "A", "1", ""),
	})
	writeJar(t, dir, "mods/disabled/b.jar", map[string]string{
		"fabric.mod.json": fabricJSON("b", "B", "1", ""),
	})

	if _, err := Remove(dir, "a.jar"); err != nil {
		t.Fatalf("删除启用 mod: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mods", "a.jar")); !os.IsNotExist(err) {
		t.Error("启用 mod 应被删除")
	}
	if _, err := Remove(dir, "b"); err != nil {
		t.Fatalf("按 id 删除禁用 mod: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mods", "disabled", "b.jar")); !os.IsNotExist(err) {
		t.Error("禁用 mod 应被删除")
	}
	if _, err := Remove(dir, "a.jar"); err == nil {
		t.Error("重复删除应报错")
	}
}

func TestCheck(t *testing.T) {
	mods := []Mod{
		{
			File: "a.jar", Path: "mods/a.jar", ID: "same", Loader: LoaderFabric, Known: true,
			Dependencies: []Dependency{{ID: "missing", Required: true}},
		},
		{File: "b.jar", Path: "mods/b.jar", ID: "same", Loader: LoaderForge, Known: true},
		{File: "c.jar", Path: "mods/disabled/c.jar", ID: "offdep", Loader: LoaderFabric, Disabled: true, Known: true},
		{
			File: "d.jar", Path: "mods/d.jar", ID: "d", Loader: LoaderFabric, Known: true,
			Dependencies: []Dependency{
				{ID: "offdep", Required: true},
				{ID: "minecraft", Required: true},
				{ID: "mixinextras", Required: true},
				{ID: "opt", Required: false},
			},
		},
		{File: "e.jar", Path: "mods/e.jar", Loader: LoaderUnknown, Err: "bad metadata"},
	}

	got := strings.Join(Check(mods, "fabric"), "\n")
	for _, want := range []string{
		"mods/e.jar 无法识别",
		"mods/b.jar 是 Forge mod，但实例加载器为 fabric",
		`mod id "same" 重复`,
		"mods/a.jar 缺少前置依赖: missing",
		`mods/d.jar 的前置依赖 "offdep" 已安装但处于禁用状态`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少警告 %q，got:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"minecraft", "mixinextras", "opt"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("内置/可选依赖不应告警 %q，got:\n%s", unwanted, got)
		}
	}
}

func TestNestedModulesProvideDependencies(t *testing.T) {
	dir := t.TempDir()
	nested := jarBytes(t, map[string]string{
		"fabric.mod.json": fabricJSON("fabric-api-base", "Fabric API Base", "2.0.0", ""),
	})
	writeJar(t, dir, "mods/fabric-api.jar", map[string]string{
		"fabric.mod.json":            fabricJSON("fabric-api", "Fabric API", "0.100.0", ""),
		"META-INF/jars/api-base.jar": string(nested),
	})
	writeJar(t, dir, "mods/sodium.jar", map[string]string{
		"fabric.mod.json": fabricJSON("sodium", "Sodium", "0.5.0",
			`{"fabric-api-base":">=2.0","fabricloader":">=0.15"}`),
	})

	mods, err := Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	api := findMod(t, mods, "fabric-api.jar")
	if len(api.Provides) != 1 || api.Provides[0] != "fabric-api-base" {
		t.Fatalf("嵌套模块未识别: %+v", api)
	}
	for _, w := range Check(mods, "fabric") {
		if strings.Contains(w, "fabric-api-base") {
			t.Errorf("嵌套模块不应被误报为缺失依赖: %s", w)
		}
	}
}

func TestParseForgeTOMLQuotesAndComments(t *testing.T) {
	meta, err := parseForgeTOML([]byte(`
# top comment
[[mods]]
modId = 'quotedmod'
version = "1.2.3" # inline comment
displayName = "A # Name"

[[dependencies.quotedmod]]
modId = "required_lib"
mandatory = true
versionRange = "[1.0,2.0)"
`))
	if err != nil {
		t.Fatalf("parseForgeTOML: %v", err)
	}
	if meta.id != "quotedmod" || meta.version != "1.2.3" || meta.name != "A # Name" {
		t.Errorf("字符串解析错误: %+v", meta)
	}
	if len(meta.deps) != 1 || meta.deps[0].ID != "required_lib" || !meta.deps[0].Required {
		t.Errorf("依赖解析错误: %+v", meta.deps)
	}
}

func TestParseForgeTOMLMissingID(t *testing.T) {
	if _, err := parseForgeTOML([]byte("[[mods]]\nversion=\"1\"\n")); err == nil {
		t.Fatal("缺少 modId 应报错")
	}
}
