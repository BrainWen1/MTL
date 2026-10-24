// Package mod 提供实例 mods 目录的离线管理能力：扫描 mod 元数据、启用/禁用与删除。
// 本包只操作实例目录内的普通 jar 文件，不联网、不修改 jar 内容。
// 禁用通过把 jar 移动到 mods/disabled/ 子目录实现，可随时恢复。
package mod

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mtl/internal/mrpack"
)

const (
	// DirName 是实例根目录下的 mod 目录名。
	DirName = "mods"
	// DisabledDirName 是 mods 下存放已禁用 mod 的子目录名。
	DisabledDirName = "disabled"
	// DisabledRelPath 是禁用目录相对实例根目录的路径。
	DisabledRelPath = DirName + "/" + DisabledDirName
)

// Loader 表示 mod 元数据声明的加载器类型。
type Loader string

const (
	LoaderFabric  Loader = "fabric"
	LoaderForge   Loader = "forge"
	LoaderUnknown Loader = "unknown"
)

// DisplayName 返回便于终端展示的加载器名称。
func (l Loader) DisplayName() string {
	switch l {
	case LoaderFabric:
		return "Fabric"
	case LoaderForge:
		return "Forge"
	default:
		return "未知"
	}
}

// Dependency 是 mod 元数据中的一条前置依赖。
type Dependency struct {
	ID string
	// Required 表示该依赖是必需的；Forge 的 mandatory=false 视为可选。
	Required bool
	// VersionRange 是元数据声明的版本约束，可能为空。
	VersionRange string
}

// Mod 是 mods 目录中的一个 jar 及其元数据。
type Mod struct {
	// File 是 jar 的文件名，不含目录。
	File string
	// Path 是相对实例根目录的路径，使用 / 分隔：mods/x.jar 或 mods/disabled/x.jar。
	Path string
	Name string
	ID   string
	// Version 保留元数据原始版本字符串；${file.jarVersion} 等占位符不做替换。
	Version      string
	Loader       Loader
	Dependencies []Dependency
	// Provides 是 jar 内嵌套模块（META-INF/jars/）声明的额外 mod id。
	Provides []string
	// Disabled 表示文件位于 mods/disabled/ 下。
	Disabled bool
	// Known 为 false 表示元数据缺失或无法解析，展示为“未知mod”。
	Known bool
	// Err 记录元数据读取失败的原因，仅用于诊断。
	Err string
}

// DisplayName 返回终端展示名称：元数据缺失时统一标记为“未知mod”。
func (m Mod) DisplayName() string {
	if !m.Known || strings.TrimSpace(m.Name) == "" {
		if !m.Known {
			return "未知mod"
		}
		return m.ID
	}
	return m.Name
}

// Scan 读取实例 mods 与 mods/disabled 下的所有 jar 元数据。
// mods 目录不存在时返回空列表，不报错；非 jar、目录与符号链接会被忽略。
// 单个 jar 元数据损坏不会中断扫描，而是标记为未知 mod。
func Scan(dir string) ([]Mod, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("实例目录为空")
	}
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("实例目录不存在: %s", dir)
		}
		return nil, fmt.Errorf("读取实例目录失败: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("实例路径不是目录: %s", dir)
	}

	var mods []Mod
	for _, group := range []struct {
		rel      string
		disabled bool
	}{
		{DirName, false},
		{DisabledRelPath, true},
	} {
		full, err := mrpack.SafeJoin(dir, group.rel)
		if err != nil {
			return nil, fmt.Errorf("定位 mod 目录失败: %w", err)
		}
		entries, err := os.ReadDir(full)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("读取 mod 目录 %s 失败: %w", full, err)
		}
		for _, de := range entries {
			// 只处理普通 jar 文件；目录、符号链接和其他文件一律跳过，
			// 避免通过软链接读取实例目录之外的内容。
			if !de.Type().IsRegular() {
				continue
			}
			if !isJarFile(de.Name()) {
				continue
			}
			m := Mod{
				File:     de.Name(),
				Path:     group.rel + "/" + de.Name(),
				Disabled: group.disabled,
			}
			meta, metaErr := readMetadata(filepath.Join(full, de.Name()))
			if metaErr != nil {
				m.Loader = LoaderUnknown
				m.Known = false
				m.Err = metaErr.Error()
			} else {
				m.Known = true
				m.ID = meta.id
				m.Name = meta.name
				m.Version = meta.version
				m.Loader = meta.loader
				m.Dependencies = meta.deps
				m.Provides = meta.provides
			}
			mods = append(mods, m)
		}
	}

	sort.Slice(mods, func(i, j int) bool {
		if mods[i].Disabled != mods[j].Disabled {
			return !mods[i].Disabled
		}
		// 未知 mod 的 ID 为空，排在已知 mod 之后。
		li, lj := strings.ToLower(mods[i].ID), strings.ToLower(mods[j].ID)
		if (li == "") != (lj == "") {
			return li != ""
		}
		if li != lj {
			return li < lj
		}
		return strings.ToLower(mods[i].File) < strings.ToLower(mods[j].File)
	})
	return mods, nil
}

