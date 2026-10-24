// Package template 编排不依赖 mrpack 的纯净实例安装：纯原版、Fabric、Forge。
// 它与 mrpack 安装共用 fabric/forge/loader/assets 等既有能力，但不会生成 pack-lock。
package template

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"mtl/internal/assets"
	"mtl/internal/config"
	"mtl/internal/fabric"
	"mtl/internal/forge"
	"mtl/internal/loader"
	"mtl/internal/vanilla"
)

// Options 描述一次纯净模板安装。
type Options struct {
	MinecraftVersion string
	// LoaderType 为空表示纯原版；目前支持 fabric / forge。
	LoaderType    string
	LoaderVersion string
	Fabric        fabric.Options
	Forge         forge.Options
	Assets        assets.Options
	// Progress 接收高层进度（不含 "[Template]" 前缀）。
	Progress func(string)
}

// Result 描述安装结果。
type Result struct {
	MinecraftVersion string
	LoaderType       string
	LoaderVersion    string
	ProfileID        string
}

// Install 按参数安装纯原版或带加载器的模板实例。
// 任何失败都不会写入 pack-lock；已下载的文件可安全复用。
func Install(ctx context.Context, dir string, opts Options) (Result, error) {
	dir = strings.TrimSpace(dir)
	mcVersion := strings.TrimSpace(opts.MinecraftVersion)
	loaderType := strings.ToLower(strings.TrimSpace(opts.LoaderType))
	loaderVersion := strings.TrimSpace(opts.LoaderVersion)

	if dir == "" {
		return Result{}, errors.New("实例目录为空")
	}
	if mcVersion == "" {
		return Result{}, errors.New("必须用 --mc 指定 Minecraft 版本")
	}
	switch loaderType {
	case "", loader.TypeFabric, loader.TypeForge:
	default:
		return Result{}, fmt.Errorf("不支持的加载器 %q：仅支持 fabric / forge；纯原版请省略 --loader", opts.LoaderType)
	}
	if loaderType != "" && loaderVersion == "" {
		return Result{}, fmt.Errorf("使用 --loader %s 时必须同时指定 --loader-version", loaderType)
	}
	if loaderType == "" && loaderVersion != "" {
		return Result{}, errors.New("--loader-version 必须与 --loader 一起使用")
	}

	vanillaOpts := vanilla.Options{
		Fabric:   opts.Fabric,
		Assets:   opts.Assets,
		Progress: opts.Progress,
	}

	if loaderType == "" {
		report(opts.Progress, "安装模式: 纯原版（Minecraft %s）", mcVersion)
		res, err := vanilla.Install(ctx, dir, mcVersion, vanillaOpts)
		if err != nil {
			return Result{}, err
		}
		return Result{
			MinecraftVersion: mcVersion,
			ProfileID:        res.ProfileID,
		}, nil
	}

	report(opts.Progress, "安装模式: %s %s（Minecraft %s）", loaderType, loaderVersion, mcVersion)
	if _, err := vanilla.Prepare(ctx, dir, mcVersion, vanillaOpts); err != nil {
		return Result{}, err
	}

	report(opts.Progress, "安装加载器: %s %s", loaderType, loaderVersion)
	ld, err := loader.NewLoaderWithOptions(loaderType, mcVersion, loader.Options{
		Fabric: opts.Fabric,
		Forge:  opts.Forge,
	})
	if err != nil {
		return Result{}, err
	}
	inst := &config.Instance{
		Dir:              dir,
		MinecraftVersion: mcVersion,
		LoaderType:       loaderType,
		LoaderVersion:    loaderVersion,
	}
	if err := ld.Install(ctx, inst); err != nil {
		return Result{}, fmt.Errorf("安装 %s 加载器失败（Minecraft %s / loader %s）：%w",
			loaderType, mcVersion, loaderVersion, err)
	}
	if err := config.SetLoaderInfo(dir, loaderType, loaderVersion, config.InstallModeTemplate); err != nil {
		return Result{}, fmt.Errorf("写入实例配置失败: %w", err)
	}
	report(opts.Progress, "安装完成: %s %s（Minecraft %s）", loaderType, loaderVersion, mcVersion)
	return Result{
		MinecraftVersion: mcVersion,
		LoaderType:       loaderType,
		LoaderVersion:    loaderVersion,
	}, nil
}

func report(fn func(string), format string, args ...any) {
	if fn != nil {
		fn(fmt.Sprintf(format, args...))
	}
}
