package fabric

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestNativeClassifierArch(t *testing.T) {
	cases := []struct {
		classifier string
		arch       string
		native     bool
	}{
		{"natives-windows", "amd64", true},
		{"natives-windows-x86", "386", true},
		{"natives-windows-arm64", "arm64", true},
		{"natives-windows-x86_64", "amd64", true},
		{"natives-windows-64", "amd64", true},
		{"natives-windows-32", "386", true},
		{"natives-macos", "amd64", true},
		{"natives-macos-arm64", "arm64", true},
		{"natives-linux", "amd64", true},
		{"natives-linux-arm64", "arm64", true},
		{"natives-foo", "", false},
		{"sources", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		arch, native := nativeClassifierArch(c.classifier)
		if arch != c.arch || native != c.native {
			t.Fatalf("nativeClassifierArch(%q) = (%q, %v), want (%q, %v)",
				c.classifier, arch, native, c.arch, c.native)
		}
	}
}

func nativeFixtureLib(classifier, osName, body string) mojangLibrary {
	return mojangLibrary{
		Name: "org.lwjgl:lwjgl:3.3.1:" + classifier,
		Downloads: mojangDownloads{Artifact: &mojangArtifact{
			Path: "lwjgl/" + classifier + ".jar",
			URL:  "https://example.com/" + classifier + ".jar",
			SHA1: sha1Hex(body),
			Size: int64(len(body)),
		}},
		Rules: []mojangRule{{Action: "allow", OS: &mojangOS{Name: osName}}},
	}
}

func lwjglNativesFixture() []mojangLibrary {
	base := mojangLibrary{
		Name: "org.lwjgl:lwjgl:3.3.1",
		Downloads: mojangDownloads{Artifact: &mojangArtifact{
			Path: "org/lwjgl/lwjgl/3.3.1/lwjgl-3.3.1.jar",
			URL:  "https://example.com/lwjgl.jar",
			SHA1: sha1Hex("base"),
			Size: 4,
		}},
	}
	n := func(classifier, osName string) mojangLibrary {
		return nativeFixtureLib(classifier, osName, classifier)
	}
	return []mojangLibrary{
		base,
		n("natives-windows", "windows"),
		n("natives-windows-x86", "windows"),
		n("natives-windows-arm64", "windows"),
		n("natives-macos", "osx"),
		n("natives-macos-arm64", "osx"),
		n("natives-linux", "linux"),
	}
}

func TestVanillaNativesFilteredByArch(t *testing.T) {
	const base = "org.lwjgl:lwjgl:3.3.1"
	cases := []struct {
		name    string
		goos    string
		goarch  string
		want    []string
		skipped []string
	}{
		{
			name: "windows/amd64", goos: "windows", goarch: "amd64",
			want:    []string{base, "org.lwjgl:lwjgl:3.3.1:natives-windows"},
			skipped: []string{"org.lwjgl:lwjgl:3.3.1:natives-windows-x86", "org.lwjgl:lwjgl:3.3.1:natives-windows-arm64"},
		},
		{
			name: "windows/arm64", goos: "windows", goarch: "arm64",
			want:    []string{base, "org.lwjgl:lwjgl:3.3.1:natives-windows-arm64"},
			skipped: []string{"org.lwjgl:lwjgl:3.3.1:natives-windows", "org.lwjgl:lwjgl:3.3.1:natives-windows-x86"},
		},
		{
			name: "windows/386", goos: "windows", goarch: "386",
			want:    []string{base, "org.lwjgl:lwjgl:3.3.1:natives-windows-x86"},
			skipped: []string{"org.lwjgl:lwjgl:3.3.1:natives-windows", "org.lwjgl:lwjgl:3.3.1:natives-windows-arm64"},
		},
		{
			name: "linux/amd64", goos: "linux", goarch: "amd64",
			want: []string{base, "org.lwjgl:lwjgl:3.3.1:natives-linux"},
		},
		{
			name: "linux/arm64", goos: "linux", goarch: "arm64",
			want:    []string{base},
			skipped: []string{"org.lwjgl:lwjgl:3.3.1:natives-linux"},
		},
		{
			name: "darwin/arm64", goos: "darwin", goarch: "arm64",
			want:    []string{base, "org.lwjgl:lwjgl:3.3.1:natives-macos-arm64"},
			skipped: []string{"org.lwjgl:lwjgl:3.3.1:natives-macos"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var logs []string
			rep := &reporter{fn: func(msg string) { logs = append(logs, msg) }}
			arts, err := vanillaArtifacts(mojangVersion{Libraries: lwjglNativesFixture()}, c.goos, c.goarch, rep)
			if err != nil {
				t.Fatalf("vanillaArtifacts: %v", err)
			}

			got := make(map[string]bool)
			for _, a := range arts {
				got[a.display] = true
			}
			if len(arts) != len(c.want) {
				t.Fatalf("构件数 = %d (%v), want %d", len(arts), got, len(c.want))
			}
			for _, name := range c.want {
				if !got[name] {
					t.Fatalf("缺少构件 %s，实际 %v", name, got)
				}
			}

			if len(logs) != len(c.skipped) {
				t.Fatalf("跳过日志 = %v, want %d 条", logs, len(c.skipped))
			}
			for _, name := range c.skipped {
				want := "跳过不匹配当前架构的native: " + name
				found := false
				for _, msg := range logs {
					if msg == want {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("缺少日志 %q，实际 %v", want, logs)
				}
			}
		})
	}
}

func TestInstallFiltersNativesBeforeDownload(t *testing.T) {
	m := newMockFabric(t)
	const (
		winBody = "native-win"
		armBody = "native-arm"
		x86Body = "native-x86"
	)
	var winHits, armHits, x86Hits int32
	addNative := func(classifier, body string, hits *int32) {
		urlPath := "/natives/" + classifier + ".jar"
		m.routes[urlPath] = func() []byte {
			atomic.AddInt32(hits, 1)
			return []byte(body)
		}
		m.vanilla.Libraries = append(m.vanilla.Libraries, mojangLibrary{
			Name: "org.lwjgl:lwjgl:3.3.1:" + classifier,
			Downloads: mojangDownloads{Artifact: &mojangArtifact{
				Path: "org/lwjgl/lwjgl/3.3.1/lwjgl-3.3.1-" + classifier + ".jar",
				URL:  m.srv.URL + urlPath,
				SHA1: sha1Hex(body),
				Size: int64(len(body)),
			}},
			Rules: []mojangRule{{Action: "allow", OS: &mojangOS{Name: "windows"}}},
		})
	}
	addNative("natives-windows", winBody, &winHits)
	addNative("natives-windows-arm64", armBody, &armHits)
	addNative("natives-windows-x86", x86Body, &x86Hits)

	var logs []string
	var progressMu sync.Mutex
	opts := m.options()
	opts.TargetOS = "windows"
	opts.TargetArch = "amd64"
	opts.Progress = func(msg string) {
		progressMu.Lock()
		logs = append(logs, msg)
		progressMu.Unlock()
	}

	dir := t.TempDir()
	if _, err := Install(context.Background(), dir, "1.20.1", "0.15.0", opts); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if got := atomic.LoadInt32(&winHits); got != 1 {
		t.Fatalf("windows native 请求次数 = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&armHits); got != 0 {
		t.Fatalf("arm64 native 不应发起请求，实际 %d 次", got)
	}
	if got := atomic.LoadInt32(&x86Hits); got != 0 {
		t.Fatalf("x86 native 不应发起请求，实际 %d 次", got)
	}

	if _, err := os.Stat(filepath.Join(dir, "libraries", "org", "lwjgl", "lwjgl", "3.3.1", "lwjgl-3.3.1-natives-windows.jar")); err != nil {
		t.Fatalf("匹配的 native 未落盘: %v", err)
	}
	for _, classifier := range []string{"natives-windows-arm64", "natives-windows-x86"} {
		p := filepath.Join(dir, "libraries", "org", "lwjgl", "lwjgl", "3.3.1", "lwjgl-3.3.1-"+classifier+".jar")
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s 不应落盘", classifier)
		}
	}

	joined := strings.Join(logs, "\n")
	for _, classifier := range []string{"natives-windows-arm64", "natives-windows-x86"} {
		want := "跳过不匹配当前架构的native: org.lwjgl:lwjgl:3.3.1:" + classifier
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少日志 %q:\n%s", want, joined)
		}
	}
}

func TestInstallRejectsInvalidTarget(t *testing.T) {
	m := newMockFabric(t)
	for _, opts := range []Options{
		{TargetOS: "plan9", TargetArch: "amd64"},
		{TargetOS: "windows", TargetArch: "mips"},
	} {
		opts.MetaBaseURL = m.srv.URL
		opts.MojangManifestURL = m.srv.URL + "/manifest.json"
		if _, err := Install(context.Background(), t.TempDir(), "1.20.1", "0.15.0", opts); err == nil {
			t.Fatalf("目标平台 %s/%s 应被拒绝", opts.TargetOS, opts.TargetArch)
		}
	}
	if got := atomic.LoadInt32(&m.hits); got != 0 {
		t.Fatalf("非法目标平台不应发起请求，实际 %d 次", got)
	}
}
