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
)

// Config 对应 .mtl/config.json 的结构。
// JSON 字段名使用 snake_case，便于用户直接编辑。
type Config struct {
	MemoryMode   string   `json:"memory_mode"`
	ManualMemory string   `json:"manual_memory"`
	JVMArgs      []string `json:"jvm_args"`
	GameArgs     []string `json:"game_args"`
}

// Default 返回配置文件缺失或损坏时使用的默认配置：自动内存、无自定义参数。
func Default() Config {
	return Config{
		MemoryMode: ModeAuto,
		JVMArgs:    []string{},
		GameArgs:   []string{},
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

// normalize 补齐缺省字段并校验取值。空 memory_mode 视为 auto。
func normalize(cfg Config) (Config, error) {
	cfg.MemoryMode = strings.ToLower(strings.TrimSpace(cfg.MemoryMode))
	if cfg.MemoryMode == "" {
		cfg.MemoryMode = ModeAuto
	}
	cfg.ManualMemory = strings.TrimSpace(cfg.ManualMemory)
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
