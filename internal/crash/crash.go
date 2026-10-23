// Package crash 归档 Minecraft 崩溃日志，并基于文本关键词做离线 Mod 冲突诊断。
// 不联网、不调用外部程序；所有读写失败都降级为告警，不阻断启动流程。
package crash

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mtl/internal/mrpack"
)

const (
	archiveDirName = "crash_reports"
	archivePrefix  = "crash-"
	archiveLayout  = "20060102T150405" // UTC 紧凑时间戳，例如 20261024T223015
)

// sourceDirRels 是相对实例根目录的崩溃日志扫描目录。
// 第一项是 Minecraft 标准路径；mtl 当前把实例目录直接作为游戏目录（--gameDir），
// 因此同时扫描实例根目录下的 crash-reports，保证真实崩溃日志能被发现。
var sourceDirRels = []string{
	".minecraft/crash-reports",
	"crash-reports",
}

// 诊断提示文案。保持稳定，便于测试和用户检索。
const (
	diagMixin           = "Mixin冲突：多个Mod抢占了同一个类的Mixin注入点，是Fabric最常见的启动崩溃原因"
	diagDupMod          = "检测到重复Mod：同一个Mod在mods文件夹内存在多份（mod ID 重复注册）"
	diagMissingDep      = "缺少Mod前置依赖：请按日志提示安装对应的依赖Mod"
	diagClassMissing    = "类缺失（NoClassDefFoundError）：大概率是Mod版本不匹配或前置依赖缺失"
	diagVersionMismatch = "Mod版本与当前Minecraft版本不兼容（Minecraft version mismatch）"
	diagRenderConflict  = "渲染类Mod互相冲突：检测到Oculus/Rubidium等渲染Mod报错，Sodium/Iris生态常见，请只保留一套渲染Mod"
	diagUnknown         = "未能识别明确的冲突类型，请直接打开原始崩溃日志文件排查"
)

// otherRenderMods 是 Sodium/Iris 之外的常见渲染类 Mod 关键词。
var otherRenderMods = []string{
	"oculus", "rubidium", "embeddium", "magnesium", "optifine", "optifabric", "canvas",
}

// DiagnoseCrashLog 按优先级扫描崩溃日志，返回所有命中的诊断提示。
// 多条规则可以同时命中；全部未命中时返回一条兜底提示。
func DiagnoseCrashLog(content string) []string {
	lower := strings.ToLower(content)
	var out []string

	if strings.Contains(lower, "mixin apply failed") || strings.Contains(lower, "mixin injection") {
		out = append(out, diagMixin)
	}
	if strings.Contains(lower, "mod id already registered") || strings.Contains(lower, "duplicate mod id") {
		out = append(out, diagDupMod)
	}
	if strings.Contains(lower, "requires mod") ||
		strings.Contains(lower, "dependency not found") ||
		strings.Contains(lower, "missing dependency") {
		out = append(out, diagMissingDep)
	}
	if hasModRelatedNoClassDefFound(lower) {
		out = append(out, diagClassMissing)
	}
	if strings.Contains(lower, "wrong minecraft version") || strings.Contains(lower, "minecraft version mismatch") {
		out = append(out, diagVersionMismatch)
	}
	for _, marker := range otherRenderMods {
		if strings.Contains(lower, marker) {
			out = append(out, diagRenderConflict)
			break
		}
	}

	if len(out) == 0 {
		out = append(out, diagUnknown)
	}
	return out
}

// hasModRelatedNoClassDefFound 检查 NoClassDefFoundError 附近是否存在 Mod 相关包名。
// 只取异常前后的窗口，避免把日志中无关的 Mod 列表误当成证据。
func hasModRelatedNoClassDefFound(lower string) bool {
	const marker = "noclassdeffounderror"
	const before, after = 500, 900
	for from := 0; ; {
		i := strings.Index(lower[from:], marker)
		if i < 0 {
			return false
		}
		i += from
		start := max(i-before, 0)
		end := min(i+len(marker)+after, len(lower))
		if containsModPackage(lower[start:end]) {
			return true
		}
		from = i + len(marker)
	}
}

// containsModPackage 判断文本中是否出现 Mod 相关标识（fabric/mixin/modid 或包名片段含 mod）。
func containsModPackage(text string) bool {
	if strings.Contains(text, "fabric") || strings.Contains(text, "mixin") ||
		strings.Contains(text, "modid") || strings.Contains(text, "mod id") {
		return true
	}
	// 按包名/路径分隔符切分，识别 mymod、modmenu、mods 等片段；
	// 排除 Java 自带 model/module/command 等常见误报词。
	segments := strings.FieldsFunc(text, func(r rune) bool {
		switch r {
		case '/', '.', ' ', '\t', '\r', '\n', ':', '"', '\'', '(', ')', ',', '[', ']', '{', '}':
			return true
		}
		return false
	})
	for _, seg := range segments {
		if !strings.Contains(seg, "mod") {
			continue
		}
		switch seg {
		case "model", "models", "module", "modules", "modular",
			"command", "commands", "modify", "modified", "modifier":
			continue
		}
		return true
	}
	return false
}

// FileState 记录崩溃日志在启动前的状态，用于识别本次运行新生成或追加的文件。
type FileState struct {
	ModTime time.Time
	Size    int64
}

// Snapshot 记录当前已有的崩溃日志；目录不存在或不可读时静默跳过。
type Snapshot map[string]FileState

func TakeSnapshot(instanceDir string) Snapshot {
	snap := make(Snapshot)
	for _, dir := range sourceDirs(instanceDir) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !isCrashLog(e.Name()) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			snap[filepath.Join(dir, e.Name())] = FileState{ModTime: info.ModTime(), Size: info.Size()}
		}
	}
	return snap
}

