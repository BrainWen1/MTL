package launch

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestRunCustomArgsOrder 验证最终命令顺序：
// 固定 JVM 参数 -> -Xms/-Xmx -> --jvm-arg -> 主类 -> --game-arg。
func TestRunCustomArgsOrder(t *testing.T) {
	exe := buildFakeJava(t)
	dir := writeTestInstance(t)

	var out bytes.Buffer
	err := Run(context.Background(), Options{
		InstanceDir: dir,
		Username:    "Steve",
		JavaPath:    exe,
		AssetsDir:   t.TempDir(),
		Memory:      &Memory{Min: "768M", Max: "3G"},
		ExtraJVMArgs: []string{
			"-XX:-OmitStackTraceInFastThrow",
			"-Dfml.ignorePatchDiscrepancies=True",
			"   ", // 空参数应被过滤
		},
		ExtraGameArgs: []string{"--noChat", ""},
		Debug:         true,
		Out:           &out,
		ErrOut:        &out,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	log := out.String()
	start := strings.Index(log, "[Launcher] 命令: ")
	if start < 0 {
		t.Fatalf("--debug 未输出完整命令:\n%s", log)
	}
	cmdLine := log[start:]

	order := []struct {
		name string
		arg  string
	}{
		{"固定 JVM 参数", "-Dminecraft.launcher.brand"},
		{"classpath", "-cp "},
		{"内存参数 Xms", "-Xms768M"},
		{"内存参数 Xmx", "-Xmx3G"},
		{"自定义 JVM 参数 1", "-XX:-OmitStackTraceInFastThrow"},
		{"自定义 JVM 参数 2", "-Dfml.ignorePatchDiscrepancies=True"},
		{"主类", "net.fabricmc.loader.impl.launch.knot.KnotClient"},
		{"游戏参数", "--noChat"},
	}
	last := -1
	for _, item := range order {
		idx := strings.Index(cmdLine, item.arg)
		if idx < 0 {
			t.Fatalf("命令缺少%s %q:\n%s", item.name, item.arg, cmdLine)
		}
		if idx <= last {
			t.Fatalf("%s (%q) 顺序不正确:\n%s", item.name, item.arg, cmdLine)
		}
		last = idx
	}

	// 用户传入的空白参数不得进入命令（原版参数中的 --clientId "" 等合法空值不在此列）。
	if strings.Contains(cmdLine, `"   "`) {
		t.Fatalf("空白参数未被过滤:\n%s", cmdLine)
	}
}

// TestFilterArgs 确认只过滤空参数，其余内容原样保留。
func TestFilterArgs(t *testing.T) {
	in := []string{"-Da=b c", "", "  ", "-XX:+UseG1GC"}
	got := filterArgs(in)
	want := []string{"-Da=b c", "-XX:+UseG1GC"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("filterArgs = %q，期望 %q", got, want)
	}
	if got := filterArgs(nil); len(got) != 0 {
		t.Fatalf("nil 应返回空: %v", got)
	}
}
