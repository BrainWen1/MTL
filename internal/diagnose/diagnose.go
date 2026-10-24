package diagnose

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Severity 表示诊断问题的严重程度。
type Severity int

const (
	SeverityInfo Severity = iota
	SeverityWarning
	SeverityCritical
)

// String 返回中文严重程度，便于直接输出给普通用户。
func (s Severity) String() string {
	switch s {
	case SeverityCritical:
		return "严重"
	case SeverityWarning:
		return "警告"
	default:
		return "提示"
	}
}

// Finding 是一条可疑问题及其修复建议。
type Finding struct {
	Category   string
	Severity   Severity
	Title      string
	Detail     string
	Advice     []string
	Evidence   []string
	Confidence int
}

// Report 是一次诊断的完整结果。
type Report struct {
	InstanceName string
	InstanceDir  string
	LogFile      string
	CrashFile    string
	ExitCode     *int
	// Crashed 表示是否判定为崩溃/异常退出。
	Crashed bool
	// Reason 是退出原因预判，Confidence 为 0-100。
	Reason     string
	Confidence int
	Findings   []Finding
	// Highlights 是从崩溃报告与日志中提取的关键行。
	Highlights []string
	Notes      []string
}

// Input 是 Analyze 的输入；Log/Crash 为文本内容，路径仅用于报告展示。
type Input struct {
	InstanceName string
	InstanceDir  string
	LogFile      string
	CrashFile    string
	Log          string
	Crash        string
	ExitCode     *int
	// CrashStale 表示崩溃报告早于最近一次运行，只保留路径展示，不参与关键词匹配，
	// 避免旧 OOM / Mod 报错把正常退出误判为崩溃。
	CrashStale bool
}

type rule struct {
	category   string
	markers    []string
	severity   Severity
	title      string
	detail     string
	advice     []string
	confidence int
}

