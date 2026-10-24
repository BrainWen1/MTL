package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	defaultPromotionsURL = "https://files.minecraftforge.net/net/minecraftforge/forge/promotions_slim.json"
	maxPromotionsBytes   = 1 << 20
)

// SupportsMinecraftVersion 报告当前 Forge 安装器是否支持该 Minecraft 版本。
// 仅支持 1.20.1 及以上的正式版本号；快照等无法比较的版本返回 false。
func SupportsMinecraftVersion(mcVersion string) bool {
	return validateMinecraftVersion(mcVersion) == nil
}

// LatestVersion 读取 Forge 官方 promotions，优先返回 recommended 版本，
// 其次返回 latest 版本。返回值是 Forge 自身的版本号（如 47.4.28），
// 可直接交给 template.Options.LoaderVersion，由安装层补全完整 Maven 版本。
func LatestVersion(ctx context.Context, mcVersion string, opts Options) (string, error) {
	mcVersion = strings.TrimSpace(mcVersion)
	l, err := NewLoader(mcVersion, opts)
	if err != nil {
		return "", err
	}
	raw, err := l.getBytes(ctx, l.promotionsURL(), maxPromotionsBytes)
	if err != nil {
		return "", fmt.Errorf("获取 Forge 版本清单失败: %w", err)
	}
	var promotions struct {
		Promos map[string]string `json:"promos"`
	}
	if err := json.Unmarshal(raw, &promotions); err != nil {
		return "", fmt.Errorf("解析 Forge 版本清单失败: %w", err)
	}

	for _, suffix := range []string{"-recommended", "-latest"} {
		version := strings.TrimSpace(promotions.Promos[mcVersion+suffix])
		if version == "" {
			continue
		}
		// 版本号会进入 Maven URL 与缓存路径，先复用安装层的规范化和校验。
		_, forgeVersion, err := normalizeForgeVersion(mcVersion, version)
		if err != nil {
			continue
		}
		return forgeVersion, nil
	}
	return "", fmt.Errorf("Forge 未提供 Minecraft %s 可用的版本（建议选择 1.20.1 及以上）", mcVersion)
}

func (l *Loader) promotionsURL() string {
	if raw := strings.TrimSpace(l.opts.PromotionsURL); raw != "" {
		return raw
	}
	return defaultPromotionsURL
}
