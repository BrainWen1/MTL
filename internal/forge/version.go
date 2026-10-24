package forge

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"mtl/internal/mrpack"
)

// minMinecraft 是当前支持的 Forge 最低 Minecraft 版本。
const minMinecraft = "1.20.1"

// forgeVersionPartPattern 限制进入 Maven URL / 缓存路径的版本片段。
var forgeVersionPartPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)

// validateMinecraftVersion 只接受 >= 1.20.1 的正式版本号；快照、预发布等无法
// 可靠比较的版本一律拒绝，避免把不兼容的 Forge 装进实例。
func validateMinecraftVersion(v string) error {
	got, err := parseMinecraftVersion(v)
	if err != nil {
		return fmt.Errorf("无法识别 Minecraft 版本 %q：Forge 仅支持 %s 及以上", v, minMinecraft)
	}
	want, _ := parseMinecraftVersion(minMinecraft)
	for i := range want {
		if got[i] > want[i] {
			return nil
		}
		if got[i] < want[i] {
			return fmt.Errorf("当前暂不支持该旧Forge版本：Forge 仅支持 Minecraft %s 及以上（当前 %s）", minMinecraft, v)
		}
	}
	return nil
}

func parseMinecraftVersion(v string) ([3]int, error) {
	var out [3]int
	parts := strings.Split(strings.TrimSpace(v), ".")
	if len(parts) == 0 || len(parts) > 3 {
		return out, fmt.Errorf("版本号段数非法")
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, fmt.Errorf("版本号片段 %q 非法", p)
		}
		out[i] = n
	}
	if out[0] == 0 {
		return out, fmt.Errorf("版本号非法")
	}
	return out, nil
}

// normalizeForgeVersion 把 mrpack 中的 forge 依赖规范成完整 Maven 版本。
// Modrinth 通常提供 "1.20.1-47.1.0"；也兼容只写 Forge 版本 "47.1.0" 的整合包。
func normalizeForgeVersion(mcVersion, dep string) (full, forgeVersion string, err error) {
	mcVersion = strings.TrimSpace(mcVersion)
	dep = strings.TrimSpace(dep)
	if err := validateMinecraftVersion(mcVersion); err != nil {
		return "", "", err
	}
	if dep == "" {
		return "", "", fmt.Errorf("Forge 版本为空")
	}
	if strings.HasPrefix(dep, mcVersion+"-") {
		full = dep
		forgeVersion = strings.TrimPrefix(dep, mcVersion+"-")
	} else {
		full = mcVersion + "-" + dep
		forgeVersion = dep
	}
	if forgeVersion == "" {
		return "", "", fmt.Errorf("Forge 版本为空")
	}
	for _, part := range []string{full, forgeVersion} {
		if !forgeVersionPartPattern.MatchString(part) {
			return "", "", fmt.Errorf("Forge 版本 %q 含非法字符", part)
		}
	}
	if err := mrpack.ValidateRelPath(full); err != nil {
		return "", "", fmt.Errorf("Forge 版本 %q 非法: %w", full, err)
	}
	return full, forgeVersion, nil
}