// rules 按优先级排列；同一类别只保留第一条命中，避免重复提示刷屏。
var rules = []rule{
	{
		category: "oom",
		markers: []string{
			"java.lang.outofmemoryerror: java heap space",
			"outofmemoryerror: java heap space",
			"java heap space",
			"outofmemoryerror",
		},
		severity: SeverityCritical,
		title:    "内存不足（OutOfMemoryError）",
		detail:   "游戏分配给 Java 的内存已经用完，进程无法继续运行。",
		advice: []string{
			"提高最大内存，例如：mtl config set-memory manual --value 6G --dir <实例目录>（大型整合包建议 6G～8G）",
			"启动前关闭浏览器、其他游戏等占用内存的程序",
			"检查 --jvm-arg 是否覆盖了 -Xmx，或手动内存是否设置得过小",
		},
		confidence: 95,
	},
	{
		category: "mod_resolution",
		markers: []string{
			"modresolutionexception",
			"incompatible mod set",
			"mod resolution failed",
			"mod resolution encountered",
			"incompatible mods found",
		},
		severity: SeverityCritical,
		title:    "Mod 依赖关系冲突",
		detail:   "加载器无法同时满足当前 Mod 的依赖、版本或互斥要求。",
		advice: []string{
			"查看日志中列出的 Mod 名称，按提示补齐前置或删除冲突 Mod",
			"运行 mtl mod list --dir <实例目录> 检查 Mod 版本与依赖",
			"暂时禁用最近新增或更新的 Mod，确认能否启动",
		},
		confidence: 88,
	},
	{
		category: "mod_parse",
		markers: []string{
			"mod parse error",
			"invalid mod file",
			"unable to parse mod",
			"failed to parse mod",
			"not a valid mod file",
		},
		severity: SeverityCritical,
		title:    "Mod 文件损坏或格式不兼容",
		detail:   "加载器无法读取某个 Mod 的描述文件，通常意味着文件损坏或加载器类型不匹配。",
		advice: []string{
			"重新下载日志中提到的 Mod 文件",
			"确认 Fabric Mod 没有放进 Forge 实例（或相反）",
			"用 mtl mod list --dir <实例目录> --debug 查看无法识别的 Mod",
		},
		confidence: 85,
	},
	{
		category: "mod_duplicate",
		markers: []string{
			"duplicate mod id",
			"mod id already registered",
			"duplicate mod",
			"is already registered",
		},
		severity: SeverityCritical,
		title:    "存在重复 Mod",
		detail:   "同一个 Mod 在 mods 目录中存在多份，导致 ID 重复注册。",
		advice: []string{
			"在 mods 目录中只保留一份，删除多余副本（注意名称不同但 ID 相同的文件）",
			"用 mtl mod list --dir <实例目录> 查看重复的 Mod ID",
		},
		confidence: 90,
	},
	{
		category: "mod_dependency",
		markers: []string{
			"requires mod",
			"dependency not found",
			"missing dependency",
			"requires any version",
			"missing or unsupported mandatory dependencies",
		},
		severity: SeverityCritical,
		title:    "缺少 Mod 前置依赖",
		detail:   "某个 Mod 需要的前置 Mod 没有安装，或版本不满足要求。",
		advice: []string{
			"按日志提示安装对应的前置 Mod（例如 Fabric API、Cloth Config 等）",
			"检查前置 Mod 的版本是否与当前 Minecraft / 加载器版本匹配",
			"mtl mod list --dir <实例目录> 可查看已安装 Mod 及其依赖",
		},
		confidence: 88,
	},
	{
		category: "mod_version",
		markers: []string{
			"wrong minecraft version",
			"minecraft version mismatch",
			"not compatible with minecraft",
			"requires minecraft",
		},
		severity: SeverityCritical,
		title:    "Mod 与 Minecraft 版本不兼容",
		detail:   "Mod 声明支持的 Minecraft 版本与当前实例不一致。",
		advice: []string{
			"下载与当前 Minecraft 版本对应的 Mod",
			"确认加载器类型（Fabric / Forge）和版本也匹配",
			"升级整合包通常能一次性解决版本不一致",
		},
		confidence: 85,
	},
	{
		category: "mixin",
		markers: []string{
			"mixin apply failed",
			"mixin injection",
			"mixin transformation",
			"mixin apply",
		},
		severity: SeverityCritical,
		title:    "Mixin 注入冲突",
		detail:   "多个 Mod 修改了同一个游戏类，是 Fabric 最常见的启动崩溃原因。",
		advice: []string{
			"禁用最近新增的 Mod，逐个排查是否恢复",
			"避免同时安装功能重复的优化/渲染 Mod",
			"将相关 Mod 更新到支持当前游戏版本的最新版",
		},
		confidence: 82,
	},
	{
		category: "java_version",
		markers: []string{
			"unsupportedclassversionerror",
			"compiled by a more recent version",
			"低于版本要求",
			"java version mismatch",
		},
		severity: SeverityCritical,
		title:    "Java 版本不匹配",
		detail:   "当前 Java 版本低于游戏或加载器要求（例如 1.20.5+ 需要 Java 21）。",
		advice: []string{
			"让 mtl 自动选择运行时：执行 mtl config set-java <实例目录> 清空实例绑定",
			"或用 --java 指定正确的 java 可执行文件",
			"用 mtl java list 查看已缓存的运行时，mtl java use <runtime-id> 设置全局默认",
		},
		confidence: 90,
	},
	{
		category: "java_path",
		markers: []string{
			"指定的java不存在",
			"指定的java不可执行",
			"未找到系统 path 中的 java",
			"java -version 失败",
		},
		severity: SeverityCritical,
		title:    "Java 路径不可用",
		detail:   "配置或命令行指定的 Java 可执行文件不存在、不可执行或无法启动。",
		advice: []string{
			"执行 mtl config set-java <实例目录> 清空失效路径，让 mtl 自动匹配",
			"或执行 mtl config set-java <实例目录> <新的java路径> 重新绑定",
			"用 mtl java list 检查全局缓存的 Java 运行时",
		},
		confidence: 92,
	},
	{
		category: "jvm_args",
		markers: []string{
			"invalid maximum heap size",
			"unrecognized vm option",
			"could not create the java virtual machine",
			"error occurred during initialization of vm",
		},
		severity: SeverityCritical,
		title:    "JVM 参数或内存设置无效",
		detail:   "Java 无法识别自定义 JVM 参数，或 -Xmx 等内存参数格式不正确。",
		advice: []string{
			"执行 mtl config show --dir <实例目录> 检查 jvm_args 与手动内存",
			"删除无效的 --jvm-arg 或改用合法的参数格式",
			"内存值请写成 4G、512M 这样的格式",
		},
		confidence: 85,
	},
	{
		category: "missing_library",
		markers: []string{
			"no lwjgl in java.library.path",
			"unsatisfiedlinkerror",
			"failed to locate library",
			"couldn't load library",
		},
		severity: SeverityCritical,
		title:    "本地库（native）缺失或加载失败",
		detail:   "游戏启动所需的本地库无法加载，常见于文件缺失、系统位数或架构不匹配。",
		advice: []string{
			"重新执行 mtl install 补齐 libraries 与 native 文件",
			"确认使用的 Java 与系统架构一致（64 位系统请用 64 位 Java）",
			"删除损坏的 libraries 文件后重新安装",
		},
		confidence: 80,
	},
	{
		category: "assets",
		markers: []string{
			"failed to download asset",
			"unable to download asset",
			"missing asset",
			"下载 assets 资源失败",
			"下载 assetindex",
		},
		severity: SeverityCritical,
		title:    "assets 资源缺失或下载失败",
		detail:   "游戏资源文件没有完整下载，可能是网络中断或镜像源异常。",
		advice: []string{
			"重新执行 mtl run，已下载的文件会自动跳过并续传剩余部分",
			"检查网络，或通过 --mirror / mtl config set-mirror assets <地址> 更换镜像",
			"确认 %APPDATA%/mtl/cache/assets（Linux/macOS 为 ~/.cache/mtl/assets）可写",
		},
		confidence: 82,
	},
	{
		category: "missing_class",
		markers: []string{
			"noclassdeffounderror",
			"classnotfoundexception",
			"could not find or load main class",
		},
		severity: SeverityCritical,
		title:    "类缺失（NoClassDefFoundError / ClassNotFoundException）",
		detail:   "运行期缺少某个类，通常是 Mod 版本不匹配、前置缺失或启动文件损坏。",
		advice: []string{
			"检查日志中缺失的类属于哪个 Mod，更新或补装对应 Mod",
			"确认 Mod 与 Minecraft、加载器版本匹配",
			"若发生在原版类上，可重新执行 mtl install 补齐 libraries",
		},
		confidence: 80,
	},
}

