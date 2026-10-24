// Package backup 负责实例的只读扫描、流式打包与安全还原。
//
// 备份只包含实例目录内的用户数据与本地配置，不打包 libraries、assets、
// versions 等可重新获取的托管内容，也不会跟随软链接或目录联接读取实例外文件。
package backup

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"mtl/internal/mrpack"
)

const (
	// MetadataFile 是备份包根目录下的元信息文件名。
	MetadataFile = "backup.json"
	// EnvBackupsDir 可显式指定默认备份目录（测试与高级用法）。
	EnvBackupsDir = "MTL_BACKUPS_DIR"

	maxMetadataBytes = 1 << 20
)

// includedDirs 是允许打包/还原的实例一级目录。
// libraries、assets、versions 等托管内容不在此列表中，永远不会进入备份。
var includedDirs = []string{
	".mtl",
	"config",
	"crash-reports",
	"logs",
	"mods",
	"resourcepacks",
	"saves",
	"screenshots",
}

var managedDirs = map[string]bool{
	"assets":    true,
	"libraries": true,
	"versions":  true,
}

// Metadata 对应备份包根目录的 backup.json。
type Metadata struct {
	Instance  string    `json:"instance"`
	CreatedAt time.Time `json:"created_at"`
	Name      string    `json:"name,omitempty"`
	Include   []string  `json:"include"`
}

// Entry 是计划打包或待还原的一个路径。Rel 始终使用 / 分隔。
type Entry struct {
	Rel     string
	Size    int64
	Dir     bool
	ModTime time.Time
}

// Skip 记录扫描时被安全排除的路径。
type Skip struct {
	Path   string
	Reason string
}

// Plan 是一次只读扫描的结果。
type Plan struct {
	InstanceDir string
	Entries     []Entry
	Include     []string
	FileCount   int
	TotalBytes  int64
	Skipped     []Skip
}

// Info 是备份包的轻量元信息。
type Info struct {
	Path     string
	Size     int64
	ModTime  time.Time
	Metadata Metadata
}

// Listed 是 --list 使用的一项；Err 非空表示 zip 无法读取元信息。
type Listed struct {
	Info Info
	Err  string
}

// WriteOptions 控制备份包输出。
type WriteOptions struct {
	// Out 为空时写入默认备份目录；否则写入指定 zip 路径。
	Out  string
	Name string
	Now  time.Time
}

// WriteResult 描述已生成的备份包。
type WriteResult struct {
	Path        string
	Metadata    Metadata
	FileCount   int
	DirCount    int
	TotalBytes  int64
	ArchiveSize int64
}

// DefaultDir 返回默认备份目录：
// Windows 为 %APPDATA%/mtl/backups，其他平台为 ~/.cache/mtl/backups。
// MTL_BACKUPS_DIR 优先于平台默认值。
func DefaultDir() (string, error) {
	if p := strings.TrimSpace(os.Getenv(EnvBackupsDir)); p != "" {
		return p, nil
	}
	if runtime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return "", errors.New("未找到 APPDATA 环境变量，无法确定备份目录")
		}
		return filepath.Join(appData, "mtl", "backups"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法确定用户主目录: %w", err)
	}
	return filepath.Join(home, ".cache", "mtl", "backups"), nil
}

