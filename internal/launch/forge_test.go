package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testForgeProfileJSON = `{
  "id": "1.20.1-forge-47.1.0",
  "inheritsFrom": "1.20.1",
  "mainClass": "cpw.mods.bootstraplauncher.BootstrapLauncher",
  "libraries": [
    {"name": "net.minecraftforge:forge:1.20.1-47.1.0", "url": "https://maven.minecraftforge.net/"}
  ],
  "arguments": {
    "jvm": [
      "-DlibraryDirectory=${library_directory}",
      "-DignoreList=minecraft-${version_name}.jar",
      "-p", "${library_directory}/cpw/mods/bootstraplauncher/1.1.2/bootstraplauncher-1.1.2.jar${classpath_separator}${library_directory}/cpw/mods/securejarhandler/2.1.6/securejarhandler-2.1.6.jar"
    ],
    "game": []
  }
}`

// TestLoadForgeInstance 使用 mock 版本 json 验证：
// 主类从 Forge profile 动态读取，${library_directory}/${classpath_separator} 被正确替换。
func TestLoadForgeInstance(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "versions", "1.20.1", "1.20.1.json"), testVanillaJSON)
	writeFile(t, filepath.Join(dir, "versions", "1.20.1", "1.20.1.jar"), "client")
	writeFile(t, filepath.Join(dir, "versions", "1.20.1-forge-47.1.0", "1.20.1-forge-47.1.0.json"), testForgeProfileJSON)
	writeFile(t, filepath.Join(dir, "libraries", "com", "example", "lib", "1.0", "lib-1.0.jar"), "lib")
	writeFile(t, filepath.Join(dir, "libraries", "net", "minecraftforge", "forge", "1.20.1-47.1.0", "forge-1.20.1-47.1.0.jar"), "forge")

	l, err := load(dir, "windows", "amd64")
	if err != nil {
		t.Fatalf("load forge instance: %v", err)
	}
	if l.ProfileID != "1.20.1-forge-47.1.0" {
		t.Fatalf("ProfileID = %q", l.ProfileID)
	}
	if l.MainClass != "cpw.mods.bootstraplauncher.BootstrapLauncher" {
		t.Fatalf("MainClass = %q，应从版本 json 动态读取", l.MainClass)
	}

	natives := filepath.Join(t.TempDir(), "natives")
	assetsDir := filepath.Join(t.TempDir(), "assets")
	jvm, _, err := l.buildArgs(natives, assetsDir, "Steve", "uuid", nil)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	joined := strings.Join(jvm, "\n")
	libDir := filepath.Join(dir, "libraries")
	for _, want := range []string{
		"-DlibraryDirectory=" + libDir,
		libDir + "/cpw/mods/bootstraplauncher/1.1.2/bootstraplauncher-1.1.2.jar" + string(os.PathListSeparator),
		"-DignoreList=minecraft-1.20.1-forge-47.1.0.jar",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("JVM 参数缺少 %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "${") {
		t.Fatalf("存在未替换的占位符:\n%s", joined)
	}
}
