package mod

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// maxMetaBytes 限制单个元数据文件解压后的大小，避免恶意 jar 用超大条目耗尽内存。
const maxMetaBytes = 4 << 20

// maxNestedJarBytes 限制 META-INF/jars/ 中单个嵌套 jar 的大小。
const maxNestedJarBytes = 16 << 20

// metadata 是 Fabric / Forge 元数据归一化后的结果。
type metadata struct {
	id      string
	name    string
	version string
	loader  Loader
	deps    []Dependency
	// provides 是嵌套 jar（Fabric API 子模块、Forge Jar-in-Jar）提供的额外 mod id，
	// 用于避免把包内模块误报为缺失依赖。
	provides []string
}

// readMetadata 打开 jar 并读取 mod 元数据。
// 优先级：fabric.mod.json > META-INF/neoforge.mods.toml > META-INF/mods.toml。
// 找不到元数据、jar 损坏或字段非法时返回错误，由调用方标记为未知 mod。
func readMetadata(path string) (metadata, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return metadata{}, fmt.Errorf("不是有效的 jar/zip 文件: %w", err)
	}
	defer zr.Close()

	entry := findMetadataEntry(zr.File)
	if entry == nil {
		return metadata{}, errors.New("未找到 fabric.mod.json 或 META-INF/mods.toml")
	}
	data, err := readZipEntry(entry)
	if err != nil {
		return metadata{}, err
	}

	var meta metadata
	if strings.EqualFold(entry.Name, "fabric.mod.json") {
		meta, err = parseFabricJSON(data)
	} else {
		meta, err = parseForgeTOML(data)
	}
	if err != nil {
		return metadata{}, err
	}
	// Fabric API、c2me 等 jar 会把子模块打包在 META-INF/jars/ 下，
	// 这些子模块也是合法的前置依赖提供者，必须纳入依赖检查。
	meta.provides = readNestedModIDs(zr.File)
	return meta, nil
}

// findMetadataEntry 按 Fabric > NeoForge > Forge 的优先级选择元数据条目。
func findMetadataEntry(files []*zip.File) *zip.File {
	var fabricEntry, neoforgeEntry, forgeEntry *zip.File
	for _, f := range files {
		switch strings.ToLower(f.Name) {
		case "fabric.mod.json":
			if fabricEntry == nil {
				fabricEntry = f
			}
		case "meta-inf/neoforge.mods.toml":
			if neoforgeEntry == nil {
				neoforgeEntry = f
			}
		case "meta-inf/mods.toml":
			if forgeEntry == nil {
				forgeEntry = f
			}
		}
	}
	switch {
	case fabricEntry != nil:
		return fabricEntry
	case neoforgeEntry != nil:
		return neoforgeEntry
	default:
		return forgeEntry
	}
}

// readNestedModIDs 读取 META-INF/jars/ 下嵌套 jar 声明的 mod id。
// 嵌套 jar 损坏或超标时静默跳过，不影响主 mod 的解析。
func readNestedModIDs(files []*zip.File) []string {
	seen := make(map[string]bool)
	var ids []string
	for _, f := range files {
		name := strings.ToLower(f.Name)
		if !strings.HasPrefix(name, "meta-inf/jars/") || !strings.HasSuffix(name, ".jar") {
			continue
		}
		if f.UncompressedSize64 > maxNestedJarBytes {
			continue
		}
		data, err := readZipEntryLimit(f, maxNestedJarBytes)
		if err != nil {
			continue
		}
		nested, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			continue
		}
		entry := findMetadataEntry(nested.File)
		if entry == nil {
			continue
		}
		metaData, err := readZipEntry(entry)
		if err != nil {
			continue
		}
		var meta metadata
		if strings.EqualFold(entry.Name, "fabric.mod.json") {
			meta, err = parseFabricJSON(metaData)
		} else {
			meta, err = parseForgeTOML(metaData)
		}
		if err != nil || meta.id == "" {
			continue
		}
		key := strings.ToLower(meta.id)
		if seen[key] {
			continue
		}
		seen[key] = true
		ids = append(ids, meta.id)
	}
	sort.Strings(ids)
	return ids
}

// readZipEntry 读取元数据条目，并对解压后大小做二次限制。
func readZipEntry(f *zip.File) ([]byte, error) {
	return readZipEntryLimit(f, maxMetaBytes)
}

func readZipEntryLimit(f *zip.File, limit int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("%s 超过 %d 字节上限", f.Name, limit)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", f.Name, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", f.Name, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s 超过 %d 字节上限", f.Name, limit)
	}
	return data, nil
}

