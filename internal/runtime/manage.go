package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"time"

	"mtl/internal/mrpack"
)

// javaVersionTimeout 是 `java -version` 验证的超时时间，避免损坏的二进制卡住 CLI。
const javaVersionTimeout = 15 * time.Second

// Installed 描述全局 runtime 缓存目录中的一个 Java 运行时。
type Installed struct {
	// ID 是运行时标识（缓存目录名，通常为 Mojang component，如 java-runtime-gamma）。
	ID string
	// Dir 是运行时目录绝对路径。
	Dir string
	// Version 是官方运行时版本号，例如 21.0.7；标记缺失时为空。
	Version string
	// Platform 是 Mojang 平台键，例如 windows-x64。
	Platform string
	// OS / Arch 是平台键的可读拆分，用于列表展示。
	OS   string
	Arch string
	// JavaPath 是 java 可执行文件绝对路径。
	JavaPath string
	// Valid 表示标记文件有效且 java 文件通过存在性/权限/sha1 校验。
	Valid bool
	// SizeBytes 是运行时目录内普通文件总大小；仅在 Prune 删除前统计并填写。
	SizeBytes int64
	// Err 记录校验失败原因，Valid=true 时为空。
	Err string
}

// List 扫描指定 runtime 缓存目录（dir 为空时使用 DefaultDir）。
// 目录不存在时返回空列表；隐藏目录（如 .staging-*）与 manifests/ 不是运行时，会被忽略。
// 没有标记文件但包含 bin/java(.exe) 的目录也会列出，并标记为异常，便于排查和清理。
func List(dir string) ([]Installed, error) {
	root, err := cacheDir(dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 Java 运行时缓存目录 %s 失败: %w", root, err)
	}

	var out []Installed
	for _, de := range entries {
		if !de.IsDir() {
			continue
		}
		id := de.Name()
		if strings.HasPrefix(id, ".") || strings.EqualFold(id, "manifests") {
			continue
		}
		inst, ok := inspectInstalled(root, id)
		if !ok {
			continue
		}
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].ID) < strings.ToLower(out[j].ID)
	})
	return out, nil
}

// Find 按运行时标识查找缓存目录，标识比较忽略大小写。
// 未找到或标识非法时返回错误；返回的 Installed 可能 Valid=false，由调用方决定是否接受。
func Find(dir, id string) (Installed, error) {
	id = strings.TrimSpace(id)
	if err := validateRuntimeID(id); err != nil {
		return Installed{}, err
	}
	list, err := List(dir)
	if err != nil {
		return Installed{}, err
	}
	for _, inst := range list {
		if inst.ID == id {
			return inst, nil
		}
	}
	var folded []Installed
	for _, inst := range list {
		if strings.EqualFold(inst.ID, id) {
			folded = append(folded, inst)
		}
	}
	if len(folded) == 1 {
		return folded[0], nil
	}
	if len(folded) > 1 {
		return Installed{}, fmt.Errorf("标识 %q 匹配到多个 Java 运行时，请使用完整目录名", id)
	}
	return Installed{}, fmt.Errorf("未找到 Java 运行时 %q（可执行 mtl java list 查看已缓存运行时）", id)
}

// javaVersionRunner 允许测试替换实际执行逻辑，默认执行 java -version。
var javaVersionRunner = runJavaVersion

// VerifyExecutable 实际执行 `java -version`，确认运行时能够正常启动。
// 在文件存在性与执行权限检查之外，额外捕获动态库缺失、二进制损坏等问题。
func VerifyExecutable(ctx context.Context, javaPath string) error {
	info, err := os.Lstat(javaPath)
	if err != nil {
		return fmt.Errorf("java 可执行文件不存在: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("java 路径不是普通文件: %s", javaPath)
	}
	if goruntime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("java 文件缺少执行权限: %s", javaPath)
	}
	return javaVersionRunner(ctx, javaPath)
}

func runJavaVersion(ctx context.Context, javaPath string) error {
	cctx, cancel := context.WithTimeout(ctx, javaVersionTimeout)
	defer cancel()

	var out bytes.Buffer
	cmd := exec.CommandContext(cctx, javaPath, "-version")
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("java -version 超时（%s）", javaVersionTimeout)
		}
		if msg := strings.TrimSpace(out.String()); msg != "" {
			return fmt.Errorf("java -version 执行失败: %w（输出: %s）", err, truncateText(msg, 300))
		}
		return fmt.Errorf("java -version 执行失败: %w", err)
	}
	return nil
}

// KeptRuntime 是因被引用而跳过的运行时。
type KeptRuntime struct {
	ID     string
	Reason string
}

// FailedRuntime 是清理过程中失败的运行时；其他运行时仍会继续处理。
type FailedRuntime struct {
	ID  string
	Err error
}

// PruneOptions 控制安全清理行为。
type PruneOptions struct {
	// Dir 为空时使用 DefaultDir。
	Dir string
	// Keep 是必须保留的运行时：ID -> 原因（例如“实例 demo”）。
	Keep map[string]string
}

// PruneResult 汇总一次清理的结果。
type PruneResult struct {
	Removed    []Installed
	Kept       []KeptRuntime
	Failed     []FailedRuntime
	FreedBytes int64
}

