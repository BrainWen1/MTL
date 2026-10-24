package fabric

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// MinecraftVersion 是 Mojang 版本清单中的一个条目，供交互式界面选择版本。
type MinecraftVersion struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// MinecraftVersions 读取 Mojang 版本清单，保持官方返回顺序（新版本在前）。
// 该方法只读取元数据，不下载任何游戏文件；测试可通过 Options.MojangManifestURL 注入端点。
func MinecraftVersions(ctx context.Context, opts Options) ([]MinecraftVersion, error) {
	o := opts.normalize()
	raw, err := o.getBytes(ctx, o.manifestURL, maxMetadataBytes)
	if err != nil {
		return nil, fmt.Errorf("获取 Mojang 版本清单失败: %w", err)
	}
	var manifest struct {
		Versions []MinecraftVersion `json:"versions"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("解析 Mojang 版本清单失败: %w", err)
	}
	out := make([]MinecraftVersion, 0, len(manifest.Versions))
	for _, v := range manifest.Versions {
		v.ID = strings.TrimSpace(v.ID)
		v.Type = strings.TrimSpace(v.Type)
		if v.ID == "" {
			continue
		}
		// 清单属于外部输入，版本号会进入目录名和下载地址，必须先做本地校验。
		if err := validateVersionSegment("Minecraft 版本", v.ID); err != nil {
			continue
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("Mojang 版本清单为空")
	}
	return out, nil
}

// LatestLoaderVersion 返回指定 Minecraft 版本的最新稳定 Fabric Loader 版本。
// 若官方没有标记 stable 的条目，则回落到列表中的第一个可用版本。
func LatestLoaderVersion(ctx context.Context, gameVersion string, opts Options) (string, error) {
	gameVersion = strings.TrimSpace(gameVersion)
	if err := validateVersionSegment("Minecraft 版本", gameVersion); err != nil {
		return "", err
	}
	o := opts.normalize()
	rawURL := fmt.Sprintf("%s/v2/versions/loader/%s",
		strings.TrimRight(o.metaBase, "/"), url.PathEscape(gameVersion))
	raw, err := o.getBytes(ctx, rawURL, maxMetadataBytes)
	if err != nil {
		return "", fmt.Errorf("获取 Minecraft %s 的 Fabric Loader 版本失败: %w", gameVersion, err)
	}
	var entries []struct {
		Loader struct {
			Version string `json:"version"`
			Stable  bool   `json:"stable"`
		} `json:"loader"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return "", fmt.Errorf("解析 Fabric Loader 版本列表失败: %w", err)
	}

	fallback := ""
	for _, entry := range entries {
		version := strings.TrimSpace(entry.Loader.Version)
		if version == "" {
			continue
		}
		if err := validateVersionSegment("Fabric Loader 版本", version); err != nil {
			continue
		}
		if fallback == "" {
			fallback = version
		}
		if entry.Loader.Stable {
			return version, nil
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", fmt.Errorf("Fabric 未提供 Minecraft %s 可用的 Loader 版本", gameVersion)
}