// Check 对扫描结果做离线校验，返回不阻断命令的警告文本。
// 检查项：未知元数据、重复 mod id、加载器不匹配、启用中的 mod 缺少必需前置。
// instanceLoader 为空或 vanilla 时跳过加载器匹配检查。
func Check(mods []Mod, instanceLoader string) []string {
	var warnings []string
	instanceLoader = strings.ToLower(strings.TrimSpace(instanceLoader))

	byID := make(map[string][]Mod)
	for _, m := range mods {
		if m.ID != "" {
			byID[strings.ToLower(m.ID)] = append(byID[strings.ToLower(m.ID)], m)
		}
		if !m.Known {
			warnings = append(warnings, fmt.Sprintf("%s 无法识别 mod 元数据，已标记为未知mod: %s", m.Path, m.Err))
			continue
		}
		if mismatchLoader(instanceLoader, m.Loader) {
			warnings = append(warnings, fmt.Sprintf(
				"%s 是 %s mod，但实例加载器为 %s", m.Path, m.Loader.DisplayName(), instanceLoader))
		}
	}

	// 重复 id 只在 id 有效时检查，避免把未知 mod 全部混在一起。
	// 按 id 排序遍历，保证多次执行输出顺序稳定。
	duplicateIDs := make([]string, 0, len(byID))
	for id := range byID {
		if len(byID[id]) > 1 {
			duplicateIDs = append(duplicateIDs, id)
		}
	}
	sort.Strings(duplicateIDs)
	for _, id := range duplicateIDs {
		list := byID[id]
		files := make([]string, 0, len(list))
		for _, m := range list {
			files = append(files, m.Path)
		}
		sort.Strings(files)
		warnings = append(warnings, fmt.Sprintf("mod id %q 重复: %s", id, strings.Join(files, ", ")))
	}

	// enabledIDs 用于判断依赖是否由已启用的 mod 提供；
	// presentIDs 额外包含禁用 mod，用于给出“已安装但被禁用”的提示。
	enabledIDs := make(map[string]bool)
	presentIDs := make(map[string]bool)
	for _, m := range mods {
		ids := make([]string, 0, len(m.Provides)+1)
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
		ids = append(ids, m.Provides...)
		for _, id := range ids {
			key := strings.ToLower(id)
			presentIDs[key] = true
			if !m.Disabled {
				enabledIDs[key] = true
			}
		}
	}
	for _, m := range mods {
		// 仅检查启用中的 mod：禁用的 mod 不参与加载，缺失依赖不代表当前实例异常。
		if !m.Known || m.Disabled || m.ID == "" {
			continue
		}
		for _, dep := range m.Dependencies {
			if !dep.Required || isBuiltinDependency(dep.ID) {
				continue
			}
			key := strings.ToLower(dep.ID)
			if enabledIDs[key] {
				continue
			}
			if presentIDs[key] {
				warnings = append(warnings, fmt.Sprintf(
					"%s 的前置依赖 %q 已安装但处于禁用状态", m.Path, dep.ID))
				continue
			}
			warnings = append(warnings, fmt.Sprintf("%s 缺少前置依赖: %s", m.Path, dep.ID))
		}
	}
	return warnings
}

// SetEnabled 启用或禁用目标 mod，返回移动后的 mod 与是否真正发生了移动。
// 禁用时移动到 mods/disabled/，启用时移回 mods/；目标已处于期望状态时视为幂等成功。
func SetEnabled(dir, target string, enabled bool) (Mod, bool, error) {
	mods, err := Scan(dir)
	if err != nil {
		return Mod{}, false, err
	}
	m, err := Resolve(mods, target)
	if err != nil {
		return Mod{}, false, err
	}
	if m.Disabled != enabled {
		// Disabled=true 且要求禁用，或 Disabled=false 且要求启用：无需移动。
		return m, false, nil
	}

	destRel := DirName + "/" + m.File
	if !enabled {
		destRel = DisabledRelPath + "/" + m.File
	}
	src, err := mrpack.SafeJoin(dir, m.Path)
	if err != nil {
		return Mod{}, false, fmt.Errorf("定位 mod 文件失败: %w", err)
	}
	dest, err := mrpack.SafeJoin(dir, destRel)
	if err != nil {
		return Mod{}, false, fmt.Errorf("定位目标路径失败: %w", err)
	}

	destDir := filepath.Dir(dest)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return Mod{}, false, fmt.Errorf("创建目录 %s 失败: %w", destDir, err)
	}
	if err := ensureNoCaseConflict(destDir, m.File); err != nil {
		return Mod{}, false, err
	}
	if err := os.Rename(src, dest); err != nil {
		return Mod{}, false, fmt.Errorf("移动 mod 文件失败: %w", err)
	}

	m.Path = destRel
	m.Disabled = !enabled
	return m, true, nil
}

