package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"mtl/internal/config"
	"mtl/internal/downloader"
)

// downloadSettings 汇总一次安装/启动使用的下载参数。
// 优先级：命令行 > 环境变量 > 全局配置文件 > 默认值（官方源 + summary）。
type downloadSettings struct {
	Concurrency int
	Retries     int
	Timeout     time.Duration
	LogMode     downloader.LogMode
	Mirrors     downloader.Mirrors
}

// downloadFlags 是 install / run 共用的下载相关命令行参数。
type downloadFlags struct {
	mirror    string
	modMirror string
	retries   int
	timeout   time.Duration
	logMode   string
}

// addDownloadFlags 为子命令注册通用下载参数。
func addDownloadFlags(fs *flag.FlagSet) *downloadFlags {
	f := &downloadFlags{}
	fs.StringVar(&f.mirror, "mirror", "", "Mojang 资源镜像根地址（临时覆盖全局配置；失败自动回退官方源）")
	fs.StringVar(&f.modMirror, "mod-mirror", "", "mod 下载源镜像前缀（临时覆盖全局配置）")
	fs.IntVar(&f.retries, "retries", 0, "下载失败后的额外重试次数；0 使用配置值，-1 表示不重试")
	fs.DurationVar(&f.timeout, "timeout", 0, "单文件下载超时，例如 30s、2m；0 表示使用配置值或不限制")
	fs.StringVar(&f.logMode, "log-mode", "", "下载日志模式：summary（默认）或 verbose")
	return f
}

// resolveDownloadSettings 合并命令行、环境变量与全局配置。
// 全局配置损坏时只告警，仍按可用字段继续，避免镜像配置错误导致无法安装。
func resolveDownloadSettings(fs *flag.FlagSet, f *downloadFlags, concurrency int, debug bool) (downloadSettings, error) {
	s := downloadSettings{Concurrency: concurrency}

	cfg, err := config.LoadGlobal()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[Download] 警告: %v；已忽略非法字段并继续\n", err)
	}
	cfg = config.GlobalFromEnv(cfg)

	s.Retries = cfg.Download.Retries
	if cfg.Download.Timeout != "" {
		if d, err := time.ParseDuration(cfg.Download.Timeout); err == nil {
			s.Timeout = d
		}
	}
	if cfg.Download.LogMode == config.LogModeVerbose {
		s.LogMode = downloader.LogVerbose
	}
	if s.Concurrency <= 0 {
		s.Concurrency = cfg.Download.Concurrency
	}
	s.Mirrors = normalizeConfigMirrors(cfg.Mirrors)

	explicit := map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { explicit[fl.Name] = true })

	if explicit["retries"] {
		s.Retries = f.retries
	}
	if explicit["timeout"] {
		s.Timeout = f.timeout
	}
	if explicit["log-mode"] {
		mode, err := parseLogMode(f.logMode)
		if err != nil {
			return s, err
		}
		s.LogMode = mode
	}
	if explicit["mirror"] {
		v, err := downloader.NormalizeMirror(f.mirror)
		if err != nil {
			return s, fmt.Errorf("--mirror: %w", err)
		}
		// 命令行镜像优先于配置中的所有 Mojang 细分镜像；传空串表示强制官方源。
		s.Mirrors.Mojang = v
		s.Mirrors.Assets = ""
		s.Mirrors.Libraries = ""
		s.Mirrors.VersionMeta = ""
	}
	if explicit["mod-mirror"] {
		v, err := downloader.NormalizeMirror(f.modMirror)
		if err != nil {
			return s, fmt.Errorf("--mod-mirror: %w", err)
		}
		s.Mirrors.Mod = v
	}
	// --debug 默认切到 verbose，方便排查下载问题。
	if debug {
		s.LogMode = downloader.LogVerbose
	}
	return s, nil
}

func parseLogMode(raw string) (downloader.LogMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case config.LogModeSummary:
		return downloader.LogSummary, nil
	case config.LogModeVerbose:
		return downloader.LogVerbose, nil
	default:
		return downloader.LogSummary, fmt.Errorf("--log-mode %q 无效（可用: summary、verbose）", raw)
	}
}

// normalizeConfigMirrors 规范化配置/环境变量中的镜像地址。
// 单个字段非法不会影响其他字段，也不会中断安装。
func normalizeConfigMirrors(m config.GlobalMirrors) downloader.Mirrors {
	norm := func(name, raw string) string {
		v, err := downloader.NormalizeMirror(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[Download] 警告: 忽略无效镜像 %s: %v\n", name, err)
			return ""
		}
		return v
	}
	return downloader.Mirrors{
		Mojang:      norm("mirrors.mojang", m.Mojang),
		Assets:      norm("mirrors.assets", m.Assets),
		Libraries:   norm("mirrors.libraries", m.Libraries),
		VersionMeta: norm("mirrors.version_meta", m.VersionMeta),
		Mod:         norm("mirrors.mod", m.Mod),
		Fabric:      norm("mirrors.fabric", m.Fabric),
		Forge:       norm("mirrors.forge", m.Forge),
	}
}
