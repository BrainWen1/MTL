// Package vanilla 安装不依赖 mrpack 的纯原版模板实例。
// 它复用 fabric.EnsureVanilla 获取原版版本 json、client jar 与 libraries，
// 复用 assets 全局缓存准备资源，并生成可被现有 run 流程识别的版本 profile。
package vanilla

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mtl/internal/assets"
	"mtl/internal/config"
	"mtl/internal/fabric"
	"mtl/internal/mrpack"
)

const (
	// profileSuffix 让纯原版实例拥有一个 inheritsFrom 原版的 profile，
	// 从而无需改动 launch 即可复用现有启动流程。
	profileSuffix = "-mtl-vanilla"
	// launcherProfilesSkeleton 是最小可用的 launcher_profiles.json。
	launcherProfilesSkeleton = `{"profiles":{},"settings":{},"version":3}`
)

// Options 是模板安装的可注入配置；零值使用官方端点与默认缓存。
type Options struct {
	// Fabric 用于 Mojang 版本清单、下载、缓存与并发/重试设置。
	Fabric fabric.Options
	// Assets 是 assets 全局缓存配置；Dir 为空时使用默认缓存目录。
	Assets assets.Options
	// Progress 接收高层安装进度（不含 "[Vanilla]" 前缀）。
	Progress func(string)
}

// Result 描述一次纯原版安装结果。
type Result struct {
	MinecraftVersion string
	ProfileID        string
	Files            int
	Downloaded       int
	Skipped          int
}

// Install 安装纯原版模板：
//
//	versions/<mc>/<mc>.json + <mc>.jar
//	libraries/<maven path>
//	assets（全局缓存）
//	versions/<mc>-mtl-vanilla/<id>.json（供 run 识别的薄 profile）
//	launcher_profiles.json、mods/、saves/
//	.mtl/config.json（loader_type=vanilla，install_mode=template）
//
// 不写入 .mtl/pack-lock.json。
func Install(ctx context.Context, dir, mcVersion string, opts Options) (Result, error) {
	vanilla, err := Prepare(ctx, dir, mcVersion, opts)
	if err != nil {
		return Result{}, err
	}
	profileID, err := writeVanillaProfile(dir, mcVersion)
	if err != nil {
		return Result{}, err
	}
	if err := config.SetLoaderInfo(dir, "vanilla", mcVersion, config.InstallModeTemplate); err != nil {
		return Result{}, fmt.Errorf("写入实例配置失败: %w", err)
	}
	report(opts.Progress, "安装完成: Minecraft %s（profile %s）", mcVersion, profileID)
	return Result{
		MinecraftVersion: mcVersion,
		ProfileID:        profileID,
		Files:            vanilla.Files,
		Downloaded:       vanilla.Downloaded,
		Skipped:          vanilla.Skipped,
	}, nil
}

// Prepare 准备纯原版 / Fabric / Forge 模板共用的基础内容：
// 原版版本 json、client jar、libraries，assets 全局缓存，mods/、saves/ 与 launcher_profiles.json。
func Prepare(ctx context.Context, dir, mcVersion string, opts Options) (fabric.VanillaResult, error) {
	dir = strings.TrimSpace(dir)
	mcVersion = strings.TrimSpace(mcVersion)
	if dir == "" {
		return fabric.VanillaResult{}, errors.New("实例目录为空")
	}
	if mcVersion == "" {
		return fabric.VanillaResult{}, errors.New("Minecraft 版本为空")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fabric.VanillaResult{}, fmt.Errorf("创建实例目录失败: %w", err)
	}

	report(opts.Progress, "拉取 Minecraft %s 版本清单", mcVersion)
	res, err := fabric.EnsureVanilla(ctx, dir, mcVersion, opts.Fabric)
	if err != nil {
		return fabric.VanillaResult{}, fmt.Errorf("安装 Minecraft %s 失败: %w", mcVersion, err)
	}
	report(opts.Progress, "游戏核心文件就绪：%d 个（下载 %d，跳过 %d）", res.Files, res.Downloaded, res.Skipped)

	spec, err := AssetIndexSpec(res.VersionJSON)
	if err != nil {
		return fabric.VanillaResult{}, err
	}
	report(opts.Progress, "下载资源索引与资源文件：%s", spec.ID)
	if err := assets.Ensure(ctx, spec, opts.Assets); err != nil {
		return fabric.VanillaResult{}, fmt.Errorf("准备 assets 失败: %w", err)
	}
	report(opts.Progress, "资源文件就绪")

	if err := ensureDirs(dir); err != nil {
		return fabric.VanillaResult{}, err
	}
	if err := ensureLauncherProfiles(dir); err != nil {
		return fabric.VanillaResult{}, err
	}
	return res, nil
}

func ensureDirs(dir string) error {
	for _, rel := range []string{"libraries", "mods", "saves"} {
		full, err := mrpack.SafeJoin(dir, rel)
		if err != nil {
			return fmt.Errorf("实例目录 %q 非法: %w", rel, err)
		}
		if err := os.MkdirAll(full, 0o755); err != nil {
			return fmt.Errorf("创建 %s 目录失败: %w", rel, err)
		}
	}
	return nil
}

func ensureLauncherProfiles(dir string) error {
	p, err := mrpack.SafeJoin(dir, "launcher_profiles.json")
	if err != nil {
		return fmt.Errorf("launcher_profiles.json 路径非法: %w", err)
	}
	if _, err := os.Stat(p); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查 launcher_profiles.json 失败: %w", err)
	}
	if err := writeFileAtomic(p, []byte(launcherProfilesSkeleton)); err != nil {
		return fmt.Errorf("创建 launcher_profiles.json 失败: %w", err)
	}
	return nil
}

// writeVanillaProfile 生成薄 profile：id/inheritsFrom 让 launch.findProfile 能定位原版版本，
// 其余主类、库和参数全部从原版版本 json 继承。
func writeVanillaProfile(dir, mcVersion string) (string, error) {
	profileID := mcVersion + profileSuffix
	if err := mrpack.ValidateRelPath(profileID); err != nil {
		return "", fmt.Errorf("纯原版 profile id %q 非法: %w", profileID, err)
	}
	raw, err := json.MarshalIndent(struct {
		ID           string   `json:"id"`
		InheritsFrom string   `json:"inheritsFrom"`
		Type         string   `json:"type"`
		Libraries    []string `json:"libraries"`
	}{
		ID:           profileID,
		InheritsFrom: mcVersion,
		Type:         "release",
		Libraries:    []string{},
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("编码纯原版 profile 失败: %w", err)
	}
	raw = append(raw, '\n')

	rel := "versions/" + profileID + "/" + profileID + ".json"
	p, err := mrpack.SafeJoin(dir, rel)
	if err != nil {
		return "", fmt.Errorf("纯原版 profile 路径非法: %w", err)
	}
	if err := writeFileAtomic(p, raw); err != nil {
		return "", fmt.Errorf("写入纯原版 profile 失败: %w", err)
	}
	return profileID, nil
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func report(fn func(string), format string, args ...any) {
	if fn != nil {
		fn(fmt.Sprintf(format, args...))
	}
}
