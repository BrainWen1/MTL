package diagnose

import (
	"strings"
	"testing"
)

func intPtr(v int) *int { return &v }

func hasCategory(findings []Finding, category string) bool {
	for _, f := range findings {
		if f.Category == category {
			return true
		}
	}
	return false
}

func TestAnalyzeRules(t *testing.T) {
	tests := []struct {
		name     string
		log      string
		crash    string
		exit     *int
		category string
		crashed  bool
		reason   string
	}{
		{
			name:     "OOM heap space",
			log:      "[process] 子进程退出码: 1\njava.lang.OutOfMemoryError: Java heap space",
			exit:     intPtr(1),
			category: "oom",
			crashed:  true,
			reason:   "内存不足（Java heap space）",
		},
		{
			name:     "duplicate mod",
			log:      "java.lang.IllegalStateException: Mod ID already registered: sodium",
			exit:     intPtr(1),
			category: "mod_duplicate",
			crashed:  true,
		},
		{
			name:     "mod parse error",
			log:      "[main/ERROR] Mod parse error: broken-mod.jar",
			exit:     intPtr(1),
			category: "mod_parse",
			crashed:  true,
		},
		{
			name:     "missing dependency",
			log:      "Mod example requires mod fabric-api, which is missing!",
			exit:     intPtr(1),
			category: "mod_dependency",
			crashed:  true,
		},
		{
			name:     "minecraft version mismatch",
			log:      "Minecraft version mismatch: expected 1.20.1",
			exit:     intPtr(1),
			category: "mod_version",
			crashed:  true,
		},
		{
			name:     "mixin conflict",
			log:      "Mixin apply failed: foo.mixins.json",
			exit:     intPtr(1),
			category: "mixin",
			crashed:  true,
		},
		{
			name:     "java version",
			log:      "java.lang.UnsupportedClassVersionError: net/minecraft/client/main/Main",
			exit:     intPtr(1),
			category: "java_version",
			crashed:  true,
		},
		{
			name:     "java path startup failure",
			log:      "[ERROR] [run] 启动失败: 指定的java不存在: C:\\Java\\bin\\java.exe",
			category: "java_path",
			crashed:  true,
		},
		{
			name:     "assets download failure",
			log:      "[ERROR] [run] 启动失败: 下载 assets 资源失败: timeout",
			category: "assets",
			crashed:  true,
		},
		{
			name:     "native library",
			log:      "java.lang.UnsatisfiedLinkError: no lwjgl in java.library.path",
			exit:     intPtr(1),
			category: "missing_library",
			crashed:  true,
		},
		{
			name:    "normal exit",
			log:     "[process] 子进程退出码: 0\n[Launcher] 游戏进程正常退出",
			exit:    intPtr(0),
			crashed: false,
			reason:  "游戏进程正常退出",
		},
		{
			name:    "manual close by signal",
			log:     "[Launcher] 收到退出信号，正在通知游戏进程...\n执行完毕",
			exit:    intPtr(130),
			crashed: false,
			reason:  "用户手动结束游戏",
		},
		{
			name:    "forced close",
			log:     "[Launcher] 游戏进程未在 10s 内退出，强制结束\n游戏进程被强制结束",
			exit:    intPtr(-1),
			crashed: false,
			reason:  "游戏进程被强制结束（手动关闭或超时，不是游戏崩溃）",
		},
		{
			name:     "windows access violation",
			log:      "子进程退出码: -1073741819",
			exit:     intPtr(-1073741819),
			category: "exit_code",
			crashed:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := Analyze(Input{Log: tt.log, Crash: tt.crash, ExitCode: tt.exit})
			if rep.Crashed != tt.crashed {
				t.Fatalf("Crashed = %v, want %v（reason=%s）", rep.Crashed, tt.crashed, rep.Reason)
			}
			if tt.reason != "" && rep.Reason != tt.reason {
				t.Fatalf("Reason = %q, want %q", rep.Reason, tt.reason)
			}
			if tt.category != "" && !hasCategory(rep.Findings, tt.category) {
				t.Fatalf("未命中类别 %q，findings=%+v", tt.category, rep.Findings)
			}
			if tt.crashed && len(rep.Findings) == 0 {
				t.Fatal("崩溃时应至少给出一条可疑问题")
			}
			if tt.crashed && rep.Confidence <= 0 {
				t.Fatalf("崩溃置信度应大于 0: %+v", rep)
			}
		})
	}
}

