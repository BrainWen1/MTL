package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
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

	"mtl/internal/config"
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

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256-" + hex.EncodeToString(sum[:])
}

func packIndex(version string, files ...mrpack.File) *mrpack.Index {
	return packIndexNamed("CLI Test", version, files...)
}

func packIndexNamed(name, version string, files ...mrpack.File) *mrpack.Index {
	return &mrpack.Index{
		FormatVersion: 1,
		Game:          "minecraft",
		VersionID:     version,
		Name:          name,
		Files:         files,
		Dependencies: map[string]string{
			mrpack.DepMinecraft:    "1.20.1",
			mrpack.DepFabricLoader: "0.15.0",
		},
	}
}

func remoteFile(path, url, body string) mrpack.File {
	return mrpack.File{
		Path:      path,
		Hashes:    mrpack.Hashes{SHA512: sha512Hex(body)},
		Downloads: []string{url},
		FileSize:  int64(len(body)),
	}
}

func writeInstanceFile(t *testing.T, dir, rel, body string) string {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

func mustLoadLock(t *testing.T, dir string) *mrpack.Lock {
	t.Helper()
	lock, err := mrpack.LoadLock(dir)
	if err != nil {
		t.Fatalf("LoadLock: %v", err)
	}
	if lock == nil {
		t.Fatal("缺少 pack-lock.json")
	}
	return lock
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
		"[Loader] 已记录加载器: fabric 0.15.0",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("输出缺少 %q:\n%s", want, out.String())
		}
	}

	cfg, err := config.Load(instance)
	if err != nil {
		t.Fatalf("Load 实例配置: %v", err)
	}
	if cfg.LoaderType != "fabric" || cfg.LoaderVersion != "0.15.0" {
		t.Fatalf("加载器信息未写入配置: %+v", cfg)
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
	if !strings.Contains(out.String(), "已跳过加载器安装") {
		t.Fatalf("输出缺少跳过提示:\n%s", out.String())
	}
}