// BuildPlan 只读扫描实例目录，生成待打包清单。
// 固定目录白名单之外的一级目录（libraries/assets/versions 等）跳过；
// 一级普通文件视为实例本地配置一并打包。软链接、目录联接以及解析后位于
// 实例目录之外的文件全部跳过。
func BuildPlan(instanceDir string) (Plan, error) {
	instanceDir = strings.TrimSpace(instanceDir)
	if instanceDir == "" {
		return Plan{}, errors.New("实例目录为空")
	}
	abs, err := filepath.Abs(instanceDir)
	if err != nil {
		return Plan{}, fmt.Errorf("解析实例目录失败: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Plan{}, fmt.Errorf("实例目录不存在: %s", abs)
		}
		return Plan{}, fmt.Errorf("读取实例目录失败: %w", err)
	}
	if !info.IsDir() {
		return Plan{}, fmt.Errorf("实例路径不是目录: %s", abs)
	}
	rootResolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		rootResolved = abs
	}

	plan := Plan{InstanceDir: abs}
	top, err := os.ReadDir(abs)
	if err != nil {
		return Plan{}, fmt.Errorf("读取实例目录失败: %w", err)
	}
	included := make(map[string]bool)

	for _, de := range top {
		name := de.Name()
		full, err := mrpack.SafeJoin(abs, name)
		if err != nil {
			plan.Skipped = append(plan.Skipped, Skip{Path: name, Reason: err.Error()})
			continue
		}
		linfo, err := os.Lstat(full)
		if err != nil {
			return Plan{}, fmt.Errorf("读取 %s 失败: %w", full, err)
		}
		if linfo.Mode()&os.ModeSymlink != 0 {
			plan.Skipped = append(plan.Skipped, Skip{Path: name, Reason: "软链接"})
			continue
		}

		if isIncludedDir(name) {
			if !linfo.IsDir() {
				plan.Skipped = append(plan.Skipped, Skip{Path: name, Reason: "不是目录"})
				continue
			}
			if !resolvesInside(rootResolved, full) {
				plan.Skipped = append(plan.Skipped, Skip{Path: name, Reason: "指向实例目录之外"})
				continue
			}
			plan.Entries = append(plan.Entries, Entry{Rel: name, Dir: true, ModTime: linfo.ModTime()})
			included[name] = true
			if err := walkDir(abs, rootResolved, name, &plan); err != nil {
				return Plan{}, err
			}
			continue
		}
		if linfo.IsDir() {
			// 未列入白名单的目录（libraries、assets、versions、data 等）不备份。
			continue
		}
		if !linfo.Mode().IsRegular() {
			plan.Skipped = append(plan.Skipped, Skip{Path: name, Reason: "不是普通文件"})
			continue
		}
		if name == MetadataFile || isOldBackupName(name) {
			continue
		}
		plan.Entries = append(plan.Entries, Entry{Rel: name, Size: linfo.Size(), ModTime: linfo.ModTime()})
		plan.FileCount++
		plan.TotalBytes += linfo.Size()
		included[name] = true
	}

	plan.Include = make([]string, 0, len(included))
	for name := range included {
		plan.Include = append(plan.Include, name)
	}
	sort.Strings(plan.Include)
	sort.SliceStable(plan.Entries, func(i, j int) bool {
		return plan.Entries[i].Rel < plan.Entries[j].Rel
	})
	return plan, nil
}

func walkDir(instanceDir, rootResolved, relDir string, plan *Plan) error {
	abs, err := mrpack.SafeJoin(instanceDir, relDir)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return fmt.Errorf("读取目录 %s 失败: %w", abs, err)
	}
	for _, de := range entries {
		rel := path.Join(relDir, de.Name())
		full, err := mrpack.SafeJoin(instanceDir, rel)
		if err != nil {
			plan.Skipped = append(plan.Skipped, Skip{Path: rel, Reason: err.Error()})
			continue
		}
		info, err := os.Lstat(full)
		if err != nil {
			return fmt.Errorf("读取 %s 失败: %w", full, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			plan.Skipped = append(plan.Skipped, Skip{Path: rel, Reason: "软链接"})
			continue
		}
		switch {
		case info.IsDir():
			if !resolvesInside(rootResolved, full) {
				plan.Skipped = append(plan.Skipped, Skip{Path: rel, Reason: "指向实例目录之外"})
				continue
			}
			plan.Entries = append(plan.Entries, Entry{Rel: rel, Dir: true, ModTime: info.ModTime()})
			if err := walkDir(instanceDir, rootResolved, rel, plan); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			plan.Entries = append(plan.Entries, Entry{Rel: rel, Size: info.Size(), ModTime: info.ModTime()})
			plan.FileCount++
			plan.TotalBytes += info.Size()
		default:
			plan.Skipped = append(plan.Skipped, Skip{Path: rel, Reason: "不是普通文件"})
		}
	}
	return nil
}

