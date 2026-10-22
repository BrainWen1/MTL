package fabric

import (
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func sha512Hex(s string) string {
	sum := sha512.Sum512([]byte(s))
	return hex.EncodeToString(sum[:])
}

type mockFabric struct {
	t         *testing.T
	srv       *httptest.Server
	routes    map[string]func() []byte
	profile   fabricProfile
	vanilla   mojangVersion
	versionID string // Mojang 清单中登记的版本 id
	hits      int32
}

func newMockFabric(t *testing.T) *mockFabric {
	t.Helper()
	m := &mockFabric{t: t, routes: map[string]func() []byte{}, versionID: "1.20.1"}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&m.hits, 1)
		fn, ok := m.routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(fn())
	}))
	t.Cleanup(m.srv.Close)

	m.profile = fabricProfile{
		ID:           "fabric-loader-0.15.0-1.20.1",
		InheritsFrom: "1.20.1",
		Libraries: []fabricLibrary{
			{Name: "test.group:alpha:1.0", URL: m.srv.URL + "/maven/", SHA512: sha512Hex("alpha-jar"), Size: int64(len("alpha-jar"))},
			// 不带 sha512，客户端应回退到 .sha512 sidecar。
			{Name: "test.group:beta:2.0", URL: m.srv.URL + "/maven/", Size: int64(len("beta-jar"))},
		},
	}
	m.vanilla = mojangVersion{ID: "1.20.1"}
	m.vanilla.Downloads.Client = mojangArtifact{
		URL:  m.srv.URL + "/mc/client.jar",
		SHA1: sha1Hex("client-jar"),
		Size: int64(len("client-jar")),
	}
	m.vanilla.Libraries = []mojangLibrary{{
		Name: "com.example:lib:1.0",
		Downloads: mojangDownloads{Artifact: &mojangArtifact{
			Path: "com/example/lib/1.0/lib-1.0.jar",
			URL:  m.srv.URL + "/libs/lib.jar",
			SHA1: sha1Hex("lib-jar"),
			Size: int64(len("lib-jar")),
		}},
	}}

	m.routes["/v2/versions/loader/1.20.1/0.15.0/profile/json"] = func() []byte {
		b, _ := json.Marshal(m.profile)
		return b
	}
	m.routes["/manifest.json"] = func() []byte {
		vb, _ := json.Marshal(m.vanilla)
		man := mojangManifest{Versions: []mojangVersionEntry{{
			ID:   m.versionID,
			URL:  m.srv.URL + "/mc/1.20.1.json",
			SHA1: sha1Hex(string(vb)),
		}}}
		b, _ := json.Marshal(man)
		return b
	}
	m.routes["/mc/1.20.1.json"] = func() []byte {
		b, _ := json.Marshal(m.vanilla)
		return b
	}
	m.routes["/mc/client.jar"] = func() []byte { return []byte("client-jar") }
	m.routes["/libs/lib.jar"] = func() []byte { return []byte("lib-jar") }
	m.routes["/maven/test/group/alpha/1.0/alpha-1.0.jar"] = func() []byte { return []byte("alpha-jar") }
	m.routes["/maven/test/group/beta/2.0/beta-2.0.jar"] = func() []byte { return []byte("beta-jar") }
	m.routes["/maven/test/group/beta/2.0/beta-2.0.jar.sha512"] = func() []byte { return []byte(sha512Hex("beta-jar")) }
	return m
}

func (m *mockFabric) options() Options {
	return Options{
		MetaBaseURL:       m.srv.URL,
		MojangManifestURL: m.srv.URL + "/manifest.json",
		Concurrency:       2,
		RetryDelay:        time.Millisecond,
	}
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func assertNoTemp(t *testing.T, dir string) {
	t.Helper()
	var found []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), ".mtl-fabric-") {
			found = append(found, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历目录: %v", err)
	}
	if len(found) > 0 {
		t.Fatalf("残留临时文件: %v", found)
	}
}

