// Package forge 安装 Minecraft Forge（仅 1.20.1 及以上）。
//
// 安装流程：下载并校验官方 installer 到全局缓存 -> 准备原版文件 -> 用 Mojang
// runtime 执行 installer（非 GUI）-> 从 installer 生成的版本 json 读取主类与参数。
// 启动参数不在这里拼接，launch 会动态读取版本 json，避免写死 Forge 主类。
package forge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"mtl/internal/config"
	"mtl/internal/downloader"
	"mtl/internal/fabric"
	"mtl/internal/runtime"
)

// Options 是 Forge 安装的可注入配置；零值使用官方端点与本机默认设置。
type Options struct {
	// MavenBaseURL 是 Forge Maven 根地址，空值使用官方地址（测试可注入）。
	MavenBaseURL string
	// MojangManifestURL 用于准备原版文件，空值使用 Mojang 官方清单。
	MojangManifestURL string
	// PromotionsURL 是 Forge 推荐版本清单地址，空值使用官方地址（测试可注入）。
	PromotionsURL string
	// CacheDir 是 installer 全局缓存目录，空值使用 DefaultCacheDir。
	CacheDir string
	// JavaPath 为空时使用 Mojang 官方 Java 运行时；测试可注入。
	JavaPath string
	// Concurrency 是并行下载数，<=0 时使用默认值。
	Concurrency int
	// Retries 是失败后的额外重试次数，0 时默认 3；负数表示不重试。
	Retries int
	// RetryDelay 是首次重试前的等待时间，按指数退避。
	RetryDelay time.Duration
	// Timeout 是单文件下载超时；<=0 表示不限制。
	Timeout time.Duration
	// HTTPClient 为空时使用 http.DefaultClient。
	HTTPClient *http.Client
	// Mirrors 是镜像源配置；空值使用官方地址。
	Mirrors downloader.Mirrors
	// LogMode 控制下载日志粒度；默认 summary。
	LogMode downloader.LogMode
	// TargetOS / TargetArch 用于跨平台准备原版文件，采用 Go 命名。
	TargetOS   string
	TargetArch string
	// Progress 接收进度消息（不含 "[Forge]" 前缀）。
	Progress func(string)
}

// Result 描述一次 Forge 安装结果。
type Result struct {
	ProfileID        string
	MinecraftVersion string
	ForgeVersion     string
	// SkippedInstaller 表示命中已有 profile 或 installer 缓存，未重复安装。
	SkippedInstaller bool
}

// Loader 实现统一加载器接口。
type Loader struct {
	mcVersion string
	opts      Options
	// installerRunner 仅供测试替换 installer 子进程；nil 时执行真实 Java 命令。
	installerRunner func(ctx context.Context, javaPath, installerPath, instanceDir string) (string, error)
	lastResult      Result
}

// NewLoader 构造 Forge 加载器。低于 1.20.1 或无法识别的版本直接返回友好错误。
func NewLoader(mcVersion string, opts Options) (*Loader, error) {
	if err := validateMinecraftVersion(mcVersion); err != nil {
		return nil, err
	}
	return &Loader{mcVersion: strings.TrimSpace(mcVersion), opts: opts}, nil
}

// Name 返回加载器标识。
func (l *Loader) Name() string { return "forge" }

// LastResult 返回最近一次 Install 的结果，供 CLI 输出摘要。
func (l *Loader) LastResult() Result { return l.lastResult }

