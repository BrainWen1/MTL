package fabric

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mtl/internal/config"
)

// TestAdapterInstallRegression 确认 Fabric 适配器仍然走原有 Install 流程，
// 下载、写入版本 json 与进度汇报行为不变。
func TestAdapterInstallRegression(t *testing.T) {
	m := newMockFabric(t)
	dir := t.TempDir()

	var msgs []string
	opts := m.options()
	opts.Progress = func(msg string) { msgs = append(msgs, msg) }
	ld := NewLoader(opts)
	if ld.Name() != "fabric" {
		t.Fatalf("Name = %q", ld.Name())
	}

	inst := &config.Instance{
		Dir:              dir,
		MinecraftVersion: "1.20.1",
		LoaderType:       "fabric",
		LoaderVersion:    "0.15.0",
	}
	if err := ld.Install(context.Background(), inst); err != nil {
		t.Fatalf("Install: %v", err)
	}

	const profileID = "fabric-loader-0.15.0-1.20.1"
	if _, err := os.Stat(filepath.Join(dir, "versions", profileID, profileID+".json")); err != nil {
		t.Fatalf("缺少 Fabric 版本 json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "versions", "1.20.1", "1.20.1.jar")); err != nil {
		t.Fatalf("缺少原版 client jar: %v", err)
	}
	joined := strings.Join(msgs, "\n")
	for _, want := range []string{"拉取loader元信息", "安装完成: " + profileID} {
		if !strings.Contains(joined, want) {
			t.Fatalf("进度缺少 %q:\n%s", want, joined)
		}
	}
}

func TestAdapterRejectsEmptyInstance(t *testing.T) {
	ld := NewLoader(Options{})
	if err := ld.Install(context.Background(), nil); err == nil {
		t.Fatal("nil 实例应报错")
	}
	if err := ld.Install(context.Background(), &config.Instance{}); err == nil {
		t.Fatal("空实例目录应报错")
	}
}