func TestInstallWritesNativeLayout(t *testing.T) {
	m := newMockFabric(t)
	dir := t.TempDir()

	var mu sync.Mutex
	var msgs []string
	opts := m.options()
	opts.Progress = func(msg string) {
		mu.Lock()
		msgs = append(msgs, msg)
		mu.Unlock()
	}

	res, err := Install(context.Background(), dir, "1.20.1", "0.15.0", opts)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.ProfileID != "fabric-loader-0.15.0-1.20.1" || res.VersionID != "1.20.1" {
		t.Fatalf("Result = %+v", res)
	}
	if res.Files != 4 || res.Downloaded != 4 || res.Skipped != 0 {
		t.Fatalf("Result = %+v, want Files/Downloaded=4", res)
	}

	assertContent(t, filepath.Join(dir, "versions", "1.20.1", "1.20.1.jar"), "client-jar")
	assertContent(t, filepath.Join(dir, "libraries", "test", "group", "alpha", "1.0", "alpha-1.0.jar"), "alpha-jar")
	assertContent(t, filepath.Join(dir, "libraries", "test", "group", "beta", "2.0", "beta-2.0.jar"), "beta-jar")
	assertContent(t, filepath.Join(dir, "libraries", "com", "example", "lib", "1.0", "lib-1.0.jar"), "lib-jar")

	profilePath := filepath.Join(dir, "versions", "fabric-loader-0.15.0-1.20.1", "fabric-loader-0.15.0-1.20.1.json")
	profileRaw, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("缺少 Fabric 版本 json: %v", err)
	}
	var profile fabricProfile
	if err := json.Unmarshal(profileRaw, &profile); err != nil || profile.ID != "fabric-loader-0.15.0-1.20.1" {
		t.Fatalf("Fabric 版本 json 内容异常: %v\n%s", err, profileRaw)
	}
	if _, err := os.Stat(filepath.Join(dir, "versions", "1.20.1", "1.20.1.json")); err != nil {
		t.Fatalf("缺少 vanilla 版本 json: %v", err)
	}
	assertNoTemp(t, dir)

	joined := strings.Join(msgs, "\n")
	for _, want := range []string{
		"拉取loader元信息",
		"下载library test.group:alpha:1.0",
		"下载library test.group:beta:2.0",
		"下载game jar 1.20.1",
		"写入版本 json",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("进度缺少 %q:\n%s", want, joined)
		}
	}

	// 二次安装应全部跳过。
	res2, err := Install(context.Background(), dir, "1.20.1", "0.15.0", m.options())
	if err != nil {
		t.Fatalf("二次 Install: %v", err)
	}
	if res2.Downloaded != 0 || res2.Skipped != 4 {
		t.Fatalf("二次安装 Result = %+v", res2)
	}
}

func TestInstallRejectsProfileMismatch(t *testing.T) {
	m := newMockFabric(t)
	m.profile.ID = "fabric-loader-9.9.9-1.20.1"
	if _, err := Install(context.Background(), t.TempDir(), "1.20.1", "0.15.0", m.options()); err == nil {
		t.Fatal("profile id 不符应报错")
	}

	m = newMockFabric(t)
	m.profile.InheritsFrom = "1.19.4"
	if _, err := Install(context.Background(), t.TempDir(), "1.20.1", "0.15.0", m.options()); err == nil {
		t.Fatal("inheritsFrom 不符应报错")
	}
}

func TestInstallRejectsUnsafePaths(t *testing.T) {
	m := newMockFabric(t)
	m.vanilla.Libraries[0].Downloads.Artifact.Path = "../../escape.jar"
	dir := t.TempDir()
	if _, err := Install(context.Background(), dir, "1.20.1", "0.15.0", m.options()); err == nil {
		t.Fatal("越界 artifact path 应报错")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.jar")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("文件逃出了实例目录")
	}

	m = newMockFabric(t)
	m.profile.Libraries[0].Name = "test.group:../evil:1.0"
	if _, err := Install(context.Background(), t.TempDir(), "1.20.1", "0.15.0", m.options()); err == nil {
		t.Fatal("非法 maven 坐标应报错")
	}
}

