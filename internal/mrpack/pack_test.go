package mrpack

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type zipItem struct {
	name string
	body string
	mode os.FileMode
}

func writePack(t *testing.T, items ...zipItem) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.mrpack")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, it := range items {
		h := &zip.FileHeader{Name: it.name, Method: zip.Deflate}
		if it.mode != 0 {
			h.SetMode(it.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatalf("创建 zip 条目 %q: %v", it.name, err)
		}
		if _, err := w.Write([]byte(it.body)); err != nil {
			t.Fatalf("写入 zip 条目 %q: %v", it.name, err)
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

func mustOpen(t *testing.T, path string) *Pack {
	t.Helper()
	p, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestOpenPack(t *testing.T) {
	p := mustOpen(t, writePack(t, zipItem{name: IndexFileName, body: minimalIndex()}))
	if p.Index.Name != "Test Pack" {
		t.Fatalf("Index.Name = %q", p.Index.Name)
	}
}

func TestOpenRejectsMissingOrBadIndex(t *testing.T) {
	cases := map[string][]zipItem{
		"missing index": {{name: "readme.txt", body: "hi"}},
		"invalid json":  {{name: IndexFileName, body: "{"}},
		"duplicate index": {
			{name: IndexFileName, body: minimalIndex()},
			{name: IndexFileName, body: minimalIndex()},
		},
	}
	for name, items := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Open(writePack(t, items...)); err == nil {
				t.Fatal("应当报错，却成功打开了包")
			}
		})
	}
}

func TestExtractOverrides(t *testing.T) {
	path := writePack(t,
		zipItem{name: IndexFileName, body: minimalIndex()},
		zipItem{name: "overrides/", mode: os.ModeDir | 0o755},
		zipItem{name: "overrides/config/a.txt", body: "base-a"},
		zipItem{name: "overrides/shared.txt", body: "base"},
		zipItem{name: "client-overrides/shared.txt", body: "client"},
		zipItem{name: "client-overrides/mods/x.jar", body: "jar"},
		zipItem{name: "other/ignored.txt", body: "ignored"},
	)
	dest := filepath.Join(t.TempDir(), "instance")
	if err := mustOpen(t, path).ExtractOverrides(dest); err != nil {
		t.Fatalf("ExtractOverrides: %v", err)
	}

	assertFile := func(rel, want string) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("读取 %s: %v", rel, err)
		}
		if string(b) != want {
			t.Fatalf("%s = %q, want %q", rel, b, want)
		}
	}
	assertFile("config/a.txt", "base-a")
	assertFile("shared.txt", "client")
	assertFile("mods/x.jar", "jar")

	if _, err := os.Stat(filepath.Join(dest, "other")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("overrides 之外的条目不应被解压")
	}
}

func TestExtractNoOverrides(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "instance")
	if err := mustOpen(t, writePack(t, zipItem{name: IndexFileName, body: minimalIndex()})).ExtractOverrides(dest); err != nil {
		t.Fatalf("ExtractOverrides: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("目标目录未创建: %v", err)
	}
}

func TestExtractRejectsEscapingPaths(t *testing.T) {
	badNames := []string{
		"overrides/../../escape.txt",
		"overrides/../escape.txt",
		"overrides//absolute.txt",
		`overrides/..\..\escape.txt`,
		"overrides/C:/escape.txt",
		"overrides/CON",
		"overrides/a/../../b",
		"overrides/a.",
		"overrides/a:b",
		"client-overrides/../../escape.txt",
	}
	for _, name := range badNames {
		t.Run(name, func(t *testing.T) {
			path := writePack(t,
				zipItem{name: IndexFileName, body: minimalIndex()},
				zipItem{name: name, body: "evil"},
			)
			dest := filepath.Join(t.TempDir(), "instance")
			if err := mustOpen(t, path).ExtractOverrides(dest); err == nil {
				t.Fatal("应当拒绝该路径")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "escape.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("文件逃出了目标目录")
			}
		})
	}
}

func TestExtractRejectsSymlink(t *testing.T) {
	path := writePack(t,
		zipItem{name: IndexFileName, body: minimalIndex()},
		zipItem{name: "overrides/link", body: "../outside", mode: os.ModeSymlink | 0o777},
	)
	dest := filepath.Join(t.TempDir(), "instance")
	if err := mustOpen(t, path).ExtractOverrides(dest); err == nil {
		t.Fatal("符号链接条目应被拒绝")
	}
}

func TestExtractRejectsDuplicatePaths(t *testing.T) {
	cases := map[string][]zipItem{
		"same name": {
			{name: "overrides/dup.txt", body: "1"},
			{name: "overrides/dup.txt", body: "2"},
		},
		"case collision": {
			{name: "overrides/Dup.txt", body: "1"},
			{name: "overrides/dup.txt", body: "2"},
		},
	}
	for name, items := range cases {
		t.Run(name, func(t *testing.T) {
			all := append([]zipItem{{name: IndexFileName, body: minimalIndex()}}, items...)
			dest := filepath.Join(t.TempDir(), "instance")
			if err := mustOpen(t, writePack(t, all...)).ExtractOverrides(dest); err == nil {
				t.Fatal("重复路径应被拒绝")
			}
		})
	}
}
