// Package config 负责实例级启动配置（<实例目录>/.mtl/config.json）的读写。
// 配置只影响启动参数组装，不涉及 runtime/assets/mrpack 等既有缓存。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"mtl/internal/memory"
	"mtl/internal/mrpack"
)

const (
	// DirName 是实例目录下存放 mtl 私有配置的子目录，避免污染 mods 等原版目录。
	DirName = ".mtl"
	// FileName 是实例配置文件名称。
	FileName = "config.json"

	// ModeAuto 使用按物理内存自动计算的最大堆。
	ModeAuto = "auto"
	// ModeManual 使用 ManualMemory 指定最大堆。
	ModeManual = "manual"

	// InstallModeTemplate 表示实例由 `mtl install --mc ...` 创建，是不依赖 mrpack 的纯净模板。
	InstallModeTemplate = "template"
)

// Config 对应 .mtl/config.json 的结构。
// JSON 字段名使用 snake_case，便于用户直接编辑。
type Config struct {
	MemoryMode   string   `json:"memory_mode"`
	ManualMemory string   `json:"manual_memory"`
	JVMArgs      []string `json:"jvm_args"`
	GameArgs     []string `json:"game_args"`
	// PreferHighPerfGPU 开启后在 Windows 上为 Java 进程设置高性能 GPU 首选项。
	// 非 Windows 平台静默忽略；默认 false。
	PreferHighPerfGPU bool `json:"prefer_high_perf_gpu"`
	// DefaultAccount 是实例绑定的默认离线账号名，空字符串表示未绑定。
	// 只保存账号名称，不存储密码或令牌。
	DefaultAccount string `json:"default_account"`
	// LoaderType / LoaderVersion 记录安装时使用的加载器，供 run 阶段选择实现。
	// 旧版本实例没有这两个字段，读取后为空，由 launch 按 profile id 兼容推断。
	LoaderType    string `json:"loader_type"`
	LoaderVersion string `json:"loader_version"`
	// InstallMode 记录实例安装方式；template 表示不依赖 mrpack 的纯净模板实例。
	// 旧实例与 mrpack 实例为空，保持向后兼容。
	InstallMode string `json:"install_mode,omitempty"`
	// Java 是上次成功启动时使用的 Java 可执行文件绝对路径。
	// 只绑定路径，Java 运行时本体仍存放在全局缓存中供各实例共享。
	// 下次 run 会优先复用；文件被手动删除时视为失效并重新自动匹配。
	Java string `json:"java"`
}

// Default 返回配置文件缺失或损坏时使用的默认配置：自动内存、无自定义参数、
// 不启用高性能 GPU 调度、不绑定默认账号。
func Default() Config {
	return Config{
		MemoryMode:        ModeAuto,
		JVMArgs:           []string{},
		GameArgs:          []string{},
		PreferHighPerfGPU: false,
		DefaultAccount:    "",
	}
}

// Path 返回实例配置文件路径。rel 固定为 ".mtl/config.json"，用 SafeJoin 防止越界。
func Path(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("实例目录为空")
	}
	return mrpack.SafeJoin(dir, DirName+"/"+FileName)
}