// parseFabricJSON 解析 fabric.mod.json。
// version / name 允许数字等非标准类型，尽量以文本形式展示而不是直接判定损坏。
func parseFabricJSON(data []byte) (metadata, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var raw struct {
		ID      string         `json:"id"`
		Version any            `json:"version"`
		Name    any            `json:"name"`
		Depends map[string]any `json:"depends"`
	}
	if err := dec.Decode(&raw); err != nil {
		return metadata{}, fmt.Errorf("解析 fabric.mod.json 失败: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return metadata{}, errors.New("fabric.mod.json 末尾存在多余数据")
	}
	if strings.TrimSpace(raw.ID) == "" {
		return metadata{}, errors.New("fabric.mod.json 缺少 id")
	}

	meta := metadata{
		id:      strings.TrimSpace(raw.ID),
		name:    asText(raw.Name),
		version: asText(raw.Version),
		loader:  LoaderFabric,
	}
	if meta.name == "" {
		meta.name = meta.id
	}
	for id, value := range raw.Depends {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		meta.deps = append(meta.deps, Dependency{
			ID:           id,
			Required:     true,
			VersionRange: asText(value),
		})
	}
	sortDependencies(meta.deps)
	return meta, nil
}

// asText 把 JSON 值转换为展示文本；数组用逗号连接，对象退化为 JSON 文本。
func asText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			if s := asText(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// parseForgeTOML 用轻量行级解析器读取 META-INF/mods.toml。
// 只需要 [[mods]] 与 [[dependencies.<modid>]] 两张表，避免为了一个字段引入完整 TOML 依赖。
func parseForgeTOML(data []byte) (metadata, error) {
	meta := metadata{loader: LoaderForge}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), maxMetaBytes)

	section := ""
	modsEntry := 0
	var currentDep *Dependency

	flushDep := func() {
		if currentDep != nil && strings.TrimSpace(currentDep.ID) != "" {
			meta.deps = append(meta.deps, *currentDep)
		}
		currentDep = nil
	}

	for scanner.Scan() {
		line := strings.TrimSpace(stripTOMLComment(scanner.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[[") {
			flushDep()
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "[["), "]]"))
			if section == "mods" {
				modsEntry++
			}
			if strings.HasPrefix(section, "dependencies.") {
				// 默认视为必需；显式 mandatory=false 时再改为可选。
				currentDep = &Dependency{Required: true}
			}
			continue
		}
		if strings.HasPrefix(line, "[") {
			flushDep()
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			continue
		}

		key, value, ok := splitTOMLKeyValue(line)
		if !ok {
			continue
		}
		switch {
		case section == "mods" && modsEntry == 1:
			switch key {
			case "modId":
				if meta.id == "" {
					meta.id = tomlString(value)
				}
			case "version":
				if meta.version == "" {
					meta.version = tomlString(value)
				}
			case "displayName":
				if meta.name == "" {
					meta.name = tomlString(value)
				}
			}
		case currentDep != nil && strings.HasPrefix(section, "dependencies."):
			switch key {
			case "modId":
				currentDep.ID = tomlString(value)
			case "mandatory":
				currentDep.Required = !strings.EqualFold(tomlString(value), "false")
			case "versionRange":
				currentDep.VersionRange = tomlString(value)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return metadata{}, fmt.Errorf("读取 mods.toml 失败: %w", err)
	}
	flushDep()

	meta.id = strings.TrimSpace(meta.id)
	if meta.id == "" {
		return metadata{}, errors.New("mods.toml 缺少 modId")
	}
	if meta.name == "" {
		meta.name = meta.id
	}
	sortDependencies(meta.deps)
	return meta, nil
}

// stripTOMLComment 去掉引号外的 # 注释，保留 URL、描述中的 # 字符。
func stripTOMLComment(line string) string {
	var quote rune
	escaped := false
	for i, r := range line {
		switch {
		case escaped:
			escaped = false
		case quote == '"' && r == '\\':
			escaped = true
		case quote == 0 && (r == '"' || r == '\''):
			quote = r
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && r == '#':
			return line[:i]
		}
	}
	return line
}

// splitTOMLKeyValue 按第一个 = 拆分键值，并去掉键两侧的引号。
func splitTOMLKeyValue(line string) (key, value string, ok bool) {
	key, value, ok = strings.Cut(line, "=")
	if !ok {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	key = strings.Trim(key, `"'`)
	value = strings.TrimSpace(value)
	return key, value, key != ""
}

// tomlString 解析简单的 TOML 字符串值；非字符串原样返回。
func tomlString(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		if s, err := strconv.Unquote(v); err == nil {
			return s
		}
		return v[1 : len(v)-1]
	}
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return v[1 : len(v)-1]
	}
	return v
}

// sortDependencies 按 id 排序，保证输出与测试结果稳定。
func sortDependencies(deps []Dependency) {
	for i := 1; i < len(deps); i++ {
		for j := i; j > 0 && strings.ToLower(deps[j].ID) < strings.ToLower(deps[j-1].ID); j-- {
			deps[j], deps[j-1] = deps[j-1], deps[j]
		}
	}
}
