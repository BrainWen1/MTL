package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"mtl/internal/config"
	"mtl/internal/memory"
)

// configCommand 处理 mtl config 子命令。配置存放于 <实例目录>/.mtl/config.json。
func configCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: mtl config <show|set-memory|set-highperf-gpu|add-jvm-arg|remove-jvm-arg|add-game-arg|remove-game-arg|reset> ...")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "show":
		return configShow(rest)
	case "set-memory":
		return configSetMemory(rest)
	case "set-highperf-gpu":
		return configSetHighPerfGPU(rest)
	case "add-jvm-arg":
		return configAddArg(rest, true)
	case "remove-jvm-arg":
		return configRemoveArg(rest, true)
	case "add-game-arg":
		return configAddArg(rest, false)
	case "remove-game-arg":
		return configRemoveArg(rest, false)
	case "reset":
		return configReset(rest)
	default:
		return fmt.Errorf("未知 config 子命令 %q（可用: show、set-memory、set-highperf-gpu、add-jvm-arg、remove-jvm-arg、add-game-arg、remove-game-arg、reset）", sub)
	}
}

// parseConfigArgs 解析 --dir/--value，其余参数原样作为位置参数。
// 不用 flag 包是必要的：JVM 参数常以 "-" 开头（如 -XX:-OmitStackTraceInFastThrow），
// flag 会把它误判为未定义 flag。
func parseConfigArgs(args []string) (dir, value string, positional []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dir":
			if i+1 >= len(args) {
				return "", "", nil, errors.New("--dir 缺少参数值")
			}
			i++
			dir = args[i]
		case strings.HasPrefix(a, "--dir="):
			dir = strings.TrimPrefix(a, "--dir=")
		case a == "--value":
			if i+1 >= len(args) {
				return "", "", nil, errors.New("--value 缺少参数值")
			}
			i++
			value = args[i]
		case strings.HasPrefix(a, "--value="):
			value = strings.TrimPrefix(a, "--value=")
		default:
			positional = append(positional, a)
		}
	}
	return dir, value, positional, nil
}

// loadInstanceConfig 读取实例配置；损坏或格式错误时告警并回退默认配置，不中断命令。
func loadInstanceConfig(dir string) config.Config {
	cfg, err := config.Load(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[Config] 警告: %v；回退默认配置\n", err)
	}
	return cfg
}

func configPathForLog(dir string) string {
	p, err := config.Path(dir)
	if err != nil {
		return dir
	}
	return p
}

func configShow(args []string) error {
	dir, _, positional, err := parseConfigArgs(args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("用法: mtl config show --dir <实例目录>")
	}
	if dir == "" {
		return errors.New("必须用 --dir 指定实例目录")
	}
	fmt.Println(config.Format(loadInstanceConfig(dir)))
	return nil
}

func configSetMemory(args []string) error {
	dir, value, positional, err := parseConfigArgs(args)
	if err != nil {
		return err
	}
	if dir == "" {
		return errors.New("必须用 --dir 指定实例目录")
	}
	if len(positional) != 1 {
		return errors.New("用法: mtl config set-memory <auto|manual> [--value <内存>] --dir <实例目录>")
	}

	mode := strings.ToLower(strings.TrimSpace(positional[0]))
	cfg := loadInstanceConfig(dir)
	switch mode {
	case config.ModeAuto:
		cfg.MemoryMode = config.ModeAuto
	case config.ModeManual:
		value = strings.TrimSpace(value)
		if value == "" {
			return errors.New("手动内存模式必须用 --value 指定内存，例如 --value 4G")
		}
		if _, err := memory.Parse(value); err != nil {
			return err
		}
		cfg.MemoryMode = config.ModeManual
		cfg.ManualMemory = value
	default:
		return fmt.Errorf("未知内存模式 %q（可用: auto、manual）", positional[0])
	}

	if err := config.Save(dir, cfg); err != nil {
		return err
	}
	if mode == config.ModeAuto {
		fmt.Printf("[Config] 已设置自动内存: %s\n", configPathForLog(dir))
	} else {
		fmt.Printf("[Config] 已设置手动内存 %s: %s\n", cfg.ManualMemory, configPathForLog(dir))
	}
	return nil
}

