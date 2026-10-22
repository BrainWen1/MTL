package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"mtl/internal/fabric"
	"mtl/internal/mrpack"
)

func sha512Hex(s string) string {
	sum := sha512.Sum512([]byte(s))
	return hex.EncodeToString(sum[:])
}

func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func buildPack(t *testing.T, idx *mrpack.Index, overrides map[string]string) string {
	t.Helper()
	data, err := json.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "test.mrpack")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create(mrpack.IndexFileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	for name, body := range overrides {
		w, err := zw.Create("overrides/" + name)
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
	return path
}

func serveBodies(t *testing.T, bodies map[string]string) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func testIndex(baseURL string) *mrpack.Index {
	return &mrpack.Index{
		FormatVersion: 1,
		Game:          "minecraft",
		VersionID:     "1.0.0",
		Name:          "CLI Test",
		Files: []mrpack.File{
			{
				Path:      "mods/a.jar",
				Hashes:    mrpack.Hashes{SHA512: sha512Hex("AAA")},
				Downloads: []string{baseURL + "/a"},
				FileSize:  3,
			},
			{
				Path:      "config/b.txt",
				Hashes:    mrpack.Hashes{SHA512: sha512Hex("BBB")},
				Downloads: []string{baseURL + "/b"},
				FileSize:  3,
			},
			{
				Path:      "server-only.txt",
				Hashes:    mrpack.Hashes{SHA512: sha512Hex("BBB")},
				Downloads: []string{baseURL + "/b"},
				FileSize:  3,
				Env:       &mrpack.Env{Client: mrpack.EnvUnsupported, Server: mrpack.EnvRequired},
			},
		},
		Dependencies: map[string]string{
			mrpack.DepMinecraft:    "1.20.1",
			mrpack.DepFabricLoader: "0.15.0",
		},
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func TestInstallDownloadsAndExtractsOverrides(t *testing.T) {
	srv, hits := serveBodies(t, map[string]string{"/a": "AAA", "/b": "BBB"})
	pack := buildPack(t, testIndex(srv.URL), map[string]string{"options.txt": "override"})
	instance := filepath.Join(t.TempDir(), "inst")

	var out bytes.Buffer
	if err := installPack(pack, instance, installOptions{concurrency: 2, skipLoader: true}, &out); err != nil {
		t.Fatalf("installPack: %v", err)
	}

	assertFile(t, filepath.Join(instance, "mods", "a.jar"), "AAA")
	assertFile(t, filepath.Join(instance, "config", "b.txt"), "BBB")
	assertFile(t, filepath.Join(instance, "options.txt"), "override")

	if _, err := os.Stat(filepath.Join(instance, "server-only.txt")); !os.IsNotExist(err) {
		t.Fatal("server-only 文件不应下载")
	}
	if got := atomic.LoadInt32(hits); got != 2 {
		t.Fatalf("请求次数 = %d, want 2", got)
	}

	output := out.String()
	for _, want := range []string{
		"整合包: CLI Test",
		"Minecraft 1.20.1 / fabric 0.15.0",
		"客户端文件 2 个",
		"[1/2]",
		"[2/2]",
		"下载完成: 2 个文件",
		"overrides 已解压",
		"实例安装完成",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("输出缺少 %q:\n%s", want, output)
		}
	}
}

func TestInstallSkipsExistingFiles(t *testing.T) {
	srv, hits := serveBodies(t, map[string]string{"/a": "AAA", "/b": "BBB"})
	pack := buildPack(t, testIndex(srv.URL), nil)
	instance := filepath.Join(t.TempDir(), "inst")

	if err := installPack(pack, instance, installOptions{skipLoader: true}, io.Discard); err != nil {
		t.Fatalf("首次安装: %v", err)
	}
	var out bytes.Buffer
	if err := installPack(pack, instance, installOptions{skipLoader: true}, &out); err != nil {
		t.Fatalf("重复安装: %v", err)
	}

	if got := atomic.LoadInt32(hits); got != 2 {
		t.Fatalf("重复安装不应重新下载，请求次数 = %d, want 2", got)
	}
	if !strings.Contains(out.String(), "（已存在，跳过）") {
		t.Fatalf("输出缺少跳过提示:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "2 个已存在") {
		t.Fatalf("输出缺少统计:\n%s", out.String())
	}
}

func TestInstallRequiresDir(t *testing.T) {
	if err := install([]string{"pack.mrpack"}); err == nil {
		t.Fatal("缺少 --dir 应报错")
	}
}

func TestInstallRejectsBadPack(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.mrpack")
	if err := install([]string{"--dir", t.TempDir(), missing}); err == nil {
		t.Fatal("不存在的包应报错")
	}
}

// serveFabric 模拟 Fabric meta + Mojang meta，返回包含单个 library 和 client jar 的极小版本。
func serveFabric(t *testing.T) *httptest.Server {
	t.Helper()
	const (
		fabricLib = "fabric-jar"
		clientJar = "client-jar"
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

	versionJSON := []byte(fmt.Sprintf(
		`{"id":"1.20.1","downloads":{"client":{"sha1":%q,"size":%d,"url":%q}},"libraries":[]}`,
		sha1Hex(clientJar), len(clientJar), srv.URL+"/client.jar"))
	manifestJSON := []byte(fmt.Sprintf(
		`{"versions":[{"id":"1.20.1","url":%q,"sha1":%q}]}`,
		srv.URL+"/mc.json", sha1Hex(string(versionJSON))))
	profileJSON := []byte(fmt.Sprintf(
		`{"id":"fabric-loader-0.15.0-1.20.1","inheritsFrom":"1.20.1","libraries":[{"name":"test.group:fabric-lib:1.0","url":%q,"sha512":%q,"size":%d}]}`,
		srv.URL+"/maven/", sha512Hex(fabricLib), len(fabricLib)))

	routes["/manifest.json"] = func() []byte { return manifestJSON }
	routes["/mc.json"] = func() []byte { return versionJSON }
	routes["/client.jar"] = func() []byte { return []byte(clientJar) }
	routes["/v2/versions/loader/1.20.1/0.15.0/profile/json"] = func() []byte { return profileJSON }
	routes["/maven/test/group/fabric-lib/1.0/fabric-lib-1.0.jar"] = func() []byte { return []byte(fabricLib) }
	return srv
}

func TestInstallFabricLoader(t *testing.T) {
	srv := serveFabric(t)
	idx := &mrpack.Index{
		FormatVersion: 1,
		Game:          "minecraft",
		VersionID:     "1.0.0",
		Name:          "Loader Test",
		Files:         []mrpack.File{},
		Dependencies: map[string]string{
			mrpack.DepMinecraft:    "1.20.1",
			mrpack.DepFabricLoader: "0.15.0",
		},
	}
	pack := buildPack(t, idx, nil)
	instance := filepath.Join(t.TempDir(), "inst")

	var out bytes.Buffer
	err := installPack(pack, instance, installOptions{fabric: fabric.Options{
		MetaBaseURL:       srv.URL,
		MojangManifestURL: srv.URL + "/manifest.json",
	}}, &out)
	if err != nil {
		t.Fatalf("installPack: %v", err)
	}

	const profileID = "fabric-loader-0.15.0-1.20.1"
	if _, err := os.Stat(filepath.Join(instance, "versions", profileID, profileID+".json")); err != nil {
		t.Fatalf("缺少 Fabric 版本 json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(instance, "versions", "1.20.1", "1.20.1.json")); err != nil {
		t.Fatalf("缺少 vanilla 版本 json: %v", err)
	}
	assertFile(t, filepath.Join(instance, "versions", "1.20.1", "1.20.1.jar"), "client-jar")
	assertFile(t, filepath.Join(instance, "libraries", "test", "group", "fabric-lib", "1.0", "fabric-lib-1.0.jar"), "fabric-jar")

	for _, want := range []string{
		"[Fabric] 拉取loader元信息",
		"[Fabric] 下载library test.group:fabric-lib:1.0",
		"[Fabric] 安装完成",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("输出缺少 %q:\n%s", want, out.String())
		}
	}
}

func TestInstallSkipLoader(t *testing.T) {
	srv, _ := serveBodies(t, map[string]string{"/a": "AAA", "/b": "BBB"})
	pack := buildPack(t, testIndex(srv.URL), nil)
	instance := filepath.Join(t.TempDir(), "inst")

	var out bytes.Buffer
	if err := installPack(pack, instance, installOptions{skipLoader: true}, &out); err != nil {
		t.Fatalf("installPack: %v", err)
	}
	if _, err := os.Stat(filepath.Join(instance, "versions")); !os.IsNotExist(err) {
		t.Fatal("--skip-loader 不应写入 versions 目录")
	}
	if !strings.Contains(out.String(), "已跳过 Fabric loader 安装") {
		t.Fatalf("输出缺少跳过提示:\n%s", out.String())
	}
}

func TestInstallRejectsUnsupportedLoader(t *testing.T) {
	idx := &mrpack.Index{
		FormatVersion: 1,
		Game:          "minecraft",
		VersionID:     "1.0.0",
		Name:          "Quilt Test",
		Files:         []mrpack.File{},
		Dependencies: map[string]string{
			mrpack.DepMinecraft:   "1.20.1",
			mrpack.DepQuiltLoader: "0.20.0",
		},
	}
	pack := buildPack(t, idx, nil)
	err := installPack(pack, filepath.Join(t.TempDir(), "inst"), installOptions{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "quilt") {
		t.Fatalf("应拒绝非 Fabric loader，实际: %v", err)
	}
}
