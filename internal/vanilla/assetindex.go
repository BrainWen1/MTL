package vanilla

import (
	"encoding/json"
	"fmt"

	"mtl/internal/assets"
)

// versionMetadata 只解析准备 assets 所需的最小字段，避免复制完整的版本结构。
type versionMetadata struct {
	ID         string `json:"id"`
	AssetIndex struct {
		ID   string `json:"id"`
		URL  string `json:"url"`
		SHA1 string `json:"sha1"`
		Size int64  `json:"size"`
	} `json:"assetIndex"`
}

// AssetIndexSpec 从 Mojang 版本 json 中提取 assetIndex 信息。
// 字段完整性与 URL/哈希合法性由 assets.Ensure 继续校验。
func AssetIndexSpec(versionJSON []byte) (assets.IndexSpec, error) {
	var meta versionMetadata
	if err := json.Unmarshal(versionJSON, &meta); err != nil {
		return assets.IndexSpec{}, fmt.Errorf("解析版本 json 失败: %w", err)
	}
	a := meta.AssetIndex
	if a.ID == "" || a.URL == "" || a.SHA1 == "" {
		id := meta.ID
		if id == "" {
			id = "未知"
		}
		return assets.IndexSpec{}, fmt.Errorf("Minecraft %s 版本 json 缺少完整的 assetIndex 信息（id/url/sha1）", id)
	}
	return assets.IndexSpec{ID: a.ID, URL: a.URL, SHA1: a.SHA1, Size: a.Size}, nil
}
