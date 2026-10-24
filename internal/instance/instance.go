// Package instance 以只读方式扫描实例根目录、读取实例配置并做轻量健康检查。
// 本包不会创建或修改任何配置文件，也不会启动游戏。
package instance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"mtl/internal/config"
	"mtl/internal/mrpack"
)

// Java 路径状态。空路径表示启动时自动匹配或下载，不算异常。
const (
	JavaAuto          = "自动"
	JavaOK            = "正常"
	JavaMissing       = "缺失"
	JavaNotExecutable = "不可执行"
)

// Entry 是一个可识别的实例及其只读快照。
type Entry struct {
	Name       string
	Dir        string
	ConfigPath string
	Config     config.Config
	// JavaStatus 是配置中 Java 路径的检查结果；JavaDetail 为异常说明。
	JavaStatus string
	JavaDetail string
}

// Skip 描述扫描时跳过的目录及原因。
type Skip struct {
	Name   string
	Dir    string
	Reason error
}

// Check 是健康检查中的单项结果。
type Check struct {
	Level  Level
	Item   string
	Detail string
}

// Level 表示健康检查的严重程度。
type Level int

const (
	LevelOK Level = iota
	LevelWarning
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelOK:
		return "正常"
	case LevelWarning:
		return "警告"
	default:
		return "异常"
	}
}

// Scan 读取 root 下的一级子目录，返回其中配置有效的实例。
// 缺少配置、配置损坏或不是目录的条目会被跳过并记录原因，不阻断其他实例。
func Scan(root string) ([]Entry, []Skip, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, nil, errors.New("实例根目录为空")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, fmt.Errorf("解析实例根目录失败: %w", err)
	}
	dirs, err := os.ReadDir(abs)
	if err != nil {
		return nil, nil, fmt.Errorf("读取实例根目录 %s 失败: %w", abs, err)
	}

	var entries []Entry
	var skipped []Skip
	for _, de := range dirs {
		if !de.IsDir() {
			continue
		}
		name := de.Name()
		dir := filepath.Join(abs, name)
		entry, err := loadEntry(name, dir)
		if err != nil {
			skipped = append(skipped, Skip{Name: name, Dir: dir, Reason: err})
			continue
		}
		entries = append(entries, entry)
	}
	return entries, skipped, nil
}

// Load 按实例名读取单个实例。实例名必须是 root 下的单层目录名，防止路径穿越。
func Load(root, name string) (Entry, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return Entry{}, errors.New("实例根目录为空")
	}
	name = strings.TrimSpace(name)
	if err := validateName(name); err != nil {
		return Entry{}, err
	}
	dir, err := mrpack.SafeJoin(root, name)
	if err != nil {
		return Entry{}, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return Entry{}, fmt.Errorf("实例不存在: %s", dir)
		}
		return Entry{}, fmt.Errorf("读取实例目录失败: %w", err)
	}
	if !info.IsDir() {
		return Entry{}, fmt.Errorf("实例路径不是目录: %s", dir)
	}
	return loadEntry(name, dir)
}

// CheckJava 检查配置中的 Java 可执行文件。
// 空路径视为自动匹配；只做本地文件检查，不执行 java -version。
func CheckJava(path string) (status, detail string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return JavaAuto, "未指定 Java 路径，启动时自动匹配或下载"
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return JavaMissing, "Java 路径不存在: " + path
		}
		return JavaMissing, fmt.Sprintf("无法访问 Java 路径 %s: %v", path, err)
	}
	if info.IsDir() {
		return JavaNotExecutable, "Java 路径是目录: " + path
	}
	if !info.Mode().IsRegular() {
		return JavaNotExecutable, "Java 路径不是普通文件: " + path
	}
	// Windows 不依赖执行位；POSIX 上要求至少一个执行位。
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return JavaNotExecutable, "Java 文件缺少执行权限: " + path
	}
	return JavaOK, ""
}

// Health 返回实例的只读健康检查项。配置字段为空只给出警告或正常提示，
// 不会让 mtl info 失败。
func (e Entry) Health() []Check {
	checks := make([]Check, 0, 5)

	switch e.JavaStatus {
	case JavaOK:
		checks = append(checks, Check{LevelOK, "Java 路径", "可执行: " + e.Config.Java})
	case JavaAuto:
		checks = append(checks, Check{LevelOK, "Java 路径", e.JavaDetail})
	default:
		checks = append(checks, Check{LevelError, "Java 路径", e.JavaDetail})
	}
	if java := strings.TrimSpace(e.Config.Java); java != "" && !filepath.IsAbs(java) {
		checks = append(checks, Check{LevelWarning, "Java 路径", "不是绝对路径，切换工作目录后可能失效: " + java})
	}

	switch strings.ToLower(strings.TrimSpace(e.Config.LoaderType)) {
	case "":
		checks = append(checks, Check{LevelWarning, "加载器", "未记录 loader_type，启动时按 profile id 推断"})
	case "vanilla", "fabric", "forge":
		checks = append(checks, Check{LevelOK, "加载器", e.Config.LoaderType + " " + e.Config.LoaderVersion})
	default:
		checks = append(checks, Check{LevelWarning, "加载器", fmt.Sprintf("MTL 当前支持 vanilla / fabric / forge，配置为 %q", e.Config.LoaderType)})
	}
	if strings.TrimSpace(e.Config.LoaderVersion) == "" {
		checks = append(checks, Check{LevelWarning, "加载器版本", "未记录 loader_version，展示为 \"-\""})
	}
	if e.Config.InstallMode == config.InstallModeTemplate {
		checks = append(checks, Check{LevelOK, "实例类型", "手动纯净模板（不参与 mrpack 增量清理）"})
	}

	if strings.TrimSpace(e.Config.DefaultAccount) == "" {
		checks = append(checks, Check{LevelWarning, "默认账号", "未绑定，mtl run 需要 --account 或 --username"})
	} else {
		checks = append(checks, Check{LevelOK, "默认账号", e.Config.DefaultAccount})
	}
	return checks
}

// CountHealth 统计健康检查结果中的异常与警告数量。
func CountHealth(checks []Check) (failures, warnings int) {
	for _, c := range checks {
		switch c.Level {
		case LevelError:
			failures++
		case LevelWarning:
			warnings++
		}
	}
	return failures, warnings
}

func loadEntry(name, dir string) (Entry, error) {
	configPath, err := config.Path(dir)
	if err != nil {
		return Entry{}, err
	}
	if _, err := os.Stat(configPath); err != nil {
		if os.IsNotExist(err) {
			return Entry{}, errors.New("缺少 .mtl/config.json")
		}
		return Entry{}, fmt.Errorf("检查实例配置失败: %w", err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return Entry{}, fmt.Errorf("实例配置无效: %w", err)
	}
	status, detail := CheckJava(cfg.Java)
	return Entry{
		Name:       name,
		Dir:        dir,
		ConfigPath: configPath,
		Config:     cfg,
		JavaStatus: status,
		JavaDetail: detail,
	}, nil
}

// validateName 只接受单层目录名，复用 mrpack 的跨平台路径安全规则。
func validateName(name string) error {
	if name == "" {
		return errors.New("实例名为空")
	}
	if err := mrpack.ValidateRelPath(name); err != nil {
		return fmt.Errorf("实例名 %q 非法: %w", name, err)
	}
	if strings.Contains(name, "/") {
		return fmt.Errorf("实例名 %q 必须是单个目录名", name)
	}
	return nil
}
