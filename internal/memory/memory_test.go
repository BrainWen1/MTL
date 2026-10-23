package memory

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr string
	}{
		{in: "4G", want: 4 * GiB},
		{in: "512M", want: 512 * MiB},
		{in: " 2g ", want: 2 * GiB},
		{in: "1.5G", want: 1536 * MiB},
		{in: "256m", want: MinHeap},
		{in: "4", wantErr: "数字+单位"},
		{in: "abc", wantErr: "数字+单位"},
		{in: "4T", wantErr: "数字+单位"},
		{in: "1.2.3G", wantErr: "数字+单位"},
		{in: "", wantErr: "不能为空"},
		{in: "-1G", wantErr: "必须大于 0"},
		{in: "0M", wantErr: "必须大于 0"},
		{in: "0.0001M", wantErr: "最小单位为 1M"},
	}
	for _, tt := range tests {
		got, err := Parse(tt.in)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Parse(%q) err = %v，应包含 %q", tt.in, err, tt.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("Parse(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Parse(%q) = %d，期望 %d", tt.in, got, tt.want)
		}
	}
}

func TestFormat(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{in: 4 * GiB, want: "4G"},
		{in: 2 * GiB, want: "2G"},
		{in: 512 * MiB, want: "512M"},
		{in: 1536 * MiB, want: "1536M"},
		{in: GiB + MiB, want: "1025M"},
		{in: MiB + 1, want: "2M"},
	}
	for _, tt := range tests {
		if got := Format(tt.in); got != tt.want {
			t.Errorf("Format(%d) = %q，期望 %q", tt.in, got, tt.want)
		}
	}
}

func TestValidate(t *testing.T) {
	const total = 10 * GiB
	if err := Validate(2*GiB, total); err != nil {
		t.Fatalf("2G 不应被拒绝: %v", err)
	}
	// 恰好 90% 允许。
	if err := Validate(total/10*9, total); err != nil {
		t.Fatalf("恰好 90%% 不应被拒绝: %v", err)
	}
	if err := Validate(total/10*9+MiB, total); err == nil || !strings.Contains(err.Error(), "90%") {
		t.Fatalf("超过 90%% 应报错: %v", err)
	}
	if err := Validate(128*MiB, total); err == nil || !strings.Contains(err.Error(), "256M") {
		t.Fatalf("小于 256M 应报错: %v", err)
	}
	// 物理内存未知时只做最小值校验。
	if err := Validate(2*GiB, 0); err != nil {
		t.Fatalf("total 未知时 2G 不应被拒绝: %v", err)
	}
	if err := Validate(128*MiB, 0); err == nil {
		t.Fatal("total 未知时仍应检查最小值")
	}
}

func TestAutoMax(t *testing.T) {
	tests := []struct {
		total int64
		want  int64
	}{
		{total: 8 * GiB, want: 3276 * MiB}, // 3.2G
		{total: 16 * GiB, want: 6 * GiB},   // 40% = 6.4G，受 6G 上限约束
		{total: 12 * GiB, want: 4915 * MiB},
		{total: 32 * GiB, want: 8 * GiB}, // 大于 16G 上限 8G
		{total: 64 * GiB, want: 8 * GiB},
		{total: 4 * GiB, want: 1638 * MiB},
		{total: 512 * MiB, want: MinHeap}, // 40% 低于下限时抬到 256M
		{total: 0, want: 0},
	}
	for _, tt := range tests {
		if got := AutoMax(tt.total); got != tt.want {
			t.Errorf("AutoMax(%d) = %d (%s)，期望 %d (%s)",
				tt.total, got, Format(got), tt.want, Format(tt.want))
		}
	}
}

func TestNewAllocation(t *testing.T) {
	tests := []struct {
		max     int64
		wantMin int64
	}{
		{max: 4 * GiB, wantMin: 1 * GiB},
		{max: 2 * GiB, wantMin: 512 * MiB},
		{max: 1 * GiB, wantMin: MinHeap},
		{max: MinHeap, wantMin: MinHeap},
	}
	for _, tt := range tests {
		a := NewAllocation(tt.max)
		if a.Min != tt.wantMin || a.Max != tt.max {
			t.Errorf("NewAllocation(%d) = {%d, %d}，期望 {%d, %d}",
				tt.max, a.Min, a.Max, tt.wantMin, tt.max)
		}
	}
	if got := NewAllocation(2 * GiB).Xms(); got != "512M" {
		t.Errorf("Xms = %q，期望 512M", got)
	}
	if got := NewAllocation(4 * GiB).Xmx(); got != "4G" {
		t.Errorf("Xmx = %q，期望 4G", got)
	}
}
