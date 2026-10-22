package launch

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const fakeJavaSource = `package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-version" {
		fmt.Fprintf(os.Stderr, "openjdk version %q 2024-01-16\n", "17.0.10")
		return
	}
	fmt.Println("fake minecraft running")
}
`

// buildFakeJava 编译一个能响应 -version 的假 java，用于跨平台测试自定义路径分支。
func buildFakeJava(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(fakeJavaSource), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "fake-java")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, src)
	cmd.Dir = dir
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("编译 fake java 失败: %v\n%s", err, b)
	}
	return out
}

func TestFindJavaCustomPath(t *testing.T) {
	exe := buildFakeJava(t)
	abs, err := filepath.Abs(exe)
	if err != nil {
		t.Fatal(err)
	}

	path, major, err := findJava(context.Background(), 17, exe)
	if err != nil {
		t.Fatalf("findJava: %v", err)
	}
	if path != abs {
		t.Fatalf("path = %q, want %q", path, abs)
	}
	if major != 17 {
		t.Fatalf("major = %d, want 17", major)
	}

	if _, _, err := findJava(context.Background(), 21, exe); err == nil || !strings.Contains(err.Error(), "低于版本要求") {
		t.Fatalf("低版本应报错: %v", err)
	}
}

func TestFindJavaMissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope", "java.exe")
	_, _, err := findJava(context.Background(), 0, missing)
	if err == nil || !strings.Contains(err.Error(), "指定的java不存在") {
		t.Fatalf("缺少文件应报错: %v", err)
	}
}

func TestFindJavaRejectsDirectory(t *testing.T) {
	_, _, err := findJava(context.Background(), 0, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "指定的java不存在") {
		t.Fatalf("目录应被拒绝: %v", err)
	}
}

func TestFindJavaRejectsNonExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不使用 POSIX 执行位")
	}
	p := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := findJava(context.Background(), 0, p)
	if err == nil || !strings.Contains(err.Error(), "指定的java不可执行") {
		t.Fatalf("不可执行文件应报错: %v", err)
	}
}

func TestRunUsesCustomJavaPath(t *testing.T) {
	exe := buildFakeJava(t)
	abs, err := filepath.Abs(exe)
	if err != nil {
		t.Fatal(err)
	}
	dir := writeTestInstance(t)

	var out bytes.Buffer
	err = Run(context.Background(), Options{
		InstanceDir: dir,
		Username:    "Steve",
		JavaPath:    exe,
		Debug:       true,
		Out:         &out,
		ErrOut:      &out,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	log := out.String()
	if !strings.Contains(log, "[Launcher] Java: "+abs) {
		t.Fatalf("日志未输出 java 完整路径:\n%s", log)
	}
	if !strings.Contains(log, "[Launcher] 命令: ") || !strings.Contains(log, abs) {
		t.Fatalf("--debug 未输出包含 java 路径的完整命令:\n%s", log)
	}
	if !strings.Contains(log, "fake minecraft running") {
		t.Fatalf("游戏进程 stdout 未转发:\n%s", log)
	}
}
