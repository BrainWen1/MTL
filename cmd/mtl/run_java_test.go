package main

import (
	"os"
	"path/filepath"
	"testing"

	"mtl/internal/config"
)

func writeFakeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUsableJavaPath(t *testing.T) {
	dir := t.TempDir()
	file := writeFakeExecutable(t, dir, "java.exe")

	got, ok := usableJavaPath(file)
	if !ok {
		t.Fatalf("usableJavaPath(%q) 应可用", file)
	}
	if want, _ := filepath.Abs(file); got != want {
		t.Fatalf("路径 = %q，期望 %q", got, want)
	}

	if _, ok := usableJavaPath(filepath.Join(dir, "missing.exe")); ok {
		t.Fatal("不存在的文件应视为不可用")
	}
	if _, ok := usableJavaPath(dir); ok {
		t.Fatal("目录应视为不可用")
	}
	if _, ok := usableJavaPath("   "); ok {
		t.Fatal("空路径应视为不可用")
	}
}

func TestResolveConfiguredJavaPriority(t *testing.T) {
	dir := t.TempDir()
	configJava := writeFakeExecutable(t, dir, "cache-java.exe")
	cliJava := writeFakeExecutable(t, dir, "cli-java.exe")
	cfg := config.Default()
	cfg.Java = configJava

	// 命令行优先于实例配置。
	got, source := resolveConfiguredJava(cliJava, cfg)
	if got != cliJava || source != javaSourceCLI {
		t.Fatalf("命令行解析 = %q, %q，期望 %q, %q", got, source, cliJava, javaSourceCLI)
	}

	// 未传命令行时使用实例配置。
	got, source = resolveConfiguredJava("", cfg)
	if got != configJava || source != javaSourceConfig {
		t.Fatalf("实例配置解析 = %q, %q，期望 %q, %q", got, source, configJava, javaSourceConfig)
	}

	// 命令行即使无效也不回退到实例配置，由启动阶段报错且不回填。
	missingCLI := filepath.Join(dir, "nope.exe")
	got, source = resolveConfiguredJava(missingCLI, cfg)
	if got != missingCLI || source != javaSourceCLI {
		t.Fatalf("无效命令行解析 = %q, %q，期望不退避", got, source)
	}
}

func TestResolveConfiguredJavaDeletedFileFallsBackToAuto(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Java = filepath.Join(dir, "deleted-java.exe")

	got, source := resolveConfiguredJava("", cfg)
	if got != "" || source != "" {
		t.Fatalf("失效路径解析 = %q, %q，期望空值进入自动匹配", got, source)
	}
}

func TestResolveGlobalJava(t *testing.T) {
	runtimeRoot, _ := isolateJavaEnv(t)
	goodJava, _ := writeTestRuntime(t, runtimeRoot, "java-runtime-gamma", true)
	_, _ = writeTestRuntime(t, runtimeRoot, "java-runtime-bad", false)

	if _, _, ok := resolveGlobalJava(); ok {
		t.Fatal("未设置 default_java 时应返回 false")
	}

	if err := config.SaveGlobal(config.Global{DefaultJava: "java-runtime-gamma"}); err != nil {
		t.Fatal(err)
	}
	path, id, ok := resolveGlobalJava()
	if !ok || path != goodJava || id != "java-runtime-gamma" {
		t.Fatalf("全局默认解析 = %q, %q, %v", path, id, ok)
	}

	// 校验失败或运行时被删除时回退自动匹配，不阻断启动。
	if err := config.SaveGlobal(config.Global{DefaultJava: "java-runtime-bad"}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := resolveGlobalJava(); ok {
		t.Fatal("校验失败时应返回 false")
	}
	if err := config.SaveGlobal(config.Global{DefaultJava: "missing-runtime"}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := resolveGlobalJava(); ok {
		t.Fatal("运行时缺失时应返回 false")
	}
}

func TestShouldPersistJava(t *testing.T) {
	if shouldPersistJava(javaSourceCLI) {
		t.Fatal("--java 为临时覆盖，不应回填配置")
	}
	if !shouldPersistJava(javaSourceConfig) {
		t.Fatal("实例配置来源允许幂等回填")
	}
	if !shouldPersistJava(javaSourceRuntime) {
		t.Fatal("全局缓存来源应回填配置")
	}
	if shouldPersistJava(javaSourceGlobal) {
		t.Fatal("全局默认不应回填配置，否则 mtl java use 切换后无法生效")
	}
}

func TestPersistInstanceJavaWritesOnceAndPreservesConfig(t *testing.T) {
	dir := t.TempDir()
	original := config.Default()
	original.DefaultAccount = "Steve"
	original.JVMArgs = []string{"-Xss1M"}
	if err := config.Save(dir, original); err != nil {
		t.Fatalf("初始化配置: %v", err)
	}
	javaPath := writeFakeExecutable(t, dir, "java.exe")
	abs, _ := filepath.Abs(javaPath)

	changed, err := persistInstanceJava(dir, abs)
	if err != nil {
		t.Fatalf("首次回填: %v", err)
	}
	if !changed {
		t.Fatal("首次回填应写入配置")
	}
	got, err := config.Load(dir)
	if err != nil {
		t.Fatalf("读取配置: %v", err)
	}
	if got.Java != abs {
		t.Fatalf("java = %q，期望 %q", got.Java, abs)
	}
	if got.DefaultAccount != "Steve" || len(got.JVMArgs) != 1 {
		t.Fatalf("回填不应丢失其他字段: %+v", got)
	}

	// 相同路径再次回填应跳过写入（幂等）。
	changed, err = persistInstanceJava(dir, abs)
	if err != nil || changed {
		t.Fatalf("重复回填 = changed %v, err %v，期望跳过", changed, err)
	}
}

func TestPersistInstanceJavaOverwritesManualPath(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Java = filepath.Join(dir, "old-java.exe")
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	newPath := writeFakeExecutable(t, dir, "new-java.exe")
	abs, _ := filepath.Abs(newPath)

	changed, err := persistInstanceJava(dir, abs)
	if err != nil || !changed {
		t.Fatalf("覆盖回填 = changed %v, err %v，期望写入", changed, err)
	}
	got, _ := config.Load(dir)
	if got.Java != abs {
		t.Fatalf("java = %q，期望覆盖为 %q", got.Java, abs)
	}
}

func TestPersistInstanceJavaLeavesCorruptConfigUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, config.DirName, config.FileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const broken = `{"memory_mode": "manual",`
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	javaPath := writeFakeExecutable(t, dir, "java.exe")
	abs, _ := filepath.Abs(javaPath)

	if _, err := persistInstanceJava(dir, abs); err == nil {
		t.Fatal("损坏配置应返回错误")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != broken {
		t.Fatalf("损坏配置不应被覆盖:\n%s", raw)
	}
}
