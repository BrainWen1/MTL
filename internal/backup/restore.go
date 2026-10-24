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
	"sort"
	"strings"
	"time"

	"mtl/internal/mrpack"
)

// ErrTargetExists 表示目标实例已存在且未开启 --force。
var ErrTargetExists = errors.New("目标实例已存在")

// RestoreOptions 控制还原行为。
type RestoreOptions struct {
	Force bool
	Now   time.Time
	// OnEntry 在单个目录创建或文件写入成功后回调，供 --debug 流式输出。
	OnEntry func(Entry)
}

// Renamed 记录 --force 时被保留的旧路径。
type Renamed struct {
	From string
	To   string
}

// RestoreResult 描述一次还原结果。
type RestoreResult struct {
	TargetDir string
	Instance  string
	Files     int
	Dirs      int
	Bytes     int64
	Renamed   []Renamed
}

type archiveEntry struct {
	Entry
	File *zip.File
}

// Restore 将备份包流式还原到 targetDir。
// 目标已存在且未指定 Force 时不会修改任何文件；Force 模式只重命名备份包
// 实际覆盖到的一级目录/文件，不会删除目标实例中的其他内容。
func Restore(zipPath, targetDir string, opts RestoreOptions) (RestoreResult, error) {
	absZip, err := filepath.Abs(strings.TrimSpace(zipPath))
	if err != nil {
		return RestoreResult{}, fmt.Errorf("解析备份路径失败: %w", err)
	}
	stat, err := os.Stat(absZip)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return RestoreResult{}, fmt.Errorf("备份文件不存在: %s", absZip)
		}
		return RestoreResult{}, fmt.Errorf("读取备份文件失败: %w", err)
	}
	if !stat.Mode().IsRegular() {
		return RestoreResult{}, fmt.Errorf("备份路径不是普通文件: %s", absZip)
	}

	zr, err := zip.OpenReader(absZip)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("打开备份包失败: %w", err)
	}
	defer zr.Close()

	meta, entries, err := inspectArchive(zr)
	if err != nil {
		return RestoreResult{}, err
	}

	targetDir = strings.TrimSpace(targetDir)
	if targetDir == "" {
		return RestoreResult{}, errors.New("目标实例目录为空")
	}
	targetAbs, err := filepath.Abs(targetDir)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("解析目标实例目录失败: %w", err)
	}
	targetExists := false
	targetInfo, statErr := os.Lstat(targetAbs)
	switch {
	case statErr == nil:
		if targetInfo.Mode()&os.ModeSymlink != 0 {
			return RestoreResult{}, fmt.Errorf("目标实例目录是软链接，拒绝还原: %s", targetAbs)
		}
		if !targetInfo.IsDir() {
			return RestoreResult{}, fmt.Errorf("目标实例路径不是目录: %s", targetAbs)
		}
		targetExists = true
	case errors.Is(statErr, fs.ErrNotExist):
		targetExists = false
	default:
		return RestoreResult{}, fmt.Errorf("检查目标实例目录失败: %w", statErr)
	}

	// 覆盖确认必须在任何写操作之前完成。
	if targetExists && !opts.Force {
		return RestoreResult{}, fmt.Errorf("%w: %s；确认覆盖请加 --force", ErrTargetExists, targetAbs)
	}

	res := RestoreResult{TargetDir: targetAbs, Instance: meta.Instance}
	if !targetExists {
		if err := os.MkdirAll(targetAbs, 0o755); err != nil {
			return res, fmt.Errorf("创建目标实例目录失败: %w", err)
		}
	} else {
		now := opts.Now
		if now.IsZero() {
			now = time.Now()
		}
		renamed, renameErr := renameOverlapping(targetAbs, entries, now)
		res.Renamed = renamed
		if renameErr != nil {
			return res, renameErr
		}
	}

	for _, e := range entries {
		if e.Dir {
			if err := mkdirAllSafe(targetAbs, e.Rel); err != nil {
				return res, err
			}
			res.Dirs++
			if opts.OnEntry != nil {
				opts.OnEntry(e.Entry)
			}
			continue
		}

		if err := mkdirAllSafe(targetAbs, path.Dir(e.Rel)); err != nil {
			return res, err
		}
		full, err := mrpack.SafeJoin(targetAbs, e.Rel)
		if err != nil {
			return res, fmt.Errorf("还原路径非法: %w", err)
		}
		n, writeErr := extractFile(full, e.File)
		if writeErr != nil {
			return res, writeErr
		}
		res.Files++
		res.Bytes += n
		if opts.OnEntry != nil {
			opts.OnEntry(e.Entry)
		}
	}
	return res, nil
}

