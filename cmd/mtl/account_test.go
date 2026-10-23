package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"mtl/internal/account"
)

// isolateAccountDir 把默认账号目录指向临时目录，避免污染用户缓存。
func isolateAccountDir(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", t.TempDir())
		return
	}
	t.Setenv("HOME", t.TempDir())
}

func TestAccountCommands(t *testing.T) {
	isolateAccountDir(t)

	if err := run([]string{"account", "add", "Alex"}); err != nil {
		t.Fatalf("account add: %v", err)
	}
	// 重复添加应幂等成功。
	if err := run([]string{"account", "add", "Alex"}); err != nil {
		t.Fatalf("重复 account add 应成功: %v", err)
	}
	store, err := account.OpenDefault()
	if err != nil {
		t.Fatalf("OpenDefault: %v", err)
	}
	a, ok := store.Find("Alex")
	if !ok || a.UUID != account.OfflineUUID("Alex") {
		t.Fatalf("添加后的账号 = %+v, %v", a, ok)
	}
	dir, err := account.DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, account.OfflineFileName)); err != nil {
		t.Fatalf("账号文件未生成: %v", err)
	}

	if err := run([]string{"account", "list"}); err != nil {
		t.Fatalf("account list: %v", err)
	}
	if err := run([]string{"account", "rm", "Alex"}); err != nil {
		t.Fatalf("account rm: %v", err)
	}
	store, err = account.OpenDefault()
	if err != nil {
		t.Fatalf("OpenDefault: %v", err)
	}
	if len(store.List()) != 0 {
		t.Fatalf("删除后账号列表应为空: %+v", store.List())
	}
	if err := run([]string{"account", "rm", "Alex"}); err == nil {
		t.Fatal("删除不存在的账号应报错")
	}
	if err := run([]string{"account", "add", "ab"}); err == nil {
		t.Fatal("非法用户名应被拒绝")
	}
	if err := run([]string{"account"}); err == nil {
		t.Fatal("缺少子命令应报错")
	}
}

func TestSelectAccount(t *testing.T) {
	store, err := account.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Add("Alex"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Add("WenWen12"); err != nil {
		t.Fatal(err)
	}
	alexUUID := account.OfflineUUID("Alex")
	wenUUID := account.OfflineUUID("WenWen12")

	t.Run("account 优先于 username", func(t *testing.T) {
		name, uuid, source, err := selectAccount("Alex", "Steve", true, "", store, nil, io.Discard, io.Discard)
		if err != nil || name != "Alex" || uuid != alexUUID || source != "命令行" {
			t.Fatalf("selectAccount = %q, %q, %q, %v", name, uuid, source, err)
		}
	})

	t.Run("实例默认账号", func(t *testing.T) {
		name, uuid, source, err := selectAccount("", "Player", false, "WenWen12", store, nil, io.Discard, io.Discard)
		if err != nil || name != "WenWen12" || uuid != wenUUID || source != "实例默认账号" {
			t.Fatalf("selectAccount = %q, %q, %q, %v", name, uuid, source, err)
		}
	})

	t.Run("account 覆盖实例默认账号", func(t *testing.T) {
		name, uuid, source, err := selectAccount("Alex", "Player", false, "WenWen12", store, nil, io.Discard, io.Discard)
		if err != nil || name != "Alex" || uuid != alexUUID || source != "命令行" {
			t.Fatalf("selectAccount = %q, %q, %q, %v", name, uuid, source, err)
		}
	})

	t.Run("无 account 且无默认账号时报错", func(t *testing.T) {
		if _, _, _, err := selectAccount("", "Player", false, "", store, nil, io.Discard, io.Discard); err == nil {
			t.Fatal("未指定账号应报错")
		}
	})

	t.Run("默认账号不存在时报错", func(t *testing.T) {
		if _, _, _, err := selectAccount("", "Player", false, "Nobody", store, nil, io.Discard, io.Discard); err == nil {
			t.Fatal("不存在的默认账号应报错")
		}
	})

	t.Run("account 不存在时报错", func(t *testing.T) {
		if _, _, _, err := selectAccount("Nobody", "", false, "", store, nil, io.Discard, io.Discard); err == nil {
			t.Fatal("不存在的账号应报错")
		}
	})

	t.Run("临时 username", func(t *testing.T) {
		name, uuid, source, err := selectAccount("", "Bob", true, "", store, nil, io.Discard, io.Discard)
		if err != nil || name != "Bob" || uuid != account.OfflineUUID("Bob") || source != "命令行 --username" {
			t.Fatalf("selectAccount = %q, %q, %q, %v", name, uuid, source, err)
		}
	})

	t.Run("username 命中已保存账号", func(t *testing.T) {
		name, uuid, _, err := selectAccount("", "alex", true, "", store, nil, io.Discard, io.Discard)
		if err != nil || name != "Alex" || uuid != alexUUID {
			t.Fatalf("selectAccount = %q, %q, %v", name, uuid, err)
		}
	})

	t.Run("非法 username", func(t *testing.T) {
		if _, _, _, err := selectAccount("", "bad name", true, "", store, nil, io.Discard, io.Discard); err == nil {
			t.Fatal("非法用户名应报错")
		}
	})

	t.Run("损坏配置回退到 account 名", func(t *testing.T) {
		name, uuid, _, err := selectAccount("Alex", "Player", false, "", nil, errors.New("corrupt"), io.Discard, io.Discard)
		if err != nil || name != "Alex" || uuid != alexUUID {
			t.Fatalf("selectAccount = %q, %q, %v", name, uuid, err)
		}
	})

	t.Run("损坏配置回退到默认账号", func(t *testing.T) {
		name, uuid, source, err := selectAccount("", "Player", false, "WenWen12", nil, errors.New("corrupt"), io.Discard, io.Discard)
		if err != nil || name != "WenWen12" || uuid != wenUUID || source != "实例默认账号" {
			t.Fatalf("selectAccount = %q, %q, %q, %v", name, uuid, source, err)
		}
	})

	t.Run("损坏配置回退到显式 username", func(t *testing.T) {
		name, uuid, _, err := selectAccount("Alex", "Steve", true, "", nil, errors.New("corrupt"), io.Discard, io.Discard)
		if err != nil || name != "Steve" || uuid != account.OfflineUUID("Steve") {
			t.Fatalf("selectAccount = %q, %q, %v", name, uuid, err)
		}
	})
}
