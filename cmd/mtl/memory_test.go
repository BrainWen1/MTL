package main

import (
	"bytes"
	"strings"
	"testing"
)

const testTotalMemory = int64(16) << 30

func TestBuildMemoryDefaultAndManual(t *testing.T) {
	mem, err := buildMemory("2G", false, false, testTotalMemory, 0, nil)
	if err != nil {
		t.Fatalf("默认 2G: %v", err)
	}
	if mem.Min != "512M" || mem.Max != "2G" {
		t.Fatalf("默认内存 = %+v，期望 Xms512M/Xmx2G", mem)
	}

	mem, err = buildMemory("3G", false, true, testTotalMemory, 0, nil)
	if err != nil {
		t.Fatalf("--memory 3G: %v", err)
	}
	if mem.Min != "768M" || mem.Max != "3G" {
		t.Fatalf("3G 内存 = %+v，期望 Xms768M/Xmx3G", mem)
	}
}

func TestBuildMemoryValidation(t *testing.T) {
	tests := []struct {
		value   string
		wantErr string
	}{
		{value: "abc", wantErr: "数字+单位"},
		{value: "4", wantErr: "数字+单位"},
		{value: "128M", wantErr: "256M"},
		{value: "15G", wantErr: "90%"},
	}
	for _, tt := range tests {
		if _, err := buildMemory(tt.value, false, true, testTotalMemory, 0, nil); err == nil ||
			!strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("--memory %s err = %v，应包含 %q", tt.value, err, tt.wantErr)
		}
	}
}

func TestBuildMemoryAuto(t *testing.T) {
	const total8G = int64(8) << 30
	var out bytes.Buffer
	mem, err := buildMemory("4G", true, true, total8G, 6<<30, &out)
	if err != nil {
		t.Fatalf("自动模式: %v", err)
	}
	if mem.Max != "3276M" {
		t.Fatalf("8G 主机自动 Xmx = %q，期望 3276M", mem.Max)
	}
	if mem.Min != "819M" {
		t.Fatalf("8G 主机自动 Xms = %q，期望 819M", mem.Min)
	}
	log := out.String()
	if !strings.Contains(log, "忽略 --memory=4G") {
		t.Fatalf("自动模式应提示忽略 --memory:\n%s", log)
	}
	if !strings.Contains(log, "推荐 Xmx=3276M") {
		t.Fatalf("自动模式日志缺少推荐值:\n%s", log)
	}

	mem, err = buildMemory("2G", true, false, testTotalMemory, 0, nil)
	if err != nil {
		t.Fatalf("16G 主机自动模式: %v", err)
	}
	if mem.Max != "6G" {
		t.Fatalf("16G 主机自动 Xmx = %q，期望 6G", mem.Max)
	}

	if _, err := buildMemory("2G", true, false, 0, 0, nil); err == nil {
		t.Fatal("无法读取物理内存时自动模式应报错")
	}
}
