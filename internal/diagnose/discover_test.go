package diagnose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string, modTime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if !modTime.IsZero() {
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLatestLogFilePrefersRunLogs(t *testing.T) {
	dir := t.TempDir()
	old := time.Date(2026, 10, 24, 22, 0, 0, 0, time.Local)
	newer := old.Add(time.Hour)
	writeFile(t, filepath.Join(dir, "logs", "run-20261024-2200.log"), "old run", old)
	writeFile(t, filepath.Join(dir, "logs", "run-20261024-2230.log"), "new run", newer)
	writeFile(t, filepath.Join(dir, "logs", "latest.log"), "vanilla", newer.Add(time.Hour))

	got, err := LatestLogFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "run-20261024-2230.log" {
		t.Fatalf("应优先最新的 run 日志，实际 %s", got)
	}
}

func TestLatestLogFileFallsBackToAnyLog(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "logs", "latest.log"), "vanilla", time.Now())
	got, err := LatestLogFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "latest.log" {
		t.Fatalf("没有 run 日志时应回退 latest.log: %s", got)
	}
}

func TestLatestLogFileMissingDir(t *testing.T) {
	got, err := LatestLogFile(t.TempDir())
	if err != nil || got != "" {
		t.Fatalf("logs 目录不存在应返回空字符串和 nil: %q, %v", got, err)
	}
}

func TestLatestCrashFilePrefersRawReports(t *testing.T) {
	dir := t.TempDir()
	old := time.Date(2026, 10, 24, 20, 0, 0, 0, time.Local)
	newer := old.Add(time.Hour)
	writeFile(t, filepath.Join(dir, "crash-reports", "crash-2026-10-24_20.00.00-client.txt"), "raw", old)
	writeFile(t, filepath.Join(dir, "crash_reports", "crash-20261024T220000-archived.txt"), "archived", newer)

	got, err := LatestCrashFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filepath.ToSlash(got), "crash-reports/") {
		t.Fatalf("应优先原版 crash-reports，实际 %s", got)
	}
}

func TestLatestCrashFileUsesArchiveFallback(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "crash_reports", "crash-20261024T220000-demo.txt"), "archived", time.Now())
	got, err := LatestCrashFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filepath.ToSlash(got), "crash_reports/") {
		t.Fatalf("应回退 mtl 归档目录: %s", got)
	}
}

func TestReadTextTailTruncatesAtLineBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.log")
	var b strings.Builder
	for range 100 {
		b.WriteString("0123456789 line\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadTextTail(path, 60)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(got, "23456789") {
		t.Fatalf("截断后不应保留半行: %q", got)
	}
	if !strings.HasSuffix(got, "line\n") {
		t.Fatalf("应保留日志尾部: %q", got)
	}
}

func TestReadTextTailEmptyPath(t *testing.T) {
	got, err := ReadTextTail("", 100)
	if err != nil || got != "" {
		t.Fatalf("空路径应返回空字符串: %q, %v", got, err)
	}
}

func TestCrashIsCurrent(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "run.log")
	crashPath := filepath.Join(dir, "crash.txt")
	now := time.Now()
	writeFile(t, logPath, "log", now)
	writeFile(t, crashPath, "crash", now.Add(-5*time.Minute))
	if !CrashIsCurrent(crashPath, logPath, 10*time.Minute) {
		t.Fatal("同一次运行的崩溃报告应视为最新")
	}
	if err := os.Chtimes(crashPath, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if CrashIsCurrent(crashPath, logPath, 10*time.Minute) {
		t.Fatal("早于运行日志的崩溃报告应视为过期")
	}
	if !CrashIsCurrent(crashPath, "", 10*time.Minute) {
		t.Fatal("缺少日志时只能基于崩溃报告，应视为可用")
	}
	if CrashIsCurrent("", logPath, 10*time.Minute) {
		t.Fatal("空崩溃路径不应视为最新")
	}
}

func TestStripArchiveSummary(t *testing.T) {
	in := "[Crash] ===== mtl 崩溃诊断摘要 =====\n[Crash] hint\n[Crash] ===== 以下为原始崩溃日志 =====\n\noriginal"
	got := StripArchiveSummary(in)
	if got != "original" {
		t.Fatalf("StripArchiveSummary = %q", got)
	}
	if raw := "plain crash"; StripArchiveSummary(raw) != raw {
		t.Fatal("原始崩溃日志不应被修改")
	}
}