// Install 执行 Forge 安装。失败时只清理本次新增的版本目录，
// 已校验的 libraries 和原版文件会保留，重复执行可断点续装。
func (l *Loader) Install(ctx context.Context, inst *config.Instance) error {
	if inst == nil {
		return errors.New("实例为空")
	}
	if strings.TrimSpace(inst.Dir) == "" {
		return errors.New("实例目录为空")
	}

	mcVersion := strings.TrimSpace(inst.MinecraftVersion)
	if mcVersion == "" {
		mcVersion = l.mcVersion
	}
	if err := validateMinecraftVersion(mcVersion); err != nil {
		return err
	}
	fullVersion, forgeVersion, err := normalizeForgeVersion(mcVersion, inst.LoaderVersion)
	if err != nil {
		return err
	}

	// installer 会以自己的工作目录解析 --installClient 参数，必须使用绝对路径，
	// 否则相对路径会被拼接到实例目录之下。
	instanceDir, err := filepath.Abs(inst.Dir)
	if err != nil {
		return fmt.Errorf("解析实例目录失败: %w", err)
	}
	l.lastResult = Result{}

	l.report("准备 Minecraft %s 原版文件", mcVersion)
	vanilla, err := fabric.EnsureVanilla(ctx, instanceDir, mcVersion, l.fabricOptions())
	if err != nil {
		return fmt.Errorf("准备 Minecraft 原版文件失败: %w", err)
	}

	// installer 生成的版本目录 id 是固定格式；已存在且主类可用时直接复用。
	expectedID := mcVersion + "-forge-" + forgeVersion
	if profile, err := readVersionInfo(instanceDir, expectedID); err == nil &&
		profile.InheritsFrom == mcVersion && strings.TrimSpace(profile.MainClass) != "" {
		if err := l.ensureProfileLibraries(ctx, instanceDir, profile); err != nil {
			return err
		}
		l.report("Forge 已安装，跳过 installer: %s", expectedID)
		l.lastResult = Result{ProfileID: expectedID, MinecraftVersion: mcVersion, ForgeVersion: forgeVersion, SkippedInstaller: true}
		return nil
	}

	javaPath, err := l.ensureJava(ctx, vanilla, mcVersion)
	if err != nil {
		return err
	}
	installerPath, installerCached, err := l.ensureInstaller(ctx, fullVersion)
	if err != nil {
		return err
	}

	// Forge installer 会读取实例中的原版版本 json 与 launcher_profiles.json，
	// 必须在拉起 installer 之前准备好，否则报 "There is no minecraft launcher profile"。
	if err := l.ensureVanillaProfile(instanceDir, mcVersion, vanilla.VersionJSON); err != nil {
		return err
	}
	if err := l.ensureLauncherProfiles(instanceDir); err != nil {
		return err
	}

	before, err := listVersionIDs(instanceDir)
	if err != nil {
		return err
	}

	l.report("运行 Forge installer（非 GUI）: %s", fullVersion)
	output, err := l.runInstaller(ctx, javaPath, installerPath, instanceDir)
	if err != nil {
		cleanupNewVersionDirs(instanceDir, before)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("Forge installer 执行失败: %w%s", err, outputTail(output))
	}

	profile, err := findGeneratedProfile(instanceDir, mcVersion, before)
	if err != nil {
		cleanupNewVersionDirs(instanceDir, before)
		return err
	}
	if err := l.ensureProfileLibraries(ctx, instanceDir, profile); err != nil {
		return err
	}

	l.lastResult = Result{
		ProfileID:        profile.ID,
		MinecraftVersion: mcVersion,
		ForgeVersion:     forgeVersion,
		SkippedInstaller: installerCached,
	}
	l.report("安装完成: %s", profile.ID)
	return nil
}

// GetLaunchArgs 返回加载器特有的额外 JVM 参数。
// Forge 的主类、JVM 参数与游戏参数全部由 installer 生成的版本 json 提供，
// launch 会动态读取；这里不重复拼接，避免与版本 json 中的参数冲突。
func (l *Loader) GetLaunchArgs(ctx context.Context, inst *config.Instance) ([]string, error) {
	return nil, nil
}

func (l *Loader) report(format string, args ...any) {
	if l.opts.Progress == nil {
		return
	}
	l.opts.Progress(fmt.Sprintf(format, args...))
}

func (l *Loader) fabricOptions() fabric.Options {
	return fabric.Options{
		MojangManifestURL: l.opts.MojangManifestURL,
		Concurrency:       l.opts.Concurrency,
		Retries:           l.opts.Retries,
		RetryDelay:        l.opts.RetryDelay,
		Timeout:           l.opts.Timeout,
		HTTPClient:        l.opts.HTTPClient,
		Mirrors:           l.opts.Mirrors,
		LogMode:           l.opts.LogMode,
		TargetOS:          l.opts.TargetOS,
		TargetArch:        l.opts.TargetArch,
		Progress:          l.opts.Progress,
	}
}

func (l *Loader) ensureJava(ctx context.Context, vanilla fabric.VanillaResult, mcVersion string) (string, error) {
	if p := strings.TrimSpace(l.opts.JavaPath); p != "" {
		return p, nil
	}
	spec := runtime.Spec{
		Component:    vanilla.JavaComponent,
		MajorVersion: vanilla.JavaMajor,
	}
	if spec.Component == "" && spec.MajorVersion <= 0 {
		spec.MajorVersion = recommendedJavaMajor(mcVersion)
	}
	l.report("准备 Java 运行时（Forge installer）")
	javaPath, err := runtime.Ensure(ctx, spec, runtime.Options{
		Concurrency: l.opts.Concurrency,
		Retries:     l.opts.Retries,
		RetryDelay:  l.opts.RetryDelay,
		Timeout:     l.opts.Timeout,
		HTTPClient:  l.opts.HTTPClient,
		Mirrors:     l.opts.Mirrors,
		Progress:    l.opts.Progress,
	})
	if err != nil {
		return "", fmt.Errorf("准备 Java 运行时失败: %w", err)
	}
	return javaPath, nil
}

// recommendedJavaMajor 仅在原版版本 json 未提供 Java 信息时兜底。
func recommendedJavaMajor(mcVersion string) int {
	v, err := parseMinecraftVersion(mcVersion)
	if err != nil {
		return 17
	}
	// 1.20.5 起官方要求 Java 21。
	if v[0] > 1 || (v[0] == 1 && (v[1] > 20 || (v[1] == 20 && v[2] >= 5))) {
		return 21
	}
	return 17
}
