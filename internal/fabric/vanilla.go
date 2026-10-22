package fabric

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"mtl/internal/mrpack"
)

type mojangManifest struct {
	Versions []mojangVersionEntry `json:"versions"`
}

type mojangVersionEntry struct {
	ID   string `json:"id"`
	URL  string `json:"url"`
	SHA1 string `json:"sha1"`
}

type mojangVersion struct {
	ID        string `json:"id"`
	Downloads struct {
		Client mojangArtifact `json:"client"`
	} `json:"downloads"`
	Libraries []mojangLibrary `json:"libraries"`
}

type mojangLibrary struct {
	Name      string            `json:"name"`
	Downloads mojangDownloads   `json:"downloads"`
	Natives   map[string]string `json:"natives"`
	Rules     []mojangRule      `json:"rules"`
}

type mojangDownloads struct {
	Artifact    *mojangArtifact           `json:"artifact"`
	Classifiers map[string]mojangArtifact `json:"classifiers"`
}

type mojangArtifact struct {
	Path string `json:"path"`
	SHA1 string `json:"sha1"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

type mojangRule struct {
	Action   string          `json:"action"`
	OS       *mojangOS       `json:"os"`
	Features map[string]bool `json:"features"`
}

type mojangOS struct {
	Name string `json:"name"`
	Arch string `json:"arch"`
}

// fetchVanillaVersion 从 Mojang 清单定位版本并校验版本 json 的 sha1。
func fetchVanillaVersion(ctx context.Context, gameVersion string, o options) ([]byte, error) {
	raw, err := o.getBytes(ctx, o.manifestURL, maxMetadataBytes)
	if err != nil {
		return nil, fmt.Errorf("获取 Mojang 版本清单失败: %w", err)
	}
	var manifest mojangManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("解析 Mojang 版本清单失败: %w", err)
	}

	var entry *mojangVersionEntry
	for i := range manifest.Versions {
		if manifest.Versions[i].ID == gameVersion {
			entry = &manifest.Versions[i]
			break
		}
	}
	if entry == nil {
		return nil, fmt.Errorf("Mojang 版本清单中找不到 Minecraft %s", gameVersion)
	}
	if err := validateDownloadURL(entry.URL); err != nil {
		return nil, fmt.Errorf("Minecraft %s 版本 json: %w", gameVersion, err)
	}
	if err := validateHash(hashSHA1, entry.SHA1); err != nil {
		return nil, fmt.Errorf("Minecraft %s 版本清单 sha1 无效: %w", gameVersion, err)
	}

	data, err := o.getBytes(ctx, entry.URL, maxMetadataBytes)
	if err != nil {
		return nil, fmt.Errorf("下载 Minecraft %s 版本 json 失败: %w", gameVersion, err)
	}
	got := sumHex(hashSHA1, data)
	if !strings.EqualFold(got, entry.SHA1) {
		return nil, fmt.Errorf("%w: Minecraft %s 版本 json 得到 %s，期望 %s", errHashMismatch, gameVersion, got, entry.SHA1)
	}
	return data, nil
}

// vanillaArtifacts 按目标平台筛选 vanilla libraries。
// 兼容两种格式：现代版每条 library 自带 artifact + rules；旧版使用 natives/classifiers。
// native 构件在解析阶段按 GOOS/GOARCH 过滤，避免为不匹配的平台发起下载。
func vanillaArtifacts(v mojangVersion, goos, goarch string, progress *reporter) ([]artifact, error) {
	osName, archName := ruleOS(goos, goarch)
	var arts []artifact
	for _, lib := range v.Libraries {
		if !rulesAllow(lib.Rules, osName, archName) {
			continue
		}
		if arch, isNative := nativeClassifierArch(libraryClassifier(lib.Name)); isNative && arch != goarch {
			progress.report(fmt.Sprintf("跳过不匹配当前架构的native: %s", lib.Name))
			continue
		}
		if lib.Downloads.Artifact != nil {
			a, err := mojangArtifactEntry(*lib.Downloads.Artifact, lib.Name)
			if err != nil {
				return nil, fmt.Errorf("vanilla library %q: %w", lib.Name, err)
			}
			arts = append(arts, a)
		}
		if len(lib.Natives) > 0 {
			classifier, ok := lib.Natives[osName]
			if !ok {
				continue
			}
			classifier = strings.ReplaceAll(classifier, "${arch}", archBits(goarch))
			if arch, isNative := nativeClassifierArch(classifier); isNative && arch != goarch {
				progress.report(fmt.Sprintf("跳过不匹配当前架构的native: %s", lib.Name))
				continue
			}
			art, ok := lib.Downloads.Classifiers[classifier]
			if !ok {
				continue
			}
			a, err := mojangArtifactEntry(art, lib.Name+":"+classifier)
			if err != nil {
				return nil, fmt.Errorf("vanilla library %q (%s): %w", lib.Name, classifier, err)
			}
			arts = append(arts, a)
		}
	}
	return arts, nil
}

func mojangArtifactEntry(a mojangArtifact, display string) (artifact, error) {
	if a.Path == "" {
		return artifact{}, fmt.Errorf("缺少 artifact.path")
	}
	rel := "libraries/" + a.Path
	if err := mrpack.ValidateRelPath(rel); err != nil {
		return artifact{}, fmt.Errorf("artifact.path %q 非法: %w", a.Path, err)
	}
	if err := validateDownloadURL(a.URL); err != nil {
		return artifact{}, err
	}
	if err := validateHash(hashSHA1, a.SHA1); err != nil {
		return artifact{}, fmt.Errorf("sha1 无效: %w", err)
	}
	if a.Size < 0 || a.Size > maxArtifactBytes {
		return artifact{}, fmt.Errorf("声明大小 %d 非法", a.Size)
	}
	return artifact{
		relPath: rel,
		url:     a.URL,
		kind:    hashSHA1,
		sum:     a.SHA1,
		size:    a.Size,
		display: display,
		label:   "library",
	}, nil
}

// rulesAllow 使用 Mojang 规则语义：无规则默认允许，否则按顺序匹配、最后一条命中为准。
func rulesAllow(rules []mojangRule, osName, arch string) bool {
	if len(rules) == 0 {
		return true
	}
	allowed := false
	for _, r := range rules {
		if !ruleApplies(r, osName, arch) {
			continue
		}
		allowed = r.Action == "allow"
	}
	return allowed
}

func ruleApplies(r mojangRule, osName, arch string) bool {
	if r.OS != nil {
		if r.OS.Name != "" && r.OS.Name != osName {
			return false
		}
		if r.OS.Arch != "" && r.OS.Arch != arch {
			return false
		}
	}
	// 未实现 demo 等启动特性，统一视为未启用。
	for _, on := range r.Features {
		if on {
			return false
		}
	}
	return true
}

func ruleOS(goos, goarch string) (name, arch string) {
	switch goos {
	case "windows":
		name = "windows"
	case "darwin":
		name = "osx"
	default:
		name = "linux"
	}
	arch = goarch
	switch goarch {
	case "386":
		arch = "x86"
	case "arm64":
		arch = "aarch64"
	}
	return name, arch
}

func archBits(goarch string) string {
	if goarch == "386" {
		return "32"
	}
	return "64"
}