func TestInstallRejectsOldForge(t *testing.T) {
	idx := &mrpack.Index{
		FormatVersion: 1,
		Game:          "minecraft",
		VersionID:     "1.0.0",
		Name:          "Old Forge Test",
		Files:         []mrpack.File{},
		Dependencies: map[string]string{
			mrpack.DepMinecraft: "1.19.2",
			mrpack.DepForge:     "1.19.2-43.2.0",
		},
	}
	pack := buildPack(t, idx, nil)
	err := installPack(pack, filepath.Join(t.TempDir(), "inst"), installOptions{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "暂不支持") {
		t.Fatalf("低版本 Forge 应直接拒绝: %v", err)
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

func TestInstallWritesPackLock(t *testing.T) {
	srv, _ := serveBodies(t, map[string]string{"/a": "AAA", "/b": "BBB"})
	pack := buildPack(t, testIndex(srv.URL), map[string]string{
		"options.txt":     "override",
		"saves/world.dat": "save",
	})
	instance := filepath.Join(t.TempDir(), "inst")

	var out bytes.Buffer
	if err := installPack(pack, instance, installOptions{skipLoader: true}, &out); err != nil {
		t.Fatalf("installPack: %v", err)
	}

	lock := mustLoadLock(t, instance)
	if lock.PackID != "CLI Test" || lock.PackVersion != "1.0.0" {
		t.Fatalf("锁定清单包信息错误: %+v", lock)
	}
	if lock.Source == "" {
		t.Fatal("锁定清单应记录来源")
	}
	for _, rel := range []string{"mods/a.jar", "config/b.txt", "options.txt"} {
		if lock.Files[rel] != sha256Hex(string(mustReadFile(t, filepath.Join(instance, filepath.FromSlash(rel))))) {
			t.Errorf("%s 哈希未正确写入: %v", rel, lock.Files)
		}
	}
	for _, rel := range []string{"server-only.txt", "saves/world.dat"} {
		if _, ok := lock.Files[rel]; ok {
			t.Errorf("%s 不应纳入锁定清单: %v", rel, lock.Files)
		}
	}
	if len(lock.Files) != 3 {
		t.Fatalf("托管文件数量 = %d, want 3: %v", len(lock.Files), lock.Files)
	}
	if !strings.Contains(out.String(), "未找到可用的旧锁定清单") {
		t.Fatalf("首次安装缺少提示:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "已写入锁定清单: .mtl/pack-lock.json（3 个托管文件）") {
		t.Fatalf("缺少锁定清单写入提示:\n%s", out.String())
	}
}

func TestInstallIncrementalPrune(t *testing.T) {
	srv, _ := serveBodies(t, map[string]string{
		"/v1-old":     "OLD",
		"/v1-changed": "V1",
		"/v2-changed": "V2",
		"/v2-new":     "NEW",
	})
	instance := filepath.Join(t.TempDir(), "inst")

	v1 := packIndex("1.0.0",
		remoteFile("mods/old.jar", srv.URL+"/v1-old", "OLD"),
		remoteFile("mods/changed.jar", srv.URL+"/v1-changed", "V1"),
	)
	pack1 := buildPack(t, v1, map[string]string{
		"config/old.toml": "old",
		"saves/world.dat": "save",
	})
	if err := installPack(pack1, instance, installOptions{skipLoader: true}, io.Discard); err != nil {
		t.Fatalf("安装 v1: %v", err)
	}
	writeInstanceFile(t, instance, "mods/user.jar", "USER")

	v2 := packIndex("2.0.0",
		remoteFile("mods/changed.jar", srv.URL+"/v2-changed", "V2"),
		remoteFile("mods/new.jar", srv.URL+"/v2-new", "NEW"),
	)
	pack2 := buildPack(t, v2, map[string]string{"config/new.toml": "new"})
	var out bytes.Buffer
	if err := installPack(pack2, instance, installOptions{skipLoader: true}, &out); err != nil {
		t.Fatalf("安装 v2: %v", err)
	}

	for _, rel := range []string{"mods/old.jar", "config/old.toml"} {
		if _, err := os.Stat(filepath.Join(instance, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("%s 应作为旧版托管文件被清理", rel)
		}
	}
	assertFile(t, filepath.Join(instance, "mods", "changed.jar"), "V2")
	assertFile(t, filepath.Join(instance, "mods", "new.jar"), "NEW")
	assertFile(t, filepath.Join(instance, "config", "new.toml"), "new")
	assertFile(t, filepath.Join(instance, "mods", "user.jar"), "USER")
	assertFile(t, filepath.Join(instance, "saves", "world.dat"), "save")

	lock := mustLoadLock(t, instance)
	if lock.PackVersion != "2.0.0" {
		t.Fatalf("锁定清单版本 = %q", lock.PackVersion)
	}
	if lock.Files["mods/changed.jar"] != sha256Hex("V2") {
		t.Errorf("更新文件哈希错误: %v", lock.Files)
	}
	for _, rel := range []string{"mods/old.jar", "config/old.toml", "mods/user.jar", "saves/world.dat"} {
		if _, ok := lock.Files[rel]; ok {
			t.Errorf("%s 不应出现在新锁定清单: %v", rel, lock.Files)
		}
	}

	output := out.String()
	for _, want := range []string{
		"增量检查: 新增 2，更新 1，待删除 2",
		"[Pack]   - config/old.toml",
		"[Pack]   - mods/old.jar",
		"已清理 2 个旧托管文件",
		"已写入锁定清单",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("输出缺少 %q:\n%s", want, output)
		}
	}
}

func TestInstallNoPruneKeepsOldFiles(t *testing.T) {
	srv, _ := serveBodies(t, map[string]string{"/old": "OLD", "/new": "NEW"})
	instance := filepath.Join(t.TempDir(), "inst")

	pack1 := buildPack(t, packIndex("1.0.0", remoteFile("mods/old.jar", srv.URL+"/old", "OLD")), nil)
	if err := installPack(pack1, instance, installOptions{skipLoader: true}, io.Discard); err != nil {
		t.Fatalf("安装 v1: %v", err)
	}
	pack2 := buildPack(t, packIndex("2.0.0", remoteFile("mods/new.jar", srv.URL+"/new", "NEW")), nil)
	var out bytes.Buffer
	if err := installPack(pack2, instance, installOptions{skipLoader: true, noPrune: true}, &out); err != nil {
		t.Fatalf("安装 v2: %v", err)
	}

	assertFile(t, filepath.Join(instance, "mods", "old.jar"), "OLD")
	assertFile(t, filepath.Join(instance, "mods", "new.jar"), "NEW")
	if !strings.Contains(out.String(), "--no-prune 已开启，保留 1 个旧托管文件") {
		t.Fatalf("缺少 --no-prune 提示:\n%s", out.String())
	}
	lock := mustLoadLock(t, instance)
	if _, ok := lock.Files["mods/old.jar"]; ok {
		t.Errorf("--no-prune 保留的旧文件不应继续纳入锁定清单: %v", lock.Files)
	}
}

func TestInstallKeepsUserModifiedRemovedFile(t *testing.T) {
	srv, _ := serveBodies(t, map[string]string{"/old": "OLD", "/new": "NEW"})
	instance := filepath.Join(t.TempDir(), "inst")

	pack1 := buildPack(t, packIndex("1.0.0", remoteFile("mods/old.jar", srv.URL+"/old", "OLD")), nil)
	if err := installPack(pack1, instance, installOptions{skipLoader: true}, io.Discard); err != nil {
		t.Fatalf("安装 v1: %v", err)
	}
	writeInstanceFile(t, instance, "mods/old.jar", "USER EDIT")

	pack2 := buildPack(t, packIndex("2.0.0", remoteFile("mods/new.jar", srv.URL+"/new", "NEW")), nil)
	var out bytes.Buffer
	if err := installPack(pack2, instance, installOptions{skipLoader: true}, &out); err != nil {
		t.Fatalf("安装 v2: %v", err)
	}

	assertFile(t, filepath.Join(instance, "mods", "old.jar"), "USER EDIT")
	if !strings.Contains(out.String(), "保留 mods/old.jar（已被用户修改）") {
		t.Fatalf("缺少保护用户修改的提示:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "已清理 0 个旧托管文件") && !strings.Contains(out.String(), "待删除 0") {
		t.Fatalf("不应清理用户修改过的文件:\n%s", out.String())
	}
}

func TestInstallDifferentPackSkipsPrune(t *testing.T) {
	srv, _ := serveBodies(t, map[string]string{"/a-old": "A-OLD", "/b-new": "B-NEW"})
	instance := filepath.Join(t.TempDir(), "inst")

	packA := buildPack(t, packIndexNamed("Pack A", "1.0.0", remoteFile("mods/a-old.jar", srv.URL+"/a-old", "A-OLD")), nil)
	if err := installPack(packA, instance, installOptions{skipLoader: true}, io.Discard); err != nil {
		t.Fatalf("安装 Pack A: %v", err)
	}
	packB := buildPack(t, packIndexNamed("Pack B", "1.0.0", remoteFile("mods/b-new.jar", srv.URL+"/b-new", "B-NEW")), nil)
	var out bytes.Buffer
	if err := installPack(packB, instance, installOptions{skipLoader: true}, &out); err != nil {
		t.Fatalf("安装 Pack B: %v", err)
	}

	assertFile(t, filepath.Join(instance, "mods", "a-old.jar"), "A-OLD")
	assertFile(t, filepath.Join(instance, "mods", "b-new.jar"), "B-NEW")
	if !strings.Contains(out.String(), "整合包标识由 \"Pack A\" 变为 \"Pack B\"，跳过清理") {
		t.Fatalf("缺少不同整合包跳过清理的提示:\n%s", out.String())
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s: %v", path, err)
	}
	return data
}