func extractFile(full string, f *zip.File) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, fmt.Errorf("打开备份条目 %s 失败: %w", f.Name, err)
	}
	defer rc.Close()

	out, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, fmt.Errorf("创建文件 %s 失败: %w", full, err)
	}
	n, copyErr := io.Copy(out, rc)
	closeErr := out.Close()
	if copyErr != nil {
		return n, fmt.Errorf("写入文件 %s 失败: %w", full, copyErr)
	}
	if closeErr != nil {
		return n, fmt.Errorf("关闭文件 %s 失败: %w", full, closeErr)
	}
	return n, nil
}

// renameOverlapping 把目标实例中会被本次还原覆盖的一级目录/文件改名保留。
func renameOverlapping(target string, entries []archiveEntry, now time.Time) ([]Renamed, error) {
	topSet := make(map[string]bool)
	for _, e := range entries {
		top := strings.SplitN(e.Rel, "/", 2)[0]
		topSet[top] = true
	}
	tops := make([]string, 0, len(topSet))
	for top := range topSet {
		tops = append(tops, top)
	}
	sort.Strings(tops)

	stamp := now.Format("20060102-150405")
	var renamed []Renamed
	for _, top := range tops {
		from, err := mrpack.SafeJoin(target, top)
		if err != nil {
			return renamed, fmt.Errorf("目标路径非法: %w", err)
		}
		if _, err := os.Lstat(from); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return renamed, fmt.Errorf("检查目标路径 %s 失败: %w", from, err)
		}

		oldRel, err := uniqueOldRel(target, top, stamp)
		if err != nil {
			return renamed, err
		}
		to, err := mrpack.SafeJoin(target, oldRel)
		if err != nil {
			return renamed, fmt.Errorf("旧内容备份路径非法: %w", err)
		}
		if err := os.Rename(from, to); err != nil {
			return renamed, fmt.Errorf("保留旧内容 %s 失败: %w", from, err)
		}
		renamed = append(renamed, Renamed{From: from, To: to})
	}
	return renamed, nil
}

func uniqueOldRel(target, top, stamp string) (string, error) {
	base := top + ".old-" + stamp
	for i := 1; i < 1000; i++ {
		rel := base
		if i > 1 {
			rel = fmt.Sprintf("%s-%d", base, i)
		}
		if err := mrpack.ValidateRelPath(rel); err != nil {
			return "", fmt.Errorf("生成旧内容备份名失败: %w", err)
		}
		full, err := mrpack.SafeJoin(target, rel)
		if err != nil {
			return "", err
		}
		if _, err := os.Lstat(full); errors.Is(err, fs.ErrNotExist) {
			return rel, nil
		} else if err != nil {
			return "", fmt.Errorf("检查旧内容备份路径失败: %w", err)
		}
	}
	return "", fmt.Errorf("目标实例中同名旧内容副本过多: %s", top)
}

// mkdirAllSafe 逐级创建目录，并拒绝已存在的软链接或非目录组件。
func mkdirAllSafe(root, rel string) error {
	rel = strings.TrimSpace(rel)
	if rel == "" || rel == "." {
		return nil
	}
	parts := strings.Split(rel, "/")
	cur := root
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("目录路径非法: %q", rel)
		}
		next, err := mrpack.SafeJoin(cur, part)
		if err != nil {
			return fmt.Errorf("目录路径非法: %w", err)
		}
		info, err := os.Lstat(next)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Mkdir(next, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				return fmt.Errorf("创建目录 %s 失败: %w", next, err)
			}
			info, err = os.Lstat(next)
			if err != nil {
				return fmt.Errorf("检查目录 %s 失败: %w", next, err)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("目录路径不安全: %s", next)
			}
		case err != nil:
			return fmt.Errorf("检查目录 %s 失败: %w", next, err)
		default:
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("目录路径不安全: %s", next)
			}
		}
		cur = next
	}
	return nil
}

