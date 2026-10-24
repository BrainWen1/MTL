package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mtl/internal/diagnose"
	"mtl/internal/lastrun"
)

func TestParseDiagnoseOptions(t *testing.T) {
	opts, err := parseDiagnoseOptions([]string{"demo", "--dir", "instances"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.name != "demo" || opts.root != "instances" || opts.last {
		t.Fatalf("opts = %+v", opts)
	}

	opts, err = parseDiagnoseOptions([]string{"--last"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.last {
		t.Fatal("--last 未生效")
	}

	for _, args := range [][]string{
		nil,
		{"a", "b"},
		{"--last", "demo"},
		{"--unknown"},
		{"--dir"},
	} {
		if _, err := parseDiagnoseOptions(args); err == nil {
			t.Errorf("参数 %v 应报错", args)
		}
	}
}

func TestResolveDiagnoseDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := resolveDiagnoseDir(root, "demo", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("dir = %s, want %s", got, dir)
	}

	if _, err := resolveDiagnoseDir(root, "../escape", false); err == nil {
		t.Fatal("路径穿越应被拒绝")
	}
	if _, err := resolveDiagnoseDir(root, "missing", false); err == nil {
		t.Fatal("不存在的实例应报错")
	}
}

func TestSameInstanceDir(t *testing.T) {
	dir := t.TempDir()
	same := filepath.Join(dir, ".", "logs", "..")
	if !sameInstanceDir(dir, same) {
		t.Fatalf("%s 与 %s 应视为同一目录", dir, same)
	}
	other := filepath.Join(dir, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if sameInstanceDir(dir, other) {
		t.Fatal("不同目录不应相等")
	}
	if runtime.GOOS == "windows" && !sameInstanceDir(dir, strings.ToUpper(dir)) {
		t.Fatal("Windows 路径比较应忽略大小写")
	}
}

func TestInstanceLogForDiagnosePrefersRecord(t *testing.T) {
	dir := t.TempDir()
	recorded := filepath.Join(dir, "logs", "run-20261024-2230.log")
	if err := os.MkdirAll(filepath.Dir(recorded), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recorded, []byte("recorded"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "logs", "run-20261024-2300.log")
	if err := os.WriteFile(other, []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := instanceLogForDiagnose(dir, &lastrun.Record{InstanceDir: dir, LogFile: recorded})
	if err != nil {
		t.Fatal(err)
	}
	if got != recorded {
		t.Fatalf("应优先上次运行记录日志: %s", got)
	}

	// 记录指向其他实例时不采用，回退到最新日志。
	got, err = instanceLogForDiagnose(dir, &lastrun.Record{
		InstanceDir: filepath.Join(dir, "other"),
		LogFile:     recorded,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != other {
		t.Fatalf("应回退最新日志: %s", got)
	}
}

func TestDisplayExitCode(t *testing.T) {
	zero := 0
	forced := -1
	if displayExitCode(nil) == "" || displayExitCode(&zero) != "0" || displayExitCode(&forced) == "" {
		t.Fatalf("退出码展示异常: %q %q %q",
			displayExitCode(nil), displayExitCode(&zero), displayExitCode(&forced))
	}
	if displayPath("") != "未找到" || displayPath("x.log") != "x.log" {
		t.Fatal("路径展示异常")
	}
}

func TestLatestCrashHelperSmoke(t *testing.T) {
	dir := t.TempDir()
	got, err := diagnose.LatestCrashFile(dir)
	if err != nil || got != "" {
		t.Fatalf("空实例应返回空路径: %q, %v", got, err)
	}
}
