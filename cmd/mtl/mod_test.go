package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseModOptions(t *testing.T) {
	opts, err := parseModOptions([]string{"list", "--dir", "./instances/demo", "--debug"})
	if err != nil {
		t.Fatalf("parseModOptions: %v", err)
	}
	if opts.dir != "./instances/demo" || !opts.debug || len(opts.positional) != 1 || opts.positional[0] != "list" {
		t.Errorf("解析结果错误: %+v", opts)
	}

	opts, err = parseModOptions([]string{"disable", "sodium", "--dir=./instances/demo"})
	if err != nil {
		t.Fatalf("parseModOptions: %v", err)
	}
	if opts.dir != "./instances/demo" || opts.debug || opts.positional[0] != "disable" {
		t.Errorf("--dir= 解析错误: %+v", opts)
	}

	for _, args := range [][]string{
		{},
		{"list"},
		{"list", "--dir"},
		{"list", "--unknown"},
	} {
		if _, err := parseModOptions(args); err == nil {
			t.Errorf("参数 %v 应报错", args)
		}
	}
}

func TestModCommandLifecycle(t *testing.T) {
	dir := t.TempDir()
	modsDir := filepath.Join(dir, "mods")
	if err := os.MkdirAll(modsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jar := filepath.Join(modsDir, "plain.jar")
	if err := os.WriteFile(jar, []byte("jar-body"), 0o644); err != nil {
		t.Fatal(err)
	}

	// disable：文件移动到 mods/disabled/，内容不变。
	if err := modCommand([]string{"disable", "plain.jar", "--dir", dir}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := os.Stat(jar); !os.IsNotExist(err) {
		t.Fatal("disable 后原文件应不存在")
	}
	disabled := filepath.Join(modsDir, "disabled", "plain.jar")
	body, err := os.ReadFile(disabled)
	if err != nil {
		t.Fatalf("读取 disabled 文件: %v", err)
	}
	if string(body) != "jar-body" {
		t.Errorf("jar 内容被修改: %q", body)
	}

	// 重复 disable 幂等。
	if err := modCommand([]string{"disable", "PLAIN.JAR", "--dir", dir}); err != nil {
		t.Fatalf("重复 disable: %v", err)
	}

	// enable：移回 mods 根目录。
	if err := modCommand([]string{"enable", "plain", "--dir", dir}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := os.Stat(jar); err != nil {
		t.Fatalf("enable 后文件应回到 mods: %v", err)
	}

	// 再次禁用后按文件名删除。
	if err := modCommand([]string{"disable", "plain.jar", "--dir", dir}); err != nil {
		t.Fatal(err)
	}
	if err := modCommand([]string{"rm", "plain", "--dir", dir}); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if _, err := os.Stat(disabled); !os.IsNotExist(err) {
		t.Fatal("rm 应删除禁用 mod")
	}
}

func TestModCommandRequiresInstanceDir(t *testing.T) {
	if err := modCommand([]string{"list"}); err == nil {
		t.Fatal("缺少 --dir 应报错")
	}
	if err := modCommand([]string{"list", "--dir", filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("实例目录不存在应报错")
	}
	if err := modCommand([]string{"unknown", "--dir", t.TempDir()}); err == nil {
		t.Fatal("未知子命令应报错")
	}
}
