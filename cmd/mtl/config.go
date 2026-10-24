package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"mtl/internal/config"
	"mtl/internal/launch"
	"mtl/internal/memory"
)

// configCommand 处理 mtl config 子命令。配置存放于 <实例目录>/.mtl/config.json。
func configCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: mtl config <show|set-memory|set-java|set-highperf-gpu|set-default-account|add-jvm-arg|remove-jvm-arg|add-game-arg|remove-game-arg|reset> ...")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "show":
		return configShow(rest)
	case "set-memory":
		return configSetMemory(rest)
	case "set-java":
		return configSetJava(rest)
	case "set-highperf-gpu":
		return configSetHighPerfGPU(rest)
	case "set-default-account":
		return configSetDefaultAccount(rest)
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
	case "global":
		return configGlobal(rest)
	case "set-mirror":
		return configSetMirror(rest)
	default:
		return fmt.Errorf("未知 config 子命令 %q（可用: show、set-memory、set-java、set-highperf-gpu、set-default-account、add-jvm-arg、remove-jvm-arg、add-game-arg、remove-game-arg、reset、global、set-mirror）", sub)
	}
}

// configGlobal 打印全局配置路径与内容（镜像源、下载模式等）。
func configGlobal(args []string) error {
	if len(args) != 0 {
		return errors.New("用法: mtl config global")
	}
	path, err := config.GlobalPath()
	if err != nil {
		return err
	}
	cfg, err := config.LoadGlobal()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[Config] 警告: %v\n", err)
	}
	fmt.Printf("[Config] 全局配置文件: %s\n", path)
	fmt.Print(config.FormatGlobal(cfg))
	return nil
}

// configSetMirror 修改全局镜像源；url 传空字符串表示恢复官方源。
func configSetMirror(args []string) error {
	if len(args) != 2 {
		return errors.New("用法: mtl config set-mirror <mojang|assets|libraries|version-meta|mod|fabric|forge> <镜像地址|空字符串>")
	}
	kind := strings.ToLower(strings.TrimSpace(args[0]))
	raw := strings.TrimSpace(args[1])
	if raw == `""` {
		raw = ""
	}
	value, err := config.NormalizeMirror(raw)
	if err != nil {
		return err
	}
	cfg, loadErr := config.LoadGlobal()
	if loadErr != nil {
		fmt.Fprintf(os.Stderr, "[Config] 警告: %v；将基于可用字段保存\n", loadErr)
	}
	switch kind {
	case "mojang":
		cfg.Mirrors.Mojang = value
	case "assets":
		cfg.Mirrors.Assets = value
	case "libraries":
		cfg.Mirrors.Libraries = value
	case "version-meta":
		cfg.Mirrors.VersionMeta = value
	case "mod":
		cfg.Mirrors.Mod = value
	case "fabric":
		cfg.Mirrors.Fabric = value
	case "forge":
		cfg.Mirrors.Forge = value
	default:
		return fmt.Errorf("未知镜像类型 %q（可用: mojang、assets、libraries、version-meta、mod、fabric、forge）", kind)
	}
	if err := config.SaveGlobal(cfg); err != nil {
		return err
	}
	path, _ := config.GlobalPath()
	fmt.Printf("[Config] 已保存镜像 %s = %q（%s）\n", kind, value, path)
	return nil
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

// configSetJava 设置或清空实例绑定的 Java 可执行文件路径。
// 用法：mtl config set-java <实例目录> [java路径]
// 省略路径时清空 java 字段，恢复全局缓存自动匹配。
// 该命令只修改配置，不会启动游戏，也不触发 run 的成功回填逻辑。
// 同时兼容其他 config 子命令的 --dir 写法。
func configSetJava(args []string) error {
	dir, _, positional, err := parseConfigArgs(args)
	if err != nil {
		return err
	}
	if dir == "" {
		if len(positional) == 0 {
			return errors.New("用法: mtl config set-java <实例目录> [java路径]（省略路径表示清空）")
		}
		dir, positional = positional[0], positional[1:]
	}
	if len(positional) > 1 {
		return errors.New("用法: mtl config set-java <实例目录> [java路径]（省略路径表示清空）")
	}

	cfg := loadInstanceConfig(dir)

	// 未提供路径：清空 java 字段，下次 run 重新进入自动匹配。
	if len(positional) == 0 {
		if cfg.Java == "" {
			fmt.Printf("[Config] 实例未设置 Java 路径，已是自动匹配模式: %s\n", configPathForLog(dir))
			return nil
		}
		cfg.Java = ""
		if err := config.Save(dir, cfg); err != nil {
			return err
		}
		fmt.Printf("[Config] 已清空 Java 路径，恢复自动匹配: %s\n", configPathForLog(dir))
		return nil
	}

	// 复用 run 的校验逻辑：路径存在、可执行，且能响应 -version。
	javaPath, err := launch.ValidateJavaPath(context.Background(), positional[0])
	if err != nil {
		return fmt.Errorf("Java 路径校验失败: %w", err)
	}
	if cfg.Java == javaPath {
		fmt.Printf("[Config] 实例 Java 路径已是 %s，配置未变化\n", javaPath)
		return nil
	}
	cfg.Java = javaPath
	if err := config.Save(dir, cfg); err != nil {
		return err
	}
	fmt.Printf("[Config] 已设置实例 Java 路径: %s\n", javaPath)
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

// configSetDefaultAccount 绑定实例默认离线账号；账号名为空表示解绑。
// 只保存账号名称，不校验账号是否已存在，也不涉及任何登录或令牌。
func configSetDefaultAccount(args []string) error {
	dir, _, positional, err := parseConfigArgs(args)
	if err != nil {
		return err
	}
	if dir == "" {
		return errors.New("必须用 --dir 指定实例目录")
	}
	if len(positional) != 1 {
		return errors.New("用法: mtl config set-default-account --dir <实例目录> <账号名>（传空字符串解绑）")
	}

	name := strings.TrimSpace(positional[0])
	if err := config.ValidateDefaultAccount(name); err != nil {
		return err
	}

	cfg := loadInstanceConfig(dir)
	if cfg.DefaultAccount == name {
		if name == "" {
			fmt.Printf("[Config] 实例未绑定默认账号，无需解绑: %s\n", configPathForLog(dir))
		} else {
			fmt.Printf("[Config] 实例默认账号已是 %s，配置未变化\n", name)
		}
		return nil
	}

	cfg.DefaultAccount = name
	if err := config.Save(dir, cfg); err != nil {
		return err
	}
	if name == "" {
		fmt.Printf("[Config] 已解绑实例默认账号: %s\n", configPathForLog(dir))
	} else {
		fmt.Printf("[Config] 已绑定实例默认账号 %s: %s\n", name, configPathForLog(dir))
	}
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