// Report 是一份本次归档的崩溃日志及其诊断结果。
type Report struct {
	Source    string
	Archived  string // 写入失败时为空
	Diagnoses []string
}

// Options 控制一次崩溃归档。
type Options struct {
	InstanceDir string
	// Baseline 是游戏启动前通过 TakeSnapshot 得到的快照。
	Baseline Snapshot
	// Now 仅用于测试注入固定时间；为空时使用 time.Now。
	Now func() time.Time
}

// EnsureArchiveDir 创建实例下的 crash_reports 目录。
func EnsureArchiveDir(instanceDir string) error {
	dir, err := mrpack.SafeJoin(instanceDir, archiveDirName)
	if err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o755)
}

// Collect 扫描本次新生成/变化的 crash-*.txt，逐份归档、诊断，并把诊断摘要写到归档文件顶部。
// 返回成功处理的报告和非致命告警；任何失败都不会中断启动流程。
func Collect(opts Options) ([]Report, []string) {
	if opts.InstanceDir == "" {
		return nil, nil
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	baseline := opts.Baseline
	if baseline == nil {
		baseline = Snapshot{}
	}

	archiveDir, err := mrpack.SafeJoin(opts.InstanceDir, archiveDirName)
	if err != nil {
		return nil, []string{fmt.Sprintf("解析归档目录失败: %v", err)}
	}

	var warnings []string
	var sources []string
	for _, dir := range sourceDirs(opts.InstanceDir) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				warnings = append(warnings, fmt.Sprintf("读取崩溃日志目录失败 %s: %v", dir, err))
			}
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !isCrashLog(e.Name()) {
				continue
			}
			full := filepath.Join(dir, e.Name())
			info, err := e.Info()
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("读取崩溃日志信息失败 %s: %v", full, err))
				continue
			}
			old, existed := baseline[full]
			if existed && old.Size == info.Size() && old.ModTime.Equal(info.ModTime()) {
				continue
			}
			sources = append(sources, full)
		}
	}
	sort.Strings(sources)
	if len(sources) == 0 {
		return nil, warnings
	}

	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		warnings = append(warnings, fmt.Sprintf("创建归档目录失败 %s: %v", archiveDir, err))
		return nil, warnings
	}

	var reports []Report
	used := make(map[string]bool)
	for _, src := range sources {
		data, err := os.ReadFile(src)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("读取崩溃日志失败 %s: %v", src, err))
			continue
		}
		content := string(data)
		diagnoses := DiagnoseCrashLog(content)
		stamp := now().UTC()

		name := archiveName(filepath.Base(src), stamp, archiveDir, used)
		dest, err := mrpack.SafeJoin(archiveDir, name)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("归档文件名非法 %s: %v", name, err))
			continue
		}
		used[name] = true

		if err := os.WriteFile(dest, withDiagnosisSummary(content, diagnoses, stamp), 0o644); err != nil {
			warnings = append(warnings, fmt.Sprintf("写入归档文件失败 %s: %v", dest, err))
			reports = append(reports, Report{Source: src, Diagnoses: diagnoses})
			continue
		}
		reports = append(reports, Report{Source: src, Archived: dest, Diagnoses: diagnoses})
	}
	return reports, warnings
}

func sourceDirs(instanceDir string) []string {
	seen := make(map[string]bool, len(sourceDirRels))
	var dirs []string
	for _, rel := range sourceDirRels {
		p, err := mrpack.SafeJoin(instanceDir, rel)
		if err != nil || seen[p] {
			continue
		}
		seen[p] = true
		dirs = append(dirs, p)
	}
	return dirs
}

func isCrashLog(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, archivePrefix) && strings.HasSuffix(lower, ".txt")
}

// archiveName 生成 crash-<UTC时间戳>-<原始名字>.txt，并避免覆盖已有文件。
// 原始名字去掉 crash- 前缀，避免出现 crash-<时间戳>-crash-... 的重复前缀。
func archiveName(srcBase string, stamp time.Time, archiveDir string, used map[string]bool) string {
	stem := strings.TrimSuffix(srcBase, filepath.Ext(srcBase))
	if len(stem) >= len(archivePrefix) && strings.EqualFold(stem[:len(archivePrefix)], archivePrefix) {
		stem = stem[len(archivePrefix):]
	}
	stem = sanitizeStem(stem)
	if stem == "" {
		stem = "original"
	}

	ts := stamp.Format(archiveLayout)
	name := fmt.Sprintf("%s%s-%s.txt", archivePrefix, ts, stem)
	for i := 2; used[name] || fileExists(filepath.Join(archiveDir, name)); i++ {
		name = fmt.Sprintf("%s%s-%s-%d.txt", archivePrefix, ts, stem, i)
	}
	return name
}

// sanitizeStem 只保留各平台通用的文件名字符，防止异常来源文件名带出非法路径。
func sanitizeStem(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), " ._")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// withDiagnosisSummary 在原始崩溃日志前追加诊断摘要，方便以后直接打开归档文件排查。
func withDiagnosisSummary(content string, diagnoses []string, stamp time.Time) []byte {
	var b strings.Builder
	b.WriteString("[Crash] ===== mtl 崩溃诊断摘要 =====\n")
	b.WriteString("[Crash] 生成时间(UTC): ")
	b.WriteString(stamp.Format(time.RFC3339))
	b.WriteString("\n")
	for _, d := range diagnoses {
		b.WriteString("[Crash] ")
		b.WriteString(d)
		b.WriteString("\n")
	}
	b.WriteString("[Crash] ===== 以下为原始崩溃日志 =====\n\n")
	b.WriteString(content)
	return []byte(b.String())
}
