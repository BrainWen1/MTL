package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"mtl/internal/mrpack"
)

func sha512Hex(s string) string {
	sum := sha512.Sum512([]byte(s))
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
	if err := installPack(pack, instance, 2, &out); err != nil {
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

	if err := installPack(pack, instance, 0, io.Discard); err != nil {
		t.Fatalf("首次安装: %v", err)
	}
	var out bytes.Buffer
	if err := installPack(pack, instance, 0, &out); err != nil {
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
