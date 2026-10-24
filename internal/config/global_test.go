package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// isolateGlobalConfig 把全局配置指向临时文件，避免影响真实用户配置。
func isolateGlobalConfig(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	t.Setenv(EnvGlobalConfig, p)
	return p
}

func TestLoadGlobalMissingFileUsesDefaults(t *testing.T) {
	isolateGlobalConfig(t)
	cfg, err := LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if cfg.Download.LogMode != LogModeSummary {
		t.Fatalf("默认日志模式 = %q, want summary", cfg.Download.LogMode)
	}
}

func TestSaveAndLoadGlobal(t *testing.T) {
	p := isolateGlobalConfig(t)
	want := Global{
		Mirrors: GlobalMirrors{
			Mojang: "https://mirror.example/mc/",
			Mod:    "https://mods.example",
		},
		Download: GlobalDownload{
			LogMode:     LogModeVerbose,
			Retries:     2,
			Timeout:     "30s",
			Concurrency: 8,
		},
	}
	if err := SaveGlobal(want); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	// Windows 不支持 POSIX 权限位，os.Chmod 会被忽略。
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("全局配置权限 = %v, 期望仅当前用户可读写", info.Mode().Perm())
	}

	got, err := LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if got.Mirrors.Mojang != "https://mirror.example/mc" {
		t.Fatalf("镜像未去除末尾斜杠: %q", got.Mirrors.Mojang)
	}
	if got.Mirrors.Mod != want.Mirrors.Mod {
		t.Fatalf("mod 镜像 = %q, want %q", got.Mirrors.Mod, want.Mirrors.Mod)
	}
	if got.Download != want.Download {
		t.Fatalf("下载配置 = %+v, want %+v", got.Download, want.Download)
	}
}

func TestLoadGlobalCorruptReturnsDefaultsAndError(t *testing.T) {
	p := isolateGlobalConfig(t)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadGlobal()
	if err == nil || !strings.Contains(err.Error(), "损坏") {
		t.Fatalf("损坏配置应返回错误: %v", err)
	}
	if cfg.Download.LogMode != LogModeSummary {
		t.Fatalf("损坏配置应回退默认值: %+v", cfg)
	}
}

func TestNormalizeGlobalKeepsValidFields(t *testing.T) {
	cfg, err := NormalizeGlobal(Global{
		Mirrors: GlobalMirrors{
			Mojang: "ftp://bad.example",
			Assets: "https://ok.example/assets/",
		},
		Download: GlobalDownload{LogMode: "LOUD", Retries: -5, Timeout: "soon"},
	})
	if err == nil {
		t.Fatal("非法字段应返回错误")
	}
	if cfg.Mirrors.Mojang != "" {
		t.Fatalf("非法镜像应被清空: %q", cfg.Mirrors.Mojang)
	}
	if cfg.Mirrors.Assets != "https://ok.example/assets" {
		t.Fatalf("合法镜像应保留并规范化: %q", cfg.Mirrors.Assets)
	}
	if cfg.Download.LogMode != LogModeSummary || cfg.Download.Retries != 0 || cfg.Download.Timeout != "" {
		t.Fatalf("非法下载字段应回退默认值: %+v", cfg.Download)
	}
}

func TestGlobalFromEnv(t *testing.T) {
	t.Setenv("MTL_MOJANG_MIRROR", " https://env.example/mc ")
	t.Setenv("MTL_DOWNLOAD_LOG", LogModeVerbose)
	t.Setenv("MTL_DOWNLOAD_RETRIES", "1")
	t.Setenv("MTL_DOWNLOAD_CONCURRENCY", "6")
	t.Setenv("MTL_DOWNLOAD_TIMEOUT", "45s")

	cfg := GlobalFromEnv(DefaultGlobal())
	if cfg.Mirrors.Mojang != "https://env.example/mc" {
		t.Fatalf("Mojang 镜像 = %q", cfg.Mirrors.Mojang)
	}
	if cfg.Download.LogMode != LogModeVerbose || cfg.Download.Retries != 1 ||
		cfg.Download.Concurrency != 6 || cfg.Download.Timeout != "45s" {
		t.Fatalf("环境变量未生效: %+v", cfg.Download)
	}
}
