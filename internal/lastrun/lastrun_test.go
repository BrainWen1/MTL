package lastrun

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func isolate(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "last-run.json")
	t.Setenv(EnvPath, p)
	return p
}

func TestSaveAndLoad(t *testing.T) {
	p := isolate(t)
	exit := 1
	ended := time.Date(2026, 10, 24, 22, 31, 0, 0, time.UTC)
	want := Record{
		InstanceDir:      filepath.Join(t.TempDir(), "instances", "demo"),
		InstanceName:     "demo",
		MinecraftVersion: "1.20.1",
		LogFile:          "logs/run-20261024-2230.log",
		StartedAt:        time.Date(2026, 10, 24, 22, 30, 0, 0, time.UTC),
		EndedAt:          &ended,
		ExitCode:         &exit,
		ProcessPID:       1234,
	}
	if err := Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("记录文件未生成: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.InstanceDir != want.InstanceDir || got.InstanceName != want.InstanceName ||
		got.MinecraftVersion != want.MinecraftVersion || got.LogFile != want.LogFile ||
		got.ProcessPID != want.ProcessPID {
		t.Fatalf("Load = %+v, want %+v", got, want)
	}
	if got.ExitCode == nil || *got.ExitCode != 1 {
		t.Fatalf("ExitCode = %v", got.ExitCode)
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(ended) {
		t.Fatalf("EndedAt = %v", got.EndedAt)
	}

	// 重复保存相同内容应覆盖成功，不产生额外文件。
	if err := Save(want); err != nil {
		t.Fatalf("重复 Save: %v", err)
	}
}

func TestLoadMissing(t *testing.T) {
	isolate(t)
	_, err := Load()
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("文件不存在应返回 os.ErrNotExist: %v", err)
	}
}

func TestLoadCorrupt(t *testing.T) {
	p := isolate(t)
	if err := os.WriteFile(p, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("损坏记录应报错")
	}
}

func TestSaveRequiresInstanceDir(t *testing.T) {
	isolate(t)
	if err := Save(Record{}); err == nil {
		t.Fatal("缺少实例目录应报错")
	}
}
