package vanilla

import (
	"context"
	"errors"
	"strings"

	"mtl/internal/config"
)

// Loader 把纯原版安装适配为统一加载器接口，供 run 选择。
// 纯原版没有额外的启动参数，主类与参数完全来自原版版本 json。
type Loader struct {
	mcVersion string
	opts      Options
}

// NewLoader 返回纯原版加载器适配器。
func NewLoader(mcVersion string, opts Options) (*Loader, error) {
	if strings.TrimSpace(mcVersion) == "" {
		return nil, errors.New("Minecraft 版本为空")
	}
	return &Loader{mcVersion: strings.TrimSpace(mcVersion), opts: opts}, nil
}

// Install 安装纯原版模板（版本文件、assets、profile 与实例配置）。
func (l *Loader) Install(ctx context.Context, inst *config.Instance) error {
	if inst == nil {
		return errors.New("实例为空")
	}
	mc := strings.TrimSpace(inst.MinecraftVersion)
	if mc == "" {
		mc = l.mcVersion
	}
	_, err := Install(ctx, inst.Dir, mc, l.opts)
	return err
}

// GetLaunchArgs 不需要追加参数：原版主类、JVM 参数和游戏参数都在版本 json 中。
func (l *Loader) GetLaunchArgs(ctx context.Context, inst *config.Instance) ([]string, error) {
	return nil, nil
}

// Name 返回加载器标识。
func (l *Loader) Name() string { return "vanilla" }