var exitCodePattern = regexp.MustCompile(`(?i)(?:退出码|exit code)[^0-9\-\r\n]*(-?\d+)`)

// Analyze 对一份运行日志与崩溃报告做离线诊断。输入为空时返回友好提示，不会 panic。
func Analyze(in Input) Report {
	in.Crash = StripArchiveSummary(in.Crash)
	rep := Report{
		InstanceName: in.InstanceName,
		InstanceDir:  in.InstanceDir,
		LogFile:      in.LogFile,
		CrashFile:    in.CrashFile,
	}
	if in.CrashStale {
		in.Crash = ""
	}

	hasLog := strings.TrimSpace(in.Log) != ""
	hasCrash := strings.TrimSpace(in.Crash) != ""
	switch {
	case in.CrashStale && hasLog:
		rep.Notes = append(rep.Notes, "最近的崩溃报告早于本次运行日志，已忽略其内容（仅展示路径）")
	case in.CrashStale && !hasLog:
		rep.Notes = append(rep.Notes, "未找到运行日志，最近的崩溃报告也早于本次运行，无法判断当前状态")
	case !hasLog && !hasCrash:
		rep.Notes = append(rep.Notes, "未找到运行日志或崩溃报告，请先执行一次 mtl run，或确认实例目录是否正确")
	case !hasLog && hasCrash:
		rep.Notes = append(rep.Notes, "未找到 mtl 运行日志，本次仅根据崩溃报告分析")
	case hasLog && !hasCrash:
		rep.Notes = append(rep.Notes, "未找到崩溃报告，本次仅根据运行日志分析")
	}

	combined := in.Crash + "\n" + in.Log
	lower := strings.ToLower(combined)
	findings := matchRules(lower, combined)

	exit := in.ExitCode
	if exit == nil {
		exit = ParseExitCode(combined)
	}
	rep.ExitCode = exit

	if hasCrash && !hasCritical(findings) {
		findings = append(findings, Finding{
			Category: "crash_report",
			Severity: SeverityCritical,
			Title:    "检测到 Minecraft 崩溃报告",
			Detail:   "游戏生成了崩溃报告，但没有命中常见关键词，需要结合原始堆栈排查。",
			Advice: []string{
				"打开崩溃报告或运行日志，查看第一条 Exception / Caused by 的完整堆栈",
				"暂时禁用最近新增的 Mod 后重试",
				"把完整崩溃报告反馈给 Mod 或整合包作者",
			},
			Confidence: 70,
		})
	}

	rep.Crashed, rep.Reason, rep.Confidence = classify(exit, lower, hasLog || hasCrash, hasCrash, findings)
	if rep.Crashed && len(findings) == 0 {
		findings = append(findings, exitFinding(exit))
	}
	rep.Findings = findings
	rep.Highlights = extractHighlights(in.Crash, in.Log)
	return rep
}

