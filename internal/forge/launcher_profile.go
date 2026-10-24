package forge

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"mtl/internal/mrpack"
)

// launcherProfilesSkeleton 是 Forge installer 启动前所要求的最小
// launcher_profiles.json 内容；installer 会自行在其中登记 Forge profile。
var launcherProfilesSkeleton = []byte(`{"profiles":{},"settings":{},"version":3}`)

// ensureVanillaProfile 把 Mojang 返回的原版版本 json 原文写入实例：
//
//	versions/<mc>/<mc>.json
//
// Forge 1.20.1+ installer 依赖该文件识别原版版本；内容一致时不重复写。
func (l *Loader) ensureVanillaProfile(instanceDir, mcVersion string, raw []byte) error {
	if len(raw) == 0 {
		return fmt.Errorf("Mojang 原版版本 json 为空，无法写入实例")
	}
	p, err := mrpack.SafeJoin(instanceDir, "versions/"+mcVersion+"/"+mcVersion+".json")
	if err != nil {
		return fmt.Errorf("原版版本 json 路径非法: %w", err)
	}

	existing, err := os.ReadFile(p)
	if err == nil && bytes.Equal(existing, raw) {
		l.report("写入原版vanilla profile json: %s（内容一致，跳过）", p)
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("读取原版版本 json 失败: %w", err)
	}

	l.report("写入原版vanilla profile json: %s", p)
	if err := writeFileAtomic(p, raw); err != nil {
		return fmt.Errorf("写入原版版本 json 失败: %w", err)
	}
	return nil
}

// ensureLauncherProfiles 在实例根目录准备最小的 launcher_profiles.json。
// Forge installer 缺少该文件时会直接报 "There is no minecraft launcher profile"。
// 已存在时保持原样，不覆盖用户/其他启动器写入的内容。
func (l *Loader) ensureLauncherProfiles(instanceDir string) error {
	p, err := mrpack.SafeJoin(instanceDir, "launcher_profiles.json")
	if err != nil {
		return fmt.Errorf("launcher_profiles.json 路径非法: %w", err)
	}
	if _, err := os.Stat(p); err == nil {
		l.report("launcher_profiles.json 已存在，跳过创建: %s", p)
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查 launcher_profiles.json 失败: %w", err)
	}

	l.report("创建 launcher_profiles.json（Forge installer 前置要求）: %s", p)
	if err := writeFileAtomic(p, launcherProfilesSkeleton); err != nil {
		return fmt.Errorf("创建 launcher_profiles.json 失败: %w", err)
	}
	return nil
}

// writeFileAtomic 先写同目录临时文件再重命名，避免中断留下半个文件。
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
}
