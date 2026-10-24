package backup

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func makeInstance(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range []string{
		".mtl/config.json",
		"saves/world/level.dat",
		"mods/example.jar",
		"config/example.toml",
		"logs/latest.log",
		"crash-reports/crash-1.txt",
		"resourcepacks/example.zip",
		"screenshots/shot.png",
		"libraries/dependency.jar",
		"assets/indexes/1.20.json",
		"versions/1.20.1/1.20.1.jar",
		"data/not-backed-up.txt",
	} {
		mustWrite(t, filepath.Join(root, filepath.FromSlash(rel)), "content:"+rel)
	}
	mustWrite(t, filepath.Join(root, "options.txt"), "options")
	mustWrite(t, filepath.Join(root, "launcher_profiles.json"), "{}")
	return root
}

func planFiles(plan Plan) map[string]Entry {
	out := make(map[string]Entry)
	for _, e := range plan.Entries {
		if !e.Dir {
			out[e.Rel] = e
		}
	}
	return out
}

func TestBuildPlanIncludesOnlyInstanceData(t *testing.T) {
	root := makeInstance(t)
	plan, err := BuildPlan(root)
	if err != nil {
		t.Fatal(err)
	}
	files := planFiles(plan)
	for _, want := range []string{
		".mtl/config.json",
		"saves/world/level.dat",
		"mods/example.jar",
		"config/example.toml",
		"logs/latest.log",
		"crash-reports/crash-1.txt",
		"resourcepacks/example.zip",
		"screenshots/shot.png",
		"options.txt",
		"launcher_profiles.json",
	} {
		if _, ok := files[want]; !ok {
			t.Errorf("缺少应打包文件 %s", want)
		}
	}
	for _, forbidden := range []string{
		"libraries/dependency.jar",
		"assets/indexes/1.20.json",
		"versions/1.20.1/1.20.1.jar",
		"data/not-backed-up.txt",
	} {
		if _, ok := files[forbidden]; ok {
			t.Errorf("不应打包 %s", forbidden)
		}
	}
	if plan.FileCount != len(files) {
		t.Fatalf("FileCount = %d, files = %d", plan.FileCount, len(files))
	}
	wantInclude := map[string]bool{
		".mtl": true, "config": true, "crash-reports": true, "logs": true,
		"mods": true, "resourcepacks": true, "saves": true, "screenshots": true,
		"options.txt": true, "launcher_profiles.json": true,
	}
	for _, inc := range plan.Include {
		if !wantInclude[inc] {
			t.Errorf("include 中出现意外项 %q", inc)
		}
		delete(wantInclude, inc)
	}
	if len(wantInclude) != 0 {
		t.Errorf("include 缺少 %v", wantInclude)
	}
}

