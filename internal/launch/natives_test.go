package launch

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveLibrariesNativeFiltering(t *testing.T) {
	dir := t.TempDir()
	winRule := []Rule{{Action: "allow", OS: &OSRule{Name: "windows"}}}
	libs := []Library{
		{
			Name:      "com.example:plain:1.0",
			Downloads: LibraryDownloads{Artifact: &Download{Path: "com/example/plain-1.0.jar"}},
		},
		{
			Name:      "org.lwjgl:lwjgl:3.3.1:natives-windows",
			Downloads: LibraryDownloads{Artifact: &Download{Path: "org/lwjgl/lwjgl/3.3.1/lwjgl-3.3.1-natives-windows.jar"}},
			Rules:     winRule,
		},
		{
			Name:      "org.lwjgl:lwjgl:3.3.1:natives-windows-arm64",
			Downloads: LibraryDownloads{Artifact: &Download{Path: "org/lwjgl/lwjgl/3.3.1/lwjgl-3.3.1-natives-windows-arm64.jar"}},
			Rules:     winRule,
		},
		{
			Name:      "org.lwjgl:lwjgl:3.3.1:natives-windows-x86",
			Downloads: LibraryDownloads{Artifact: &Download{Path: "org/lwjgl/lwjgl/3.3.1/lwjgl-3.3.1-natives-windows-x86.jar"}},
			Rules:     winRule,
		},
		{
			Name: "org.lwjgl.lwjgl:lwjgl:2.9.4",
			Downloads: LibraryDownloads{
				Artifact: &Download{Path: "org/lwjgl/lwjgl/lwjgl/2.9.4/lwjgl-2.9.4.jar"},
				Classifiers: map[string]Download{
					"natives-windows-64": {Path: "org/lwjgl/lwjgl/lwjgl/2.9.4/lwjgl-2.9.4-natives-windows.jar"},
				},
			},
			Natives: map[string]string{"windows": "natives-windows-${arch}"},
		},
	}

	out, err := resolveLibraries(dir, "windows", "amd64", libs)
	if err != nil {
		t.Fatalf("resolveLibraries: %v", err)
	}

	jars := make(map[string]bool)
	natives := make(map[string]bool)
	for _, lf := range out {
		if lf.Jar != "" {
			jars[filepath.Base(lf.Jar)] = true
		}
		if lf.NativeJar != "" {
			natives[filepath.Base(lf.NativeJar)] = true
		}
	}
	// 旧版 natives 通过 classifiers 提供，只解压、不进 classpath；
	// 现代版每条 native 是独立 library，artifact 本身就是 classpath 条目。
	for _, want := range []string{
		"plain-1.0.jar",
		"lwjgl-3.3.1-natives-windows.jar",
		"lwjgl-2.9.4.jar",
	} {
		if !jars[want] {
			t.Fatalf("classpath 缺少 %s: %v", want, jars)
		}
	}
	if jars["lwjgl-2.9.4-natives-windows.jar"] {
		t.Fatalf("旧版 native classifier 不应进入 classpath: %v", jars)
	}
	for _, bad := range []string{"lwjgl-3.3.1-natives-windows-arm64.jar", "lwjgl-3.3.1-natives-windows-x86.jar"} {
		if jars[bad] {
			t.Fatalf("不匹配架构的 native 不应加入 classpath: %v", jars)
		}
	}
	if !natives["lwjgl-3.3.1-natives-windows.jar"] || !natives["lwjgl-2.9.4-natives-windows.jar"] {
		t.Fatalf("native 列表不正确: %v", natives)
	}
	if natives["lwjgl-3.3.1-natives-windows-arm64.jar"] || natives["lwjgl-3.3.1-natives-windows-x86.jar"] {
		t.Fatalf("不匹配架构的 native 不应被解压: %v", natives)
	}
}

func writeTestZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExtractNatives(t *testing.T) {
	jar := filepath.Join(t.TempDir(), "natives.jar")
	writeTestZip(t, jar, map[string]string{
		"libtest.dll":          "dll",
		"META-INF/MANIFEST.MF": "manifest",
	})
	dir, err := extractNatives([]string{jar})
	if err != nil {
		t.Fatalf("extractNatives: %v", err)
	}
	defer os.RemoveAll(dir)

	got, err := os.ReadFile(filepath.Join(dir, "libtest.dll"))
	if err != nil || string(got) != "dll" {
		t.Fatalf("native 未正确解压: %v, %q", err, got)
	}
	if _, err := os.Stat(filepath.Join(dir, "META-INF", "MANIFEST.MF")); err != nil {
		t.Fatalf("manifest 未解压: %v", err)
	}
}

func TestExtractNativesRejectsZipSlip(t *testing.T) {
	jar := filepath.Join(t.TempDir(), "evil.jar")
	writeTestZip(t, jar, map[string]string{"../evil.dll": "evil"})

	dir, err := extractNatives([]string{jar})
	if err == nil {
		os.RemoveAll(dir)
		t.Fatal("zip slip 条目应被拒绝")
	}
	if dir != "" {
		t.Fatalf("失败时应返回空目录，得到 %q", dir)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(jar), "evil.dll")); !os.IsNotExist(statErr) {
		t.Fatal("文件逃出了临时目录")
	}
	if strings.Contains(err.Error(), jar) == false {
		t.Fatalf("错误信息应包含 jar 路径: %v", err)
	}
}
