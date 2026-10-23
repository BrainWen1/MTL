package launch

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunInjectsMemoryArgs(t *testing.T) {
	exe := buildFakeJava(t)
	dir := writeTestInstance(t)

	var out bytes.Buffer
	err := Run(context.Background(), Options{
		InstanceDir: dir,
		Username:    "Steve",
		JavaPath:    exe,
		AssetsDir:   t.TempDir(),
		Memory:      &Memory{Min: "512M", Max: "4G"},
		Debug:       true,
		Out:         &out,
		ErrOut:      &out,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	log := out.String()
	if !strings.Contains(log, "[Launch] JVM内存参数：-Xms512M -Xmx4G") {
		t.Fatalf("未输出 JVM 内存日志:\n%s", log)
	}
	cmdLine := log[strings.Index(log, "[Launcher] 命令: "):]
	if !strings.Contains(cmdLine, "-Xms512M") || !strings.Contains(cmdLine, "-Xmx4G") {
		t.Fatalf("启动命令缺少内存参数:\n%s", log)
	}
	// 顺序：固定 JVM 参数 -> 内存参数 -> 主类。
	if strings.Index(cmdLine, "-Xms512M") < strings.Index(cmdLine, "-Dminecraft.launcher.brand") {
		t.Fatalf("内存参数应位于固定 JVM 参数之后:\n%s", cmdLine)
	}
	if strings.Index(cmdLine, "-Xms512M") < strings.Index(cmdLine, "-cp ") {
		t.Fatalf("内存参数应位于 classpath 之后:\n%s", cmdLine)
	}
	if strings.Index(cmdLine, "-Xms512M") > strings.Index(cmdLine, "net.fabricmc.loader.impl.launch.knot.KnotClient") {
		t.Fatalf("内存参数应位于主类之前:\n%s", cmdLine)
	}
}

func TestRunWithoutMemoryArgs(t *testing.T) {
	exe := buildFakeJava(t)
	dir := writeTestInstance(t)

	var out bytes.Buffer
	err := Run(context.Background(), Options{
		InstanceDir: dir,
		Username:    "Steve",
		JavaPath:    exe,
		AssetsDir:   t.TempDir(),
		Debug:       true,
		Out:         &out,
		ErrOut:      &out,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(out.String(), "[Launch] JVM内存参数") {
		t.Fatalf("未配置 Memory 时不应输出内存日志:\n%s", out.String())
	}
}

func TestComposeJVMOrderAndFiltering(t *testing.T) {
	base := []string{"-Dfoo=bar", "-cp", "x"}
	got, memArgs := composeJVM(base, &Memory{Min: "512M", Max: "4G"}, []string{"-Xmx9G", "", "   "})
	want := []string{"-Dfoo=bar", "-cp", "x", "-Xms512M", "-Xmx4G", "-Xmx9G"}
	if strings.Join(memArgs, " ") != "-Xms512M -Xmx4G" {
		t.Fatalf("memArgs = %v", memArgs)
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("composeJVM = %v，期望 %v", got, want)
	}
	if got, memArgs := composeJVM(base, nil, nil); len(memArgs) != 0 || len(got) != len(base) {
		t.Fatalf("nil Memory/自定义参数不应改变参数: %v %v", got, memArgs)
	}
}
