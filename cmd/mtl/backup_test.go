package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mtl/internal/backup"
)

func TestParseBackupOptions(t *testing.T) {
	opts, err := parseBackupOptions([]string{
		"demo", "--out", "./mybackup.zip", "--name", "v1 测试", "--dry-run", "--debug", "--dir", "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.instance != "demo" || opts.out != "./mybackup.zip" || opts.name != "v1 测试" ||
		!opts.dryRun || !opts.debug || opts.root != "root" {
		t.Fatalf("opts = %+v", opts)
	}

	opts, err = parseBackupOptions([]string{"--dir=instances", "--out=out.zip", "--name=note", "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.root != "instances" || opts.out != "out.zip" || opts.name != "note" || opts.instance != "demo" {
		t.Fatalf("opts = %+v", opts)
	}

	for _, args := range [][]string{
		nil,
		{"a", "b"},
		{"../escape"},
		{"a/b"},
		{"--unknown", "demo"},
		{"demo", "--out"},
		{"demo", "--name"},
		{"demo", "--dir"},
		{"demo", "--dir="},
	} {
		if _, err := parseBackupOptions(args); err == nil {
			t.Errorf("参数 %v 应报错", args)
		}
	}
}

func TestParseRestoreOptions(t *testing.T) {
	opts, err := parseRestoreOptions([]string{
		"backup.zip", "--target", "demo", "--force", "--debug", "--dir", "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.zip != "backup.zip" || opts.target != "demo" || !opts.force || !opts.debug || opts.root != "root" || opts.list {
		t.Fatalf("opts = %+v", opts)
	}

	opts, err = parseRestoreOptions([]string{"--list", "--debug"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.list || !opts.debug {
		t.Fatalf("opts = %+v", opts)
	}

	for _, args := range [][]string{
		nil,
		{"backup.zip"},
		{"backup.zip", "--target", "../escape"},
		{"backup.zip", "--target", "a/b"},
		{"backup.zip", "--target", "demo", "extra"},
		{"--list", "backup.zip"},
		{"--list", "--target", "demo"},
		{"--list", "--force"},
		{"--unknown"},
		{"backup.zip", "--target"},
		{"backup.zip", "--target", "demo", "--dir="},
	} {
		if _, err := parseRestoreOptions(args); err == nil {
			t.Errorf("参数 %v 应报错", args)
		}
	}
}

func TestResolveBackupInstancePaths(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := resolveNamedInstance(root, "demo")
	if err != nil || got != dir {
		t.Fatalf("resolveNamedInstance = %q, %v, want %q", got, err, dir)
	}
	for _, name := range []string{"../escape", "a/b", `a\b`, ""} {
		if _, err := resolveNamedInstance(root, name); err == nil {
			t.Errorf("实例名 %q 应被拒绝", name)
		}
	}
	if _, err := resolveNamedInstance(root, "missing"); err == nil {
		t.Fatal("不存在的实例应报错")
	}

	target, err := resolveRestoreTarget(root, "new-instance")
	if err != nil || target != filepath.Join(root, "new-instance") {
		t.Fatalf("resolveRestoreTarget = %q, %v", target, err)
	}
	if _, err := resolveRestoreTarget(root, "../escape"); err == nil {
		t.Fatal("目标实例名路径穿越应被拒绝")
	}
}

func TestBackupRestoreCommandFlow(t *testing.T) {
	temp := t.TempDir()
	instanceRoot := filepath.Join(temp, "instances")
	src := filepath.Join(instanceRoot, "demo")
	for rel, content := range map[string]string{
		".mtl/config.json":      `{"memory_mode":"auto"}`,
		"saves/world/level.dat": "level",
		"mods/a.jar":            "jar",
		"options.txt":           "options",
		"libraries/skip.jar":    "skip",
	} {
		path := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	backupDir := filepath.Join(temp, "backups")
	t.Setenv(backup.EnvBackupsDir, backupDir)
	if err := backupCommand([]string{"demo", "--dir", instanceRoot}); err != nil {
		t.Fatal(err)
	}
	zips, err := filepath.Glob(filepath.Join(backupDir, "*.zip"))
	if err != nil {
		t.Fatal(err)
	}
	if len(zips) != 1 {
		t.Fatalf("默认备份目录中 zip 数 = %d, want 1", len(zips))
	}
	if !strings.HasPrefix(filepath.Base(zips[0]), "demo-") {
		t.Fatalf("备份文件名 = %s", filepath.Base(zips[0]))
	}

	// dry-run 只预览，不产生新备份包。
	if err := backupCommand([]string{"demo", "--dir", instanceRoot, "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	again, err := filepath.Glob(filepath.Join(backupDir, "*.zip"))
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 {
		t.Fatalf("dry-run 不应生成文件，zip 数 = %d", len(again))
	}

	// 自定义输出路径。
	custom := filepath.Join(temp, "custom", "manual.zip")
	if err := backupCommand([]string{"demo", "--dir", instanceRoot, "--out", custom, "--name", "手动备份"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(custom); err != nil {
		t.Fatal(err)
	}

	if err := restoreCommand([]string{"--list"}); err != nil {
		t.Fatal(err)
	}

	// 目标不存在时自动新建。
	restoreRoot := filepath.Join(temp, "restored")
	if err := restoreCommand([]string{zips[0], "--target", "demo", "--dir", restoreRoot}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(restoreRoot, "demo", "saves", "world", "level.dat"))
	if err != nil || string(got) != "level" {
		t.Fatalf("还原存档 = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(restoreRoot, "demo", "libraries", "skip.jar")); !os.IsNotExist(err) {
		t.Fatalf("libraries 不应被还原: %v", err)
	}

	// 目标已存在时默认拒绝。
	if err := restoreCommand([]string{zips[0], "--target", "demo", "--dir", restoreRoot}); err == nil {
		t.Fatal("已存在目标未加 --force 应报错")
	}
	// --force 保留旧内容后覆盖。
	if err := restoreCommand([]string{zips[0], "--target", "demo", "--dir", restoreRoot, "--force"}); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(restoreRoot, "demo", "saves.old-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("force 还原应保留旧 saves 副本，got %v", matches)
	}
}

func TestBackupCommandMissingInstance(t *testing.T) {
	t.Setenv(backup.EnvBackupsDir, t.TempDir())
	if err := backupCommand([]string{"missing", "--dir", t.TempDir()}); err == nil {
		t.Fatal("不存在的实例应报错")
	}
}
