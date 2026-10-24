package backup

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func buildTestBackup(t *testing.T, instanceName string, files map[string]string, now time.Time) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		mustWrite(t, filepath.Join(root, filepath.FromSlash(rel)), content)
	}
	plan, err := BuildPlan(root)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "backup.zip")
	if _, err := WriteArchive(plan, instanceName, WriteOptions{Out: out, Now: now}); err != nil {
		t.Fatal(err)
	}
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRestoreCreatesTarget(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.Local)
	zipPath := buildTestBackup(t, "demo", map[string]string{
		"saves/world/level.dat": "level",
		"mods/a.jar":            "jar",
		"options.txt":           "opts",
		"libraries/skip.jar":    "skip",
	}, now)

	parent := filepath.Join(t.TempDir(), "new-root")
	target := filepath.Join(parent, "demo")
	res, err := Restore(zipPath, target, RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.TargetDir != target || res.Instance != "demo" {
		t.Fatalf("result = %+v", res)
	}
	if res.Files != 3 {
		t.Fatalf("Files = %d, want 3", res.Files)
	}
	if got := readFile(t, filepath.Join(target, "saves", "world", "level.dat")); got != "level" {
		t.Fatalf("level.dat = %q", got)
	}
	if got := readFile(t, filepath.Join(target, "options.txt")); got != "opts" {
		t.Fatalf("options.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(target, "libraries", "skip.jar")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("libraries 不应被还原: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, MetadataFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup.json 不应还原到实例目录: %v", err)
	}
}

func TestRestoreExistingWithoutForceRefuses(t *testing.T) {
	zipPath := buildTestBackup(t, "demo", map[string]string{
		"saves/new.txt": "new",
	}, time.Now())
	target := filepath.Join(t.TempDir(), "demo")
	mustWrite(t, filepath.Join(target, "saves", "keep.txt"), "keep")

	_, err := Restore(zipPath, target, RestoreOptions{})
	if !errors.Is(err, ErrTargetExists) {
		t.Fatalf("err = %v, want ErrTargetExists", err)
	}
	if got := readFile(t, filepath.Join(target, "saves", "keep.txt")); got != "keep" {
		t.Fatalf("原有文件被修改: %q", got)
	}
	if _, err := os.Stat(filepath.Join(target, "saves", "new.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("未加 --force 时不应写入新文件: %v", err)
	}
}

func TestRestoreForceKeepsOldEntries(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.Local)
	zipPath := buildTestBackup(t, "demo", map[string]string{
		"saves/new.txt":   "new-save",
		"config/new.toml": "new-config",
		"options.txt":     "new-options",
	}, now)
	target := filepath.Join(t.TempDir(), "demo")
	mustWrite(t, filepath.Join(target, "saves", "keep.txt"), "keep-save")
	mustWrite(t, filepath.Join(target, "config", "keep.toml"), "keep-config")
	mustWrite(t, filepath.Join(target, "options.txt"), "old-options")
	mustWrite(t, filepath.Join(target, "unrelated", "keep.txt"), "unrelated")

	res, err := Restore(zipPath, target, RestoreOptions{Force: true, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Renamed) != 3 {
		t.Fatalf("Renamed = %+v, want 3", res.Renamed)
	}
	suffix := ".old-20261024-120000"
	oldSave := filepath.Join(target, "saves"+suffix, "keep.txt")
	oldConfig := filepath.Join(target, "config"+suffix, "keep.toml")
	oldOptions := filepath.Join(target, "options.txt"+suffix)
	if got := readFile(t, oldSave); got != "keep-save" {
		t.Fatalf("旧 saves 内容 = %q", got)
	}
	if got := readFile(t, oldConfig); got != "keep-config" {
		t.Fatalf("旧 config 内容 = %q", got)
	}
	if got := readFile(t, oldOptions); got != "old-options" {
		t.Fatalf("旧 options 内容 = %q", got)
	}
	if got := readFile(t, filepath.Join(target, "saves", "new.txt")); got != "new-save" {
		t.Fatalf("新 saves 内容 = %q", got)
	}
	if got := readFile(t, filepath.Join(target, "options.txt")); got != "new-options" {
		t.Fatalf("新 options 内容 = %q", got)
	}
	if _, err := os.Stat(filepath.Join(target, "saves", "keep.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("旧 saves 不应残留在新目录: %v", err)
	}
	if got := readFile(t, filepath.Join(target, "unrelated", "keep.txt")); got != "unrelated" {
		t.Fatalf("未覆盖的无关目录被修改: %q", got)
	}
}

func TestRestoreRejectsZipSlipBeforeWriting(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.Local)
	zipPath := filepath.Join(t.TempDir(), "evil.zip")
	writeRawZip(t, zipPath,
		map[string]string{"../escape.txt": "escaped"},
		map[string]string{MetadataFile: validMetadataJSON(t, "demo")})

	targetRoot := t.TempDir()
	target := filepath.Join(targetRoot, "demo")
	mustWrite(t, filepath.Join(target, "keep.txt"), "keep")

	_, err := Restore(zipPath, target, RestoreOptions{Force: true, Now: now})
	if err == nil || !strings.Contains(err.Error(), "不安全") {
		t.Fatalf("路径穿越应被拦截, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetRoot, "escape.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("实例目录外出现文件: %v", err)
	}
	if got := readFile(t, filepath.Join(target, "keep.txt")); got != "keep" {
		t.Fatalf("校验失败时不应修改目标: %q", got)
	}
}

func TestRestoreRejectsOutsideScope(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "outside-scope.zip")
	writeRawZip(t, zipPath,
		map[string]string{"libraries/evil.jar": "x"},
		map[string]string{MetadataFile: validMetadataJSON(t, "demo")})
	if _, err := Restore(zipPath, filepath.Join(t.TempDir(), "demo"), RestoreOptions{}); err == nil {
		t.Fatal("libraries 不在可还原范围内，应报错")
	}
}

func TestRestoreRejectsSymlinkEntry(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.Local)
	zipPath := filepath.Join(t.TempDir(), "symlink.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	metaHeader := &zip.FileHeader{Name: MetadataFile, Method: zip.Deflate}
	mw, err := zw.CreateHeader(metaHeader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mw.Write([]byte(validMetadataJSON(t, "demo"))); err != nil {
		t.Fatal(err)
	}
	header := &zip.FileHeader{Name: "mods/link.jar", Method: zip.Store}
	header.SetMode(os.ModeSymlink | 0o777)
	w, err := zw.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("/etc/passwd")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(t.TempDir(), "demo")
	_, err = Restore(zipPath, target, RestoreOptions{Force: true, Now: now})
	if err == nil || !strings.Contains(err.Error(), "软链接") {
		t.Fatalf("软链接条目应被拒绝, got %v", err)
	}
	if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("校验失败时不应创建目标目录: %v", statErr)
	}
}

func TestRestoreRejectsDuplicateAndConflictingPaths(t *testing.T) {
	t.Run("duplicate", func(t *testing.T) {
		zipPath := filepath.Join(t.TempDir(), "duplicate.zip")
		f, err := os.Create(zipPath)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(f)
		for _, name := range []string{MetadataFile, "saves/a", "saves/a"} {
			w, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			content := "1"
			if name == MetadataFile {
				content = validMetadataJSON(t, "demo")
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
		if _, err := Restore(zipPath, filepath.Join(t.TempDir(), "demo"), RestoreOptions{}); err == nil {
			t.Fatal("重复路径应被拒绝")
		}
	})

	t.Run("file-parent", func(t *testing.T) {
		zipPath := filepath.Join(t.TempDir(), "conflict.zip")
		writeRawZip(t, zipPath,
			map[string]string{"saves": "file", "saves/a": "2"},
			map[string]string{MetadataFile: validMetadataJSON(t, "demo")})
		if _, err := Restore(zipPath, filepath.Join(t.TempDir(), "demo"), RestoreOptions{}); err == nil {
			t.Fatal("文件/目录冲突应被拒绝")
		}
	})
}

func TestRestoreRejectsMissingMetadata(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "no-meta.zip")
	writeRawZip(t, zipPath, map[string]string{"saves/a": "1"}, nil)
	if _, err := Restore(zipPath, filepath.Join(t.TempDir(), "demo"), RestoreOptions{}); err == nil {
		t.Fatal("缺少 backup.json 应报错")
	}
}

func TestRestoreRejectsTargetSymlink(t *testing.T) {
	zipPath := buildTestBackup(t, "demo", map[string]string{"saves/a": "1"}, time.Now())
	real := filepath.Join(t.TempDir(), "real")
	mustMkdir(t, real)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("当前环境无法创建软链接: %v", err)
	}
	if _, err := Restore(zipPath, link, RestoreOptions{Force: true}); err == nil {
		t.Fatal("目标为软链接时应在写入前拒绝")
	}
}
