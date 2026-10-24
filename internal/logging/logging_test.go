package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		raw  string
		want Level
		ok   bool
	}{
		{"", LevelInfo, true},
		{"INFO", LevelInfo, true},
		{" debug ", LevelDebug, true},
		{"verbose", LevelDebug, true},
		{"warn", LevelWarn, true},
		{"warning", LevelWarn, true},
		{"error", LevelError, true},
		{"loud", LevelInfo, false},
	}
	for _, tt := range tests {
		got, err := ParseLevel(tt.raw)
		if (err == nil) != tt.ok {
			t.Errorf("ParseLevel(%q) err = %v, ok = %v", tt.raw, err, tt.ok)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}

func TestLoggerFiltersConsoleAndKeepsFileDetailed(t *testing.T) {
	var console, file bytes.Buffer
	fixed := time.Date(2026, 10, 24, 22, 30, 0, 0, time.Local)
	logger := New(Options{
		Console: &console,
		Level:   LevelInfo,
		File:    &file,
		Now:     func() time.Time { return fixed },
	})

	logger.Debug("runtime", "debug %d", 1)
	logger.Info("assets", "info")
	logger.Warn("java", "warn")
	logger.Error("process", "error")

	consoleText := console.String()
	if strings.Contains(consoleText, "debug 1") {
		t.Fatalf("INFO 级别不应输出 DEBUG 日志:\n%s", consoleText)
	}
	for _, want := range []string{
		"2026-10-24 22:30:00.000 [INFO ] [assets] info",
		"[WARN ] [java] warn",
		"[ERROR] [process] error",
	} {
		if !strings.Contains(consoleText, want) {
			t.Fatalf("终端日志缺少 %q:\n%s", want, consoleText)
		}
	}
	fileText := file.String()
	if !strings.Contains(fileText, "debug 1") {
		t.Fatalf("日志文件应记录 DEBUG 及以上:\n%s", fileText)
	}
	if !strings.Contains(fileText, "[INFO ] [assets] info") {
		t.Fatalf("日志文件缺少 INFO 日志:\n%s", fileText)
	}
}

func TestRawWriterWritesBothTargets(t *testing.T) {
	var console, file bytes.Buffer
	logger := New(Options{Console: &console, Level: LevelError, File: &file})
	if _, err := logger.RawWriter().Write([]byte("game output\n")); err != nil {
		t.Fatal(err)
	}
	if console.String() != "game output\n" || file.String() != "game output\n" {
		t.Fatalf("RawWriter 输出不一致: console=%q file=%q", console.String(), file.String())
	}
}

func TestOpenInstanceLogCreatesIndependentFiles(t *testing.T) {
	dir := t.TempDir()
	stamp := time.Date(2026, 10, 24, 22, 30, 0, 0, time.Local)

	f1, p1, err := OpenInstanceLog(dir, stamp)
	if err != nil {
		t.Fatalf("第一次创建日志: %v", err)
	}
	if filepath.Base(p1) != "run-20261024-2230.log" {
		t.Fatalf("日志文件名 = %s", filepath.Base(p1))
	}
	if _, err := f1.WriteString("first"); err != nil {
		t.Fatal(err)
	}
	if err := f1.Close(); err != nil {
		t.Fatal(err)
	}

	f2, p2, err := OpenInstanceLog(dir, stamp)
	if err != nil {
		t.Fatalf("第二次创建日志: %v", err)
	}
	if filepath.Base(p2) != "run-20261024-2230-2.log" {
		t.Fatalf("同分钟第二个日志文件名 = %s", filepath.Base(p2))
	}
	if _, err := f2.WriteString("second"); err != nil {
		t.Fatal(err)
	}
	if err := f2.Close(); err != nil {
		t.Fatal(err)
	}

	first, err := os.ReadFile(p1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(p2)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != "first" || string(second) != "second" {
		t.Fatalf("两次运行日志不应互相追加: %q / %q", first, second)
	}
	if filepath.Dir(p1) != filepath.Join(dir, "logs") {
		t.Fatalf("日志目录 = %s", filepath.Dir(p1))
	}
}

func TestOpenInstanceLogRejectsEmptyDir(t *testing.T) {
	if _, _, err := OpenInstanceLog("  ", time.Now()); err == nil {
		t.Fatal("空实例目录应报错")
	}
}
