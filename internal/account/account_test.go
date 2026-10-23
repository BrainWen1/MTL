package account

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOfflineUUIDKnownValues(t *testing.T) {
	cases := map[string]string{
		"Notch":  "b50ad385-829d-3141-a216-7e7d7539ba7f",
		"Steve":  "5627dd98-e6be-3c21-b8a8-e92344183641",
		"Alex":   "36532b5e-c442-3dbb-a24c-c7e55d0f979a",
		"Player": "a01e3843-e521-3998-958a-f459800e4d11",
	}
	for name, want := range cases {
		if got := OfflineUUID(name); got != want {
			t.Fatalf("OfflineUUID(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestOfflineUUIDStableAndDistinct(t *testing.T) {
	first := OfflineUUID("Steve")
	for i := range 100 {
		if got := OfflineUUID("Steve"); got != first {
			t.Fatalf("同一用户名第 %d 次计算结果不一致: %q != %q", i, got, first)
		}
	}
	if OfflineUUID("Steve") == OfflineUUID("Alex") {
		t.Fatal("不同用户名不应得到相同 UUID")
	}
	// 大小写不同按不同用户名处理，UUID 必须不同。
	if OfflineUUID("Steve") == OfflineUUID("steve") {
		t.Fatal("大小写不同的用户名应得到不同 UUID")
	}
}

func TestValidateName(t *testing.T) {
	valid := []string{"abc", "Steve", "a_b1", "0123456789abcdef"}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Fatalf("ValidateName(%q) 应通过: %v", name, err)
		}
	}
	invalid := []string{"", "ab", "0123456789abcdefg", "bad name", "玩家", "a-b", "a.b", " steve", "steve "}
	for _, name := range invalid {
		if err := ValidateName(name); err == nil {
			t.Fatalf("ValidateName(%q) 应被拒绝", name)
		}
	}
}

func TestNormalizeUUID(t *testing.T) {
	const dashed = "b50ad385-829d-3141-a216-7e7d7539ba7f"
	for _, in := range []string{dashed, "B50AD385829D3141A2167E7D7539BA7F", "B50AD385-829D-3141-A216-7E7D7539BA7F"} {
		got, err := NormalizeUUID(in)
		if err != nil || got != dashed {
			t.Fatalf("NormalizeUUID(%q) = %q, %v, want %q", in, got, err, dashed)
		}
	}
	for _, in := range []string{"", "xyz", "b50ad385-829d-3141-a216-7e7d7539ba7", "b50ad385-829d-3141-a216-7e7d7539ba7g"} {
		if _, err := NormalizeUUID(in); err == nil {
			t.Fatalf("NormalizeUUID(%q) 应报错", in)
		}
	}
}

func TestStoreAddListRemove(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(s.List()) != 0 {
		t.Fatalf("新账号列表应为空: %+v", s.List())
	}

	alex, created, err := s.Add("Alex")
	if err != nil || !created {
		t.Fatalf("Add(Alex) = %+v, %v, %v", alex, created, err)
	}
	if alex.UUID != OfflineUUID("Alex") {
		t.Fatalf("UUID = %q, want %q", alex.UUID, OfflineUUID("Alex"))
	}
	steve, created, err := s.Add("Steve")
	if err != nil || !created {
		t.Fatalf("Add(Steve) = %+v, %v, %v", steve, created, err)
	}

	// 重复添加是幂等的，不改变已有 UUID。
	again, created, err := s.Add("Alex")
	if err != nil || created || again != alex {
		t.Fatalf("重复 Add 应返回已有账号: %+v, %v, %v", again, created, err)
	}

	list := s.List()
	if len(list) != 2 || list[0].Name != "Alex" || list[1].Name != "Steve" {
		t.Fatalf("List = %+v, want [Alex Steve]", list)
	}

	// 重新打开应读到相同数据。
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("重新 Open: %v", err)
	}
	if a, ok := reopened.Find("alex"); !ok || a != alex {
		t.Fatalf("大小写不敏感查找失败: %+v, %v", a, ok)
	}
	if len(reopened.List()) != 2 {
		t.Fatalf("持久化后账号数 = %d, want 2", len(reopened.List()))
	}

	removed, err := reopened.Remove("Alex")
	if err != nil || removed != alex {
		t.Fatalf("Remove(Alex) = %+v, %v", removed, err)
	}
	if _, ok := reopened.Find("Alex"); ok {
		t.Fatal("删除后仍能查到账号")
	}
	if len(reopened.List()) != 1 {
		t.Fatalf("删除后账号数 = %d, want 1", len(reopened.List()))
	}

	if _, err := reopened.Remove("Alex"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除不存在的账号应返回 ErrNotFound: %v", err)
	}
	if _, _, err := reopened.Add("bad name"); err == nil {
		t.Fatal("非法用户名应被拒绝")
	}
}

func TestOpenMissingFile(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(s.List()) != 0 {
		t.Fatalf("缺失文件应返回空账号列表: %+v", s.List())
	}
}

func TestOpenCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, OfflineFileName)
	cases := []string{
		"",
		"{not json",
		`{"version":1,"accounts":[{"name":"ab","uuid":"b50ad385-829d-3141-a216-7e7d7539ba7f"}]}`,
		`{"version":1,"accounts":[{"name":"Alex","uuid":"not-a-uuid"}]}`,
		`{"version":99,"accounts":[]}`,
		`{"version":1,"accounts":[{"name":"Alex","uuid":"b50ad385-829d-3141-a216-7e7d7539ba7f"},{"name":"alex","uuid":"36532b5e-c442-3dbb-a24c-c7e55d0f979a"}]}`,
	}
	for i, body := range cases {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir); err == nil {
			t.Fatalf("案例 %d 的损坏配置应报错", i)
		}
	}
}

func TestOpenFileNormalizesUUID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, OfflineFileName)
	body := `{"version":1,"accounts":[{"name":"Alex","uuid":"36532B5EC4423DBBA24CC7E55D0F979A"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := "36532b5e-c442-3dbb-a24c-c7e55d0f979a"
	if a, ok := s.Find("Alex"); !ok || a.UUID != want {
		t.Fatalf("Find = %+v, %v, want UUID %s", a, ok, want)
	}
}

func TestSaveFileShape(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Add("Alex"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, OfflineFileName))
	if err != nil {
		t.Fatal(err)
	}
	var f accountFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("保存的文件不是合法 JSON: %v\n%s", err, raw)
	}
	if f.Version != fileVersion || len(f.Accounts) != 1 {
		t.Fatalf("文件内容 = %+v", f)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("保存后残留临时文件: %s", e.Name())
		}
	}
}

func TestDefaultDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		appData := filepath.Join(t.TempDir(), "Roaming")
		t.Setenv("APPDATA", appData)
		got, err := DefaultDir()
		if err != nil || got != filepath.Join(appData, "mtl", "cache", "accounts") {
			t.Fatalf("DefaultDir() = %q, %v", got, err)
		}
		return
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := DefaultDir()
	if err != nil || got != filepath.Join(home, ".cache", "mtl", "accounts") {
		t.Fatalf("DefaultDir() = %q, %v", got, err)
	}
}