// inspectArchive 在修改目标目录前完成全部条目校验，包括元信息、路径穿越、
// 软链接/特殊文件、重复条目以及文件/目录前缀冲突。
func inspectArchive(zr *zip.ReadCloser) (Metadata, []archiveEntry, error) {
	var (
		meta      Metadata
		metaFound bool
		entries   []archiveEntry
		seen      = make(map[string]bool)
	)
	for _, f := range zr.File {
		if f.Name == MetadataFile {
			if metaFound {
				return meta, nil, fmt.Errorf("备份包包含多个 %s", MetadataFile)
			}
			raw, err := readZipMetadata(f)
			if err != nil {
				return meta, nil, err
			}
			if err := json.Unmarshal(raw, &meta); err != nil {
				return meta, nil, fmt.Errorf("备份元信息损坏: %w", err)
			}
			metaFound = true
			continue
		}

		name := f.Name
		isDir := strings.HasSuffix(name, "/") || f.FileInfo().IsDir()
		rel := strings.TrimSuffix(name, "/")
		if rel == "" {
			return meta, nil, fmt.Errorf("备份包包含空路径条目")
		}
		if err := mrpack.ValidateRelPath(rel); err != nil {
			return meta, nil, fmt.Errorf("备份条目路径不安全 %q: %w", name, err)
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return meta, nil, fmt.Errorf("备份条目是软链接，拒绝还原: %s", name)
		}
		if isDir {
			if f.Mode()&(os.ModeDevice|os.ModeNamedPipe|os.ModeSocket|os.ModeCharDevice|os.ModeIrregular) != 0 {
				return meta, nil, fmt.Errorf("备份条目不是普通目录: %s", name)
			}
		} else if f.Mode()&(os.ModeDevice|os.ModeNamedPipe|os.ModeSocket|os.ModeCharDevice|os.ModeIrregular) != 0 {
			return meta, nil, fmt.Errorf("备份条目不是普通文件: %s", name)
		}
		if !allowedRestorePath(rel, isDir) {
			return meta, nil, fmt.Errorf("备份条目不在可还原范围内: %s", name)
		}
		if seen[rel] {
			return meta, nil, fmt.Errorf("备份包包含重复路径: %s", rel)
		}
		seen[rel] = true
		entries = append(entries, archiveEntry{
			Entry: Entry{Rel: rel, Size: int64(f.UncompressedSize64), Dir: isDir, ModTime: f.Modified},
			File:  f,
		})
	}
	if !metaFound {
		return meta, nil, fmt.Errorf("不是 mtl 备份包：缺少 %s", MetadataFile)
	}
	if err := validateMetadata(meta); err != nil {
		return meta, nil, err
	}
	if err := validateInclude(meta.Include, entries); err != nil {
		return meta, nil, err
	}

	// 禁止“文件作为另一个条目的父目录”的恶意布局，避免解压时互相覆盖。
	fileSet := make(map[string]bool)
	for _, e := range entries {
		if !e.Dir {
			fileSet[e.Rel] = true
		}
	}
	for _, e := range entries {
		for parent := path.Dir(e.Rel); parent != "."; parent = path.Dir(parent) {
			if fileSet[parent] {
				return meta, nil, fmt.Errorf("备份包路径冲突: %s 与文件 %s 冲突", e.Rel, parent)
			}
		}
	}
	return meta, entries, nil
}

// validateInclude 确保元信息中展示的范围与 zip 内实际一级条目一致，
// 避免还原摘要与实际写入内容不符。
func validateInclude(include []string, entries []archiveEntry) error {
	want := make(map[string]bool, len(include))
	for _, name := range include {
		want[name] = true
	}
	got := make(map[string]bool)
	for _, e := range entries {
		got[strings.SplitN(e.Rel, "/", 2)[0]] = true
	}
	if len(want) != len(got) {
		return errors.New("backup.json 的 include 与备份内容不一致")
	}
	for name := range got {
		if !want[name] {
			return fmt.Errorf("backup.json 的 include 缺少 %q", name)
		}
	}
	return nil
}

func allowedRestorePath(rel string, isDir bool) bool {
	parts := strings.Split(rel, "/")
	if len(parts) > 1 {
		return isIncludedDir(parts[0])
	}
	name := parts[0]
	if isIncludedDir(name) {
		return isDir
	}
	if managedDirs[name] {
		return false
	}
	return !isDir
}