func TestInstallRejectsUnsafeVersion(t *testing.T) {
	m := newMockFabric(t)
	for _, v := range []string{"../1.20.1", "1.20/1", `1.20\1`, "..", ""} {
		if _, err := Install(context.Background(), t.TempDir(), v, "0.15.0", m.options()); err == nil {
			t.Fatalf("版本 %q 应被拒绝", v)
		}
	}
	if got := atomic.LoadInt32(&m.hits); got != 0 {
		t.Fatalf("非法版本不应发起网络请求，实际 %d 次", got)
	}
}

func TestInstallMissingMinecraftVersion(t *testing.T) {
	m := newMockFabric(t)
	m.versionID = "1.20.2"
	_, err := Install(context.Background(), t.TempDir(), "1.20.1", "0.15.0", m.options())
	if err == nil || !strings.Contains(err.Error(), "找不到") {
		t.Fatalf("应提示版本不存在: %v", err)
	}
}

func TestInstallVerifiesVanillaJSON(t *testing.T) {
	m := newMockFabric(t)
	m.routes["/mc/1.20.1.json"] = func() []byte { return []byte(`{"id":"1.20.1"}`) }
	_, err := Install(context.Background(), t.TempDir(), "1.20.1", "0.15.0", m.options())
	if !errors.Is(err, errHashMismatch) {
		t.Fatalf("版本 json 哈希不符应报错: %v", err)
	}
}

