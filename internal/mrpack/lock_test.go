package mrpack

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testHash(seed string) string {
	// 测试只关心字符串是否一致，构造合法的 sha256- 哈希即可。
	return hashPrefix + strings.Repeat(seed, 64)[:64]
}

func writeLockFile(t *testing.T, dir, body string) {
	t.Helper()
	path := filepath.Join(dir, ".mtl", "pack-lock.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeInstanceFile(t *testing.T, dir, rel, body string) string {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

func TestDiffFiles(t *testing.T) {
	oldFiles := map[string]string{
		"mods/same.jar":    testHash("a"),
		"mods/changed.jar": testHash("b"),
		"mods/removed.jar": testHash("c"),
		"saves/world.dat":  testHash("d"),
		"mods/Case.jar":    testHash("e"),
	}
	newFiles := map[string]string{
		"mods/same.jar":    testHash("a"),
		"mods/changed.jar": testHash("f"),
		"mods/new.jar":     testHash("1"),
		// 仅大小写变化视为同一文件，避免 Windows 上误删。
		"mods/case.jar": testHash("e"),
	}

	got := DiffFiles(oldFiles, newFiles)
	if want := []string{"mods/new.jar"}; !reflect.DeepEqual(got.Added, want) {
		t.Errorf("Added = %v, want %v", got.Added, want)
	}
	if want := []string{"mods/changed.jar"}; !reflect.DeepEqual(got.Updated, want) {
		t.Errorf("Updated = %v, want %v", got.Updated, want)
	}
	if want := []string{"mods/removed.jar", "saves/world.dat"}; !reflect.DeepEqual(got.Removed, want) {
		t.Errorf("Removed = %v, want %v", got.Removed, want)
	}
}

func TestDiffFilesEmpty(t *testing.T) {
	got := DiffFiles(nil, nil)
	if len(got.Added)+len(got.Updated)+len(got.Removed) != 0 {
		t.Fatalf("空清单不应产生差异: %+v", got)
	}
}

func TestIsProtectedPath(t *testing.T) {
	protected := []string{"saves", "saves/world/level.dat", "Saves/world.dat", ".mtl/pack-lock.json"}
	for _, p := range protected {
		if !IsProtectedPath(p) {
			t.Errorf("%q 应受保护", p)
		}
	}
	open := []string{"mods/saves.jar", "saves.txt", "config/saves/a.toml", "mods/a.jar"}
	for _, p := range open {
		if IsProtectedPath(p) {
			t.Errorf("%q 不应受保护", p)
		}
	}
}

func TestLockSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := &Lock{
		PackID:      "Test Pack",
		PackVersion: "1.0.0",
		Source:      "/packs/test.mrpack",
		Files: map[string]string{
			"mods/a.jar":       testHash("a"),
			"config/base.toml": testHash("b"),
		},
	}
	if err := SaveLock(dir, want); err != nil {
		t.Fatalf("SaveLock: %v", err)
	}
	got, err := LoadLock(dir)
	if err != nil {
		t.Fatalf("LoadLock: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadLock = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(LockRelPath))); err != nil {
		t.Fatalf("锁定清单未落盘: %v", err)
	}
}

func TestLoadLockMissing(t *testing.T) {
	lock, err := LoadLock(t.TempDir())
	if err != nil {
		t.Fatalf("缺少清单不应报错: %v", err)
	}
	if lock != nil {
		t.Fatalf("缺少清单应返回 nil，got %+v", lock)
	}
}

func TestLoadLockRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"bad json":      "{",
		"bad path":      `{"pack_id":"x","pack_version":"1","files":{"../evil":"` + testHash("a") + `"}}`,
		"bad hash":      `{"pack_id":"x","pack_version":"1","files":{"mods/a.jar":"md5-abc"}}`,
		"trailing data": `{"pack_id":"x","pack_version":"1","files":{}} {}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeLockFile(t, dir, body)
			if _, err := LoadLock(dir); err == nil {
				t.Fatal("损坏的锁定清单应报错")
			}
		})
	}
}

func TestManagedFilesExcludesProtected(t *testing.T) {
	idx := strings.Replace(minimalIndex(), `"files": [`, `"files": [
    {
      "path": "saves/level.dat",
      "hashes": {"sha512": "`+strings.Repeat("c", 128)+`"},
      "downloads": ["https://cdn.example.com/level.dat"],
      "fileSize": 4
    },`, 1)
	packPath := writePack(t,
		zipItem{name: IndexFileName, body: idx},
		zipItem{name: "overrides/config/base.toml", body: "base"},
		zipItem{name: "overrides/saves/world.dat", body: "save"},
		zipItem{name: "client-overrides/config/base.toml", body: "client"},
	)
	dir := t.TempDir()
	p := mustOpen(t, packPath)
	if err := p.ExtractOverrides(dir); err != nil {
		t.Fatalf("ExtractOverrides: %v", err)
	}
	writeInstanceFile(t, dir, "mods/example.jar", "jar")

	files, err := p.ManagedFiles(dir)
	if err != nil {
		t.Fatalf("ManagedFiles: %v", err)
	}
	for _, rel := range []string{"mods/example.jar", "config/base.toml"} {
		if _, ok := files[rel]; !ok {
			t.Errorf("缺少托管文件 %q: %v", rel, files)
		}
	}
	if _, ok := files["saves/world.dat"]; ok {
		t.Errorf("saves/ 不应纳入锁定清单: %v", files)
	}
	if len(files) != 2 {
		t.Errorf("托管文件数量 = %d, want 2: %v", len(files), files)
	}
	wantHash, err := hashFile(filepath.Join(dir, "config", "base.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if files["config/base.toml"] != wantHash {
		t.Errorf("client-overrides 应覆盖 overrides，hash = %q, want %q", files["config/base.toml"], wantHash)
	}
}

func TestPlanPruneProtectsUserFiles(t *testing.T) {
	dir := t.TempDir()
	oldBody := writeInstanceFile(t, dir, "mods/old.jar", "old")
	changedBody := writeInstanceFile(t, dir, "mods/changed.jar", "user-edited")
	keepBody := writeInstanceFile(t, dir, "mods/keep.jar", "keep")
	saveBody := writeInstanceFile(t, dir, "saves/world.dat", "save")

	hash := func(path string) string {
		t.Helper()
		sum, err := hashFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return sum
	}
	// changed.jar 的旧锁定哈希来自原始内容，与当前用户修改后的内容不同。
	old := &Lock{Files: map[string]string{
		"mods/old.jar":     hash(oldBody),
		"mods/changed.jar": hashOfStringTest(t, "original"),
		"mods/keep.jar":    hash(keepBody),
		"saves/world.dat":  hash(saveBody),
		"mods/missing.jar": testHash("c"),
	}}
	desired := map[string]string{"mods/keep.jar": hash(keepBody)}

	plan := PlanPrune(dir, old, desired)
	if want := []string{"mods/old.jar"}; !reflect.DeepEqual(plan.Delete, want) {
		t.Fatalf("Delete = %v, want %v", plan.Delete, want)
	}
	reasons := map[string]string{}
	for _, k := range plan.Keep {
		reasons[k.Path] = k.Reason
	}
	if reasons["mods/changed.jar"] != "已被用户修改" {
		t.Errorf("用户修改文件应保留，got %v", plan.Keep)
	}
	if reasons["saves/world.dat"] != "受保护路径" {
		t.Errorf("saves/ 应受保护，got %v", plan.Keep)
	}
	if _, ok := reasons["mods/missing.jar"]; ok {
		t.Errorf("已不存在的文件无需保留记录: %v", plan.Keep)
	}

	deleted, err := ApplyPrune(dir, plan.Delete)
	if err != nil || deleted != 1 {
		t.Fatalf("ApplyPrune = %d, %v", deleted, err)
	}
	if _, err := os.Stat(oldBody); !os.IsNotExist(err) {
		t.Fatal("旧托管文件应被删除")
	}
	for _, path := range []string{changedBody, keepBody, saveBody} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s 不应被删除: %v", path, err)
		}
	}
	if n, err := ApplyPrune(dir, plan.Delete); err != nil || n != 0 {
		t.Fatalf("重复清理应幂等，got %d, %v", n, err)
	}
}

func TestIsDisabledModPath(t *testing.T) {
	disabled := []string{
		"mods/disabled/a.jar",
		"Mods/Disabled/A.jar",
		"mods/DISABLED/sub/a.jar",
	}
	for _, p := range disabled {
		if !IsDisabledModPath(p) {
			t.Errorf("%q 应识别为禁用目录", p)
		}
	}
	open := []string{
		"mods/a.jar",
		"mods/disabled.txt",
		"disabled/a.jar",
		"config/disabled/a.toml",
		"mods",
	}
	for _, p := range open {
		if IsDisabledModPath(p) {
			t.Errorf("%q 不应识别为禁用目录", p)
		}
	}
}

func TestPlanPruneKeepsDisabledMods(t *testing.T) {
	dir := t.TempDir()
	disabledBody := writeInstanceFile(t, dir, "mods/disabled/other.jar", "manual")
	movedBody := writeInstanceFile(t, dir, "mods/disabled/manual.jar", "moved")

	// mods/manual.jar 已不在原位，但同名文件位于 mods/disabled/，说明用户手动禁用了它。
	// mods/disabled/other.jar 直接出现在旧清单中时也必须保留。
	old := &Lock{Files: map[string]string{
		"mods/manual.jar":         testHash("a"),
		"mods/disabled/other.jar": testHash("b"),
	}}
	plan := PlanPrune(dir, old, map[string]string{})
	if len(plan.Delete) != 0 {
		t.Fatalf("禁用目录不应进入清理计划: %+v", plan.Delete)
	}
	reasons := map[string]string{}
	for _, k := range plan.Keep {
		reasons[k.Path] = k.Reason
	}
	if reasons["mods/manual.jar"] != "mod 已被手动禁用" {
		t.Errorf("移动后的 mod 应保留，got %+v", plan.Keep)
	}
	if reasons["mods/disabled/other.jar"] != "手动禁用的 mod" {
		t.Errorf("禁用目录应受保护，got %+v", plan.Keep)
	}

	// 双重保护：即使调用方显式传入禁用路径，ApplyPrune 也不能删除。
	deleted, err := ApplyPrune(dir, []string{"mods/disabled/other.jar"})
	if err != nil || deleted != 0 {
		t.Fatalf("ApplyPrune = %d, %v", deleted, err)
	}
	for _, path := range []string{disabledBody, movedBody} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s 不应被删除: %v", path, err)
		}
	}
}

func hashOfStringTest(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	returnFunc := writeInstanceFile(t, dir, "x", body)
	sum, err := hashFile(returnFunc)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}
