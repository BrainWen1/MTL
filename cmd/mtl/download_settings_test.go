package main

import (
	"flag"
	"path/filepath"
	"testing"
	"time"

	"mtl/internal/config"
	"mtl/internal/downloader"
)

func TestResolveDownloadSettingsPriority(t *testing.T) {
	t.Setenv(config.EnvGlobalConfig, filepath.Join(t.TempDir(), "config.json"))
	if err := config.SaveGlobal(config.Global{
		Mirrors: config.GlobalMirrors{Mojang: "https://file.example/mc"},
		Download: config.GlobalDownload{
			LogMode:     config.LogModeVerbose,
			Retries:     2,
			Timeout:     "10s",
			Concurrency: 3,
		},
	}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	t.Setenv("MTL_MOJANG_MIRROR", "https://env.example/mc")

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	f := addDownloadFlags(fs)
	if err := fs.Parse([]string{
		"--mirror", "https://cli.example/mc",
		"--retries", "-1",
		"--timeout", "5s",
		"--log-mode", "summary",
	}); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	got, err := resolveDownloadSettings(fs, f, 0, false)
	if err != nil {
		t.Fatalf("resolveDownloadSettings: %v", err)
	}
	if got.Mirrors.Mojang != "https://cli.example/mc" {
		t.Fatalf("镜像优先级错误: %q", got.Mirrors.Mojang)
	}
	if got.Retries != -1 || got.Timeout != 5*time.Second || got.LogMode != downloader.LogSummary {
		t.Fatalf("命令行下载参数未生效: %+v", got)
	}
	if got.Concurrency != 3 {
		t.Fatalf("并发数应回落到全局配置: %d", got.Concurrency)
	}
}

func TestResolveDownloadSettingsDebugForcesVerbose(t *testing.T) {
	t.Setenv(config.EnvGlobalConfig, filepath.Join(t.TempDir(), "config.json"))
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	f := addDownloadFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got, err := resolveDownloadSettings(fs, f, 0, true)
	if err != nil {
		t.Fatalf("resolveDownloadSettings: %v", err)
	}
	if got.LogMode != downloader.LogVerbose {
		t.Fatalf("--debug 应切换为 verbose: %v", got.LogMode)
	}
}

func TestResolveDownloadSettingsRejectsBadLogMode(t *testing.T) {
	t.Setenv(config.EnvGlobalConfig, filepath.Join(t.TempDir(), "config.json"))
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	f := addDownloadFlags(fs)
	if err := fs.Parse([]string{"--log-mode", "loud"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := resolveDownloadSettings(fs, f, 0, false); err == nil {
		t.Fatal("非法 --log-mode 应报错")
	}
}
