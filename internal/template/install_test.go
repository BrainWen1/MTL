package template

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mtl/internal/assets"
	"mtl/internal/config"
	"mtl/internal/fabric"
)

func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func sha512Hex(s string) string {
	sum := sha512.Sum512([]byte(s))
	return hex.EncodeToString(sum[:])
}

type mockMeta struct {
	srv    *httptest.Server
	routes map[string]func() []byte
}

func newMockMeta(t *testing.T) *mockMeta {
	t.Helper()
	m := &mockMeta{routes: map[string]func() []byte{}}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fn, ok := m.routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(fn())
	}))
	t.Cleanup(m.srv.Close)
	return m
}

// addVanilla 注册 Mojang 清单、版本 json、client jar、libraries 与 assets。
func (m *mockMeta) addVanilla(t *testing.T, versionID string) (versionJSON []byte, assetsDir string) {
	t.Helper()
	const (
		clientJar = "client-jar"
		libJar    = "lib-jar"
		assetBody = "asset-body"
	)
	assetHash := sha1Hex(assetBody)
	indexJSON := []byte(fmt.Sprintf(`{"objects":{"test/a.txt":{"hash":%q,"size":%d}}}`, assetHash, len(assetBody)))

	version := map[string]any{
		"id":        versionID,
		"type":      "release",
		"mainClass": "net.minecraft.client.main.Main",
		"downloads": map[string]any{
			"client": map[string]any{
				"url":  m.srv.URL + "/client.jar",
				"sha1": sha1Hex(clientJar),
				"size": len(clientJar),
			},
		},
		"libraries": []any{map[string]any{
			"name": "com.example:lib:1.0",
			"downloads": map[string]any{
				"artifact": map[string]any{
					"path": "com/example/lib/1.0/lib-1.0.jar",
					"url":  m.srv.URL + "/libs/lib.jar",
					"sha1": sha1Hex(libJar),
					"size": len(libJar),
				},
			},
		}},
		"assetIndex": map[string]any{
			"id":   "test-assets",
			"url":  m.srv.URL + "/assets/index.json",
			"sha1": sha1Hex(string(indexJSON)),
			"size": len(indexJSON),
		},
		"assets":      "test-assets",
		"arguments":   map[string]any{"game": []string{}, "jvm": []string{}},
		"javaVersion": map[string]any{"component": "java-runtime-gamma", "majorVersion": 17},
	}
	versionJSON, err := json.Marshal(version)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(map[string]any{
		"versions": []any{map[string]any{
			"id":   versionID,
			"url":  m.srv.URL + "/" + versionID + ".json",
			"sha1": sha1Hex(string(versionJSON)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	m.routes["/manifest.json"] = func() []byte { return manifest }
	m.routes["/"+versionID+".json"] = func() []byte { return versionJSON }
	m.routes["/client.jar"] = func() []byte { return []byte(clientJar) }
	m.routes["/libs/lib.jar"] = func() []byte { return []byte(libJar) }
	m.routes["/assets/index.json"] = func() []byte { return indexJSON }
	m.routes["/assets/"+assetHash[:2]+"/"+assetHash] = func() []byte { return []byte(assetBody) }

	assetsDir = t.TempDir()
	return versionJSON, assetsDir
}

func (m *mockMeta) baseOptions(assetsDir string) (fabric.Options, assets.Options) {
	return fabric.Options{
			MojangManifestURL: m.srv.URL + "/manifest.json",
			Concurrency:       2,
			RetryDelay:        time.Millisecond,
		}, assets.Options{
			Dir:            assetsDir,
			ObjectsBaseURL: m.srv.URL + "/assets/",
			Concurrency:    2,
			RetryDelay:     time.Millisecond,
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

func assertDir(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("目录 %s 不存在: %v", path, err)
	}
	if !info.IsDir() {
		t.Fatalf("%s 不是目录", path)
	}
}

func assertNoPackLock(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, ".mtl", "pack-lock.json")); !os.IsNotExist(err) {
		t.Fatalf("纯净模板实例不应生成 pack-lock.json: %v", err)
	}
}

func TestInstallVanillaTemplate(t *testing.T) {
	m := newMockMeta(t)
	versionJSON, assetsDir := m.addVanilla(t, "1.20.1")
	fabricOpts, assetsOpts := m.baseOptions(assetsDir)
	instance := filepath.Join(t.TempDir(), "vanilla")

	var logs bytes.Buffer
	res, err := Install(context.Background(), instance, Options{
		MinecraftVersion: "1.20.1",
		Fabric:           fabricOpts,
		Assets:           assetsOpts,
		Progress:         func(msg string) { logs.WriteString(msg + "\n") },
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.ProfileID != "1.20.1-mtl-vanilla" || res.LoaderType != "" {
		t.Fatalf("result = %+v", res)
	}

	assertFile(t, filepath.Join(instance, "versions", "1.20.1", "1.20.1.json"), string(versionJSON))
	assertFile(t, filepath.Join(instance, "versions", "1.20.1", "1.20.1.jar"), "client-jar")
	assertFile(t, filepath.Join(instance, "libraries", "com", "example", "lib", "1.0", "lib-1.0.jar"), "lib-jar")
	assertDir(t, filepath.Join(instance, "mods"))
	assertDir(t, filepath.Join(instance, "saves"))

	profilePath := filepath.Join(instance, "versions", "1.20.1-mtl-vanilla", "1.20.1-mtl-vanilla.json")
	var profile struct {
		ID           string `json:"id"`
		InheritsFrom string `json:"inheritsFrom"`
	}
	raw, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("读取纯原版 profile: %v", err)
	}
	if err := json.Unmarshal(raw, &profile); err != nil {
		t.Fatalf("解析纯原版 profile: %v", err)
	}
	if profile.ID != "1.20.1-mtl-vanilla" || profile.InheritsFrom != "1.20.1" {
		t.Fatalf("profile = %+v", profile)
	}

	// launcher_profiles.json 必须是合法 JSON。
	lp, err := os.ReadFile(filepath.Join(instance, "launcher_profiles.json"))
	if err != nil {
		t.Fatalf("读取 launcher_profiles.json: %v", err)
	}
	var profiles map[string]any
	if err := json.Unmarshal(lp, &profiles); err != nil {
		t.Fatalf("launcher_profiles.json 非法: %v", err)
	}

	// assets 必须写入全局缓存目录，而不是实例目录。
	assertFile(t, filepath.Join(assetsDir, "indexes", "test-assets.json"), string([]byte(fmt.Sprintf(`{"objects":{"test/a.txt":{"hash":%q,"size":%d}}}`, sha1Hex("asset-body"), len("asset-body")))))
	assetHash := sha1Hex("asset-body")
	assertFile(t, filepath.Join(assetsDir, "objects", assetHash[:2], assetHash), "asset-body")
	if _, err := os.Stat(filepath.Join(instance, "assets")); !os.IsNotExist(err) {
		t.Fatal("实例内不应创建 assets 目录")
	}

	cfg, err := config.Load(instance)
	if err != nil {
		t.Fatalf("Load 配置: %v", err)
	}
	if cfg.LoaderType != "vanilla" || cfg.LoaderVersion != "1.20.1" || cfg.InstallMode != config.InstallModeTemplate {
		t.Fatalf("配置未正确写入: %+v", cfg)
	}
	assertNoPackLock(t, instance)

	for _, want := range []string{"拉取 Minecraft 1.20.1 版本清单", "游戏核心文件就绪", "资源文件就绪", "安装完成"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("进度日志缺少 %q:\n%s", want, logs.String())
		}
	}
}

func TestInstallFabricTemplate(t *testing.T) {
	m := newMockMeta(t)
	_, assetsDir := m.addVanilla(t, "1.20.1")
	fabricOpts, assetsOpts := m.baseOptions(assetsDir)
	const loaderVersion = "0.15.11"
	const fabricJar = "fabric-jar"
	profileID := "fabric-loader-" + loaderVersion + "-1.20.1"
	fabricProfile, err := json.Marshal(map[string]any{
		"id":           profileID,
		"inheritsFrom": "1.20.1",
		"libraries": []any{map[string]any{
			"name":   "test.group:fablib:1.0",
			"url":    m.srv.URL + "/maven/",
			"sha512": sha512Hex(fabricJar),
			"size":   len(fabricJar),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.routes["/v2/versions/loader/1.20.1/"+loaderVersion+"/profile/json"] = func() []byte { return fabricProfile }
	m.routes["/maven/test/group/fablib/1.0/fablib-1.0.jar"] = func() []byte { return []byte(fabricJar) }
	fabricOpts.MetaBaseURL = m.srv.URL

	instance := filepath.Join(t.TempDir(), "fabric-clean")
	var logs bytes.Buffer
	res, err := Install(context.Background(), instance, Options{
		MinecraftVersion: "1.20.1",
		LoaderType:       "fabric",
		LoaderVersion:    loaderVersion,
		Fabric:           fabricOpts,
		Assets:           assetsOpts,
		Progress:         func(msg string) { logs.WriteString(msg + "\n") },
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.LoaderType != "fabric" || res.LoaderVersion != loaderVersion {
		t.Fatalf("result = %+v", res)
	}

	assertFile(t, filepath.Join(instance, "versions", profileID, profileID+".json"), string(fabricProfile))
	assertFile(t, filepath.Join(instance, "libraries", "test", "group", "fablib", "1.0", "fablib-1.0.jar"), fabricJar)
	assertDir(t, filepath.Join(instance, "mods"))
	assertDir(t, filepath.Join(instance, "saves"))
	if _, err := os.Stat(filepath.Join(instance, "launcher_profiles.json")); err != nil {
		t.Fatalf("缺少 launcher_profiles.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(instance, "versions", "1.20.1-mtl-vanilla")); !os.IsNotExist(err) {
		t.Fatal("Fabric 模板不应生成纯原版薄 profile")
	}

	cfg, err := config.Load(instance)
	if err != nil {
		t.Fatalf("Load 配置: %v", err)
	}
	if cfg.LoaderType != "fabric" || cfg.LoaderVersion != loaderVersion || cfg.InstallMode != config.InstallModeTemplate {
		t.Fatalf("配置未正确写入: %+v", cfg)
	}
	assertNoPackLock(t, instance)

	output := logs.String()
	for _, want := range []string{"安装模式: fabric " + loaderVersion, "安装加载器: fabric " + loaderVersion, "安装完成"} {
		if !strings.Contains(output, want) {
			t.Fatalf("进度日志缺少 %q:\n%s", want, output)
		}
	}
}

func TestInstallRejectsInvalidOptions(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		want string
	}{
		{"empty mc", Options{}, "Minecraft 版本"},
		{"loader without version", Options{MinecraftVersion: "1.20.1", LoaderType: "fabric"}, "--loader-version"},
		{"version without loader", Options{MinecraftVersion: "1.20.1", LoaderVersion: "0.15.11"}, "--loader-version 必须与 --loader"},
		{"unsupported loader", Options{MinecraftVersion: "1.20.1", LoaderType: "quilt", LoaderVersion: "1.0"}, "仅支持 fabric / forge"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Install(context.Background(), t.TempDir(), tt.opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want contains %q", err, tt.want)
			}
		})
	}
}

func TestInstallUnknownMinecraftVersion(t *testing.T) {
	m := newMockMeta(t)
	fabricOpts, assetsOpts := m.baseOptions(t.TempDir())
	m.routes["/manifest.json"] = func() []byte { return []byte(`{"versions":[]}`) }
	_, err := Install(context.Background(), filepath.Join(t.TempDir(), "inst"), Options{
		MinecraftVersion: "9.9.9",
		Fabric:           fabricOpts,
		Assets:           assetsOpts,
	})
	if err == nil || !strings.Contains(err.Error(), "找不到 Minecraft 9.9.9") {
		t.Fatalf("err = %v", err)
	}
}
