package instance

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mtl/internal/config"
)

// writeInstance 创建一个带合法 .mtl/config.json 的实例目录。
func writeInstance(t *testing.T, root, name string, mutate func(*config.Config)) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	if mutate != nil {
		mutate(&cfg)
	}
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestScanInstances(t *testing.T) {
	root := t.TempDir()
	writeInstance(t, root, "alpha", func(c *config.Config) {
		c.LoaderType = "fabric"
		c.LoaderVersion = "0.15.11"
		c.DefaultAccount = "Steve"
	})
	writeInstance(t, root, "beta", nil)

	// 配置损坏：应跳过而不是报错。
	broken := filepath.Join(root, "broken")
	if err := os.MkdirAll(filepath.Join(broken, ".mtl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, ".mtl", "config.json"), []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 没有配置文件的目录：同样跳过。
	if err := os.MkdirAll(filepath.Join(root, "missing"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 普通文件不是实例目录，直接忽略。
	if err := os.WriteFile(filepath.Join(root, "readme.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, skipped, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("有效实例数 = %d，期望 2: %+v", len(entries), entries)
	}
	if entries[0].Name != "alpha" || entries[1].Name != "beta" {
		t.Fatalf("实例顺序 = %q, %q", entries[0].Name, entries[1].Name)
	}
	if entries[0].Config.LoaderType != "fabric" || entries[0].Config.LoaderVersion != "0.15.11" {
		t.Fatalf("alpha 加载器信息 = %+v", entries[0].Config)
	}
	if entries[0].Config.DefaultAccount != "Steve" {
		t.Fatalf("alpha 默认账号 = %q", entries[0].Config.DefaultAccount)
	}
	if entries[0].JavaStatus != JavaAuto {
		t.Fatalf("未配置 Java 应为自动匹配，得到 %q", entries[0].JavaStatus)
	}
	if !strings.HasSuffix(entries[0].ConfigPath, filepath.Join(".mtl", "config.json")) {
		t.Fatalf("ConfigPath = %q", entries[0].ConfigPath)
	}

	if len(skipped) != 2 {
		t.Fatalf("跳过数 = %d，期望 2: %+v", len(skipped), skipped)
	}
	reasons := skipped[0].Reason.Error() + " " + skipped[1].Reason.Error()
	if !strings.Contains(reasons, "配置") || !strings.Contains(reasons, "缺少") {
		t.Fatalf("跳过原因不明确: %s", reasons)
	}
}

func TestLoadInstance(t *testing.T) {
	root := t.TempDir()
	dir := writeInstance(t, root, "alpha", func(c *config.Config) {
		c.LoaderType = "forge"
		c.LoaderVersion = "1.20.1-47.4.3"
	})

	e, err := Load(root, "alpha")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if e.Dir != dir || e.Config.LoaderType != "forge" {
		t.Fatalf("Load 结果 = %+v", e)
	}

	if _, err := Load(root, "../alpha"); err == nil {
		t.Fatal("路径穿越的实例名应被拒绝")
	}
	if _, err := Load(root, "nope"); err == nil {
		t.Fatal("不存在的实例应报错")
	}

	// 配置损坏时 Load 返回错误，便于 info 直接提示。
	broken := filepath.Join(root, "broken")
	if err := os.MkdirAll(filepath.Join(broken, ".mtl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, ".mtl", "config.json"), []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, "broken"); err == nil {
		t.Fatal("损坏的配置应返回错误")
	}
}

func TestCheckJava(t *testing.T) {
	if status, _ := CheckJava(""); status != JavaAuto {
		t.Fatalf("空路径状态 = %q，期望 %q", status, JavaAuto)
	}

	missing := filepath.Join(t.TempDir(), "missing-java")
	if status, detail := CheckJava(missing); status != JavaMissing || detail == "" {
		t.Fatalf("缺失路径状态 = %q, detail=%q", status, detail)
	}

	if status, _ := CheckJava(t.TempDir()); status != JavaNotExecutable {
		t.Fatalf("目录状态 = %q，期望 %q", status, JavaNotExecutable)
	}

	java := filepath.Join(t.TempDir(), "java")
	if err := os.WriteFile(java, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	if status, detail := CheckJava(java); status != JavaOK {
		t.Fatalf("可执行文件状态 = %q, detail=%q", status, detail)
	}

	if runtime.GOOS != "windows" {
		plain := filepath.Join(t.TempDir(), "plain")
		if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if status, _ := CheckJava(plain); status != JavaNotExecutable {
			t.Fatalf("无执行权限文件状态 = %q，期望 %q", status, JavaNotExecutable)
		}
	}
}

func TestHealth(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-java")
	status, detail := CheckJava(missing)
	e := Entry{
		Name:       "alpha",
		Config:     config.Config{},
		JavaStatus: status,
		JavaDetail: detail,
	}
	checks := e.Health()
	failures, warnings := CountHealth(checks)
	if failures != 1 {
		t.Fatalf("异常数 = %d，期望 1: %+v", failures, checks)
	}
	// 加载器类型、加载器版本、默认账号为空各产生一条警告。
	if warnings != 3 {
		t.Fatalf("警告数 = %d，期望 3: %+v", warnings, checks)
	}
}