// matchRules 返回命中的诊断规则；同类规则只保留第一条证据最充分的结果。
func matchRules(lower, original string) []Finding {
	var out []Finding
	index := make(map[string]int)
	for _, r := range rules {
		if !containsAny(lower, r.markers) {
			continue
		}
		title := r.title
		if r.category == "oom" && strings.Contains(lower, "heap space") {
			title = "内存不足（Java heap space）"
		}
		f := Finding{
			Category:   r.category,
			Severity:   r.severity,
			Title:      title,
			Detail:     r.detail,
			Advice:     append([]string(nil), r.advice...),
			Evidence:   evidenceFor(original, r.markers),
			Confidence: r.confidence,
		}
		if i, ok := index[r.category]; ok {
			if f.Confidence > out[i].Confidence {
				out[i].Confidence = f.Confidence
			}
			out[i].Evidence = mergeEvidence(out[i].Evidence, f.Evidence)
			continue
		}
		index[r.category] = len(out)
		out = append(out, f)
	}
	return out
}

func containsAny(lower string, markers []string) bool {
	for _, m := range markers {
		if m != "" && strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// evidenceFor 提取包含关键词的原始行，每条问题最多保留 3 行，供用户快速定位。
func evidenceFor(text string, markers []string) []string {
	if text == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if !containsAny(lower, markers) {
			continue
		}
		if containsLine(out, line) {
			continue
		}
		out = append(out, truncateLine(line, 300))
		if len(out) >= 3 {
			break
		}
	}
	return out
}

func mergeEvidence(dst, src []string) []string {
	for _, line := range src {
		if !containsLine(dst, line) && len(dst) < 3 {
			dst = append(dst, line)
		}
	}
	return dst
}

func containsLine(lines []string, want string) bool {
	return slices.Contains(lines, want)
}

func hasCritical(findings []Finding) bool {
	for _, f := range findings {
		if f.Severity == SeverityCritical {
			return true
		}
	}
	return false
}

func firstCritical(findings []Finding) *Finding {
	for i := range findings {
		if findings[i].Severity == SeverityCritical {
			return &findings[i]
		}
	}
	return nil
}

// classify 预判退出原因。手动关闭（正常退出码或收到退出信号）不视为崩溃；
// 关键词证据优先于退出码，避免把 OOM 误判为普通异常退出。
func classify(exit *int, lower string, hasContent, hasCrashReport bool, findings []Finding) (bool, string, int) {
	normalExit := strings.Contains(lower, "游戏进程正常退出")
	manualSignal := strings.Contains(lower, "收到退出信号")
	forced := strings.Contains(lower, "游戏进程被强制结束")
	critical := firstCritical(findings)

	if critical != nil && (exit == nil || *exit != 0 || hasCrashReport) {
		return true, critical.Title, critical.Confidence
	}
	if hasCrashReport {
		return true, "检测到 Minecraft 崩溃报告", 80
	}
	if forced {
		return false, "游戏进程被强制结束（手动关闭或超时，不是游戏崩溃）", 75
	}
	// 没有崩溃证据时，收到退出信号即视为用户手动关闭，兼容 Windows 下
	// 转发 Ctrl+C 后子进程以非 130/143 退出码结束的情况。
	if manualSignal && critical == nil && !hasCrashReport {
		return false, "用户手动结束游戏", 80
	}
	if normalExit {
		return false, "游戏进程正常退出", 95
	}
	if exit == nil {
		if !hasContent {
			return false, "没有可诊断的运行记录", 0
		}
		return false, "未找到退出码，无法确定是否崩溃", 30
	}
	if *exit == 0 {
		return false, "游戏进程以退出码 0 结束", 85
	}
	return true, exitReason(*exit), 60
}

func exitReason(code int) string {
	switch code {
	case -1:
		return "游戏进程被强制结束"
	case 130, 143:
		return fmt.Sprintf("游戏被系统信号结束（退出码 %d）", code)
	case 134:
		return "Java 进程崩溃（SIGABRT，退出码 134）"
	case 1:
		return "游戏异常退出（退出码 1）"
	case 3221225477, -1073741819:
		return "Windows 原生崩溃（0xC0000005 访问冲突）"
	case 3221225725, -1073741571:
		return "Java 栈溢出（0xC00000FD）"
	case 3221226505, -1073740791:
		return "Windows 原生崩溃（0xC0000409）"
	default:
		return fmt.Sprintf("游戏异常退出（退出码 %d）", code)
	}
}

// exitFinding 在没有其他可疑问题时，把非零退出码转成一条通用建议。
func exitFinding(exit *int) Finding {
	if exit == nil {
		return Finding{
			Category:   "unknown_crash",
			Severity:   SeverityCritical,
			Title:      "未识别的启动异常",
			Detail:     "没有命中已知崩溃关键词，也没有可用的退出码。",
			Advice:     []string{"查看日志末尾的 Exception / Caused by 堆栈", "暂时禁用最近新增的 Mod 后重试"},
			Confidence: 40,
		}
	}
	code := *exit
	f := Finding{
		Category:   "exit_code",
		Severity:   SeverityCritical,
		Title:      exitReason(code),
		Detail:     fmt.Sprintf("Java 子进程以非零退出码 %d 结束，但没有命中常见的 Mod / Java / 内存关键词。", code),
		Confidence: 55,
	}
	switch code {
	case -1:
		f.Severity = SeverityWarning
		f.Advice = []string{"如果是手动关闭启动器，可忽略；否则查看日志末尾是否还有 Java 报错"}
	case 130, 143:
		f.Advice = []string{"通常由手动关闭或系统终止导致；若游戏没有正常退出，请查看日志末尾"}
	case 3221225477, -1073741819:
		f.Title = "Windows 原生崩溃（0xC0000005 访问冲突）"
		f.Detail = "Java 进程在 Windows 上因访问冲突退出，常见于显卡驱动、native 库或 Mod 问题。"
		f.Advice = []string{
			"更新显卡驱动，或尝试关闭高性能显卡调度后重试",
			"暂时禁用最近新增的 Mod / 光影 / 优化 Mod",
			"确认 Java 与系统位数一致，并尝试重新安装实例",
		}
	case 3221225725, -1073741571:
		f.Advice = []string{"检查 Mod 是否造成无限递归；尝试增大线程栈或禁用最近新增的 Mod"}
	case 134:
		f.Advice = []string{"检查日志末尾的 native 崩溃信息，更新显卡驱动或排查最近新增的 Mod"}
	default:
		f.Advice = []string{
			"查看日志末尾的 Exception / Caused by 堆栈",
			"暂时禁用最近新增的 Mod，或用 mtl mod list 检查重复与依赖",
			"把完整日志反馈给整合包或 Mod 作者",
		}
	}
	return f
}

// ParseExitCode 从日志中解析最后一次出现的退出码；识别到正常退出时返回 0。
func ParseExitCode(text string) *int {
	matches := exitCodePattern.FindAllStringSubmatch(text, -1)
	if len(matches) > 0 {
		last := matches[len(matches)-1][1]
		if code, err := strconv.Atoi(last); err == nil {
			return &code
		}
	}
	if strings.Contains(text, "游戏进程正常退出") {
		code := 0
		return &code
	}
	return nil
}

var highlightMarkers = []string{
	"caused by",
	"exception",
	"error",
	"fatal",
	"crash report",
	"outofmemory",
	"modresolution",
	"mixin",
	"at net.fabricmc",
	"at net.minecraft",
	"unable to launch",
	"failed to",
	"exit code",
	"退出码",
	"崩溃",
}

// extractHighlights 优先从崩溃报告、其次从运行日志提取关键堆栈行，最多 15 行。
func extractHighlights(crash, log string) []string {
	var out []string
	for _, text := range []string{crash, log} {
		if strings.TrimSpace(text) == "" {
			continue
		}
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
			if line == "" || strings.HasPrefix(line, "[Diagnose]") {
				continue
			}
			if !containsAny(strings.ToLower(line), highlightMarkers) {
				continue
			}
			line = truncateLine(line, 300)
			if containsLine(out, line) {
				continue
			}
			out = append(out, line)
			if len(out) >= 15 {
				return out
			}
		}
	}
	return out
}

// truncateLine 按字符数截断过长日志行，避免终端刷屏。
func truncateLine(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}
