package crash

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDiagnoseCrashLog(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "mixin apply failed",
			content: "java.lang.RuntimeException: Mixin apply failed: foo.mixins.json",
			want:    []string{diagMixin},
		},
		{
			name:    "mixin injection",
			content: "Mixin injection failed in mixin from mod a",
			want:    []string{diagMixin},
		},
		{
			name:    "mod id already registered",
			content: "java.lang.IllegalStateException: mod ID already registered: examplemod",
			want:    []string{diagDupMod},
		},
		{
			name:    "duplicate mod id",
			content: "duplicate mod id: examplemod found twice",
			want:    []string{diagDupMod},
		},
		{
			name:    "requires mod",
			content: "Mod example requires mod fabric-api, which is missing!",
			want:    []string{diagMissingDep},
		},
		{
			name:    "dependency not found",
			content: "dependency not found: cloth-config",
			want:    []string{diagMissingDep},
		},
		{
			name:    "missing dependency",
			content: "missing dependency for mod example",
			want:    []string{diagMissingDep},
		},
		{
			name: "noclassdef with mod package",
			content: "java.lang.NoClassDefFoundError: com/example/mymod/FooBar\n" +
				"\tat com.example.mymod.FooBar.<init>(FooBar.java:10)",
			want: []string{diagClassMissing},
		},
		{
			name:    "noclassdef with fabric package",
			content: "java.lang.NoClassDefFoundError: net/fabricmc/api/EnvType",
			want:    []string{diagClassMissing},
		},
		{
			name:    "noclassdef without mod context",
			content: "java.lang.NoClassDefFoundError: java/sql/DriverManager",
			want:    []string{diagUnknown},
		},
		{
			name:    "wrong minecraft version",
			content: "wrong minecraft version: mod needs 1.20.4",
			want:    []string{diagVersionMismatch},
		},
		{
			name:    "minecraft version mismatch",
			content: "Minecraft version mismatch: expected 1.20.1",
			want:    []string{diagVersionMismatch},
		},
		{
			name:    "oculus render mod",
			content: "Caused by: oculus render thread crashed",
			want:    []string{diagRenderConflict},
		},
		{
			name:    "rubidium render mod",
			content: "Rubidium failed to initialize",
			want:    []string{diagRenderConflict},
		},
		{
			name:    "sodium and iris alone do not trigger",
			content: "Sodium and Iris loaded normally",
			want:    []string{diagUnknown},
		},
		{
			name:    "multiple rules hit in priority order",
			content: "Mixin apply failed\nmod ID already registered: examplemod",
			want:    []string{diagMixin, diagDupMod},
		},
		{
			name:    "empty log",
			content: "",
			want:    []string{diagUnknown},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DiagnoseCrashLog(tt.content)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("DiagnoseCrashLog() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestEnsureArchiveDir(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureArchiveDir(dir); err != nil {
		t.Fatalf("EnsureArchiveDir() error = %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, archiveDirName))
	if err != nil {
		t.Fatalf("stat archive dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s 不是目录", archiveDirName)
	}
}

func TestCollectArchivesNewCrashLog(t *testing.T) {
	dir := t.TempDir()
	baseline := TakeSnapshot(dir)

	srcDir := filepath.Join(dir, ".minecraft", "crash-reports")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const content = "java.lang.RuntimeException: Mixin apply failed: foo.mixins.json\n"
	src := filepath.Join(srcDir, "crash-2026-10-24_22.30.15-client.txt")
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	stamp := time.Date(2026, 10, 24, 22, 30, 15, 0, time.UTC)
	reports, warnings := Collect(Options{
		InstanceDir: dir,
		Baseline:    baseline,
		Now:         func() time.Time { return stamp },
	})
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}

	r := reports[0]
	if r.Source != src {
		t.Fatalf("Source = %q, want %q", r.Source, src)
	}
	wantName := "crash-20261024T223015-2026-10-24_22.30.15-client.txt"
	if filepath.Base(r.Archived) != wantName {
		t.Fatalf("Archived = %q, want basename %q", r.Archived, wantName)
	}
	if r.Archived == "" || !filepath.IsAbs(r.Archived) {
		t.Fatalf("Archived = %q, want absolute path", r.Archived)
	}
	if !reflect.DeepEqual(r.Diagnoses, []string{diagMixin}) {
		t.Fatalf("Diagnoses = %#v", r.Diagnoses)
	}

	archived, err := os.ReadFile(r.Archived)
	if err != nil {
		t.Fatal(err)
	}
	text := string(archived)
	if !strings.HasPrefix(text, "[Crash] ") {
		t.Fatalf("归档文件未以诊断摘要开头: %q", text[:min(40, len(text))])
	}
	if !strings.Contains(text, diagMixin) {
		t.Fatalf("归档文件缺少诊断摘要: %q", text)
	}
	if !strings.Contains(text, content) {
		t.Fatalf("归档文件缺少原始日志")
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("源崩溃日志不应被移动或删除: %v", err)
	}
}

func TestCollectArchivesRootCrashReports(t *testing.T) {
	// mtl 把实例目录直接作为游戏目录，真实崩溃日志位于 <instance>/crash-reports。
	dir := t.TempDir()
	baseline := TakeSnapshot(dir)
	srcDir := filepath.Join(dir, "crash-reports")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(srcDir, "crash-2026-10-24_22.31.00-server.txt")
	if err := os.WriteFile(src, []byte("duplicate mod id: examplemod"), 0o644); err != nil {
		t.Fatal(err)
	}

	reports, warnings := Collect(Options{InstanceDir: dir, Baseline: baseline})
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}
	if !reflect.DeepEqual(reports[0].Diagnoses, []string{diagDupMod}) {
		t.Fatalf("Diagnoses = %#v", reports[0].Diagnoses)
	}
}

