package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testVanillaJSON = `{
  "id": "1.20.1",
  "mainClass": "net.minecraft.client.main.Main",
  "type": "release",
  "javaVersion": {"component": "java-runtime-gamma", "majorVersion": 17},
  "assetIndex": {"id": "5"},
  "assets": "5",
  "arguments": {
    "game": [
      "--username", "${auth_player_name}",
      "--version", "${version_name}",
      "--gameDir", "${game_directory}",
      "--assetsDir", "${assets_root}",
      "--assetIndex", "${assets_index_name}",
      "--uuid", "${auth_uuid}",
      "--accessToken", "${auth_access_token}",
      "--clientId", "${clientid}",
      "--xuid", "${auth_xuid}",
      "--userType", "${user_type}",
      "--versionType", "${version_type}"
    ],
    "jvm": [
      "-Djava.library.path=${natives_directory}",
      "-Dminecraft.launcher.brand=${launcher_name}",
      "-cp", "${classpath}"
    ]
  },
  "libraries": [
    {"name": "com.example:lib:1.0", "downloads": {"artifact": {"path": "com/example/lib/1.0/lib-1.0.jar"}}}
  ],
  "downloads": {"client": {"sha1": "x", "size": 1, "url": "https://example.com/client.jar"}}
}`

const testProfileJSON = `{
  "id": "fabric-loader-0.15.0-1.20.1",
  "inheritsFrom": "1.20.1",
  "mainClass": "net.fabricmc.loader.impl.launch.knot.KnotClient",
  "libraries": [
    {"name": "net.fabricmc:fabric-loader:0.15.0", "url": "https://maven.fabricmc.net/"}
  ],
  "arguments": {
    "jvm": ["-DFabricMcEmu= net.minecraft.client.main.Main"],
    "game": []
  }
}`

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeTestInstance(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "versions", "1.20.1", "1.20.1.json"), testVanillaJSON)
	writeFile(t, filepath.Join(dir, "versions", "1.20.1", "1.20.1.jar"), "client")
	writeFile(t, filepath.Join(dir, "versions", "fabric-loader-0.15.0-1.20.1", "fabric-loader-0.15.0-1.20.1.json"), testProfileJSON)
	writeFile(t, filepath.Join(dir, "libraries", "com", "example", "lib", "1.0", "lib-1.0.jar"), "lib")
	writeFile(t, filepath.Join(dir, "libraries", "net", "fabricmc", "fabric-loader", "0.15.0", "fabric-loader-0.15.0.jar"), "loader")
	writeFile(t, filepath.Join(dir, "mods", "a.jar"), "mod-a")
	writeFile(t, filepath.Join(dir, "mods", "b.JAR"), "mod-b")
	writeFile(t, filepath.Join(dir, "mods", "readme.txt"), "ignored")
	return dir
}

func TestLoadFabricInstance(t *testing.T) {
	dir := writeTestInstance(t)
	l, err := load(dir, "windows", "amd64")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if l.ProfileID != "fabric-loader-0.15.0-1.20.1" {
		t.Fatalf("ProfileID = %q", l.ProfileID)
	}
	if l.GameVersion != "1.20.1" {
		t.Fatalf("GameVersion = %q", l.GameVersion)
	}
	if l.MainClass != "net.fabricmc.loader.impl.launch.knot.KnotClient" {
		t.Fatalf("MainClass = %q", l.MainClass)
	}
	if l.JavaMajor != 17 {
		t.Fatalf("JavaMajor = %d", l.JavaMajor)
	}

	want := []string{
		filepath.Join(dir, "versions", "1.20.1", "1.20.1.jar"),
		filepath.Join(dir, "libraries", "com", "example", "lib", "1.0", "lib-1.0.jar"),
		filepath.Join(dir, "libraries", "net", "fabricmc", "fabric-loader", "0.15.0", "fabric-loader-0.15.0.jar"),
		filepath.Join(dir, "mods", "a.jar"),
		filepath.Join(dir, "mods", "b.JAR"),
	}
	got := l.ClassPathEntries()
	if len(got) != len(want) {
		t.Fatalf("classpath = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("classpath[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	cp := l.ClassPath()
	if sep := string(os.PathListSeparator); strings.Count(cp, sep) != len(want)-1 {
		t.Fatalf("classpath 分隔符数量错误: %q", cp)
	}
}

func TestLoadWithoutModsDir(t *testing.T) {
	dir := writeTestInstance(t)
	if err := os.RemoveAll(filepath.Join(dir, "mods")); err != nil {
		t.Fatal(err)
	}
	l, err := load(dir, "windows", "amd64")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(l.Mods) != 0 {
		t.Fatalf("mods = %v", l.Mods)
	}
}

func TestLoadRejectsAmbiguousProfiles(t *testing.T) {
	dir := writeTestInstance(t)
	second := map[string]string{
		"id":           "forge-47.2.0-1.20.1",
		"inheritsFrom": "1.20.1",
		"mainClass":    "cpw.mods.bootstraplauncher.BootstrapLauncher",
	}
	body := `{"id":"` + second["id"] + `","inheritsFrom":"` + second["inheritsFrom"] + `","mainClass":"` + second["mainClass"] + `"}`
	writeFile(t, filepath.Join(dir, "versions", second["id"], second["id"]+".json"), body)

	_, err := load(dir, "windows", "amd64")
	if err == nil || !strings.Contains(err.Error(), "多个") {
		t.Fatalf("应拒绝多个 profile: %v", err)
	}
}

func TestLoadWithoutProfile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "versions", "1.20.1", "1.20.1.json"), testVanillaJSON)
	if _, err := load(dir, "windows", "amd64"); err == nil {
		t.Fatal("缺少 Fabric profile 应报错")
	}
}

