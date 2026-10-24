package loader

import (
	"context"
	"strings"
	"testing"

	"mtl/internal/config"
)

func TestNewLoaderTypes(t *testing.T) {
	fab, err := NewLoader(TypeFabric, "1.20.1")
	if err != nil || fab.Name() != TypeFabric {
		t.Fatalf("fabric loader = %v, %v", fab, err)
	}
	args, err := fab.GetLaunchArgs(context.Background(), &config.Instance{})
	if err != nil || len(args) != 0 {
		t.Fatalf("fabric GetLaunchArgs = %v, %v", args, err)
	}

	fg, err := NewLoader(TypeForge, "1.20.1")
	if err != nil || fg.Name() != TypeForge {
		t.Fatalf("forge loader = %v, %v", fg, err)
	}
	args, err = fg.GetLaunchArgs(context.Background(), &config.Instance{})
	if err != nil || len(args) != 0 {
		t.Fatalf("forge GetLaunchArgs = %v, %v", args, err)
	}
}

func TestNewLoaderRejectsUnsupported(t *testing.T) {
	tests := []struct {
		name    string
		typ     string
		version string
		want    string
	}{
		{"forge 低于 1.20.1", TypeForge, "1.19.2", "1.20.1"},
		{"forge 恰好 1.20", TypeForge, "1.20", "1.20.1"},
		{"forge 快照", TypeForge, "1.20.1-pre1", "无法识别"},
		{"neoforge 预留", TypeNeoForge, "1.20.1", "NeoForge"},
		{"quilt 未支持", TypeQuilt, "1.20.1", "quilt"},
		{"未知类型", "liteloader", "1.20.1", "liteloader"},
		{"空类型", "", "1.20.1", "缺少"},
		{"fabric 缺版本", TypeFabric, "", "版本"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewLoader(tt.typ, tt.version)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v，期望包含 %q", err, tt.want)
			}
		})
	}
}

func TestDetect(t *testing.T) {
	cases := map[string]string{
		"fabric-loader-0.15.0-1.20.1": TypeFabric,
		"1.20.1-forge-47.1.0":         TypeForge,
		"neoforge-21.1.0":             TypeNeoForge,
		"quilt-loader-0.20.0":         TypeQuilt,
		"legacy-profile":              TypeFabric, // 改动前的实例默认按 Fabric 兼容
	}
	for id, want := range cases {
		if got := Detect(id); got != want {
			t.Fatalf("Detect(%q) = %q, want %q", id, got, want)
		}
	}
}
