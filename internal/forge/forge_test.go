package forge

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mtl/internal/config"
)

func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestValidateMinecraftVersion(t *testing.T) {
	valid := []string{"1.20.1", "1.20.2", "1.21", "1.21.1"}
	for _, v := range valid {
		if err := validateMinecraftVersion(v); err != nil {
			t.Fatalf("%q 应被接受: %v", v, err)
		}
	}
	invalid := []string{"1.19.2", "1.20", "1.18.2", "1.20.1-pre1", "23w31a", ""}
	for _, v := range invalid {
		if err := validateMinecraftVersion(v); err == nil {
			t.Fatalf("%q 应被拒绝", v)
		}
	}
	if err := validateMinecraftVersion("1.19.2"); !strings.Contains(err.Error(), "暂不支持") {
		t.Fatalf("低版本错误不够友好: %v", err)
	}
}

func TestNormalizeForgeVersion(t *testing.T) {
	full, ver, err := normalizeForgeVersion("1.20.1", "1.20.1-47.1.0")
	if err != nil || full != "1.20.1-47.1.0" || ver != "47.1.0" {
		t.Fatalf("full=%q ver=%q err=%v", full, ver, err)
	}
	full, ver, err = normalizeForgeVersion("1.20.1", "47.1.0")
	if err != nil || full != "1.20.1-47.1.0" || ver != "47.1.0" {
		t.Fatalf("仅 Forge 版本: full=%q ver=%q err=%v", full, ver, err)
	}
	if _, _, err := normalizeForgeVersion("1.20.1", ""); err == nil {
		t.Fatal("空 Forge 版本应报错")
	}
	for _, dep := range []string{"../47.1.0", "47/1/0", "47 1 0"} {
		if _, _, err := normalizeForgeVersion("1.20.1", dep); err == nil {
			t.Fatalf("非法 Forge 版本 %q 应报错", dep)
		}
	}
	if _, _, err := normalizeForgeVersion("1.19.2", "43.2.0"); err == nil {
		t.Fatal("低版本 Minecraft 应报错")
	}
}

func TestRecommendedJavaMajor(t *testing.T) {
	cases := map[string]int{"1.20.1": 17, "1.20.4": 17, "1.20.5": 21, "1.21": 21}
	for v, want := range cases {
		if got := recommendedJavaMajor(v); got != want {
			t.Fatalf("recommendedJavaMajor(%q) = %d, want %d", v, got, want)
		}
	}
}

func TestParseSHA1Sidecar(t *testing.T) {
	if got, err := parseSHA1Sidecar([]byte(sha1Hex("x") + "  forge-installer.jar\n")); err != nil || got != sha1Hex("x") {
		t.Fatalf("sidecar = %q, %v", got, err)
	}
	for _, body := range []string{"", "xyz", "abcd"} {
		if _, err := parseSHA1Sidecar([]byte(body)); err == nil {
			t.Fatalf("sidecar %q 应报错", body)
		}
	}
}