// configSetHighPerfGPU 设置实例是否优先使用高性能显卡（仅 Windows 生效）。
func configSetHighPerfGPU(args []string) error {
	dir, _, positional, err := parseConfigArgs(args)
	if err != nil {
		return err
	}
	if dir == "" {
		return errors.New("必须用 --dir 指定实例目录")
	}
	if len(positional) != 1 {
		return errors.New("用法: mtl config set-highperf-gpu <true|false> --dir <实例目录>")
	}

	enabled, err := strconv.ParseBool(strings.ToLower(strings.TrimSpace(positional[0])))
	if err != nil {
		return fmt.Errorf("高性能显卡开关值 %q 无效（可用: true、false）", positional[0])
	}

	cfg := loadInstanceConfig(dir)
	cfg.PreferHighPerfGPU = enabled
	if err := config.Save(dir, cfg); err != nil {
		return err
	}
	state := "关闭"
	if enabled {
		state = "开启"
	}
	fmt.Printf("[Config] 已%s优先使用高性能显卡: %s\n", state, configPathForLog(dir))
	return nil
}

func configAddArg(args []string, jvm bool) error {
	dir, _, positional, err := parseConfigArgs(args)
	if err != nil {
		return err
	}
	if dir == "" {
		return errors.New("必须用 --dir 指定实例目录")
	}
	if len(positional) != 1 {
		return fmt.Errorf("用法: mtl config %s --dir <实例目录> <参数>", argCommandName("add", jvm))
	}
	arg := positional[0]
	if strings.TrimSpace(arg) == "" {
		return errors.New("参数不能为空")
	}

	cfg := loadInstanceConfig(dir)
	var added bool
	if jvm {
		added = cfg.AddJVMArg(arg)
	} else {
		added = cfg.AddGameArg(arg)
	}
	if !added {
		fmt.Printf("[Config] %s参数已存在，无需重复添加: %s\n", argKind(jvm), arg)
		return nil
	}
	if err := config.Save(dir, cfg); err != nil {
		return err
	}
	fmt.Printf("[Config] 已添加%s参数: %s\n", argKind(jvm), arg)
	return nil
}

func configRemoveArg(args []string, jvm bool) error {
	dir, _, positional, err := parseConfigArgs(args)
	if err != nil {
		return err
	}
	if dir == "" {
		return errors.New("必须用 --dir 指定实例目录")
	}
	if len(positional) != 1 {
		return fmt.Errorf("用法: mtl config %s --dir <实例目录> <参数>", argCommandName("remove", jvm))
	}
	arg := positional[0]

	cfg := loadInstanceConfig(dir)
	var removed bool
	if jvm {
		removed = cfg.RemoveJVMArg(arg)
	} else {
		removed = cfg.RemoveGameArg(arg)
	}
	if !removed {
		fmt.Printf("[Config] 未找到%s参数: %s（配置未变化）\n", argKind(jvm), arg)
		return nil
	}
	if err := config.Save(dir, cfg); err != nil {
		return err
	}
	fmt.Printf("[Config] 已删除%s参数: %s\n", argKind(jvm), arg)
	return nil
}

func configReset(args []string) error {
	dir, _, positional, err := parseConfigArgs(args)
	if err != nil {
		return err
	}
	if dir == "" {
		return errors.New("必须用 --dir 指定实例目录")
	}
	if len(positional) != 0 {
		return errors.New("用法: mtl config reset --dir <实例目录>")
	}
	if err := config.Save(dir, config.Default()); err != nil {
		return err
	}
	fmt.Printf("[Config] 已重置实例配置: %s\n", configPathForLog(dir))
	return nil
}

func argKind(jvm bool) string {
	if jvm {
		return "JVM "
	}
	return "游戏"
}

func argCommandName(action string, jvm bool) string {
	if jvm {
		return action + "-jvm-arg"
	}
	return action + "-game-arg"
}