// WriteArchive 把扫描清单流式写入 zip。元信息位于包根目录 backup.json。
// 输出先写同目录临时文件，成功关闭后再原子改名，避免中断留下半个正式备份。
func WriteArchive(plan Plan, instanceName string, opts WriteOptions) (WriteResult, error) {
	instanceName = strings.TrimSpace(instanceName)
	if err := validateInstanceName(instanceName); err != nil {
		return WriteResult{}, err
	}
	if strings.TrimSpace(plan.InstanceDir) == "" {
		return WriteResult{}, errors.New("备份计划缺少实例目录")
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	out, err := resolveOutputPath(instanceName, now, opts)
	if err != nil {
		return WriteResult{}, err
	}
	parent := filepath.Dir(out)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return WriteResult{}, fmt.Errorf("创建备份目录失败: %w", err)
	}
	if opts.Out != "" {
		if info, statErr := os.Stat(out); statErr == nil {
			if info.IsDir() {
				return WriteResult{}, fmt.Errorf("--out 指向目录，请指定 zip 文件路径: %s", out)
			}
			return WriteResult{}, fmt.Errorf("输出文件已存在，请更换 --out 或先手动删除: %s", out)
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			return WriteResult{}, fmt.Errorf("检查输出路径失败: %w", statErr)
		}
	}

	tmp, err := os.CreateTemp(parent, ".mtl-backup-*.tmp")
	if err != nil {
		return WriteResult{}, fmt.Errorf("创建备份临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	metadata := Metadata{
		Instance:  instanceName,
		CreatedAt: now,
		Name:      strings.TrimSpace(opts.Name),
		Include:   append([]string{}, plan.Include...),
	}
	if metadata.Include == nil {
		metadata.Include = []string{}
	}
	if err := validateMetadata(metadata); err != nil {
		_ = tmp.Close()
		return WriteResult{}, fmt.Errorf("备份备注无效: %w", err)
	}
	if err := writeZip(tmp, plan, metadata); err != nil {
		_ = tmp.Close()
		return WriteResult{}, err
	}
	if err := tmp.Close(); err != nil {
		return WriteResult{}, fmt.Errorf("关闭备份文件失败: %w", err)
	}
	if err := os.Rename(tmpName, out); err != nil {
		return WriteResult{}, fmt.Errorf("保存备份文件失败: %w", err)
	}
	stat, err := os.Stat(out)
	if err != nil {
		return WriteResult{}, fmt.Errorf("读取备份文件信息失败: %w", err)
	}

	dirs := 0
	for _, e := range plan.Entries {
		if e.Dir {
			dirs++
		}
	}
	return WriteResult{
		Path:        out,
		Metadata:    metadata,
		FileCount:   plan.FileCount,
		DirCount:    dirs,
		TotalBytes:  plan.TotalBytes,
		ArchiveSize: stat.Size(),
	}, nil
}

func writeZip(dst io.Writer, plan Plan, metadata Metadata) error {
	zw := zip.NewWriter(dst)
	metaData, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("编码备份元信息失败: %w", err)
	}
	metaData = append(metaData, '\n')
	header := &zip.FileHeader{Name: MetadataFile, Method: zip.Deflate, Modified: metadata.CreatedAt}
	header.SetMode(0o644)
	w, err := zw.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("写入备份元信息失败: %w", err)
	}
	if _, err := w.Write(metaData); err != nil {
		return fmt.Errorf("写入备份元信息失败: %w", err)
	}

	for _, e := range plan.Entries {
		entryPath, err := mrpack.SafeJoin(plan.InstanceDir, e.Rel)
		if err != nil {
			return fmt.Errorf("备份路径非法: %w", err)
		}
		if e.Dir {
			h := &zip.FileHeader{Name: e.Rel + "/", Method: zip.Store, Modified: e.ModTime}
			h.SetMode(os.ModeDir | 0o755)
			if _, err := zw.CreateHeader(h); err != nil {
				return fmt.Errorf("写入目录 %s 失败: %w", e.Rel, err)
			}
			continue
		}

		info, err := os.Lstat(entryPath)
		if err != nil {
			return fmt.Errorf("读取文件 %s 失败: %w", e.Rel, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("文件在扫描后变为软链接，已停止备份: %s", e.Rel)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("文件不是普通文件，已停止备份: %s", e.Rel)
		}
		f, err := os.Open(entryPath)
		if err != nil {
			return fmt.Errorf("打开文件 %s 失败: %w", e.Rel, err)
		}
		func() {
			defer f.Close()
			h, headerErr := zip.FileInfoHeader(info)
			if headerErr != nil {
				err = fmt.Errorf("生成文件头 %s 失败: %w", e.Rel, headerErr)
				return
			}
			h.Name = e.Rel
			h.Method = zip.Deflate
			dst, createErr := zw.CreateHeader(h)
			if createErr != nil {
				err = fmt.Errorf("写入文件 %s 失败: %w", e.Rel, createErr)
				return
			}
			if _, copyErr := io.Copy(dst, f); copyErr != nil {
				err = fmt.Errorf("打包文件 %s 失败: %w", e.Rel, copyErr)
			}
		}()
		if err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("完成备份包失败: %w", err)
	}
	return nil
}

