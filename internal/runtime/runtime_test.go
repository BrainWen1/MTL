package runtime

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func testSHA1(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestPlatformKey(t *testing.T) {
	cases := []struct {
		goos, goarch string
		want         string
		ok           bool
	}{
		{"windows", "amd64", "windows-x64", true},
		{"windows", "386", "windows-x86", true},
		{"windows", "arm64", "windows-arm64", true},
		{"darwin", "amd64", "mac-os", true},
		{"darwin", "arm64", "mac-os-arm64", true},
		{"linux", "amd64", "linux", true},
		{"linux", "386", "linux-i386", true},
		{"linux", "arm64", "", false},
		{"freebsd", "amd64", "", false},
	}
	for _, c := range cases {
		got, err := platformKey(c.goos, c.goarch)
		if c.ok {
			if err != nil || got != c.want {
				t.Fatalf("platformKey(%s,%s) = %q, %v, want %q", c.goos, c.goarch, got, err, c.want)
			}
		} else if err == nil {
			t.Fatalf("platformKey(%s,%s) 应报错", c.goos, c.goarch)
		}
	}
}

func TestComponentForMajor(t *testing.T) {
	cases := map[int]string{
		8:  "jre-legacy",
		16: "java-runtime-alpha",
		17: "java-runtime-gamma",
		21: "java-runtime-delta",
		25: "java-runtime-epsilon",
	}
	for major, want := range cases {
		got, ok := componentForMajor(major)
		if !ok || got != want {
			t.Fatalf("componentForMajor(%d) = %q, %v, want %q", major, got, ok, want)
		}
	}
	for _, major := range []int{0, 11, 18, 26} {
		if got, ok := componentForMajor(major); ok {
			t.Fatalf("componentForMajor(%d) = %q, 应返回 false", major, got)
		}
	}
}

func TestDefaultDir(t *testing.T) {
	if goruntime.GOOS == "windows" {
		appData := filepath.Join(t.TempDir(), "AppData", "Roaming")
		t.Setenv("APPDATA", appData)
		got, err := DefaultDir()
		if err != nil {
			t.Fatalf("DefaultDir: %v", err)
		}
		want := filepath.Join(appData, "mtl", "cache", "runtime")
		if got != want {
			t.Fatalf("DefaultDir = %q, want %q", got, want)
		}
		return
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir: %v", err)
	}
	want := filepath.Join(home, ".cache", "mtl", "runtime")
	if got != want {
		t.Fatalf("DefaultDir = %q, want %q", got, want)
	}
}

func TestBuildPlanRejectsUnsafeOrMalformedPaths(t *testing.T) {
	const rawURL = "https://example.com/f"
	goodFile := manifestEntry{Type: "file", Downloads: map[string]manifestDownload{
		"raw": {SHA1: testSHA1("x"), Size: 1, URL: rawURL},
	}}
	cases := []struct {
		name string
		rel  string
		ent  manifestEntry
	}{
		{"traversal", "../escape", goodFile},
		{"absolute", "/etc/passwd", goodFile},
		{"backslash", `bin\java.exe`, goodFile},
		{"missing raw", "bin/java.exe", manifestEntry{Type: "file"}},
		{"bad sha1", "bin/java.exe", manifestEntry{Type: "file", Downloads: map[string]manifestDownload{
			"raw": {SHA1: "xyz", Size: 1, URL: rawURL},
		}}},
		{"link traversal", "legal/a", manifestEntry{Type: "link", Target: "../../escape"}},
		{"link absolute", "legal/a", manifestEntry{Type: "link", Target: "/etc/passwd"}},
		{"unknown type", "bin/java.exe", manifestEntry{Type: "device"}},
	}
	for _, c := range cases {
		m := &manifest{Files: map[string]manifestEntry{c.rel: c.ent}}
		if _, err := buildPlan(m, "windows-x64"); err == nil {
			t.Fatalf("%s: 应拒绝 %+v", c.name, c.ent)
		}
	}
}

func TestBuildPlanFindsJavaAndLinks(t *testing.T) {
	java := manifestEntry{Type: "file", Executable: true, Downloads: map[string]manifestDownload{
		"raw": {SHA1: testSHA1("java"), Size: 4, URL: "https://example.com/java"},
	}}
	m := &manifest{Files: map[string]manifestEntry{
		"bin":                         {Type: "directory"},
		"bin/java.exe":                java,
		"legal/java.compiler/LICENSE": {Type: "link", Target: "../java.base/LICENSE"},
	}}
	p, err := buildPlan(m, "windows-x64")
	if err != nil {
		t.Fatalf("buildPlan: %v", err)
	}
	if p.javaRel != "bin/java.exe" {
		t.Fatalf("javaRel = %q", p.javaRel)
	}
	if len(p.links) != 1 || p.links[0].resolved != "legal/java.base/LICENSE" {
		t.Fatalf("links = %+v", p.links)
	}

	mac := &manifest{Files: map[string]manifestEntry{
		"jre.bundle/Contents/Home/bin/java": java,
	}}
	p, err = buildPlan(mac, "mac-os-arm64")
	if err != nil {
		t.Fatalf("buildPlan mac: %v", err)
	}
	if p.javaRel != "jre.bundle/Contents/Home/bin/java" {
		t.Fatalf("mac javaRel = %q", p.javaRel)
	}
}

type testRuntimeServer struct {
	srv          *httptest.Server
	mu           sync.Mutex
	allHits      int
	manifestHits int
	fileHits     map[string]int
}

func newTestRuntimeServer(t *testing.T) *testRuntimeServer {
	t.Helper()
	s := &testRuntimeServer{fileHits: make(map[string]int)}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		switch r.URL.Path {
		case "/all.json":
			s.mu.Lock()
			s.allHits++
			s.mu.Unlock()
			mb := testRuntimeManifest(base)
			body := fmt.Sprintf(`{"windows-x64":{"java-runtime-gamma":[{"availability":{"progress":100},"manifest":{"sha1":%q,"size":%d,"url":%q},"version":{"name":"17.0.15"}}]}}`,
				testSHA1(mb), len(mb), base+"/manifest.json")
			_, _ = w.Write([]byte(body))
		case "/manifest.json":
			s.mu.Lock()
			s.manifestHits++
			s.mu.Unlock()
			_, _ = w.Write([]byte(testRuntimeManifest(base)))
		case "/files/java.exe":
			s.mu.Lock()
			s.fileHits["java"]++
			s.mu.Unlock()
			_, _ = w.Write([]byte("fake-java-binary"))
		case "/files/readme.txt":
			s.mu.Lock()
			s.fileHits["readme"]++
			s.mu.Unlock()
			_, _ = w.Write([]byte("library"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func testRuntimeManifest(base string) string {
	return fmt.Sprintf(`{"files":{
		"bin":{"type":"directory"},
		"bin/java.exe":{"type":"file","executable":true,"downloads":{"raw":{"sha1":%q,"size":%d,"url":%q}}},
		"lib":{"type":"directory"},
		"lib/readme.txt":{"type":"file","downloads":{"raw":{"sha1":%q,"size":%d,"url":%q}}}
	}}`,
		testSHA1("fake-java-binary"), len("fake-java-binary"), base+"/files/java.exe",
		testSHA1("library"), len("library"), base+"/files/readme.txt")
}

func TestEnsureDownloadsAndReusesRuntime(t *testing.T) {
	srv := newTestRuntimeServer(t)
	dir := t.TempDir()

	var mu sync.Mutex
	var logs []string
	logFn := func(msg string) {
		mu.Lock()
		logs = append(logs, msg)
		mu.Unlock()
	}
	opts := Options{
		Dir:        dir,
		TargetOS:   "windows",
		TargetArch: "amd64",
		MetaURL:    srv.srv.URL + "/all.json",
		Retries:    -1,
		RetryDelay: time.Millisecond,
		Progress:   logFn,
	}
	spec := Spec{Component: "java-runtime-gamma"}

	javaPath, err := Ensure(context.Background(), spec, opts)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	wantPath := filepath.Join(dir, "java-runtime-gamma", "bin", "java.exe")
	if javaPath != wantPath {
		t.Fatalf("javaPath = %q, want %q", javaPath, wantPath)
	}
	for _, p := range []string{
		wantPath,
		filepath.Join(dir, "java-runtime-gamma", "lib", "readme.txt"),
		filepath.Join(dir, "java-runtime-gamma", markerName),
		filepath.Join(dir, "manifests", "java-runtime-gamma-"+testSHA1(testRuntimeManifest(srv.srv.URL))+".json"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("缺少文件 %s: %v", p, err)
		}
	}
	mu.Lock()
	joined := strings.Join(logs, "\n")
	mu.Unlock()
	for _, want := range []string{
		"正在检查 Java 运行时：java-runtime-gamma（windows-x64）",
		"开始下载 Java 运行时文件：共 2 个",
		"下载 Java 运行时文件：2/2（100%）",
		"Java 运行时安装完成：java-runtime-gamma（17.0.15）",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("日志缺少 %q:\n%s", want, joined)
		}
	}

	mu.Lock()
	logs = nil
	mu.Unlock()
	if _, err := Ensure(context.Background(), spec, opts); err != nil {
		t.Fatalf("二次 Ensure: %v", err)
	}
	mu.Lock()
	joined = strings.Join(logs, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "Java 运行时校验通过，跳过下载：java-runtime-gamma") {
		t.Fatalf("二次执行缺少跳过日志:\n%s", joined)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.allHits != 2 || srv.manifestHits != 1 || srv.fileHits["java"] != 1 || srv.fileHits["readme"] != 1 {
		t.Fatalf("请求次数 all=%d manifest=%d java=%d readme=%d, want 2/1/1/1",
			srv.allHits, srv.manifestHits, srv.fileHits["java"], srv.fileHits["readme"])
	}
}

func TestEnsureFallsBackToCachedRuntime(t *testing.T) {
	srv := newTestRuntimeServer(t)
	dir := t.TempDir()
	opts := Options{
		Dir:        dir,
		TargetOS:   "windows",
		TargetArch: "amd64",
		MetaURL:    srv.srv.URL + "/all.json",
		Retries:    -1,
		RetryDelay: time.Millisecond,
	}
	spec := Spec{Component: "java-runtime-gamma"}
	first, err := Ensure(context.Background(), spec, opts)
	if err != nil {
		t.Fatalf("首次 Ensure: %v", err)
	}
	srv.srv.Close()

	var mu sync.Mutex
	var logs []string
	opts.Progress = func(msg string) {
		mu.Lock()
		logs = append(logs, msg)
		mu.Unlock()
	}
	got, err := Ensure(context.Background(), spec, opts)
	if err != nil {
		t.Fatalf("离线 Ensure 应复用缓存: %v", err)
	}
	if got != first {
		t.Fatalf("离线 javaPath = %q, want %q", got, first)
	}
	mu.Lock()
	joined := strings.Join(logs, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "使用本地缓存") {
		t.Fatalf("缺少离线回退日志:\n%s", joined)
	}
}

func TestEnsureRejectsInvalidSpecBeforeNetwork(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		spec Spec
		opts Options
	}{
		{"no requirement", Spec{}, Options{Dir: dir, TargetOS: "windows", TargetArch: "amd64"}},
		{"bad component", Spec{Component: "../escape"}, Options{Dir: dir, TargetOS: "windows", TargetArch: "amd64"}},
		{"unsupported platform", Spec{Component: "java-runtime-gamma"}, Options{Dir: dir, TargetOS: "linux", TargetArch: "arm64"}},
	}
	for _, c := range cases {
		if _, err := Ensure(context.Background(), c.spec, c.opts); err == nil {
			t.Fatalf("%s: 应在联网前报错", c.name)
		}
	}
}

func TestEnsureReportsMissingComponent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"windows-x64":{"java-runtime-gamma":[]}}`))
	}))
	defer srv.Close()

	_, err := Ensure(context.Background(), Spec{Component: "java-runtime-gamma"}, Options{
		Dir:        t.TempDir(),
		TargetOS:   "windows",
		TargetArch: "amd64",
		MetaURL:    srv.URL,
		Retries:    -1,
	})
	if err == nil || !strings.Contains(err.Error(), "没有可用的官方 Java 运行时组件") {
		t.Fatalf("应提示无可用运行时: %v", err)
	}
}