// Load 读取实例配置。文件不存在时返回默认配置且不报错；
// 文件损坏或字段非法时返回默认配置和错误，调用方应告警后继续使用返回值，不能 panic。
func Load(dir string) (Config, error) {
	def := Default()
	path, err := Path(dir)
	if err != nil {
		return def, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return def, nil
	}
	if err != nil {
		return def, fmt.Errorf("读取实例配置 %s 失败: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return def, fmt.Errorf("实例配置 %s 损坏: %w", path, err)
	}
	cfg, err = normalize(cfg)
	if err != nil {
		return def, fmt.Errorf("实例配置 %s 无效: %w", path, err)
	}
	return cfg, nil
}

// Save 校验并原子写入实例配置，目录不存在时自动创建。
// 重复保存相同内容不会改变结果，满足幂等要求。
func Save(dir string, cfg Config) error {
	cfg, err := normalize(cfg)
	if err != nil {
		return err
	}
	path, err := Path(dir)
	if err != nil {
		return err
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("创建实例配置目录失败: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("编码实例配置失败: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(parent, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("创建实例配置临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // 成功后 Rename 会使之不存在，忽略错误即可

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("设置实例配置权限失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入实例配置失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭实例配置失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("保存实例配置失败: %w", err)
	}
	return nil
}

// SetLoaderInfo 保留实例现有配置（内存、账号、参数等），只更新加载器与安装方式。
// 配置缺失或损坏时回落到默认配置，行为与安装流程一致。
func SetLoaderInfo(dir, loaderType, loaderVersion, installMode string) error {
	cfg, err := Load(dir)
	if err != nil {
		cfg = Default()
	}
	cfg.LoaderType = loaderType
	cfg.LoaderVersion = loaderVersion
	cfg.InstallMode = installMode
	return Save(dir, cfg)
}

// Format 返回便于打印的缩进 JSON，供 mtl config show 与 run --debug 使用。
func Format(cfg Config) string {
	if norm, err := normalize(cfg); err == nil {
		cfg = norm
	} else {
		cfg = Default()
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}

// ValidateDefaultAccount 对实例默认账号名做轻量校验：空字符串表示解绑；
// 非空时禁止路径分隔符、控制字符等可能被误用为路径的字符。
// 这里不做账号存在性检查，只保证写入配置的内容安全。
func ValidateDefaultAccount(name string) error {
	if name == "" {
		return nil
	}
	if name == "." || name == ".." {
		return fmt.Errorf("default_account %q 非法", name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("default_account %q 非法：包含控制字符", name)
		}
		if strings.ContainsRune(`/\:*?"<>|`, r) {
			return fmt.Errorf("default_account %q 非法：包含路径保留字符 %q", name, r)
		}
	}
	return nil
}

// normalize 补齐缺省字段并校验取值。空 memory_mode 视为 auto。
func normalize(cfg Config) (Config, error) {
	cfg.MemoryMode = strings.ToLower(strings.TrimSpace(cfg.MemoryMode))
	if cfg.MemoryMode == "" {
		cfg.MemoryMode = ModeAuto
	}
	cfg.ManualMemory = strings.TrimSpace(cfg.ManualMemory)
	cfg.DefaultAccount = strings.TrimSpace(cfg.DefaultAccount)
	cfg.LoaderType = strings.ToLower(strings.TrimSpace(cfg.LoaderType))
	cfg.LoaderVersion = strings.TrimSpace(cfg.LoaderVersion)
	cfg.InstallMode = strings.ToLower(strings.TrimSpace(cfg.InstallMode))
	cfg.Java = strings.TrimSpace(cfg.Java)
	if err := ValidateDefaultAccount(cfg.DefaultAccount); err != nil {
		return Default(), err
	}
	if cfg.JVMArgs == nil {
		cfg.JVMArgs = []string{}
	}
	if cfg.GameArgs == nil {
		cfg.GameArgs = []string{}
	}

	switch cfg.MemoryMode {
	case ModeAuto:
		// 自动模式忽略 manual_memory，保留字段原值以便切回 manual 时复用。
	case ModeManual:
		if cfg.ManualMemory == "" {
			return Default(), errors.New("memory_mode=manual 时必须设置 manual_memory")
		}
		if _, err := memory.Parse(cfg.ManualMemory); err != nil {
			return Default(), fmt.Errorf("manual_memory 无效: %w", err)
		}
	default:
		return Default(), fmt.Errorf("memory_mode %q 无效（可用: %s、%s）", cfg.MemoryMode, ModeAuto, ModeManual)
	}
	return cfg, nil
}

// MemoryChoice 是合并实例配置与命令行后最终生效的内存设置。
type MemoryChoice struct {
	Value    string
	Auto     bool
	ValueSet bool
	Source   string
}

// EffectiveMemory 按“命令行 > 实例配置 > 默认自动”的优先级解析内存设置。
// 同时传入 --memory 与 --memory-auto 时保持既有行为：自动模式优先。
func EffectiveMemory(cfg Config, cliValue string, cliValueSet, cliAuto bool) MemoryChoice {
	switch {
	case cliAuto:
		return MemoryChoice{Value: cliValue, Auto: true, ValueSet: cliValueSet, Source: "命令行 --memory-auto"}
	case cliValueSet:
		return MemoryChoice{Value: cliValue, Auto: false, ValueSet: true, Source: "命令行 --memory"}
	case cfg.MemoryMode == ModeManual:
		return MemoryChoice{Value: cfg.ManualMemory, Auto: false, ValueSet: true, Source: "实例配置（manual）"}
	default:
		return MemoryChoice{Auto: true, Source: "实例配置/默认（auto）"}
	}
}

// EffectiveHighPerfGPU 按“命令行 > 实例配置 > 默认 false”的优先级解析高性能 GPU 调度。
// cliSet 表示命令行动了显式设置（--highperf-gpu 或 --no-highperf-gpu）。
func EffectiveHighPerfGPU(cfg Config, cliValue, cliSet bool) (enabled bool, source string) {
	if cliSet {
		return cliValue, "命令行 --highperf-gpu/--no-highperf-gpu"
	}
	if cfg.PreferHighPerfGPU {
		return true, "实例配置（prefer_high_perf_gpu）"
	}
	return false, "实例配置/默认（false）"
}

// AccountChoice 是按优先级解析出的本次启动账号及其来源。
type AccountChoice struct {
	Name   string
	Source string
}

// Set 表示是否解析到了账号。
func (c AccountChoice) Set() bool { return c.Name != "" }

// EffectiveAccount 按“命令行 --account > 实例配置 default_account”的优先级解析账号。
// 两者都为空时返回零值，由调用方决定是否报错。
func EffectiveAccount(cfg Config, cliAccount string) AccountChoice {
	if name := strings.TrimSpace(cliAccount); name != "" {
		return AccountChoice{Name: name, Source: "命令行"}
	}
	if cfg.DefaultAccount != "" {
		return AccountChoice{Name: cfg.DefaultAccount, Source: "实例默认账号"}
	}
	return AccountChoice{}
}

// MergeArgs 合并参数：实例配置在前，命令行追加在后。
// 返回新切片，不修改入参。
func MergeArgs(base, extra []string) []string {
	out := make([]string, 0, len(base)+len(extra))
	out = append(out, base...)
	out = append(out, extra...)
	return out
}

// AddJVMArg 追加一条 JVM 参数；已存在时返回 false 且不改变列表。
func (c *Config) AddJVMArg(arg string) bool {
	list, added := addArg(c.JVMArgs, arg)
	c.JVMArgs = list
	return added
}

// RemoveJVMArg 删除第一条匹配的 JVM 参数；不存在时返回 false。
func (c *Config) RemoveJVMArg(arg string) bool {
	list, removed := removeArg(c.JVMArgs, arg)
	c.JVMArgs = list
	return removed
}

// AddGameArg 追加一条游戏参数；已存在时返回 false 且不改变列表。
func (c *Config) AddGameArg(arg string) bool {
	list, added := addArg(c.GameArgs, arg)
	c.GameArgs = list
	return added
}

// RemoveGameArg 删除第一条匹配的游戏参数；不存在时返回 false。
func (c *Config) RemoveGameArg(arg string) bool {
	list, removed := removeArg(c.GameArgs, arg)
	c.GameArgs = list
	return removed
}

func addArg(list []string, arg string) ([]string, bool) {
	if slices.Contains(list, arg) {
		return list, false
	}
	return append(list, arg), true
}

func removeArg(list []string, arg string) ([]string, bool) {
	for i, a := range list {
		if a == arg {
			return append(list[:i], list[i+1:]...), true
		}
	}
	return list, false
}
