package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, DirName, FileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load 缺失配置: %v", err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Fatalf("缺失配置 = %+v，期望默认 %+v", cfg, Default())
	}
	if cfg.JVMArgs == nil || cfg.GameArgs == nil {
		t.Fatal("默认参数应为空切片而不是 nil")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := Config{
		MemoryMode:        ModeManual,
		ManualMemory:      "4G",
		JVMArgs:           []string{"-XX:-OmitStackTraceInFastThrow"},
		GameArgs:          []string{"--noChat"},
		PreferHighPerfGPU: true,
	}
	if err := Save(dir, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path := filepath.Join(dir, DirName, FileName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("配置未写入 %s: %v", path, err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load = %+v，期望 %+v", got, want)
	}
}

func TestSaveIdempotent(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{MemoryMode: ModeAuto, JVMArgs: []string{"-Dfoo=1"}, GameArgs: []string{}}
	if err := Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, DirName, FileName)
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("重复保存内容不一致:\n%s\n---\n%s", first, second)
	}
}

func TestLoadCorruptedFallsBackToDefault(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"memory_mode": "manual", `)

	cfg, err := Load(dir)
	if err == nil {
		t.Fatal("损坏配置应返回错误")
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Fatalf("损坏配置回退 = %+v，期望默认 %+v", cfg, Default())
	}
}

func TestLoadInvalidValuesFallBackToDefault(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"未知内存模式", `{"memory_mode":"weird"}`},
		{"手动模式缺内存值", `{"memory_mode":"manual","manual_memory":""}`},
		{"手动模式内存格式错误", `{"memory_mode":"manual","manual_memory":"4X"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, tt.content)
			cfg, err := Load(dir)
			if err == nil {
				t.Fatal("非法配置应返回错误")
			}
			if !reflect.DeepEqual(cfg, Default()) {
				t.Fatalf("非法配置回退 = %+v，期望默认 %+v", cfg, Default())
			}
		})
	}
}

func TestLoadPartialAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"jvm_args":["-Dfoo=1"]}`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MemoryMode != ModeAuto || cfg.ManualMemory != "" {
		t.Fatalf("缺失字段未回落默认: %+v", cfg)
	}
	if len(cfg.GameArgs) != 0 || cfg.GameArgs == nil {
		t.Fatalf("game_args = %v，期望非 nil 空切片", cfg.GameArgs)
	}
	if !reflect.DeepEqual(cfg.JVMArgs, []string{"-Dfoo=1"}) {
		t.Fatalf("jvm_args = %v", cfg.JVMArgs)
	}
}

func TestArgAddRemove(t *testing.T) {
	cfg := Default()
	if !cfg.AddJVMArg("-XX:+UseG1GC") {
		t.Fatal("首次添加应成功")
	}
	if cfg.AddJVMArg("-XX:+UseG1GC") {
		t.Fatal("重复添加不应改变配置")
	}
	cfg.AddJVMArg("-Dfoo=1")
	cfg.AddGameArg("--noChat")
	cfg.AddGameArg("--noChat")

	if !reflect.DeepEqual(cfg.JVMArgs, []string{"-XX:+UseG1GC", "-Dfoo=1"}) {
		t.Fatalf("jvm_args = %v", cfg.JVMArgs)
	}
	if !reflect.DeepEqual(cfg.GameArgs, []string{"--noChat"}) {
		t.Fatalf("game_args = %v", cfg.GameArgs)
	}

	if !cfg.RemoveJVMArg("-XX:+UseG1GC") {
		t.Fatal("删除已存在参数应成功")
	}
	if cfg.RemoveJVMArg("-XX:+UseG1GC") {
		t.Fatal("删除不存在参数应返回 false")
	}
	if !cfg.RemoveGameArg("--noChat") {
		t.Fatal("删除游戏参数应成功")
	}
	if !reflect.DeepEqual(cfg.JVMArgs, []string{"-Dfoo=1"}) || len(cfg.GameArgs) != 0 {
		t.Fatalf("删除后 jvm=%v game=%v", cfg.JVMArgs, cfg.GameArgs)
	}
}

func TestMergeArgs(t *testing.T) {
	base := []string{"-Dconfig=1", "-Xss1M"}
	extra := []string{"-Dcli=2"}
	got := MergeArgs(base, extra)
	want := []string{"-Dconfig=1", "-Xss1M", "-Dcli=2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MergeArgs = %v，期望 %v", got, want)
	}
	if !reflect.DeepEqual(base, []string{"-Dconfig=1", "-Xss1M"}) || !reflect.DeepEqual(extra, []string{"-Dcli=2"}) {
		t.Fatal("MergeArgs 不应修改入参")
	}
	if got := MergeArgs(nil, nil); got == nil || len(got) != 0 {
		t.Fatalf("空合并 = %v，期望非 nil 空切片", got)
	}
}

func TestEffectiveMemoryPriority(t *testing.T) {
	manualCfg := Config{MemoryMode: ModeManual, ManualMemory: "4G"}
	autoCfg := Default()

	tests := []struct {
		name       string
		cfg        Config
		cliValue   string
		cliSet     bool
		cliAuto    bool
		wantValue  string
		wantAuto   bool
		wantSet    bool
		wantSource string
	}{
		{"无命令行使用实例手动内存", manualCfg, "2G", false, false, "4G", false, true, "实例配置"},
		{"命令行内存覆盖实例手动内存", manualCfg, "2G", true, false, "2G", false, true, "命令行 --memory"},
		{"命令行自动内存覆盖实例手动内存", manualCfg, "2G", true, true, "2G", true, true, "命令行 --memory-auto"},
		{"实例自动模式", autoCfg, "2G", false, false, "", true, false, "实例配置/默认"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EffectiveMemory(tt.cfg, tt.cliValue, tt.cliSet, tt.cliAuto)
			if got.Value != tt.wantValue || got.Auto != tt.wantAuto || got.ValueSet != tt.wantSet {
				t.Fatalf("EffectiveMemory = %+v，期望 value=%q auto=%v set=%v",
					got, tt.wantValue, tt.wantAuto, tt.wantSet)
			}
			if !strings.Contains(got.Source, tt.wantSource) {
				t.Fatalf("Source = %q，期望包含 %q", got.Source, tt.wantSource)
			}
		})
	}
}

func TestEffectiveHighPerfGPU(t *testing.T) {
	tests := []struct {
		name       string
		cfg        Config
		cliValue   bool
		cliSet     bool
		want       bool
		wantSource string
	}{
		{"默认关闭", Default(), true, false, false, "默认"},
		{"实例配置开启", Config{MemoryMode: ModeAuto, PreferHighPerfGPU: true}, false, false, true, "实例配置"},
		{"命令行开启覆盖实例关闭", Default(), true, true, true, "命令行"},
		{"命令行关闭覆盖实例开启", Config{MemoryMode: ModeAuto, PreferHighPerfGPU: true}, false, true, false, "命令行"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, source := EffectiveHighPerfGPU(tt.cfg, tt.cliValue, tt.cliSet)
			if got != tt.want {
				t.Fatalf("EffectiveHighPerfGPU = %v，期望 %v", got, tt.want)
			}
			if !strings.Contains(source, tt.wantSource) {
				t.Fatalf("Source = %q，期望包含 %q", source, tt.wantSource)
			}
		})
	}
}

func TestFormatIncludesHighPerfGPUField(t *testing.T) {
	text := Format(Default())
	if !strings.Contains(text, `"prefer_high_perf_gpu": false`) {
		t.Fatalf("Format 未包含 prefer_high_perf_gpu:\n%s", text)
	}
}

func TestPathUsesPrivateDir(t *testing.T) {
	dir := t.TempDir()
	p, err := Path(dir)
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	want := filepath.Join(dir, DirName, FileName)
	if p != want {
		t.Fatalf("Path = %q，期望 %q", p, want)
	}
	if _, err := Path(""); err == nil {
		t.Fatal("空实例目录应报错")
	}
}

func TestSaveRejectsInvalidConfig(t *testing.T) {
	err := Save(t.TempDir(), Config{MemoryMode: "weird"})
	if err == nil || !strings.Contains(err.Error(), "memory_mode") {
		t.Fatalf("Save 非法配置 err = %v", err)
	}
}
