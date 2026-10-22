package mrpack

import (
	"fmt"
	"strings"
	"testing"
)

func minimalIndex() string {
	return fmt.Sprintf(`{
  "formatVersion": 1,
  "game": "minecraft",
  "versionId": "1.0.0",
  "name": "Test Pack",
  "summary": "for tests",
  "files": [
    {
      "path": "mods/example.jar",
      "hashes": {"sha1": "%s", "sha512": "%s"},
      "downloads": ["https://cdn.example.com/example.jar"],
      "fileSize": 1024
    },
    {
      "path": "mods/serveronly.jar",
      "hashes": {"sha1": "%s", "sha512": "%s"},
      "downloads": ["https://cdn.example.com/server.jar"],
      "fileSize": 2048,
      "env": {"client": "unsupported", "server": "required"}
    }
  ],
  "dependencies": {"minecraft": "1.20.1", "fabric-loader": "0.15.11"}
}`, strings.Repeat("a", 40), strings.Repeat("b", 128),
		strings.Repeat("a", 40), strings.Repeat("b", 128))
}

func TestParseIndexValid(t *testing.T) {
	idx, err := ParseIndex([]byte(minimalIndex()))
	if err != nil {
		t.Fatalf("ParseIndex 报错: %v", err)
	}
	if idx.Name != "Test Pack" || idx.VersionID != "1.0.0" {
		t.Fatalf("顶层字段解析错误: %+v", idx)
	}
	if len(idx.Files) != 2 {
		t.Fatalf("files 数量 = %d, want 2", len(idx.Files))
	}
	if idx.Files[1].IsClientFile() {
		t.Fatal("server-only 文件不应被识别为客户端文件")
	}
	client := idx.ClientFiles()
	if len(client) != 1 || client[0].Path != "mods/example.jar" {
		t.Fatalf("ClientFiles = %+v", client)
	}
	if typ, ver := idx.Loader(); typ != LoaderFabric || ver != "0.15.11" {
		t.Fatalf("Loader = %s %s", typ, ver)
	}
	if idx.Files[0].Hashes.SHA512 != strings.Repeat("b", 128) {
		t.Fatal("sha512 解析错误")
	}
}

func TestParseIndexRejectsMalformed(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new  string
	}{
		{"formatVersion", `"formatVersion": 1`, `"formatVersion": 2`},
		{"game", `"game": "minecraft"`, `"game": "bedrock"`},
		{"versionId", `"versionId": "1.0.0",`, ``},
		{"name", `"name": "Test Pack",`, ``},
		{"files missing", `"files": [`, `"renamed": [`},
		{"dependencies missing", `"dependencies": {`, `"renamedDeps": {`},
		{"minecraft dep", `"minecraft": "1.20.1", `, ``},
		{"no loader", `"fabric-loader": "0.15.11"`, `"other-loader": "1"`},
		{"neoforge", `"fabric-loader": "0.15.11"`, `"neoforge": "20.4.0"`},
		{"two loaders", `"fabric-loader": "0.15.11"`, `"fabric-loader": "0.15.11", "forge": "47.2.0"`},
		{"bad path", `"path": "mods/example.jar"`, `"path": "../evil.jar"`},
		{"duplicate path", `"path": "mods/serveronly.jar"`, `"path": "MODS/EXAMPLE.JAR"`},
		{"bad sha512", `"sha512": "` + strings.Repeat("b", 128) + `"`, `"sha512": "abcd"`},
		{"bad sha1", `"sha1": "` + strings.Repeat("a", 40) + `"`, `"sha1": "xyz"`},
		{"bad env", `"client": "unsupported"`, `"client": "disabled"`},
		{"missing env.server", `, "server": "required"`, ``},
		{"bad download", `"https://cdn.example.com/example.jar"`, `"ftp://cdn.example.com/example.jar"`},
		{"negative fileSize", `"fileSize": 1024`, `"fileSize": -1`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := strings.Replace(minimalIndex(), tc.old, tc.new, 1)
			if src == minimalIndex() {
				t.Fatal("测试用例没有改动源 JSON")
			}
			if _, err := ParseIndex([]byte(src)); err == nil {
				t.Fatal("应当解析失败，却成功了")
			}
		})
	}
}

func TestParseIndexRejectsInvalidJSONAndTrailingData(t *testing.T) {
	if _, err := ParseIndex([]byte("{")); err == nil {
		t.Error("非法 JSON 应报错")
	}
	if _, err := ParseIndex([]byte(minimalIndex() + "{}")); err == nil {
		t.Error("末尾多余数据应报错")
	}
}
