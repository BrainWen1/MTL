package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"mtl/internal/config"
)

func TestConfigCommands(t *testing.T) {
	dir := t.TempDir()

	steps := [][]string{
		{"set-memory", "manual", "--value", "4G", "--dir", dir},
		{"add-jvm-arg", "--dir", dir, "-XX:-OmitStackTraceInFastThrow"},
		{"add-jvm-arg", "--dir", dir, "-XX:-OmitStackTraceInFastThrow"}, // 重复添加不应产生重复项
		{"add-game-arg", "--dir", dir, "--noChat"},
	}
	for _, args := range steps {
		if err := configCommand(args); err != nil {
			t.Fatalf("config %v: %v", args, err)
		}
	}

	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MemoryMode != config.ModeManual || cfg.ManualMemory != "4G" {
		t.Fatalf("内存配置 = %+v", cfg)
	}
	if !reflect.DeepEqual(cfg.JVMArgs, []string{"-XX:-OmitStackTraceInFastThrow"}) {
		t.Fatalf("jvm_args = %v", cfg.JVMArgs)
	}
	if !reflect.DeepEqual(cfg.GameArgs, []string{"--noChat"}) {
		t.Fatalf("game_args = %v", cfg.GameArgs)
	}

	// 删除参数，重复删除保持幂等。
	if err := configCommand([]string{"remove-jvm-arg", "--dir", dir, "-XX:-OmitStackTraceInFastThrow"}); err != nil {
		t.Fatalf("remove-jvm-arg: %v", err)
	}
	if err := configCommand([]string{"remove-jvm-arg", "--dir", dir, "-XX:-OmitStackTraceInFastThrow"}); err != nil {
		t.Fatalf("重复 remove-jvm-arg 应幂等: %v", err)
	}
	cfg, err = config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.JVMArgs) != 0 {
		t.Fatalf("删除后 jvm_args = %v", cfg.JVMArgs)
	}

	// 切回自动内存，手动内存值保留但不再生效。
	if err := configCommand([]string{"set-memory", "auto", "--dir", dir}); err != nil {
		t.Fatalf("set-memory auto: %v", err)
	}
	cfg, err = config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MemoryMode != config.ModeAuto {
		t.Fatalf("memory_mode = %q", cfg.MemoryMode)
	}
}

func TestConfigSetMemoryValidation(t *testing.T) {
	dir := t.TempDir()
	tests := [][]string{
		{"set-memory", "manual", "--dir", dir},                   // 缺 --value
		{"set-memory", "manual", "--value", "abc", "--dir", dir}, // 格式错误
		{"set-memory", "weird", "--dir", dir},                    // 未知模式
	}
	for _, args := range tests {
		if err := configCommand(args); err == nil {
			t.Fatalf("config %v 应报错", args)
		}
	}
}

func TestConfigReset(t *testing.T) {
	dir := t.TempDir()
	if err := configCommand([]string{"set-memory", "manual", "--value", "4G", "--dir", dir}); err != nil {
		t.Fatal(err)
	}
	if err := configCommand([]string{"add-jvm-arg", "--dir", dir, "-Dfoo=1"}); err != nil {
		t.Fatal(err)
	}
	if err := configCommand([]string{"reset", "--dir", dir}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, config.Default()) {
		t.Fatalf("reset 后 = %+v，期望默认配置", cfg)
	}
	// 重复 reset 幂等。
	if err := configCommand([]string{"reset", "--dir", dir}); err != nil {
		t.Fatalf("重复 reset: %v", err)
	}
}

func TestConfigSetHighPerfGPU(t *testing.T) {
	dir := t.TempDir()

	if err := configCommand([]string{"set-highperf-gpu", "--dir", dir, "true"}); err != nil {
		t.Fatalf("set-highperf-gpu true: %v", err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.PreferHighPerfGPU {
		t.Fatalf("prefer_high_perf_gpu 应为 true: %+v", cfg)
	}

	// 重复设置相同值保持幂等。
	if err := configCommand([]string{"set-highperf-gpu", "--dir", dir, "true"}); err != nil {
		t.Fatalf("重复开启应幂等: %v", err)
	}

	if err := configCommand([]string{"set-highperf-gpu", "--dir", dir, "false"}); err != nil {
		t.Fatalf("set-highperf-gpu false: %v", err)
	}
	cfg, err = config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PreferHighPerfGPU {
		t.Fatalf("prefer_high_perf_gpu 应为 false: %+v", cfg)
	}

	for _, args := range [][]string{
		{"set-highperf-gpu", "--dir", dir, "maybe"}, // 非法布尔值
		{"set-highperf-gpu", "--dir", dir},          // 缺参数
		{"set-highperf-gpu", "true"},                // 缺 --dir
	} {
		if err := configCommand(args); err == nil {
			t.Fatalf("config %v 应报错", args)
		}
	}
}

func TestConfigCorruptFileFallsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, config.DirName, config.FileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := loadInstanceConfig(dir)
	if !reflect.DeepEqual(cfg, config.Default()) {
		t.Fatalf("损坏配置应回退默认: %+v", cfg)
	}
	// 损坏后仍可通过命令修复为合法配置。
	if err := configCommand([]string{"set-memory", "manual", "--value", "3G", "--dir", dir}); err != nil {
		t.Fatalf("修复损坏配置: %v", err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("修复后 Load: %v", err)
	}
	if cfg.MemoryMode != config.ModeManual || cfg.ManualMemory != "3G" {
		t.Fatalf("修复后配置 = %+v", cfg)
	}
}

func TestParseConfigArgs(t *testing.T) {
	dir, value, positional, err := parseConfigArgs([]string{
		"manual", "--dir=" + t.TempDir(), "--value=4G", "-XX:+UseG1GC",
	})
	if err != nil {
		t.Fatal(err)
	}
	if dir == "" || value != "4G" {
		t.Fatalf("dir=%q value=%q", dir, value)
	}
	if !reflect.DeepEqual(positional, []string{"manual", "-XX:+UseG1GC"}) {
		t.Fatalf("positional = %v", positional)
	}
	if _, _, _, err := parseConfigArgs([]string{"--dir"}); err == nil {
		t.Fatal("--dir 缺值应报错")
	}
}
