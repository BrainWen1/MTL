package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mtl/internal/config"
)

const fakeJavaSource = `package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-version" {
		fmt.Fprintln(os.Stderr, "openjdk version \"17.0.10\" 2024-01-16")
		return
	}
}
`

// buildFakeJava 编译一个能响应 -version 的假 java，用于 set-java 的合法路径测试。
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

func TestConfigSetJavaValidPath(t *testing.T) {
	dir := t.TempDir()
	original := config.Default()
	original.DefaultAccount = "Steve"
	original.JVMArgs = []string{"-Xss1M"}
	if err := config.Save(dir, original); err != nil {
		t.Fatal(err)
	}

	exe := buildFakeJava(t)
	want, err := filepath.Abs(exe)
	if err != nil {
		t.Fatal(err)
	}

	// 同时验证位置参数写法。
	if err := configCommand([]string{"set-java", dir, exe}); err != nil {
		t.Fatalf("set-java: %v", err)
	}
	got, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Java != want {
		t.Fatalf("java = %q，期望 %q", got.Java, want)
	}
	if got.DefaultAccount != "Steve" || len(got.JVMArgs) != 1 {
		t.Fatalf("set-java 不应丢失其他字段: %+v", got)
	}

	// 重复设置同一路径应幂等。
	if err := configCommand([]string{"set-java", "--dir", dir, exe}); err != nil {
		t.Fatalf("重复 set-java: %v", err)
	}
	got, err = config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Java != want {
		t.Fatalf("重复设置后 java = %q，期望 %q", got.Java, want)
	}
}

func TestConfigSetJavaClear(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Java = filepath.Join(t.TempDir(), "java.exe")
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}

	// 省略路径表示清空，恢复自动匹配。
	if err := configCommand([]string{"set-java", dir}); err != nil {
		t.Fatalf("set-java 清空: %v", err)
	}
	got, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Java != "" {
		t.Fatalf("清空后 java = %q，期望空字符串", got.Java)
	}

	// 已是空值时再次清空保持幂等。
	if err := configCommand([]string{"set-java", "--dir", dir}); err != nil {
		t.Fatalf("重复清空: %v", err)
	}
}

func TestConfigSetJavaRejectsInvalidPath(t *testing.T) {
	dir := t.TempDir()
	const old = `C:\Java\old\bin\java.exe`
	cfg := config.Default()
	cfg.Java = old
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}

	bad := filepath.Join(t.TempDir(), "missing-java.exe")
	err := configCommand([]string{"set-java", dir, bad})
	if err == nil || !strings.Contains(err.Error(), "Java 路径校验失败") {
		t.Fatalf("无效路径应返回校验错误: %v", err)
	}
	got, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Java != old {
		t.Fatalf("校验失败不应写入配置，java = %q，期望 %q", got.Java, old)
	}
}

func TestConfigSetJavaRejectsNonJavaFile(t *testing.T) {
	dir := t.TempDir()
	notJava := filepath.Join(t.TempDir(), "not-java")
	if err := os.WriteFile(notJava, []byte("not a java executable"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := configCommand([]string{"set-java", dir, notJava}); err == nil {
		t.Fatal("非 Java 可执行文件应被拒绝")
	}
	got, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Java != "" {
		t.Fatalf("校验失败不应写入配置，java = %q", got.Java)
	}
}

func TestConfigSetJavaUsage(t *testing.T) {
	if err := configCommand([]string{"set-java"}); err == nil {
		t.Fatal("缺少实例目录应报错")
	}
	if err := configCommand([]string{"set-java", "a", "b", "c"}); err == nil {
		t.Fatal("多余参数应报错")
	}
}
