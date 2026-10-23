//go:build windows

package launch

import (
	"path/filepath"
	"testing"
)

func TestHighPerfGPUPreferenceValue(t *testing.T) {
	name, data, err := highPerfGPUPreference("java.exe")
	if err != nil {
		t.Fatalf("highPerfGPUPreference: %v", err)
	}
	abs, err := filepath.Abs("java.exe")
	if err != nil {
		t.Fatal(err)
	}
	if name != filepath.Clean(abs) {
		t.Fatalf("值名 = %q，期望 Java 绝对路径 %q", name, filepath.Clean(abs))
	}
	if data != "GpuPreference=2;" {
		t.Fatalf("数据 = %q，期望 GpuPreference=2;", data)
	}
}
