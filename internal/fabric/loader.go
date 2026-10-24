package fabric

import (
	"context"
	"errors"
	"fmt"

	"mtl/internal/config"
)

// Loader 把既有 Fabric 安装逻辑适配为统一加载器接口。
// 原有 Install 的流程、下载与校验行为保持不变，这里只做参数转发和结果汇报。
type Loader struct {
	opts Options
}

// NewLoader 返回 Fabric 加载器适配器。opts 中的端点、并发与进度回调原样透传。
func NewLoader(opts Options) *Loader {
	return &Loader{opts: opts}
}

// Install 准备原版文件并安装 Fabric loader。
func (l *Loader) Install(ctx context.Context, inst *config.Instance) error {
	if inst == nil {
		return errors.New("实例为空")
	}
	res, err := Install(ctx, inst.Dir, inst.MinecraftVersion, inst.LoaderVersion, l.opts)
	if err != nil {
		return err
	}
	if l.opts.Progress != nil {
		l.opts.Progress(fmt.Sprintf("安装完成: %s（下载 %d，跳过 %d）", res.ProfileID, res.Downloaded, res.Skipped))
	}
	return nil
}

// GetLaunchArgs 返回加载器特有的额外 JVM 参数。
// Fabric 的启动参数完全来自 Fabric profile 版本 json，由 launch 动态读取，
// 因此这里不需要追加任何参数。
func (l *Loader) GetLaunchArgs(ctx context.Context, inst *config.Instance) ([]string, error) {
	return nil, nil
}

// Name 返回加载器标识。
func (l *Loader) Name() string { return "fabric" }