func TestBuildArgs(t *testing.T) {
	dir := writeTestInstance(t)
	l, err := load(dir, "windows", "amd64")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	const natives = "/tmp/natives"
	jvm, game, err := l.buildArgs(natives, "Steve", []string{"-Xmx4G"})
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}

	joinedJVM := strings.Join(jvm, "\n")
	for _, want := range []string{
		"-Djava.library.path=" + natives,
		"-Dminecraft.launcher.brand=mtl",
		"-cp\n" + l.ClassPath(),
		"-DFabricMcEmu= net.minecraft.client.main.Main",
		"-Xmx4G",
	} {
		if !strings.Contains(joinedJVM, want) {
			t.Fatalf("JVM 参数缺少 %q:\n%s", want, joinedJVM)
		}
	}

	joinedGame := strings.Join(game, "\n")
	for _, want := range []string{
		"--username\nSteve",
		"--gameDir\n" + dir,
		"--accessToken\n0",
	} {
		if !strings.Contains(joinedGame, want) {
			t.Fatalf("游戏参数缺少 %q:\n%s", want, joinedGame)
		}
	}
	if strings.Contains(joinedGame, "${") {
		t.Fatalf("存在未替换的占位符:\n%s", joinedGame)
	}
}

func TestSubstituteRejectsUnknownPlaceholder(t *testing.T) {
	if _, err := substitute("--foo ${unknown_thing}", map[string]string{"a": "b"}); err == nil {
		t.Fatal("未知占位符应报错")
	}
	if got, err := substitute("-cp ${classpath}", map[string]string{"classpath": "a:b"}); err != nil || got != "-cp a:b" {
		t.Fatalf("substitute = %q, %v", got, err)
	}
}

func TestParseJavaMajor(t *testing.T) {
	cases := map[string]int{
		`openjdk version "17.0.10" 2024-01-16`: 17,
		`java version "1.8.0_392"`:             8,
		`openjdk version "21.0.2"`:             21,
	}
	for input, want := range cases {
		got, err := parseJavaMajor(input)
		if err != nil || got != want {
			t.Fatalf("parseJavaMajor(%q) = %d, %v, want %d", input, got, err, want)
		}
	}
	if _, err := parseJavaMajor("no version here"); err == nil {
		t.Fatal("无法解析的版本输出应报错")
	}
}

func TestParseVersionArguments(t *testing.T) {
	const raw = `{
	  "arguments": {
	    "game": ["--username", {"rules":[{"action":"allow","os":{"name":"windows"}}],"value":["--win"]}],
	    "jvm": [{"value":"-Xss1M"}]
	  }
	}`
	v, err := ParseVersion([]byte(raw))
	if err != nil {
		t.Fatalf("ParseVersion: %v", err)
	}
	if len(v.Arguments.Game) != 2 || v.Arguments.Game[0].Values[0] != "--username" {
		t.Fatalf("game args = %+v", v.Arguments.Game)
	}
	if len(v.Arguments.Game[1].Rules) != 1 || v.Arguments.Game[1].Values[0] != "--win" {
		t.Fatalf("conditional arg = %+v", v.Arguments.Game[1])
	}
	if len(v.Arguments.JVM) != 1 || v.Arguments.JVM[0].Values[0] != "-Xss1M" {
		t.Fatalf("jvm args = %+v", v.Arguments.JVM)
	}
}
