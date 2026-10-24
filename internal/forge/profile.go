package forge

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mtl/internal/downloader"
	"mtl/internal/mrpack"
)

// versionInfo 只读取启动必需的字段；主类必须来自 installer 生成的版本 json，
// 不能写死，因为不同 Forge 版本的入口类可能不同。
type versionInfo struct {
	ID           string           `json:"id"`
	InheritsFrom string           `json:"inheritsFrom"`
	MainClass    string           `json:"mainClass"`
	Libraries    []profileLibrary `json:"libraries"`
}

type profileLibrary struct {
	Name      string              `json:"name"`
	URL       string              `json:"url"`
	Downloads profileLibDownloads `json:"downloads"`
}

type profileLibDownloads struct {
	Artifact *profileLibArtifact `json:"artifact"`
}

type profileLibArtifact struct {
	Path string `json:"path"`
	SHA1 string `json:"sha1"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

// readVersionInfo 读取 versions/<id>/<id>.json。
func readVersionInfo(instanceDir, id string) (*versionInfo, error) {
	if err := mrpack.ValidateRelPath(id); err != nil {
		return nil, fmt.Errorf("版本 id %q 非法: %w", id, err)
	}
	rel := "versions/" + id + "/" + id + ".json"
	p, err := mrpack.SafeJoin(instanceDir, rel)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var v versionInfo
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("解析版本 json %s 失败: %w", rel, err)
	}
	if v.ID != "" && v.ID != id {
		return nil, fmt.Errorf("版本 json id %q 与目录 %q 不一致", v.ID, id)
	}
	return &v, nil
}

func listVersionIDs(instanceDir string) (map[string]bool, error) {
	dir, err := mrpack.SafeJoin(instanceDir, "versions")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, fmt.Errorf("读取 versions 目录失败: %w", err)
	}
	ids := make(map[string]bool, len(entries))
	for _, e := range entries {
		if !e.IsDir() || mrpack.ValidateRelPath(e.Name()) != nil {
			continue
		}
		ids[e.Name()] = true
	}
	return ids, nil
}

// findGeneratedProfile 在 installer 运行结束后定位它生成的 Forge profile。
// 优先选择本次新增的目录；installer 复用了已有目录时退回扫描全部 forge profile。
func findGeneratedProfile(instanceDir, mcVersion string, before map[string]bool) (*versionInfo, error) {
	after, err := listVersionIDs(instanceDir)
	if err != nil {
		return nil, err
	}

	var fresh, all []*versionInfo
	ids := make([]string, 0, len(after))
	for id := range after {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if id == mcVersion || !looksLikeForge(id) {
			continue
		}
		v, err := readVersionInfo(instanceDir, id)
		if err != nil || v.InheritsFrom != mcVersion || strings.TrimSpace(v.MainClass) == "" {
			continue
		}
		all = append(all, v)
		if !before[id] {
			fresh = append(fresh, v)
		}
	}

	candidates := fresh
	if len(candidates) == 0 {
		candidates = all
	}
	switch len(candidates) {
	case 0:
		return nil, fmt.Errorf("Forge installer 未生成可用的版本 json（Minecraft %s）", mcVersion)
	case 1:
		return candidates[0], nil
	default:
		names := make([]string, 0, len(candidates))
		for _, v := range candidates {
			names = append(names, v.ID)
		}
		return nil, fmt.Errorf("实例中存在多个 Forge profile，无法确定本次安装结果: %s", strings.Join(names, ", "))
	}
}

// ensureProfileLibraries 用既有 downloader 复核 Forge 版本 json 中的库文件：
// 缺失或哈希不符时按 Forge Maven 声明的 URL 重新下载（沿用重试与原子写入逻辑）。
// 没有 download 信息的库由 installer 自行处理，这里跳过。
func (l *Loader) ensureProfileLibraries(ctx context.Context, instanceDir string, v *versionInfo) error {
	var files []mrpack.File
	seen := make(map[string]bool)
	for _, lib := range v.Libraries {
		a := lib.Downloads.Artifact
		if a == nil || a.Path == "" || a.URL == "" || a.SHA1 == "" {
			continue
		}
		if b, err := hex.DecodeString(a.SHA1); err != nil || len(b) != 20 {
			continue
		}
		u, err := url.Parse(a.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			continue
		}
		rel := "libraries/" + strings.TrimPrefix(a.Path, "libraries/")
		if err := mrpack.ValidateRelPath(rel); err != nil {
			return fmt.Errorf("Forge 库 %q 路径非法: %w", lib.Name, err)
		}
		if seen[strings.ToLower(rel)] {
			continue
		}
		seen[strings.ToLower(rel)] = true
		size := max(a.Size, 0)
		files = append(files, mrpack.File{
			Path:      rel,
			Hashes:    mrpack.Hashes{SHA1: strings.ToLower(a.SHA1)},
			Downloads: []string{a.URL},
			FileSize:  size,
		})
	}
	if len(files) == 0 {
		return nil
	}
	l.report("校验 Forge 库文件: %d 个", len(files))
	if _, err := downloader.Download(ctx, instanceDir, files, downloader.Options{
		Concurrency: l.opts.Concurrency,
		Retries:     l.retries(),
		RetryDelay:  l.opts.RetryDelay,
		HTTPClient:  l.httpClient(),
	}); err != nil {
		return fmt.Errorf("校验 Forge 库文件失败: %w", err)
	}
	return nil
}

func looksLikeForge(id string) bool {
	return strings.Contains(strings.ToLower(id), "forge")
}

// cleanupNewVersionDirs 删除 installer 失败后新增的版本目录，避免留下半成品。
// 已存在的目录以及 libraries 中的已校验文件不会被动。
func cleanupNewVersionDirs(instanceDir string, before map[string]bool) {
	after, err := listVersionIDs(instanceDir)
	if err != nil {
		return
	}
	for id := range after {
		if before[id] {
			continue
		}
		p, err := mrpack.SafeJoin(instanceDir, filepath.ToSlash(filepath.Join("versions", id)))
		if err == nil {
			_ = os.RemoveAll(p)
		}
	}
}