func TestFabricArtifactHashMismatchCleanup(t *testing.T) {
	m := newMockFabric(t)
	m.routes["/maven/test/group/alpha/1.0/alpha-1.0.jar"] = func() []byte { return []byte("tampered") }
	dir := t.TempDir()
	_, err := Install(context.Background(), dir, "1.20.1", "0.15.0", m.options())
	if !errors.Is(err, errHashMismatch) {
		t.Fatalf("应返回哈希校验错误: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "libraries", "test", "group", "alpha", "1.0", "alpha-1.0.jar")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("校验失败的文件不应出现在目标路径")
	}
	assertNoTemp(t, dir)
}

func TestMavenLibraryPath(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"org.ow2.asm:asm:9.6", "libraries/org/ow2/asm/asm/9.6/asm-9.6.jar", false},
		{"org.lwjgl:lwjgl:3.3.1:natives-windows", "libraries/org/lwjgl/lwjgl/3.3.1/lwjgl-3.3.1-natives-windows.jar", false},
		{"com.example:lib:1.0@zip", "libraries/com/example/lib/1.0/lib-1.0.zip", false},
		{"", "", true},
		{"g:a", "", true},
		{":a:1", "", true},
		{"g::1", "", true},
		{"g:a:../1", "", true},
		{"g..a:b:1", "", true},
		{"g:a:1:../x", "", true},
		{"g:a:1/2", "", true},
		{"g:a:1@", "", true},
	}
	for _, c := range cases {
		got, err := mavenLibraryPath(c.in)
		if c.wantErr {
			if err == nil {
				t.Fatalf("%q 应报错，得到 %q", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Fatalf("%q = %q, %v, want %q", c.in, got, err, c.want)
		}
	}
}

func TestRulesAllow(t *testing.T) {
	osxAllow := mojangRule{Action: "allow", OS: &mojangOS{Name: "osx"}}
	allAllow := mojangRule{Action: "allow"}
	osxDeny := mojangRule{Action: "disallow", OS: &mojangOS{Name: "osx"}}
	x86Allow := mojangRule{Action: "allow", OS: &mojangOS{Arch: "x86"}}
	demoRule := mojangRule{Action: "allow", Features: map[string]bool{"is_demo_user": true}}

	cases := []struct {
		name  string
		rules []mojangRule
		os    string
		arch  string
		want  bool
	}{
		{"no rules", nil, "windows", "amd64", true},
		{"osx only on windows", []mojangRule{osxAllow}, "windows", "amd64", false},
		{"osx only on osx", []mojangRule{osxAllow}, "osx", "amd64", true},
		{"allow then deny osx on windows", []mojangRule{allAllow, osxDeny}, "windows", "amd64", true},
		{"allow then deny osx on osx", []mojangRule{allAllow, osxDeny}, "osx", "amd64", false},
		{"arch x86 on amd64", []mojangRule{x86Allow}, "windows", "amd64", false},
		{"arch x86 on 386", []mojangRule{x86Allow}, "windows", "x86", true},
		{"feature not enabled", []mojangRule{demoRule}, "windows", "amd64", false},
	}
	for _, c := range cases {
		if got := rulesAllow(c.rules, c.os, c.arch); got != c.want {
			t.Fatalf("%s: rulesAllow = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestVanillaArtifactsNativeClassifier(t *testing.T) {
	lib := mojangLibrary{
		Name: "org.lwjgl:lwjgl:2.9.4",
		Downloads: mojangDownloads{Classifiers: map[string]mojangArtifact{
			"natives-windows-64": {
				Path: "org/lwjgl/lwjgl/2.9.4/lwjgl-2.9.4-natives-windows.jar",
				URL:  "https://example.com/n.jar",
				SHA1: sha1Hex("n"),
				Size: 1,
			},
		}},
		Natives: map[string]string{"windows": "natives-windows-${arch}"},
	}
	osxOnly := mojangLibrary{
		Name: "com.example:osx:1.0",
		Downloads: mojangDownloads{Artifact: &mojangArtifact{
			Path: "com/example/osx.jar", URL: "https://example.com/o.jar", SHA1: sha1Hex("o"), Size: 1,
		}},
		Rules: []mojangRule{{Action: "allow", OS: &mojangOS{Name: "osx"}}},
	}

	arts, err := vanillaArtifacts(mojangVersion{Libraries: []mojangLibrary{lib, osxOnly}}, "windows", "amd64", nil)
	if err != nil {
		t.Fatalf("vanillaArtifacts: %v", err)
	}
	if len(arts) != 1 || arts[0].relPath != "libraries/org/lwjgl/lwjgl/2.9.4/lwjgl-2.9.4-natives-windows.jar" {
		t.Fatalf("artifacts = %+v", arts)
	}
}

func TestDownloadArtifactsRetries(t *testing.T) {
	const body = "ok"
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) < 3 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	a := artifact{
		relPath: "libraries/test/ok.jar",
		url:     srv.URL + "/ok.jar",
		kind:    hashSHA512,
		sum:     sha512Hex(body),
		display: "test:ok:1.0",
		label:   "library",
	}
	o := options{httpClient: srv.Client(), concurrency: 2, retries: 3, retryDelay: time.Millisecond}
	downloaded, skipped, err := downloadArtifacts(context.Background(), dir, []artifact{a}, o)
	if err != nil {
		t.Fatalf("downloadArtifacts: %v", err)
	}
	if downloaded != 1 || skipped != 0 {
		t.Fatalf("downloaded=%d skipped=%d", downloaded, skipped)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("请求次数 = %d, want 3", got)
	}
	assertContent(t, filepath.Join(dir, "libraries", "test", "ok.jar"), body)
}

func TestDownloadArtifactsRejectsTraversal(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	t.Cleanup(srv.Close)

	a := artifact{relPath: "../evil.jar", url: srv.URL + "/x", kind: hashSHA512, sum: sha512Hex("x")}
	o := options{httpClient: srv.Client(), concurrency: 2, retries: 0}
	if _, _, err := downloadArtifacts(context.Background(), t.TempDir(), []artifact{a}, o); err == nil {
		t.Fatal("越界路径应报错")
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("越界路径不应发起请求，实际 %d 次", got)
	}
}