// Prune 删除没有被引用（Keep 未包含）的 Java 运行时并统计释放空间。
// 只处理普通目录，绝不跟随符号链接；manifests/ 与隐藏目录不在清理范围内。
// 单个运行时删除失败不会中断其余清理，失败项通过 Failed 返回。
func Prune(opts PruneOptions) (PruneResult, error) {
	root, err := cacheDir(opts.Dir)
	if err != nil {
		return PruneResult{}, err
	}
	installed, err := List(root)
	if err != nil {
		return PruneResult{}, err
	}
	keep := make(map[string]string, len(opts.Keep))
	for id, reason := range opts.Keep {
		keep[strings.ToLower(strings.TrimSpace(id))] = reason
	}

	var res PruneResult
	for _, inst := range installed {
		if reason, ok := keep[strings.ToLower(inst.ID)]; ok {
			res.Kept = append(res.Kept, KeptRuntime{ID: inst.ID, Reason: reason})
			continue
		}
		full, err := mrpack.SafeJoin(root, inst.ID)
		if err != nil {
			res.Failed = append(res.Failed, FailedRuntime{ID: inst.ID, Err: err})
			continue
		}
		info, err := os.Lstat(full)
		if err != nil {
			res.Failed = append(res.Failed, FailedRuntime{ID: inst.ID, Err: err})
			continue
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			res.Failed = append(res.Failed, FailedRuntime{ID: inst.ID, Err: errors.New("不是普通目录，拒绝删除")})
			continue
		}
		size, err := dirSize(full)
		if err != nil {
			res.Failed = append(res.Failed, FailedRuntime{ID: inst.ID, Err: fmt.Errorf("统计目录大小失败: %w", err)})
			continue
		}
		if err := os.RemoveAll(full); err != nil {
			res.Failed = append(res.Failed, FailedRuntime{ID: inst.ID, Err: fmt.Errorf("删除失败: %w", err)})
			continue
		}
		inst.SizeBytes = size
		res.Removed = append(res.Removed, inst)
		res.FreedBytes += size
	}
	return res, nil
}

// inspectInstalled 读取单个缓存目录。返回 false 表示目录既没有有效标记，
// 也找不到标准 java 可执行文件，不应视为 Java 运行时。
func inspectInstalled(root, id string) (Installed, bool) {
	full, err := mrpack.SafeJoin(root, id)
	if err != nil {
		return Installed{ID: id, Dir: filepath.Join(root, id), Err: "目录名非法: " + err.Error()}, true
	}
	inst := Installed{ID: id, Dir: full}

	m, ok := readMarkerAny(full)
	if !ok {
		// 兼容旧缓存或手动放置的运行时：没有标记时仍尝试展示标准 java 路径。
		for _, rel := range []string{"bin/java.exe", "bin/java"} {
			p, joinErr := mrpack.SafeJoin(full, rel)
			if joinErr != nil {
				continue
			}
			if info, statErr := os.Lstat(p); statErr == nil && info.Mode().IsRegular() {
				inst.JavaPath = p
				break
			}
		}
		if inst.JavaPath == "" {
			return Installed{}, false
		}
		inst.Err = "缺少或损坏 .mtl-runtime.json"
		return inst, true
	}

	inst.Version = m.Version
	inst.Platform = m.Platform
	inst.OS, inst.Arch = describePlatform(m.Platform)
	if p, err := mrpack.SafeJoin(full, m.Java); err == nil {
		inst.JavaPath = p
	}
	switch {
	case m.Component != id:
		inst.Err = fmt.Sprintf("标记组件 %q 与目录名不一致", m.Component)
	case inst.JavaPath == "":
		inst.Err = "标记中的 java 路径非法"
	case !validJavaFile(inst.JavaPath, m.JavaSHA1):
		inst.Err = "java 可执行文件缺失、权限不足或 sha1 不匹配"
	default:
		inst.Valid = true
	}
	return inst, true
}

// readMarkerAny 读取标记文件但不校验 component/platform 是否与目录匹配。
// 标记结构损坏或关键字段缺失时返回 false。
func readMarkerAny(dir string) (marker, bool) {
	p := filepath.Join(dir, markerName)
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return marker{}, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return marker{}, false
	}
	var m marker
	if err := json.Unmarshal(data, &m); err != nil {
		return marker{}, false
	}
	if m.Component == "" || m.Platform == "" || m.Java == "" || !validSHA1(m.JavaSHA1) {
		return marker{}, false
	}
	return m, true
}

// describePlatform 把 Mojang 平台键拆成可读的操作系统与架构。
func describePlatform(platform string) (osName, arch string) {
	switch platform {
	case "windows-x64", "windows-x86", "windows-arm64":
		return "Windows", strings.TrimPrefix(platform, "windows-")
	case "linux":
		return "Linux", "x64"
	case "linux-i386":
		return "Linux", "x86"
	case "mac-os":
		return "macOS", "x64"
	case "mac-os-arm64":
		return "macOS", "arm64"
	case "":
		return "未知", "未知"
	default:
		// 未知平台键退化为原样展示，便于后续扩展。
		if osName, arch, ok := strings.Cut(platform, "-"); ok {
			return osName, arch
		}
		return platform, "未知"
	}
}

// cacheDir 解析运行时缓存目录，空字符串使用平台默认位置。
func cacheDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return DefaultDir()
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("解析 runtime 缓存目录失败: %w", err)
	}
	return abs, nil
}

// validateRuntimeID 只接受单层目录名，防止运行时标识被用作路径穿越。
func validateRuntimeID(id string) error {
	if id == "" {
		return errors.New("Java 运行时标识不能为空")
	}
	if err := mrpack.ValidateRelPath(id); err != nil {
		return fmt.Errorf("Java 运行时标识 %q 非法: %w", id, err)
	}
	if strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("Java 运行时标识 %q 必须是单层目录名", id)
	}
	return nil
}

// dirSize 统计目录内普通文件总大小，不跟随符号链接。
func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

// truncateText 截断过长的命令输出，避免错误信息刷屏。
func truncateText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}