// Remove 删除目标 mod 文件，文件位于 mods 根目录或 mods/disabled 均可。
func Remove(dir, target string) (Mod, error) {
	mods, err := Scan(dir)
	if err != nil {
		return Mod{}, err
	}
	m, err := Resolve(mods, target)
	if err != nil {
		return Mod{}, err
	}
	full, err := mrpack.SafeJoin(dir, m.Path)
	if err != nil {
		return Mod{}, fmt.Errorf("定位 mod 文件失败: %w", err)
	}
	info, err := os.Lstat(full)
	if err != nil {
		return Mod{}, fmt.Errorf("读取 mod 文件失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Mod{}, fmt.Errorf("%s 不是普通文件，拒绝删除", m.Path)
	}
	if err := os.Remove(full); err != nil {
		return Mod{}, fmt.Errorf("删除 mod 文件失败: %w", err)
	}
	return m, nil
}

// Resolve 按文件名或 mod id 定位唯一 mod。
// 文件名优先，其次是不带 .jar 后缀的文件名，最后是 mod id；所有比较均忽略大小写。
// target 只允许单层名称，包含路径分隔符时直接拒绝，防止路径穿越。
func Resolve(mods []Mod, target string) (Mod, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return Mod{}, errors.New("mod id 或文件名不能为空")
	}
	if strings.ContainsAny(target, `/\`) || target == "." || target == ".." {
		return Mod{}, fmt.Errorf("目标 %q 必须是文件名或 mod id，不能包含路径分隔符", target)
	}

	lower := strings.ToLower(target)
	if matches := filterMods(mods, func(m Mod) bool {
		return strings.ToLower(m.File) == lower
	}); len(matches) > 0 {
		return uniqueMatch(matches, target)
	}
	if !strings.HasSuffix(lower, ".jar") {
		if matches := filterMods(mods, func(m Mod) bool {
			return strings.TrimSuffix(strings.ToLower(m.File), ".jar") == lower
		}); len(matches) > 0 {
			return uniqueMatch(matches, target)
		}
	}
	if matches := filterMods(mods, func(m Mod) bool {
		return m.ID != "" && strings.ToLower(m.ID) == lower
	}); len(matches) > 0 {
		return uniqueMatch(matches, target)
	}
	return Mod{}, fmt.Errorf("未找到匹配的 mod: %s（可使用 mtl mod list 查看已安装 mod）", target)
}

func uniqueMatch(matches []Mod, target string) (Mod, error) {
	if len(matches) == 1 {
		return matches[0], nil
	}
	files := make([]string, 0, len(matches))
	for _, m := range matches {
		files = append(files, m.Path)
	}
	sort.Strings(files)
	return Mod{}, fmt.Errorf("目标 %q 匹配到多个 mod，请改用文件名: %s", target, strings.Join(files, ", "))
}

func filterMods(mods []Mod, keep func(Mod) bool) []Mod {
	var out []Mod
	for _, m := range mods {
		if keep(m) {
			out = append(out, m)
		}
	}
	return out
}

// ensureNoCaseConflict 检查目标目录中是否已存在同名文件。
// 使用大小写不敏感比较，保证 Linux 上创建的 mod 在搬运到 Windows 后行为可预期。
func ensureNoCaseConflict(destDir, name string) error {
	entries, err := os.ReadDir(destDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取目标目录 %s 失败: %w", destDir, err)
	}
	for _, de := range entries {
		if strings.EqualFold(de.Name(), name) {
			return fmt.Errorf("目标位置已存在同名文件: %s", filepath.Join(destDir, de.Name()))
		}
	}
	return nil
}

// isJarFile 判断文件名是否以 .jar 结尾，大小写不敏感。
func isJarFile(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".jar")
}

// mismatchLoader 判断 mod 加载器是否与实例加载器冲突。
// 实例未记录加载器或为原版时不做判断，避免旧实例产生误报。
func mismatchLoader(instanceLoader string, modLoader Loader) bool {
	switch instanceLoader {
	case "fabric", "forge":
		return modLoader != LoaderUnknown && string(modLoader) != instanceLoader
	default:
		return false
	}
}

// isBuiltinDependency 是游戏/加载器自身提供的依赖，不需要在 mods 目录中查找。
func isBuiltinDependency(id string) bool {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "minecraft", "java", "fabricloader", "fabric-loader", "quilt_loader",
		"quilt-loader", "forge", "neoforge", "fml", "javafml", "lowcodefml",
		// MixinExtras 自 Fabric Loader 0.15 起随加载器提供，不需要单独安装 mod。
		"mixinextras":
		return true
	default:
		return false
	}
}
