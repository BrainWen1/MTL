package mrpack

import (
	"path/filepath"
	"testing"
)

func TestValidateRelPathAccepts(t *testing.T) {
	valid := []string{
		"mods/example.jar",
		"config/x.json",
		"a/b-c_d.txt",
		"a..b",
		".hidden",
		"空格 文件.txt",
		"模组/测试.jar",
	}
	for _, p := range valid {
		if err := ValidateRelPath(p); err != nil {
			t.Errorf("%q 应合法，却报错: %v", p, err)
		}
	}
}

func TestValidateRelPathRejects(t *testing.T) {
	invalid := []string{
		"",
		".",
		"..",
		"../evil",
		"a/../b",
		"a/./b",
		"a//b",
		"a/",
		"/etc/passwd",
		`a\b`,
		`..\..\evil`,
		"C:/evil",
		"C:evil",
		"a:b",
		`a<b`,
		"a\x00b",
		"a\nb",
		"CON",
		"con.txt",
		"a/CON/b",
		"aux.jar",
		"a.",
		"a ",
		"a/ /b",
	}
	for _, p := range invalid {
		if err := ValidateRelPath(p); err == nil {
			t.Errorf("%q 应被拒绝，却通过了", p)
		}
	}
}

func TestSafeJoinStaysInsideRoot(t *testing.T) {
	root := t.TempDir()
	got, err := SafeJoin(root, "mods/a.jar")
	if err != nil {
		t.Fatalf("SafeJoin 报错: %v", err)
	}
	want := filepath.Join(root, "mods", "a.jar")
	if got != want {
		t.Fatalf("SafeJoin = %q, want %q", got, want)
	}

	for _, rel := range []string{"../x", "/abs", `a\b`, "a/../../b"} {
		if _, err := SafeJoin(root, rel); err == nil {
			t.Errorf("SafeJoin(%q) 应报错", rel)
		}
	}
}