func TestCollectSkipsFilesPresentInBaseline(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "crash-reports")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "crash-old.txt"), []byte("old crash"), 0o644); err != nil {
		t.Fatal(err)
	}

	reports, warnings := Collect(Options{InstanceDir: dir, Baseline: TakeSnapshot(dir)})
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(reports) != 0 {
		t.Fatalf("reports = %d, want 0（旧日志不应重复归档）", len(reports))
	}
}

func TestCollectArchivesAllNewLogsWithUniqueNames(t *testing.T) {
	dir := t.TempDir()
	baseline := TakeSnapshot(dir)
	srcDir := filepath.Join(dir, "crash-reports")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"crash-a-client.txt", "crash-b-client.txt"} {
		if err := os.WriteFile(filepath.Join(srcDir, name), []byte("No crash keyword here"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	stamp := time.Date(2026, 10, 24, 22, 30, 15, 0, time.UTC)
	reports, warnings := Collect(Options{
		InstanceDir: dir,
		Baseline:    baseline,
		Now:         func() time.Time { return stamp },
	})
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(reports) != 2 {
		t.Fatalf("reports = %d, want 2", len(reports))
	}
	seen := map[string]bool{}
	for _, r := range reports {
		if r.Archived == "" {
			t.Fatalf("归档路径为空: %#v", r)
		}
		if seen[r.Archived] {
			t.Fatalf("归档路径重复: %s", r.Archived)
		}
		seen[r.Archived] = true
		if _, err := os.Stat(r.Archived); err != nil {
			t.Fatalf("归档文件不存在: %v", err)
		}
		if !reflect.DeepEqual(r.Diagnoses, []string{diagUnknown}) {
			t.Fatalf("Diagnoses = %#v", r.Diagnoses)
		}
	}
}

func TestCollectDoesNotOverwriteExistingArchive(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "crash-reports")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(srcDir, "crash-a-client.txt")
	if err := os.WriteFile(src, []byte("Mixin apply failed"), 0o644); err != nil {
		t.Fatal(err)
	}

	stamp := time.Date(2026, 10, 24, 22, 30, 15, 0, time.UTC)
	opts := Options{InstanceDir: dir, Baseline: Snapshot{}, Now: func() time.Time { return stamp }}
	first, warnings := Collect(opts)
	if len(warnings) != 0 || len(first) != 1 {
		t.Fatalf("first Collect: reports=%d warnings=%v", len(first), warnings)
	}
	second, warnings := Collect(opts)
	if len(warnings) != 0 || len(second) != 1 {
		t.Fatalf("second Collect: reports=%d warnings=%v", len(second), warnings)
	}
	if first[0].Archived == second[0].Archived {
		t.Fatalf("第二次归档覆盖了已有文件: %s", first[0].Archived)
	}
	if _, err := os.Stat(second[0].Archived); err != nil {
		t.Fatalf("第二次归档文件不存在: %v", err)
	}
}