func TestBuildPlanSkipsSymlinks(t *testing.T) {
	root := makeInstance(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	mustWrite(t, outside, "outside")
	link := filepath.Join(root, "mods", "external.jar")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("当前环境无法创建软链接: %v", err)
	}

	plan, err := BuildPlan(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := planFiles(plan)["mods/external.jar"]; ok {
		t.Fatal("软链接不应进入备份")
	}
	found := false
	for _, sk := range plan.Skipped {
		if sk.Path == "mods/external.jar" {
			found = true
		}
	}
	if !found {
		t.Fatal("未记录被跳过的软链接")
	}
}

func TestWriteArchiveAndReadInfo(t *testing.T) {
	root := makeInstance(t)
	plan, err := BuildPlan(root)
	if err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(t.TempDir(), "backups")
	t.Setenv(EnvBackupsDir, backupDir)
	now := time.Date(2026, 10, 24, 15, 30, 0, 0, time.Local)

	res, err := WriteArchive(plan, "demo", WriteOptions{Name: "v1.20.1 测试", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	wantBase := "demo-20261024-153000-v1.20.1-测试.zip"
	if filepath.Base(res.Path) != wantBase {
		t.Fatalf("备份文件名 = %q, want %q", filepath.Base(res.Path), wantBase)
	}
	if filepath.Dir(res.Path) != backupDir {
		t.Fatalf("备份目录 = %q, want %q", filepath.Dir(res.Path), backupDir)
	}

	info, err := ReadInfo(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Metadata.Instance != "demo" || info.Metadata.Name != "v1.20.1 测试" {
		t.Fatalf("meta = %+v", info.Metadata)
	}
	if !info.Metadata.CreatedAt.Equal(now) {
		t.Fatalf("created_at = %s, want %s", info.Metadata.CreatedAt, now)
	}
	if info.Size <= 0 || res.ArchiveSize != info.Size {
		t.Fatalf("zip 大小异常: info=%d result=%d", info.Size, res.ArchiveSize)
	}
	if res.FileCount != plan.FileCount || res.TotalBytes != plan.TotalBytes {
		t.Fatalf("result = %+v, plan = %+v", res, plan)
	}

	zr, err := zip.OpenReader(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	names := make(map[string]bool)
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names[MetadataFile] {
		t.Fatal("缺少 backup.json")
	}
	if names["options.txt"] != true || names["saves/world/level.dat"] != true {
		t.Fatal("缺少应打包文件")
	}
	if names["libraries/dependency.jar"] || names["assets/indexes/1.20.json"] {
		t.Fatal("备份包中不应出现 libraries / assets")
	}
	if names[MetadataFile+"/"] {
		t.Fatal("backup.json 不应是目录")
	}

	if _, err := WriteArchive(plan, "demo", WriteOptions{Out: res.Path, Now: now}); err == nil {
		t.Fatal("已存在的自定义输出文件应拒绝覆盖")
	}
}

func TestWriteArchiveCustomOut(t *testing.T) {
	root := makeInstance(t)
	plan, err := BuildPlan(root)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "nested", "custom.zip")
	res, err := WriteArchive(plan, "demo", WriteOptions{Out: out, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != out {
		t.Fatalf("Path = %q, want %q", res.Path, out)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultDir(t *testing.T) {
	custom := filepath.Join(t.TempDir(), "chosen")
	t.Setenv(EnvBackupsDir, custom)
	got, err := DefaultDir()
	if err != nil || got != custom {
		t.Fatalf("DefaultDir() = %q, %v, want %q", got, err, custom)
	}

	t.Setenv(EnvBackupsDir, "")
	if runtime.GOOS == "windows" {
		appData := filepath.Join(t.TempDir(), "Roaming")
		t.Setenv("APPDATA", appData)
		got, err := DefaultDir()
		want := filepath.Join(appData, "mtl", "backups")
		if err != nil || got != want {
			t.Fatalf("DefaultDir() = %q, %v, want %q", got, err, want)
		}
		return
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err = DefaultDir()
	want := filepath.Join(home, ".cache", "mtl", "backups")
	if err != nil || got != want {
		t.Fatalf("DefaultDir() = %q, %v, want %q", got, err, want)
	}
}

func TestBackupFileNameSanitizesRemark(t *testing.T) {
	now := time.Date(2026, 10, 24, 1, 2, 3, 0, time.Local)
	got := backupFileName("demo", now, ` v1/../\bad:name*? `)
	if strings.ContainsAny(got, `/\:*?"<>|`) {
		t.Fatalf("文件名仍含非法字符: %s", got)
	}
	if !strings.HasPrefix(got, "demo-20261024-010203-") || !strings.HasSuffix(got, ".zip") {
		t.Fatalf("文件名格式错误: %s", got)
	}
}

func TestListBackups(t *testing.T) {
	root := makeInstance(t)
	plan, err := BuildPlan(root)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	older := time.Date(2026, 10, 20, 8, 0, 0, 0, time.Local)
	newer := time.Date(2026, 10, 24, 9, 0, 0, 0, time.Local)
	if _, err := WriteArchive(plan, "demo", WriteOptions{
		Out: filepath.Join(dir, "old.zip"), Name: "旧备份", Now: older,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteArchive(plan, "demo", WriteOptions{
		Out: filepath.Join(dir, "new.zip"), Name: "新备份", Now: newer,
	}); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "broken.zip"), "not a zip")
	mustWrite(t, filepath.Join(dir, "ignore.txt"), "not a backup")

	items, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("List 返回 %d 项, want 3: %+v", len(items), items)
	}
	if items[0].Info.Metadata.Name != "新备份" || items[1].Info.Metadata.Name != "旧备份" {
		t.Fatalf("排序错误: %+v", items)
	}
	if items[2].Err == "" {
		t.Fatal("损坏的 zip 应标记错误")
	}
	for _, item := range items {
		if item.Info.Size <= 0 {
			t.Fatalf("大小未填充: %+v", item)
		}
	}
}

func TestReadInfoRejectsMissingMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadInfo(path); err == nil || !strings.Contains(err.Error(), MetadataFile) {
		t.Fatalf("缺少 backup.json 应报错, got %v", err)
	}
}

func TestReadInfoRejectsMetadataWithPathTraversal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad-meta.zip")
	writeRawZip(t, path, nil, map[string]string{MetadataFile: `{"instance":"../evil","created_at":"2026-10-24T00:00:00Z","include":[]}`})
	if _, err := ReadInfo(path); err == nil {
		t.Fatal("非法实例名应被拒绝")
	}
}

// writeRawZip 生成测试 zip；meta 为 nil 时不写入 backup.json。
func writeRawZip(t *testing.T, path string, entries map[string]string, meta map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range meta {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func validMetadataJSON(t *testing.T, instance string) string {
	t.Helper()
	data, err := json.Marshal(Metadata{
		Instance:  instance,
		CreatedAt: time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC),
		Include:   []string{"saves"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestWriteArchiveRejectsInvalidInstanceName(t *testing.T) {
	root := makeInstance(t)
	plan, err := BuildPlan(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "../evil", "a/b", `a\b`} {
		if _, err := WriteArchive(plan, name, WriteOptions{Out: filepath.Join(t.TempDir(), "x.zip")}); err == nil {
			t.Errorf("实例名 %q 应被拒绝", name)
		}
	}
}

func TestWriteArchiveRejectsControlCharacterInName(t *testing.T) {
	root := makeInstance(t)
	plan, err := BuildPlan(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteArchive(plan, "demo", WriteOptions{
		Out:  filepath.Join(t.TempDir(), "x.zip"),
		Name: "bad\nname",
	}); err == nil {
		t.Fatal("备注包含控制字符时应拒绝写入")
	}
}

func TestWriteArchiveMissingSourceFile(t *testing.T) {
	root := makeInstance(t)
	plan, err := BuildPlan(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "options.txt")); err != nil {
		t.Fatal(err)
	}
	_, err = WriteArchive(plan, "demo", WriteOptions{Out: filepath.Join(t.TempDir(), "x.zip")})
	if err == nil {
		t.Fatal("扫描后消失的文件应导致备份失败")
	}
}

func TestErrorsIsTargetExists(t *testing.T) {
	// 保持哨兵错误可被调用方识别，避免 CLI 只比较字符串。
	if !errors.Is(ErrTargetExists, ErrTargetExists) {
		t.Fatal("ErrTargetExists 不可比较")
	}
}
