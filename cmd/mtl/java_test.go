package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"

	"mtl/internal/config"
	"mtl/internal/runtime"
)

// isolateJavaEnv 把 runtime 缓存、全局配置和实例目录都指向临时目录。
func isolateJavaEnv(t *testing.T) (runtimeRoot, workRoot string) {
	t.Helper()
	temp := t.TempDir()
	workRoot = filepath.Join(temp, "work")
	if err := os.MkdirAll(workRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if goruntime.GOOS == "windows" {
		appData := filepath.Join(temp, "appdata")
		t.Setenv("APPDATA", appData)
		runtimeRoot = filepath.Join(appData, "mtl", "cache", "runtime")
	} else {
		home := filepath.Join(temp, "home")
		t.Setenv("HOME", home)
		runtimeRoot = filepath.Join(home, ".cache", "mtl", "runtime")
	}
	t.Setenv(config.EnvGlobalConfig, filepath.Join(temp, "config.json"))
	return runtimeRoot, workRoot
}

// chdirForTest 切换工作目录，使 defaultInstancesRoot（instances）落在临时目录。
func chdirForTest(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(%s): %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

// writeTestRuntime 创建一个带有效标记的运行时；valid=false 时篡改 java 内容使 sha1 不匹配。
func writeTestRuntime(t *testing.T, root, id string, valid bool) (javaPath, dir string) {
	t.Helper()
	rel := "bin/java"
	if goruntime.GOOS == "windows" {
		rel = "bin/java.exe"
	}
	dir = filepath.Join(root, id)
	javaPath = filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(javaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("java-body-" + id)
	if err := os.WriteFile(javaPath, body, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha1.Sum(body)
	m := map[string]string{
		"component":    id,
		"platform":     "windows-x64",
		"manifestSha1": hex.EncodeToString(sha1.New().Sum(nil)),
		"version":      "21.0.7",
		"java":         rel,
		"javaSha1":     hex.EncodeToString(sum[:]),
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mtl-runtime.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if !valid {
		if err := os.WriteFile(javaPath, []byte("tampered"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return javaPath, dir
}

func TestParseJavaFlags(t *testing.T) {
	debug, positional, err := parseJavaFlags([]string{"--debug"}, true)
	if err != nil || !debug || len(positional) != 0 {
		t.Fatalf("--debug 解析错误: debug=%v positional=%v err=%v", debug, positional, err)
	}
	_, positional, err = parseJavaFlags([]string{"java-runtime-gamma"}, false)
	if err != nil || len(positional) != 1 || positional[0] != "java-runtime-gamma" {
		t.Fatalf("位置参数解析错误: %v %v", positional, err)
	}
	if _, _, err := parseJavaFlags([]string{"--debug"}, false); err == nil {
		t.Fatal("use/prune 不接受 --debug")
	}
	if _, _, err := parseJavaFlags([]string{"--unknown"}, true); err == nil {
		t.Fatal("未知参数应报错")
	}
}

func TestJavaCommandUsage(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"unknown"},
		{"list", "extra"},
		{"use"},
		{"use", "a", "b"},
		{"prune", "extra"},
	} {
		if err := javaCommand(args); err == nil {
			t.Errorf("参数 %v 应报错", args)
		}
	}
}

func TestCollectJavaReferences(t *testing.T) {
	runtimeRoot, _ := isolateJavaEnv(t)
	keepJava, _ := writeTestRuntime(t, runtimeRoot, "java-runtime-gamma", true)
	_, _ = writeTestRuntime(t, runtimeRoot, "java-runtime-epsilon", true)

	instancesRoot := filepath.Join(t.TempDir(), "instances")
	if err := config.Save(filepath.Join(instancesRoot, "demo"), config.Config{Java: keepJava}); err != nil {
		t.Fatal(err)
	}
	externalJava := filepath.Join(t.TempDir(), "manual-java")
	if err := os.WriteFile(externalJava, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(instancesRoot, "other"), config.Config{Java: externalJava}); err != nil {
		t.Fatal(err)
	}
	// 缺少配置的目录应被跳过，不影响其他实例。
	if err := os.MkdirAll(filepath.Join(instancesRoot, "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveGlobal(config.Global{DefaultJava: "java-runtime-epsilon"}); err != nil {
		t.Fatal(err)
	}

	installed, err := runtime.List(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := collectJavaReferences(instancesRoot, installed)
	if err != nil {
		t.Fatalf("collectJavaReferences: %v", err)
	}
	if refs.instanceCount != 2 {
		t.Fatalf("实例数量 = %d, want 2", refs.instanceCount)
	}
	if got := refs.byRuntime["java-runtime-gamma"]; len(got) != 1 || got[0] != "demo" {
		t.Fatalf("实例引用 = %v", got)
	}
	if got := refs.byRuntime["java-runtime-epsilon"]; len(got) != 1 || got[0] != "全局默认" {
		t.Fatalf("全局默认引用 = %v", got)
	}
	if refs.globalID != "java-runtime-epsilon" || !refs.globalFound {
		t.Fatalf("全局默认状态错误: %+v", refs)
	}

	// 全局默认指向不存在的 runtime 时只告警，不产生引用。
	if err := config.SaveGlobal(config.Global{DefaultJava: "missing-runtime"}); err != nil {
		t.Fatal(err)
	}
	refs, err = collectJavaReferences(instancesRoot, installed)
	if err != nil {
		t.Fatal(err)
	}
	if refs.globalFound || refs.byRuntime["missing-runtime"] != nil {
		t.Fatalf("缺失的全局默认不应被引用: %+v", refs)
	}
}

func TestJavaUse(t *testing.T) {
	runtimeRoot, _ := isolateJavaEnv(t)
	_, _ = writeTestRuntime(t, runtimeRoot, "java-runtime-gamma", true)

	oldVerify := verifyJavaExecutable
	t.Cleanup(func() { verifyJavaExecutable = oldVerify })
	verifyJavaExecutable = func(context.Context, string) error { return nil }

	// 大小写不敏感，写入规范 ID。
	if err := javaCommand([]string{"use", "JAVA-RUNTIME-GAMMA"}); err != nil {
		t.Fatalf("java use: %v", err)
	}
	cfg, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultJava != "java-runtime-gamma" {
		t.Fatalf("default_java = %q", cfg.DefaultJava)
	}

	// 执行校验失败时拒绝设置，旧值保持不变。
	verifyJavaExecutable = func(context.Context, string) error { return errors.New("cannot run") }
	if err := javaCommand([]string{"use", "java-runtime-gamma"}); err == nil {
		t.Fatal("java -version 失败应拒绝设置")
	}
	cfg, _ = config.LoadGlobal()
	if cfg.DefaultJava != "java-runtime-gamma" {
		t.Fatalf("失败后默认值不应改变: %q", cfg.DefaultJava)
	}

	// runtime 校验失败（sha1 不匹配）时拒绝设置。
	verifyJavaExecutable = func(context.Context, string) error { return nil }
	_, _ = writeTestRuntime(t, runtimeRoot, "java-runtime-bad", false)
	if err := javaCommand([]string{"use", "java-runtime-bad"}); err == nil {
		t.Fatal("无效 runtime 应拒绝设置")
	}

	if err := javaCommand([]string{"use", "missing-runtime"}); err == nil {
		t.Fatal("不存在的 runtime 应报错")
	}
}

func TestJavaListAndPrune(t *testing.T) {
	runtimeRoot, workRoot := isolateJavaEnv(t)
	chdirForTest(t, workRoot)

	keepJava, keepDir := writeTestRuntime(t, runtimeRoot, "java-runtime-gamma", true)
	_, globalDir := writeTestRuntime(t, runtimeRoot, "java-runtime-epsilon", true)
	_, dropDir := writeTestRuntime(t, runtimeRoot, "java-runtime-delta", true)

	if err := config.Save(filepath.Join(workRoot, "instances", "demo"), config.Config{Java: keepJava}); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveGlobal(config.Global{DefaultJava: "java-runtime-epsilon"}); err != nil {
		t.Fatal(err)
	}

	// list 正常路径与 --debug 目录树输出都应成功。
	if err := javaCommand([]string{"list"}); err != nil {
		t.Fatalf("java list: %v", err)
	}
	if err := javaCommand([]string{"list", "--debug"}); err != nil {
		t.Fatalf("java list --debug: %v", err)
	}

	if err := javaCommand([]string{"prune"}); err != nil {
		t.Fatalf("java prune: %v", err)
	}
	if _, err := os.Stat(keepDir); err != nil {
		t.Fatalf("被实例引用的 runtime 不应删除: %v", err)
	}
	if _, err := os.Stat(globalDir); err != nil {
		t.Fatalf("全局默认 runtime 不应删除: %v", err)
	}
	if _, err := os.Stat(dropDir); !os.IsNotExist(err) {
		t.Fatal("未引用的 runtime 应删除")
	}

	// 再次执行幂等：无删除、无错误。
	if err := javaCommand([]string{"prune"}); err != nil {
		t.Fatalf("重复 java prune: %v", err)
	}
}
