package mrpack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// LockRelPath 是实例内锁定清单的相对路径。
	LockRelPath = ".mtl/pack-lock.json"
	// hashPrefix 让清单里的哈希带算法标识，便于以后扩展。
	hashPrefix   = "sha256-"
	maxLockBytes = 16 << 20
)

// Lock 记录上一次通过 mrpack 安装的托管文件。
// 只有整合包自动释放/下载的文件会写入 Files，用户手动新增或修改的文件不进入清单。
type Lock struct {
	// PackID 使用 mrpack 的 name；modrinth.index.json 没有独立的项目 id 字段。
	PackID      string            `json:"pack_id"`
	PackVersion string            `json:"pack_version"`
	Source      string            `json:"source,omitempty"`
	Files       map[string]string `json:"files"`
}

// LockPath 返回实例锁定清单的绝对路径。
func LockPath(dir string) (string, error) {
	return SafeJoin(dir, LockRelPath)
}

// LoadLock 读取实例锁定清单；文件不存在时返回 (nil, nil)。
// 清单内的路径和哈希会做严格校验，损坏的清单不会被用于清理。
func LoadLock(dir string) (*Lock, error) {
	path, err := LockPath(dir)
	if err != nil {
		return nil, fmt.Errorf("定位锁定清单失败: %w", err)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取锁定清单失败: %w", err)
	}
	if len(data) > maxLockBytes {
		return nil, fmt.Errorf("锁定清单超过 %d 字节上限", maxLockBytes)
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	var lock Lock
	if err := dec.Decode(&lock); err != nil {
		return nil, fmt.Errorf("解析锁定清单失败: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("锁定清单末尾存在多余数据")
	}
	if lock.Files == nil {
		lock.Files = map[string]string{}
	}
	for rel, sum := range lock.Files {
		if err := ValidateRelPath(rel); err != nil {
			return nil, fmt.Errorf("锁定清单路径 %q 非法: %w", rel, err)
		}
		if !validLockHash(sum) {
			return nil, fmt.Errorf("锁定清单文件 %q 的哈希非法: %q", rel, sum)
		}
	}
	return &lock, nil
}

// SaveLock 原子写入锁定清单，重复执行不会留下半截文件。
func SaveLock(dir string, lock *Lock) error {
	if lock == nil {
		return errors.New("锁定清单为空")
	}
	files := lock.Files
	if files == nil {
		files = map[string]string{}
	}
	for rel, sum := range files {
		if err := ValidateRelPath(rel); err != nil {
			return fmt.Errorf("锁定清单路径 %q 非法: %w", rel, err)
		}
		if !validLockHash(sum) {
			return fmt.Errorf("锁定清单文件 %q 的哈希非法: %q", rel, sum)
		}
	}

	data, err := json.MarshalIndent(&Lock{
		PackID:      lock.PackID,
		PackVersion: lock.PackVersion,
		Source:      lock.Source,
		Files:       files,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化锁定清单失败: %w", err)
	}
	data = append(data, '\n')

	path, err := LockPath(dir)
	if err != nil {
		return fmt.Errorf("定位锁定清单失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建锁定清单目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pack-lock-*")
	if err != nil {
		return fmt.Errorf("创建锁定清单临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入锁定清单失败: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("设置锁定清单权限失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭锁定清单失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("保存锁定清单失败: %w", err)
	}
	committed = true
	return nil
}

// ManagedFiles 返回当前整合包在实例内实际托管的文件：相对路径 -> "sha256-..."。
// 必须在下载与 overrides 解压完成后调用，这样同名文件能取到最终落盘内容。
// saves/ 与 .mtl/ 永远不纳入清单。
func (p *Pack) ManagedFiles(dir string) (map[string]string, error) {
	paths := make(map[string]struct{})
	for _, f := range p.Index.ClientFiles() {
		if IsProtectedPath(f.Path) {
			continue
		}
		paths[f.Path] = struct{}{}
	}
	overrides, err := p.OverridePaths()
	if err != nil {
		return nil, err
	}
	for _, rel := range overrides {
		if IsProtectedPath(rel) {
			continue
		}
		paths[rel] = struct{}{}
	}

	out := make(map[string]string, len(paths))
	for rel := range paths {
		full, err := SafeJoin(dir, rel)
		if err != nil {
			return nil, fmt.Errorf("托管文件 %q 路径非法: %w", rel, err)
		}
		sum, err := hashFile(full)
		if err != nil {
			return nil, fmt.Errorf("计算托管文件 %q 哈希失败: %w", rel, err)
		}
		out[rel] = sum
	}
	return out, nil
}

// OverridePaths 返回 overrides/ 与 client-overrides/ 中的文件相对路径。
// 同一路径允许在两个目录中同时出现（后者覆盖前者），但单个目录内重复会被拒绝。
func (p *Pack) OverridePaths() ([]string, error) {
	var out []string
	for _, prefix := range []string{"overrides", "client-overrides"} {
		seen := make(map[string]string)
		for _, f := range p.r.File {
			name := f.Name
			if name == prefix+"/" || !strings.HasPrefix(name, prefix+"/") {
				continue
			}
			rel := name[len(prefix)+1:]
			if rel == "" {
				continue
			}
			mode := f.Mode()
			if mode&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("拒绝符号链接条目 %q", name)
			}
			if f.FileInfo().IsDir() || strings.HasSuffix(name, "/") {
				continue
			}
			if !mode.IsRegular() {
				return nil, fmt.Errorf("拒绝特殊文件条目 %q", name)
			}
			if f.UncompressedSize64 > maxOverrideFileBytes {
				return nil, fmt.Errorf("文件 %q 超过单文件上限", name)
			}
			if err := ValidateRelPath(rel); err != nil {
				return nil, fmt.Errorf("非法文件 %q: %w", name, err)
			}
			key := strings.ToLower(rel)
			if prev, ok := seen[key]; ok {
				return nil, fmt.Errorf("%s 内路径重复（大小写不敏感）: %q 与 %q", prefix, prev, name)
			}
			seen[key] = name
			out = append(out, rel)
		}
	}
	return out, nil
}

// IsProtectedPath 判断路径是否属于永远不清理、不纳入锁定清单的用户数据。
// 目前保护 saves/（存档）与 .mtl/（实例元数据），大小写不敏感以兼容 Windows。
func IsProtectedPath(rel string) bool {
	first, _, _ := strings.Cut(rel, "/")
	switch strings.ToLower(first) {
	case "saves", ".mtl":
		return true
	default:
		return false
	}
}

// Diff 描述新旧托管文件清单的差异，路径均按字典序排列。
type Diff struct {
	Added   []string
	Updated []string
	Removed []string
}

// DiffFiles 对比新旧清单。Removed 只表示“新版不再声明”，
// 是否真正可删除还要经过 PlanPrune 的保护目录与用户修改检查。
func DiffFiles(oldFiles, newFiles map[string]string) Diff {
	d := Diff{}
	// 仅大小写不同的路径在 Windows 上指向同一文件，按同一文件比较，避免误报。
	oldFolded := make(map[string]string, len(oldFiles))
	for rel := range oldFiles {
		oldFolded[strings.ToLower(rel)] = rel
	}
	for rel, sum := range newFiles {
		if oldSum, ok := oldFiles[rel]; ok {
			if !strings.EqualFold(oldSum, sum) {
				d.Updated = append(d.Updated, rel)
			}
			continue
		}
		if oldRel, ok := oldFolded[strings.ToLower(rel)]; ok {
			if !strings.EqualFold(oldFiles[oldRel], sum) {
				d.Updated = append(d.Updated, rel)
			}
			continue
		}
		d.Added = append(d.Added, rel)
	}

	newFolded := make(map[string]struct{}, len(newFiles))
	for rel := range newFiles {
		newFolded[strings.ToLower(rel)] = struct{}{}
	}
	for rel := range oldFiles {
		if _, ok := newFiles[rel]; ok {
			continue
		}
		if _, ok := newFolded[strings.ToLower(rel)]; ok {
			continue
		}
		d.Removed = append(d.Removed, rel)
	}

	sort.Strings(d.Added)
	sort.Strings(d.Updated)
	sort.Strings(d.Removed)
	return d
}

// KeptFile 是因安全原因保留、不参与清理的旧托管文件。
type KeptFile struct {
	Path   string
	Reason string
}

// PrunePlan 是一次增量清理的计划结果。
type PrunePlan struct {
	Delete []string
	Keep   []KeptFile
}

// PlanPrune 从旧清单中筛选可以安全删除的托管文件：
// 新版不再声明、不在保护目录、当前仍是普通文件且内容仍与旧锁定哈希一致。
// 用户修改过的文件不会被删除，避免误伤手工改动。
func PlanPrune(dir string, old *Lock, desired map[string]string) PrunePlan {
	var plan PrunePlan
	if old == nil {
		return plan
	}
	diff := DiffFiles(old.Files, desired)
	for _, rel := range diff.Removed {
		if IsProtectedPath(rel) {
			plan.Keep = append(plan.Keep, KeptFile{Path: rel, Reason: "受保护路径"})
			continue
		}
		full, err := SafeJoin(dir, rel)
		if err != nil {
			plan.Keep = append(plan.Keep, KeptFile{Path: rel, Reason: "路径非法"})
			continue
		}
		info, err := os.Lstat(full)
		if errors.Is(err, fs.ErrNotExist) {
			// 已经不存在，无需删除也不算保留。
			continue
		}
		if err != nil {
			plan.Keep = append(plan.Keep, KeptFile{Path: rel, Reason: "无法访问"})
			continue
		}
		if !info.Mode().IsRegular() {
			plan.Keep = append(plan.Keep, KeptFile{Path: rel, Reason: "不是普通文件"})
			continue
		}
		sum, err := hashFile(full)
		if err != nil {
			plan.Keep = append(plan.Keep, KeptFile{Path: rel, Reason: "无法校验哈希"})
			continue
		}
		if !strings.EqualFold(sum, old.Files[rel]) {
			plan.Keep = append(plan.Keep, KeptFile{Path: rel, Reason: "已被用户修改"})
			continue
		}
		plan.Delete = append(plan.Delete, rel)
	}
	sort.Strings(plan.Delete)
	sort.Slice(plan.Keep, func(i, j int) bool { return plan.Keep[i].Path < plan.Keep[j].Path })
	return plan
}

// ApplyPrune 删除计划中的托管文件并返回删除数量；文件已不存在时静默跳过。
func ApplyPrune(dir string, files []string) (int, error) {
	deleted := 0
	for _, rel := range files {
		full, err := SafeJoin(dir, rel)
		if err != nil {
			return deleted, fmt.Errorf("待删除文件 %q 路径非法: %w", rel, err)
		}
		info, err := os.Lstat(full)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return deleted, fmt.Errorf("检查待删除文件 %q 失败: %w", rel, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if err := os.Remove(full); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return deleted, fmt.Errorf("删除托管文件 %q 失败: %w", rel, err)
		}
		deleted++
	}
	return deleted, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hashPrefix + hex.EncodeToString(h.Sum(nil)), nil
}

func validLockHash(s string) bool {
	if !strings.HasPrefix(s, hashPrefix) {
		return false
	}
	b, err := hex.DecodeString(strings.TrimPrefix(s, hashPrefix))
	return err == nil && len(b) == sha256.Size
}
