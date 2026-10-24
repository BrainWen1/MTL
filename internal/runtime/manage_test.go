package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

// javaRelForTest 返回当前平台的标准 java 可执行文件相对路径。
func javaRelForTest() string {
	if goruntime.GOOS == "windows" {
		return "bin/java.exe"
	}
	return "bin/java"
}

// writeRuntimeFixture 在 root 下创建一个带有效标记的运行时缓存目录。
func writeRuntimeFixture(t *testing.T, root, id, platform, version, body string) (javaPath, dir string) {
	t.Helper()
	dir = filepath.Join(root, id)
	rel := javaRelForTest()
	javaPath = filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(javaPath), 0o755); err != nil {
		t.Fatalf("创建运行时目录: %v", err)
	}
	// Windows 不依赖执行位，POSIX 上必须带执行位才能通过校验。
	if err := os.WriteFile(javaPath, []byte(body), 0o755); err != nil {
		t.Fatalf("写入 java 文件: %v", err)
	}
	m := marker{
		Component:    id,
		Platform:     platform,
		ManifestSHA1: testSHA1("manifest-" + id),
		Version:      version,
		Java:         rel,
		JavaSHA1:     testSHA1(body),
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("编码标记: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, markerName), data, 0o644); err != nil {
		t.Fatalf("写入标记: %v", err)
	}
	return javaPath, dir
}

func TestListInstalled(t *testing.T) {
	root := t.TempDir()
	javaPath, _ := writeRuntimeFixture(t, root, "java-runtime-gamma", "windows-x64", "17.0.15", "gamma-body")

	// 没有标记但存在标准 java 的旧缓存：列出但标记异常。
	legacyDir := filepath.Join(root, "jre-legacy", "bin")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyJava := filepath.Join(legacyDir, filepath.Base(javaRelForTest()))
	if err := os.WriteFile(legacyJava, []byte("legacy"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 既无标记也无 java 的目录、隐藏暂存目录与 manifests/ 都不算运行时。
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".staging-123"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "manifests"), 0o755); err != nil {
		t.Fatal(err)
	}

	installed, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(installed) != 2 {
		t.Fatalf("List 返回 %d 项, want 2: %+v", len(installed), installed)
	}
	if installed[0].ID != "java-runtime-gamma" || installed[1].ID != "jre-legacy" {
		t.Fatalf("排序错误: %s, %s", installed[0].ID, installed[1].ID)
	}

	gamma := installed[0]
	if !gamma.Valid || gamma.Version != "17.0.15" || gamma.Platform != "windows-x64" {
		t.Fatalf("正常运行时解析错误: %+v", gamma)
	}
	if gamma.OS != "Windows" || gamma.Arch != "x64" {
		t.Fatalf("平台拆分错误: %s / %s", gamma.OS, gamma.Arch)
	}
	if gamma.JavaPath != javaPath {
		t.Fatalf("java 路径 = %q, want %q", gamma.JavaPath, javaPath)
	}

	legacy := installed[1]
	if legacy.Valid || legacy.Err == "" || legacy.JavaPath != legacyJava {
		t.Fatalf("旧缓存应标记异常并保留 java 路径: %+v", legacy)
	}
}

func TestListMissingDir(t *testing.T) {
	got, err := List(filepath.Join(t.TempDir(), "missing"))
	if err != nil || len(got) != 0 {
		t.Fatalf("缓存目录不存在时应返回空列表: %+v, %v", got, err)
	}
}

func TestListRejectsComponentMismatch(t *testing.T) {
	root := t.TempDir()
	_, dir := writeRuntimeFixture(t, root, "gamma", "linux", "21.0.7", "body")
	// 把标记里的 component 改成别的值，模拟被手动改名/污染。
	m := marker{
		Component: "other",
		Platform:  "linux",
		Version:   "21.0.7",
		Java:      javaRelForTest(),
		JavaSHA1:  testSHA1("body"),
	}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, markerName), data, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Find(root, "gamma")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got.Valid || !strings.Contains(got.Err, "不一致") {
		t.Fatalf("组件不一致应标记异常: %+v", got)
	}
}