func resolveOutputPath(instanceName string, now time.Time, opts WriteOptions) (string, error) {
	if p := strings.TrimSpace(opts.Out); p != "" {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", fmt.Errorf("解析输出路径失败: %w", err)
		}
		return abs, nil
	}
	dir, err := DefaultDir()
	if err != nil {
		return "", err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("解析备份目录失败: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建备份目录失败: %w", err)
	}
	base := backupFileName(instanceName, now, opts.Name)
	return uniquePath(filepath.Join(dir, base)), nil
}

func backupFileName(instance string, now time.Time, name string) string {
	parts := []string{instance, now.Format("20060102-150405")}
	if label := sanitizeLabel(name); label != "" {
		parts = append(parts, label)
	}
	return strings.Join(parts, "-") + ".zip"
}

func sanitizeLabel(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	lastDash := false
	runes := 0
	for _, r := range name {
		if runes >= 48 {
			break
		}
		replace := unicode.IsControl(r) || unicode.IsSpace(r) || strings.ContainsRune(`<>:"/\|?*`, r)
		if replace {
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
			continue
		}
		b.WriteRune(r)
		runes++
		lastDash = false
	}
	label := strings.Trim(strings.TrimSpace(b.String()), ".-_ ")
	if label == "." || label == ".." {
		return ""
	}
	return label
}

func uniquePath(candidate string) string {
	if _, err := os.Stat(candidate); errors.Is(err, fs.ErrNotExist) {
		return candidate
	}
	ext := filepath.Ext(candidate)
	stem := strings.TrimSuffix(candidate, ext)
	for i := 2; i < 1000; i++ {
		next := fmt.Sprintf("%s-%d%s", stem, i, ext)
		if _, err := os.Stat(next); errors.Is(err, fs.ErrNotExist) {
			return next
		}
	}
	return fmt.Sprintf("%s-%d%s", stem, time.Now().UnixNano(), ext)
}

// ReadInfo 读取 zip 根目录的 backup.json，不执行解压。
func ReadInfo(zipPath string) (Info, error) {
	abs, err := filepath.Abs(strings.TrimSpace(zipPath))
	if err != nil {
		return Info{}, fmt.Errorf("解析备份路径失败: %w", err)
	}
	stat, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Info{}, fmt.Errorf("备份文件不存在: %s", abs)
		}
		return Info{}, fmt.Errorf("读取备份文件失败: %w", err)
	}
	if !stat.Mode().IsRegular() {
		return Info{}, fmt.Errorf("备份路径不是普通文件: %s", abs)
	}
	zr, err := zip.OpenReader(abs)
	if err != nil {
		return Info{}, fmt.Errorf("打开备份包失败: %w", err)
	}
	defer zr.Close()

	var (
		meta  Metadata
		found bool
	)
	for _, f := range zr.File {
		if f.Name != MetadataFile {
			continue
		}
		if found {
			return Info{}, fmt.Errorf("备份包包含多个 %s", MetadataFile)
		}
		raw, readErr := readZipMetadata(f)
		if readErr != nil {
			return Info{}, readErr
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			return Info{}, fmt.Errorf("备份元信息损坏: %w", err)
		}
		found = true
	}
	if !found {
		return Info{}, fmt.Errorf("不是 mtl 备份包：缺少 %s", MetadataFile)
	}
	if err := validateMetadata(meta); err != nil {
		return Info{}, err
	}
	return Info{Path: abs, Size: stat.Size(), ModTime: stat.ModTime(), Metadata: meta}, nil
}