func TestFindGeneratedProfilePicksNewForgeProfile(t *testing.T) {
	dir := t.TempDir()
	writeVersionJSON(t, dir, "1.20.1", `{"id":"1.20.1","mainClass":"net.minecraft.client.main.Main"}`)
	before, err := listVersionIDs(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeVersionJSON(t, dir, "1.20.1-forge-47.1.0",
		`{"id":"1.20.1-forge-47.1.0","inheritsFrom":"1.20.1","mainClass":"cpw.mods.bootstraplauncher.BootstrapLauncher","libraries":[]}`)

	got, err := findGeneratedProfile(dir, "1.20.1", before)
	if err != nil {
		t.Fatalf("findGeneratedProfile: %v", err)
	}
	if got.ID != "1.20.1-forge-47.1.0" || got.MainClass != "cpw.mods.bootstraplauncher.BootstrapLauncher" {
		t.Fatalf("profile = %+v", got)
	}

	writeVersionJSON(t, dir, "1.20.1-forge-47.2.0",
		`{"id":"1.20.1-forge-47.2.0","inheritsFrom":"1.20.1","mainClass":"cpw.mods.bootstraplauncher.BootstrapLauncher"}`)
	_, err = listVersionIDs(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 新增两个 profile 时应报歧义，避免误选。
	before = map[string]bool{"1.20.1": true}
	if _, err := findGeneratedProfile(dir, "1.20.1", before); err == nil || !strings.Contains(err.Error(), "多个") {
		t.Fatalf("多个新 profile 应报错: %v", err)
	}
}

func TestFindGeneratedProfileRejectsMissingMainClass(t *testing.T) {
	dir := t.TempDir()
	writeVersionJSON(t, dir, "1.20.1-forge-47.1.0",
		`{"id":"1.20.1-forge-47.1.0","inheritsFrom":"1.20.1"}`)
	if _, err := findGeneratedProfile(dir, "1.20.1", map[string]bool{}); err == nil {
		t.Fatal("缺少 mainClass 的 profile 不应被接受")
	}
}

func TestCleanupNewVersionDirs(t *testing.T) {
	dir := t.TempDir()
	writeVersionJSON(t, dir, "1.20.1", `{"id":"1.20.1"}`)
	writeVersionJSON(t, dir, "1.20.1-forge-47.1.0", `{"id":"1.20.1-forge-47.1.0"}`)

	cleanupNewVersionDirs(dir, map[string]bool{"1.20.1": true})
	if _, err := os.Stat(filepath.Join(dir, "versions", "1.20.1-forge-47.1.0")); !os.IsNotExist(err) {
		t.Fatal("失败后新增的版本目录应被清理")
	}
	if _, err := os.Stat(filepath.Join(dir, "versions", "1.20.1", "1.20.1.json")); err != nil {
		t.Fatalf("原有版本不应被清理: %v", err)
	}
}

// mockForgeServer 提供原版清单、client jar 与 Forge Maven installer。
func mockForgeServer(t *testing.T) *httptest.Server {
	t.Helper()
	const (
		clientJar = "client-jar"
		installer = "forge-installer-jar"
	)
	routes := map[string]func() []byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fn, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(fn())
	}))
	t.Cleanup(srv.Close)

	versionJSON := fmt.Appendf(nil,
		`{"id":"1.20.1","javaVersion":{"component":"java-runtime-gamma","majorVersion":17},`+
			`"downloads":{"client":{"sha1":%q,"size":%d,"url":%q}},"libraries":[]}`,
		sha1Hex(clientJar), len(clientJar), srv.URL+"/client.jar")
	manifestJSON := fmt.Appendf(nil,
		`{"versions":[{"id":"1.20.1","url":%q,"sha1":%q}]}`,
		srv.URL+"/mc.json", sha1Hex(string(versionJSON)))

	routes["/manifest.json"] = func() []byte { return manifestJSON }
	routes["/mc.json"] = func() []byte { return versionJSON }
	routes["/client.jar"] = func() []byte { return []byte(clientJar) }
	installerPath := "/maven/net/minecraftforge/forge/1.20.1-47.1.0/forge-1.20.1-47.1.0-installer.jar"
	routes[installerPath] = func() []byte { return []byte(installer) }
	routes[installerPath+".sha1"] = func() []byte { return []byte(sha1Hex(installer)) }
	return srv
}

func testForgeOptions(t *testing.T, srv *httptest.Server) Options {
	t.Helper()
	return Options{
		MavenBaseURL:      srv.URL + "/maven",
		MojangManifestURL: srv.URL + "/manifest.json",
		CacheDir:          t.TempDir(),
		JavaPath:          "java-test",
		HTTPClient:        srv.Client(),
		RetryDelay:        time.Millisecond,
	}
}

