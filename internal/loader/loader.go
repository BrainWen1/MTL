// Package loader 定义加载器统一抽象，并按类型/版本构造 Fabric、Forge（及预留的
// NeoForge）实现，避免在 install/launch 中散落加载器专属分支。
package loader

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"mtl/internal/config"
	"mtl/internal/fabric"
	"mtl/internal/forge"
)

// 加载器类型标识；neoforge 仅为预留枚举，本阶段不提供实现。
const (
	TypeFabric   = "fabric"
	TypeForge    = "forge"
	TypeNeoForge = "neoforge"
	TypeQuilt    = "quilt"
)

// Loader 是所有加载器实现的统一接口。
type Loader interface {
	// Install 执行加载器安装，写入实例版本信息与库文件。
	Install(ctx context.Context, inst *config.Instance) error
	// GetLaunchArgs 返回版本 json 之外需要额外追加的启动参数。
	// 主类、JVM 参数和游戏参数优先从版本 json 动态读取，避免写死加载器入口。
	GetLaunchArgs(ctx context.Context, inst *config.Instance) ([]string, error)
	// Name 返回加载器标识：fabric / forge / neoforge（预留）。
	Name() string
}

// Options 允许调用方注入加载器端点、并发与进度回调（测试或镜像场景）。
type Options struct {
	Fabric fabric.Options
	Forge  forge.Options
}

// NewLoader 使用默认选项构造加载器。
func NewLoader(loaderType, mcVersion string) (Loader, error) {
	return NewLoaderWithOptions(loaderType, mcVersion, Options{})
}

// NewLoaderWithOptions 按类型和 Minecraft 版本构造加载器。
// 不支持的版本（如 Forge < 1.20.1）或未知类型返回明确错误。
func NewLoaderWithOptions(loaderType, mcVersion string, opts Options) (Loader, error) {
	t := strings.ToLower(strings.TrimSpace(loaderType))
	mcVersion = strings.TrimSpace(mcVersion)
	switch t {
	case TypeFabric:
		if mcVersion == "" {
			return nil, errors.New("Minecraft 版本为空")
		}
		return fabric.NewLoader(opts.Fabric), nil
	case TypeForge:
		return forge.NewLoader(mcVersion, opts.Forge)
	case TypeNeoForge:
		return nil, errors.New("暂不支持 NeoForge 加载器")
	case "":
		return nil, errors.New("缺少加载器类型")
	default:
		return nil, fmt.Errorf("不支持该加载器 %q", loaderType)
	}
}

// Detect 从版本 profile id 推断加载器类型。
// 改动前只存在 Fabric 实例，因此无法识别时回落到 fabric，保证老实例可继续启动。
func Detect(profileID string) string {
	p := strings.ToLower(strings.TrimSpace(profileID))
	switch {
	case strings.Contains(p, TypeNeoForge):
		return TypeNeoForge
	case strings.Contains(p, TypeFabric):
		return TypeFabric
	case strings.Contains(p, TypeQuilt):
		return TypeQuilt
	case strings.Contains(p, TypeForge):
		return TypeForge
	default:
		return TypeFabric
	}
}