func readZipMetadata(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("读取备份元信息失败: %w", err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, maxMetadataBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取备份元信息失败: %w", err)
	}
	if len(raw) > maxMetadataBytes {
		return nil, fmt.Errorf("备份元信息过大")
	}
	return raw, nil
}

func validateMetadata(meta Metadata) error {
	if err := validateInstanceName(strings.TrimSpace(meta.Instance)); err != nil {
		return fmt.Errorf("备份元信息中的实例名无效: %w", err)
	}
	if meta.CreatedAt.IsZero() {
		return errors.New("备份元信息缺少创建时间")
	}
	if hasControl(meta.Name) {
		return errors.New("备份元信息中的备注包含控制字符")
	}
	for _, inc := range meta.Include {
		if strings.TrimSpace(inc) == "" {
			return errors.New("备份元信息包含空的 include 项")
		}
		if err := mrpack.ValidateRelPath(inc); err != nil || strings.ContainsAny(inc, `/\`) {
			return fmt.Errorf("备份元信息 include 项非法: %q", inc)
		}
		if hasControl(inc) {
			return fmt.Errorf("备份元信息 include 项包含控制字符: %q", inc)
		}
	}
	return nil
}

// List 列出 dir（默认为 DefaultDir）下的 zip 备份，按创建时间倒序。
// 目录不存在时返回空列表；损坏的 zip 也会列出并通过 Err 标记。
func List(dir string) ([]Listed, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		var err error
		dir, err = DefaultDir()
		if err != nil {
			return nil, err
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("解析备份目录失败: %w", err)
	}
	entries, err := os.ReadDir(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取备份目录失败: %w", err)
	}

	var out []Listed
	for _, de := range entries {
		if de.IsDir() || !strings.EqualFold(filepath.Ext(de.Name()), ".zip") {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		item := Listed{Info: Info{
			Path:    filepath.Join(abs, de.Name()),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		}}
		read, readErr := ReadInfo(item.Info.Path)
		if readErr != nil {
			item.Err = readErr.Error()
		} else {
			item.Info = read
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Err == "" && b.Err != "" {
			return true
		}
		if a.Err != "" && b.Err == "" {
			return false
		}
		at, bt := a.Info.ModTime, b.Info.ModTime
		if a.Err == "" {
			at = a.Info.Metadata.CreatedAt
		}
		if b.Err == "" {
			bt = b.Info.Metadata.CreatedAt
		}
		if !at.Equal(bt) {
			return at.After(bt)
		}
		return a.Info.Path < b.Info.Path
	})
	return out, nil
}

func isIncludedDir(name string) bool {
	return slices.Contains(includedDirs, name)
}

// resolvesInside 用 EvalSymlinks 检查路径解析后仍位于实例目录内。
// 这同时覆盖 Unix 软链接与 Windows 目录联接，防止把全局缓存打包进备份。
func resolvesInside(rootResolved, target string) bool {
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return false
	}
	return pathWithin(rootResolved, resolved)
}

func pathWithin(root, target string) bool {
	if runtime.GOOS == "windows" {
		root, target = strings.ToLower(root), strings.ToLower(target)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func validateInstanceName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("实例名为空")
	}
	if err := mrpack.ValidateRelPath(name); err != nil {
		return fmt.Errorf("实例名 %q 非法: %w", name, err)
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("实例名 %q 必须是单个目录名", name)
	}
	return nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// isOldBackupName 识别 restore --force 生成的 xxx.old-YYYYMMDD-HHMMSS[-N] 安全副本，
// 避免再次备份时把旧副本层层套娃。
func isOldBackupName(name string) bool {
	idx := strings.LastIndex(name, ".old-")
	if idx <= 0 {
		return false
	}
	suffix := name[idx+len(".old-"):]
	if j := strings.LastIndexByte(suffix, '-'); j > 0 {
		tail := suffix[j+1:]
		if len(tail) > 0 && len(tail) <= 4 && allDigits(tail) {
			suffix = suffix[:j]
		}
	}
	if len(suffix) != len("20060102-150405") || suffix[8] != '-' {
		return false
	}
	for i, r := range suffix {
		if i == 8 {
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