func TestAnalyzeOOMDetails(t *testing.T) {
	rep := Analyze(Input{
		Log:      "java.lang.OutOfMemoryError: Java heap space\n\tat net.minecraft.client.Minecraft.run(Minecraft.java:1)",
		ExitCode: intPtr(1),
	})
	var oom *Finding
	for i := range rep.Findings {
		if rep.Findings[i].Category == "oom" {
			oom = &rep.Findings[i]
		}
	}
	if oom == nil {
		t.Fatalf("缺少 OOM 诊断: %+v", rep.Findings)
	}
	if !strings.Contains(oom.Title, "heap space") {
		t.Fatalf("标题应标明 heap space: %q", oom.Title)
	}
	if len(oom.Advice) == 0 || oom.Confidence < 90 || oom.Severity != SeverityCritical {
		t.Fatalf("OOM 建议不完整: %+v", oom)
	}
	if len(oom.Evidence) == 0 || !strings.Contains(oom.Evidence[0], "OutOfMemoryError") {
		t.Fatalf("缺少 OOM 证据行: %+v", oom.Evidence)
	}
	if len(rep.Highlights) == 0 {
		t.Fatal("应提取崩溃栈关键行")
	}
}

func TestAnalyzeCrashReportWithoutKeywords(t *testing.T) {
	rep := Analyze(Input{
		Crash: "---- Minecraft Crash Report ----\nDescription: Unexpected error",
	})
	if !rep.Crashed {
		t.Fatal("有崩溃报告应判定为崩溃")
	}
	if !hasCategory(rep.Findings, "crash_report") {
		t.Fatalf("应有通用崩溃报告提示: %+v", rep.Findings)
	}
	if rep.Reason != "检测到 Minecraft 崩溃报告" {
		t.Fatalf("Reason = %q", rep.Reason)
	}
}

func TestAnalyzeNoData(t *testing.T) {
	rep := Analyze(Input{})
	if rep.Crashed {
		t.Fatal("没有数据不应判定为崩溃")
	}
	if len(rep.Notes) == 0 {
		t.Fatal("没有数据应给出友好提示")
	}
	if rep.Reason == "" {
		t.Fatal("应说明无法判断")
	}
}

func TestAnalyzeStripsArchiveSummary(t *testing.T) {
	archive := "[Crash] ===== mtl 崩溃诊断摘要 =====\n" +
		"[Crash] 类缺失（NoClassDefFoundError）：大概率是Mod版本不匹配\n" +
		"[Crash] ===== 以下为原始崩溃日志 =====\n\n" +
		"---- Minecraft Crash Report ----\nDescription: Something harmless"
	rep := Analyze(Input{Crash: archive})
	if hasCategory(rep.Findings, "missing_class") {
		t.Fatalf("归档摘要不应参与关键词匹配: %+v", rep.Findings)
	}
}

func TestAnalyzeIgnoresStaleCrash(t *testing.T) {
	normalLog := "[Launcher] 游戏进程正常退出\n[Launcher] 子进程退出码: 0"
	rep := Analyze(Input{
		Log:        normalLog,
		Crash:      "java.lang.OutOfMemoryError: Java heap space",
		CrashFile:  "crash-reports/crash-old.txt",
		CrashStale: true,
	})
	if rep.Crashed {
		t.Fatalf("旧崩溃报告不应把正常退出判为崩溃: %+v", rep)
	}
	if hasCategory(rep.Findings, "oom") {
		t.Fatalf("旧崩溃报告不应参与关键词匹配: %+v", rep.Findings)
	}
	if len(rep.Notes) == 0 || !strings.Contains(strings.Join(rep.Notes, ""), "早于本次运行") {
		t.Fatalf("应提示崩溃报告已过期: %v", rep.Notes)
	}
}

func TestParseExitCode(t *testing.T) {
	tests := []struct {
		text string
		want *int
	}{
		{"[Launcher] 游戏进程异常退出（退出码 1）", intPtr(1)},
		{"退出码: 1\n随后 退出码: -1073741819", intPtr(-1073741819)},
		{"[Launcher] 游戏进程正常退出", intPtr(0)},
		{"no exit info", nil},
	}
	for _, tt := range tests {
		got := ParseExitCode(tt.text)
		if tt.want == nil {
			if got != nil {
				t.Errorf("ParseExitCode(%q) = %v, want nil", tt.text, *got)
			}
			continue
		}
		if got == nil || *got != *tt.want {
			t.Errorf("ParseExitCode(%q) = %v, want %d", tt.text, got, *tt.want)
		}
	}
}

func TestExtractHighlightsDedupAndLimit(t *testing.T) {
	var b strings.Builder
	for range 30 {
		b.WriteString("java.lang.RuntimeException: boom\n")
	}
	b.WriteString("Caused by: java.lang.IllegalStateException: root\n")
	got := extractHighlights("", b.String())
	if len(got) != 2 {
		t.Fatalf("应去重并保留两类关键行: %v", got)
	}
}

func TestMatchRulesDeduplicatesCategory(t *testing.T) {
	text := "OutOfMemoryError: Java heap space\nJava heap space\noutofmemoryerror"
	findings := matchRules(strings.ToLower(text), text)
	if len(findings) != 1 {
		t.Fatalf("同类规则应合并: %+v", findings)
	}
	if len(findings[0].Evidence) > 3 {
		t.Fatalf("证据行过多: %v", findings[0].Evidence)
	}
}
