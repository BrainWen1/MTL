package launch

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestRunProcessHooksNormalExit(t *testing.T) {
	exe := buildFakeJava(t)
	dir := writeTestInstance(t)

	var (
		pid    int
		code   = -99
		starts int
		exits  int
		out    strings.Builder
	)
	err := Run(context.Background(), Options{
		InstanceDir: dir,
		Username:    "Steve",
		JavaPath:    exe,
		AssetsDir:   t.TempDir(),
		Out:         &out,
		ErrOut:      &out,
		OnProcessStart: func(p int) {
			starts++
			pid = p
		},
		OnProcessExit: func(c int) {
			exits++
			code = c
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if starts != 1 || exits != 1 {
		t.Fatalf("回调次数 start=%d exit=%d, want 1/1", starts, exits)
	}
	if pid <= 0 {
		t.Fatalf("PID = %d", pid)
	}
	if code != 0 {
		t.Fatalf("正常退出码 = %d, want 0", code)
	}
}

func TestRunProcessHooksNonZeroExit(t *testing.T) {
	exe := buildExitingFakeJava(t, 3)
	dir := writeTestInstance(t)

	var code = -99
	err := Run(context.Background(), Options{
		InstanceDir: dir,
		Username:    "Steve",
		JavaPath:    exe,
		AssetsDir:   t.TempDir(),
		Out:         io.Discard,
		ErrOut:      io.Discard,
		OnProcessExit: func(c int) {
			code = c
		},
	})
	if err == nil || !strings.Contains(err.Error(), "退出码 3") {
		t.Fatalf("应返回退出码 3 的错误: %v", err)
	}
	if code != 3 {
		t.Fatalf("回调退出码 = %d, want 3", code)
	}
}

// buildExitingFakeJava 编译除 -version 外指定退出码的假 java。
func buildExitingFakeJava(t *testing.T, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	source := `package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-version" {
		fmt.Fprintf(os.Stderr, "openjdk version %q 2024-01-16\n", "17.0.10")
		return
	}
	fmt.Println("fake minecraft crash")
	os.Exit(` + strconv.Itoa(exitCode) + `)
}
`
	if err := os.WriteFile(src, []byte(source), 0o644); err != nil {
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