func TestFind(t *testing.T) {
	root := t.TempDir()
	writeRuntimeFixture(t, root, "java-runtime-Gamma", "linux", "17.0.15", "body")

	got, err := Find(root, "java-runtime-gamma")
	if err != nil {
		t.Fatalf("Find 应忽略大小写: %v", err)
	}
	if got.ID != "java-runtime-Gamma" || !got.Valid {
		t.Fatalf("Find 结果错误: %+v", got)
	}

	if _, err := Find(root, "missing-runtime"); err == nil {
		t.Fatal("未找到时应报错")
	}
	for _, id := range []string{"", ".", "..", "../etc", "a/b", `a\b`} {
		if _, err := Find(root, id); err == nil {
			t.Errorf("非法标识 %q 应报错", id)
		}
	}
}

func TestVerifyExecutable(t *testing.T) {
	old := javaVersionRunner
	t.Cleanup(func() { javaVersionRunner = old })

	var called string
	javaVersionRunner = func(_ context.Context, p string) error {
		called = p
		return nil
	}

	dir := t.TempDir()
	javaPath := filepath.Join(dir, filepath.Base(javaRelForTest()))
	if err := os.WriteFile(javaPath, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := VerifyExecutable(context.Background(), javaPath); err != nil {
		t.Fatalf("VerifyExecutable: %v", err)
	}
	if called != javaPath {
		t.Fatalf("未执行 java -version: called=%q", called)
	}

	if err := VerifyExecutable(context.Background(), filepath.Join(dir, "none")); err == nil {
		t.Fatal("文件不存在应报错")
	}
	if err := VerifyExecutable(context.Background(), dir); err == nil {
		t.Fatal("目录应报错")
	}

	javaVersionRunner = func(context.Context, string) error { return errors.New("boom") }
	if err := VerifyExecutable(context.Background(), javaPath); err == nil {
		t.Fatal("执行失败应向上返回错误")
	}

	if goruntime.GOOS != "windows" {
		noExec := filepath.Join(dir, "noexec")
		if err := os.WriteFile(noExec, []byte("fake"), 0o644); err != nil {
			t.Fatal(err)
		}
		javaVersionRunner = func(context.Context, string) error { return nil }
		if err := VerifyExecutable(context.Background(), noExec); err == nil {
			t.Fatal("缺少执行权限应报错")
		}
	}
}

func TestPrune(t *testing.T) {
	root := t.TempDir()
	_, keepDir := writeRuntimeFixture(t, root, "java-runtime-gamma", "linux", "17.0.15", "keep-body")
	_, dropDir := writeRuntimeFixture(t, root, "java-runtime-epsilon", "linux", "25.0.1", "drop-body")

	wantSize, err := dirSize(dropDir)
	if err != nil {
		t.Fatal(err)
	}

	res, err := Prune(PruneOptions{
		Dir:  root,
		Keep: map[string]string{"JAVA-RUNTIME-GAMMA": "实例 demo"},
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(res.Removed) != 1 || res.Removed[0].ID != "java-runtime-epsilon" {
		t.Fatalf("Removed = %+v", res.Removed)
	}
	if res.Removed[0].SizeBytes != wantSize {
		t.Fatalf("SizeBytes = %d, want %d", res.Removed[0].SizeBytes, wantSize)
	}
	if res.FreedBytes != wantSize {
		t.Fatalf("FreedBytes = %d, want %d", res.FreedBytes, wantSize)
	}
	if len(res.Kept) != 1 || res.Kept[0].ID != "java-runtime-gamma" || res.Kept[0].Reason != "实例 demo" {
		t.Fatalf("Kept = %+v", res.Kept)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("Failed = %+v", res.Failed)
	}
	if _, err := os.Stat(dropDir); !os.IsNotExist(err) {
		t.Fatal("未引用的运行时应被删除")
	}
	if _, err := os.Stat(keepDir); err != nil {
		t.Fatalf("被引用的运行时不应删除: %v", err)
	}

	// 幂等：再次清理不产生删除与失败。
	res, err = Prune(PruneOptions{Dir: root, Keep: map[string]string{"java-runtime-gamma": "实例 demo"}})
	if err != nil {
		t.Fatalf("重复 Prune: %v", err)
	}
	if len(res.Removed) != 0 || len(res.Failed) != 0 || res.FreedBytes != 0 {
		t.Fatalf("重复清理结果错误: %+v", res)
	}
}

func TestPruneMissingDir(t *testing.T) {
	res, err := Prune(PruneOptions{Dir: filepath.Join(t.TempDir(), "missing")})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(res.Removed) != 0 || len(res.Failed) != 0 {
		t.Fatalf("缓存目录不存在时应为空结果: %+v", res)
	}
}
