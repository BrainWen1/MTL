package mrpack

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Windows 设备名会被系统当成设备而不是普通文件，跨平台包中一律拒绝。
var windowsReservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ValidateRelPath 只接受以 / 分隔的包内相对路径。
// 拒绝绝对路径、..、反斜杠、盘符/ADS、Windows 非法字符，保证同一路径在 Linux/Windows 上含义一致。
func ValidateRelPath(p string) error {
	if p == "" {
		return fmt.Errorf("路径为空")
	}
	if strings.ContainsRune(p, '\x00') {
		return fmt.Errorf("路径包含 NUL 字符")
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("不允许绝对路径")
	}
	if strings.Contains(p, `\`) {
		return fmt.Errorf("不允许反斜杠")
	}
	if strings.ContainsAny(p, `<>:"|?*`) {
		return fmt.Errorf("路径包含非法字符")
	}
	for _, r := range p {
		if r < 0x20 {
			return fmt.Errorf("路径包含控制字符")
		}
	}

	for _, part := range strings.Split(p, "/") {
		switch part {
		case "":
			return fmt.Errorf("路径包含空目录段")
		case ".", "..":
			return fmt.Errorf("路径包含 %q", part)
		}
		if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return fmt.Errorf("目录段 %q 以点或空格结尾", part)
		}
		base := part
		if i := strings.IndexByte(base, '.'); i >= 0 {
			base = base[:i]
		}
		if windowsReservedNames[strings.ToLower(base)] {
			return fmt.Errorf("目录段 %q 是 Windows 保留名", part)
		}
	}

	// 盘符形如 C:foo，即使没有绝对路径前缀也必须拒绝。
	if len(p) >= 2 && p[1] == ':' {
		return fmt.Errorf("不允许盘符前缀")
	}
	return nil
}

// SafeJoin 把包内相对路径拼到 root 下，并再次确认结果没有逃出 root。
func SafeJoin(root, rel string) (string, error) {
	if err := ValidateRelPath(rel); err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("解析根目录失败: %w", err)
	}
	full := filepath.Join(rootAbs, filepath.FromSlash(rel))
	back, err := filepath.Rel(rootAbs, full)
	if err != nil {
		return "", fmt.Errorf("路径越界: %w", err)
	}
	if back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) || filepath.IsAbs(back) {
		return "", fmt.Errorf("路径 %q 逃出目标目录", rel)
	}
	return full, nil
}
