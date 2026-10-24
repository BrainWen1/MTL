package main

import (
	"os"
	"path/filepath"
	"testing"

	"mtl/internal/config"
	"mtl/internal/logging"
)

func setupGlobalConfig(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	t.Setenv(config.EnvGlobalConfig, p)
	t.Setenv("MTL_LOG_LEVEL", "")
	return p
}

func TestResolveRunLogLevelPriority(t *testing.T) {
	setupGlobalConfig(t)
	if err := config.SaveGlobal(config.Global{LogLevel: "warn"}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		cli   string
		debug bool
		want  logging.Level
	}{
		{"命令行优先", "error", true, logging.LevelError},
		{"debug 覆盖全局", "", true, logging.LevelDebug},
		{"读取全局", "", false, logging.LevelWarn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveRunLogLevel(tt.cli, tt.debug)
			if err != nil {
				t.Fatalf("resolveRunLogLevel: %v", err)
			}
			if got != tt.want {
				t.Fatalf("level = %v, want %v", got.Name(), tt.want.Name())
			}
		})
	}
}

func TestResolveRunLogLevelInvalid(t *testing.T) {
	setupGlobalConfig(t)
	if _, err := resolveRunLogLevel("loud", false); err == nil {
		t.Fatal("非法 --log 应报错")
	}
}

func TestResolveRunLogLevelEnv(t *testing.T) {
	setupGlobalConfig(t)
	t.Setenv("MTL_LOG_LEVEL", "debug")
	got, err := resolveRunLogLevel("", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != logging.LevelDebug {
		t.Fatalf("环境变量未生效: %s", got.Name())
	}
}

func TestConfigSetLogLevel(t *testing.T) {
	p := setupGlobalConfig(t)
	if err := configSetLogLevel([]string{"debug"}); err != nil {
		t.Fatalf("configSetLogLevel: %v", err)
	}
	cfg, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != "debug" {
		t.Fatalf("log_level = %q", cfg.LogLevel)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("全局配置未写入: %v", err)
	}
	if err := configSetLogLevel(nil); err == nil {
		t.Fatal("缺少参数应报错")
	}
}