func writeVersionJSON(t *testing.T, dir, id, body string) {
	t.Helper()
	p := filepath.Join(dir, "versions", id, id+".json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInstallWithMockedInstaller(t *testing.T) {
	srv := mockForgeServer(t)
	opts := testForgeOptions(t, srv)
	var msgs []string
	opts.Progress = func(msg string) { msgs = append(msgs, msg) }

	ld, err := NewLoader("1.20.1", opts)
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	profileJSON := `{"id":"1.20.1-forge-47.1.0","inheritsFrom":"1.20.1",` +
		`"mainClass":"cpw.mods.bootstraplauncher.BootstrapLauncher",` +
		`"libraries":[{"name":"net.minecraftforge:forge:1.20.1-47.1.0","url":"https://maven.minecraftforge.net/"}]}`
	var installerCalls int
	ld.installerRunner = func(ctx context.Context, javaPath, installerPath, instanceDir string) (string, error) {
		installerCalls++
		if javaPath != "java-test" {
			t.Errorf("javaPath = %q", javaPath)
		}
		if !filepath.IsAbs(instanceDir) {
			t.Errorf("installer 实例目录必须是绝对路径: %q", instanceDir)
		}
		if got, err := os.ReadFile(installerPath); err != nil || string(got) != "forge-installer-jar" {
			t.Errorf("installer 内容异常: %q, %v", got, err)
		}
		writeVersionJSON(t, instanceDir, "1.20.1-forge-47.1.0", profileJSON)
		return "installer ok", nil
	}

	inst := &config.Instance{
		Dir:              t.TempDir(),
		MinecraftVersion: "1.20.1",
		LoaderType:       "forge",
		LoaderVersion:    "1.20.1-47.1.0",
	}
	if err := ld.Install(context.Background(), inst); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installerCalls != 1 {
		t.Fatalf("installer 调用次数 = %d, want 1", installerCalls)
	}
	res := ld.LastResult()
	if res.ProfileID != "1.20.1-forge-47.1.0" || res.ForgeVersion != "47.1.0" {
		t.Fatalf("LastResult = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(inst.Dir, "versions", "1.20.1", "1.20.1.json")); err != nil {
		t.Fatalf("缺少原版版本 json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(inst.Dir, "versions", "1.20.1-forge-47.1.0", "1.20.1-forge-47.1.0.json")); err != nil {
		t.Fatalf("缺少 Forge 版本 json: %v", err)
	}
	cacheEntry := filepath.Join(opts.CacheDir, "forge-1.20.1-47.1.0-installer.jar")
	if got, err := os.ReadFile(cacheEntry); err != nil || string(got) != "forge-installer-jar" {
		t.Fatalf("installer 未缓存: %q, %v", got, err)
	}
	joined := strings.Join(msgs, "\n")
	for _, want := range []string{"准备 Minecraft 1.20.1 原版文件", "运行 Forge installer", "安装完成: 1.20.1-forge-47.1.0"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("进度缺少 %q:\n%s", want, joined)
		}
	}

	// 二次安装：已有可用 profile，应跳过 installer。
	ld.installerRunner = func(context.Context, string, string, string) (string, error) {
		t.Fatal("已有 profile 时不应再次运行 installer")
		return "", nil
	}
	if err := ld.Install(context.Background(), inst); err != nil {
		t.Fatalf("二次 Install: %v", err)
	}
	if !ld.LastResult().SkippedInstaller {
		t.Fatal("二次安装应标记为跳过 installer")
	}
}

func TestInstallCleansUpOnInstallerFailure(t *testing.T) {
	srv := mockForgeServer(t)
	opts := testForgeOptions(t, srv)
	ld, err := NewLoader("1.20.1", opts)
	if err != nil {
		t.Fatal(err)
	}
	ld.installerRunner = func(ctx context.Context, javaPath, installerPath, instanceDir string) (string, error) {
		writeVersionJSON(t, instanceDir, "1.20.1-forge-47.1.0",
			`{"id":"1.20.1-forge-47.1.0","inheritsFrom":"1.20.1","mainClass":"x"}`)
		return "boom", fmt.Errorf("exit status 1")
	}

	inst := &config.Instance{
		Dir:              t.TempDir(),
		MinecraftVersion: "1.20.1",
		LoaderVersion:    "1.20.1-47.1.0",
	}
	err = ld.Install(context.Background(), inst)
	if err == nil || !strings.Contains(err.Error(), "installer 执行失败") {
		t.Fatalf("应返回 installer 失败错误: %v", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("错误应包含 installer 输出: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(inst.Dir, "versions", "1.20.1-forge-47.1.0")); !os.IsNotExist(statErr) {
		t.Fatal("失败后不应残留 Forge 版本目录")
	}
	if _, statErr := os.Stat(filepath.Join(inst.Dir, "versions", "1.20.1", "1.20.1.json")); statErr != nil {
		t.Fatalf("原版文件应保留: %v", statErr)
	}
}

func TestInstallRejectsOldMinecraftBeforeNetwork(t *testing.T) {
	if _, err := NewLoader("1.19.2", Options{}); err == nil {
		t.Fatal("1.19.2 应被拒绝")
	}
}

func TestEnsureProfileLibrariesRepairsMissingFiles(t *testing.T) {
	const body = "forge-lib"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/libs/forge.jar" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	ld := &Loader{opts: Options{HTTPClient: srv.Client(), RetryDelay: time.Millisecond}}
	dir := t.TempDir()
	info := &versionInfo{Libraries: []profileLibrary{{
		Name: "net.minecraftforge:forge:1.0",
		Downloads: profileLibDownloads{Artifact: &profileLibArtifact{
			Path: "net/minecraftforge/forge/1.0/forge-1.0.jar",
			SHA1: sha1Hex(body),
			Size: int64(len(body)),
			URL:  srv.URL + "/libs/forge.jar",
		}},
	}}}

	target := filepath.Join(dir, "libraries", "net", "minecraftforge", "forge", "1.0", "forge-1.0.jar")
	if err := ld.ensureProfileLibraries(context.Background(), dir, info); err != nil {
		t.Fatalf("首次补齐: %v", err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != body {
		t.Fatalf("库文件 = %q, %v", got, err)
	}

	// 文件被破坏后应重新下载并恢复。
	if err := os.WriteFile(target, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ld.ensureProfileLibraries(context.Background(), dir, info); err != nil {
		t.Fatalf("修复: %v", err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != body {
		t.Fatalf("修复后库文件 = %q, %v", got, err)
	}
}

func TestCappedBufferKeepsTail(t *testing.T) {
	b := &cappedBuffer{limit: 5}
	io.WriteString(b, "abcdefgh")
	if got := b.String(); got != "defgh" {
		t.Fatalf("cappedBuffer = %q", got)
	}
}
