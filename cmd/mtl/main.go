package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"mtl/internal/account"
	"mtl/internal/assets"
	"mtl/internal/config"
	"mtl/internal/crash"
	"mtl/internal/downloader"
	"mtl/internal/fabric"
	"mtl/internal/forge"
	"mtl/internal/launch"
	"mtl/internal/loader"
	"mtl/internal/memory"
	"mtl/internal/mrpack"
	"mtl/internal/runtime"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	switch args[0] {
	case "install":
		return install(args[1:])
	case "run":
		return runInstance(args[1:])
	case "inspect":
		return inspect(args[1:])
	case "list":
		return listCommand(args[1:])
	case "info":
		return infoCommand(args[1:])
	case "account":
		return accountCommand(args[1:])
	case "config":
		return configCommand(args[1:])
	case "version":
		fmt.Println("mtl dev")
		return nil
	default:
		usage()
		return fmt.Errorf("未知命令 %q", args[0])
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法:")
	fmt.Fprintln(os.Stderr, "  mtl install --dir <实例目录> [--concurrency N] [--skip-loader] <pack.mrpack>（支持 Fabric / Forge 1.20.1+）")
	fmt.Fprintln(os.Stderr, "  mtl run --dir <实例目录> [--account <账号>] [--username <名字>] [--memory <内存>] [--memory-auto] [--highperf-gpu|--no-highperf-gpu] [--java <java路径>] [--jvm-arg <参数>]... [--game-arg <参数>]... [--debug]")
	fmt.Fprintln(os.Stderr, "  mtl inspect [--extract <dir>] <pack.mrpack>")
	fmt.Fprintln(os.Stderr, "  mtl list [--dir <实例根目录>]（默认 ./instances）")
	fmt.Fprintln(os.Stderr, "  mtl info [--dir <实例根目录>] <实例名>")
	fmt.Fprintln(os.Stderr, "  mtl account list")
	fmt.Fprintln(os.Stderr, "  mtl account add <name>")
	fmt.Fprintln(os.Stderr, "  mtl account rm <name>")
	fmt.Fprintln(os.Stderr, "  mtl config show --dir <实例目录>")
	fmt.Fprintln(os.Stderr, "  mtl config set-memory <auto|manual> [--value 4G] --dir <实例目录>")
	fmt.Fprintln(os.Stderr, "  mtl config set-java <实例目录> [java路径]（省略路径表示清空，恢复自动匹配）")
	fmt.Fprintln(os.Stderr, "  mtl config set-highperf-gpu <true|false> --dir <实例目录>")
	fmt.Fprintln(os.Stderr, "  mtl config set-default-account --dir <实例目录> <账号名>（传空字符串解绑）")
	fmt.Fprintln(os.Stderr, "  mtl config add-jvm-arg|remove-jvm-arg --dir <实例目录> <参数>")
	fmt.Fprintln(os.Stderr, "  mtl config add-game-arg|remove-game-arg --dir <实例目录> <参数>")
	fmt.Fprintln(os.Stderr, "  mtl config reset --dir <实例目录>")
	fmt.Fprintln(os.Stderr, "  mtl version")
}

func install(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	dir := fs.String("dir", "", "实例目录（必填）")
	concurrency := fs.Int("concurrency", 0, "并发下载数，0 表示使用默认值")
	skipLoader := fs.Bool("skip-loader", false, "跳过加载器（Fabric/Forge）安装")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("用法: mtl install --dir <实例目录> [--concurrency N] [--skip-loader] <pack.mrpack>")
	}
	if *dir == "" {
		return fmt.Errorf("必须用 --dir 指定实例目录")
	}
	return installPack(fs.Arg(0), *dir, installOptions{concurrency: *concurrency, skipLoader: *skipLoader}, os.Stdout)
}

type installOptions struct {
	concurrency int
	skipLoader  bool
	// fabric / forge 仅用于测试注入 mock 服务地址；零值使用官方端点。
	fabric fabric.Options
	forge  forge.Options
}

// installPack 初始化实例目录、下载客户端文件、解压 overrides，最后安装 Fabric loader。
// 下载先于解压，保证 overrides 中的同名文件拥有最终优先级。
func installPack(packPath, dir string, opts installOptions, out io.Writer) error {
	p, err := mrpack.Open(packPath)
	if err != nil {
		return err
	}
	defer p.Close()

	idx := p.Index
	loaderType, loaderVersion := idx.Loader()
	files := idx.ClientFiles()
	fmt.Fprintf(out, "整合包: %s (%s)\n", idx.Name, idx.VersionID)
	fmt.Fprintf(out, "Minecraft %s / %s %s，客户端文件 %d 个\n",
		idx.Dependencies[mrpack.DepMinecraft], loaderType, loaderVersion, len(files))

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建实例目录失败: %w", err)
	}
	fmt.Fprintf(out, "实例目录: %s\n", dir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	results, err := downloader.Download(ctx, dir, files, downloader.Options{
		Concurrency: opts.concurrency,
		Progress: func(done, total int, res downloader.Result) {
			note := ""
			if res.Skipped {
				note = "（已存在，跳过）"
			}
			fmt.Fprintf(out, "[%d/%d] %s%s\n", done, total, res.File.Path, note)
		},
	})
	if err != nil {
		return err
	}

	skipped := 0
	for _, r := range results {
		if r.Skipped {
			skipped++
		}
	}
	fmt.Fprintf(out, "下载完成: %d 个文件，其中 %d 个已存在\n", len(results), skipped)

	if err := p.ExtractOverrides(dir); err != nil {
		return fmt.Errorf("解压 overrides 失败: %w", err)
	}
	fmt.Fprintln(out, "overrides 已解压")

	if opts.skipLoader {
		fmt.Fprintln(out, "已跳过加载器安装")
		fmt.Fprintf(out, "实例安装完成: %s\n", dir)
		return nil
	}

	mcVersion := idx.Dependencies[mrpack.DepMinecraft]
	loaderOpts := loader.Options{
		Fabric: opts.fabric,
		Forge:  opts.forge,
	}
	loaderOpts.Fabric.Concurrency = opts.concurrency
	loaderOpts.Fabric.Progress = func(msg string) {
		fmt.Fprintf(out, "[Fabric] %s\n", msg)
	}
	loaderOpts.Forge.Concurrency = opts.concurrency
	loaderOpts.Forge.Progress = func(msg string) {
		fmt.Fprintf(out, "[Forge] %s\n", msg)
	}
	ld, err := loader.NewLoaderWithOptions(string(loaderType), mcVersion, loaderOpts)
	if err != nil {
		return err
	}
	if err := ld.Install(ctx, &config.Instance{
		Dir:              dir,
		MinecraftVersion: mcVersion,
		LoaderType:       string(loaderType),
		LoaderVersion:    loaderVersion,
	}); err != nil {
		return fmt.Errorf("%s loader 安装失败: %w", loaderType, err)
	}
	if err := recordLoaderInfo(dir, string(loaderType), loaderVersion, out); err != nil {
		return err
	}
	fmt.Fprintf(out, "实例安装完成: %s\n", dir)
	return nil
}

// recordLoaderInfo 把加载器信息写入实例配置，保留原有配置字段。
func recordLoaderInfo(dir, loaderType, loaderVersion string, out io.Writer) error {
	cfg, err := config.Load(dir)
	if err != nil {
		fmt.Fprintf(out, "[Loader] 警告: 读取实例配置失败，将重建加载器信息: %v\n", err)
		cfg = config.Default()
	}
	cfg.LoaderType = loaderType
	cfg.LoaderVersion = loaderVersion
	if err := config.Save(dir, cfg); err != nil {
		return fmt.Errorf("保存实例加载器信息失败: %w", err)
	}
	fmt.Fprintf(out, "[Loader] 已记录加载器: %s %s\n", loaderType, loaderVersion)
	return nil
}

func runInstance(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	dir := fs.String("dir", "", "实例目录（必填）")
	accountName := fs.String("account", "", "已保存的离线账号；优先于实例默认账号和 --username")
	username := fs.String("username", "Player", "离线模式用户名；未保存时临时使用")
	memoryValue := fs.String("memory", memory.DefaultValue, "JVM 最大堆，例如 4G、512M；默认 2G")
	memoryAuto := fs.Bool("memory-auto", false, "按物理内存自动计算 JVM 最大堆；开启后忽略 --memory")
	javaPath := fs.String("java", "", "java 可执行文件完整路径；不传则自动使用 Mojang 官方 Java 运行时")
	highPerfGPU := fs.Bool("highperf-gpu", false, "本次启动强制优先使用高性能显卡（覆盖实例配置，仅 Windows 生效）")
	noHighPerfGPU := fs.Bool("no-highperf-gpu", false, "本次启动强制关闭高性能显卡调度（覆盖实例配置）")
	debug := fs.Bool("debug", false, "打印完整启动命令")
	// --jvm-arg 与 --game-arg 分开收集：前者进 JVM 参数区，后者只进主类之后的游戏参数区。
	var extraJVM stringList
	fs.Var(&extraJVM, "jvm-arg", "追加 JVM 参数（可重复），排在内存参数之后、主类之前")
	var extraGame stringList
	fs.Var(&extraGame, "game-arg", "追加游戏参数（可重复），只排在主类之后")
	if err := fs.Parse(args); err != nil {
		return err
	}
	usernameSet := false
	memorySet := false
	highPerfGPUSet := false
	noHighPerfGPUSet := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "username":
			usernameSet = true
		case "memory":
			memorySet = true
		case "highperf-gpu":
			highPerfGPUSet = true
		case "no-highperf-gpu":
			noHighPerfGPUSet = true
		}
	})
	if highPerfGPUSet && noHighPerfGPUSet {
		return fmt.Errorf("--highperf-gpu 与 --no-highperf-gpu 不能同时使用")
	}
	highPerfGPUValue := *highPerfGPU
	highPerfCLISet := highPerfGPUSet || noHighPerfGPUSet
	if noHighPerfGPUSet {
		highPerfGPUValue = !*noHighPerfGPU
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("用法: mtl run --dir <实例目录> [--account <账号>] [--username <名字>] [--memory <内存>] [--memory-auto] [--highperf-gpu|--no-highperf-gpu] [--java <java路径>] [--jvm-arg <参数>]... [--game-arg <参数>]... [--debug]")
	}
	if *dir == "" {
		return fmt.Errorf("必须用 --dir 指定实例目录")
	}

	// 实例配置优先级低于命令行：--memory 覆盖实例内存设置，--memory-auto 强制自动内存；
	// --highperf-gpu/--no-highperf-gpu 覆盖实例的高性能显卡设置。
	instanceCfg := loadInstanceConfig(*dir)
	memChoice := config.EffectiveMemory(instanceCfg, *memoryValue, memorySet, *memoryAuto)
	preferHighPerfGPU, gpuSource := config.EffectiveHighPerfGPU(instanceCfg, highPerfGPUValue, highPerfCLISet)
	if *debug {
		fmt.Fprintf(os.Stdout, "[Config] 实例配置文件: %s\n", configPathForLog(*dir))
		fmt.Fprintf(os.Stdout, "[Config] 读取到的实例配置:\n%s\n", config.Format(instanceCfg))
		fmt.Fprintf(os.Stdout, "[Config] 内存来源: %s\n", memChoice.Source)
		fmt.Fprintf(os.Stdout, "[Config] 高性能显卡调度: %v（来源: %s）\n", preferHighPerfGPU, gpuSource)
	}
	mem, err := resolveMemory(memChoice.Value, memChoice.Auto, memChoice.ValueSet, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}

	// 配置参数在前，命令行参数追加在后；其余启动流程保持不变。
	jvmArgs := config.MergeArgs(instanceCfg.JVMArgs, extraJVM)
	gameArgs := config.MergeArgs(instanceCfg.GameArgs, extraGame)
	if *debug {
		fmt.Fprintf(os.Stdout, "[Config] 合并后的 JVM 参数（%d 项）: %q\n", len(jvmArgs), jvmArgs)
		fmt.Fprintf(os.Stdout, "[Config] 合并后的游戏参数（%d 项）: %q\n", len(gameArgs), gameArgs)
	}

	store, storeErr := account.OpenDefault()
	name, uuid, accountSource, err := selectAccount(*accountName, *username, usernameSet,
		instanceCfg.DefaultAccount, store, storeErr, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	if *debug {
		fmt.Fprintf(os.Stdout, "[Config] 账号: %s（来源: %s）\n", name, accountSource)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	inst, err := launch.Load(*dir)
	if err != nil {
		return err
	}

	// Java 优先级：命令行 --java > 实例配置 java（需仍真实存在）> 全局缓存自动匹配下载。
	resolvedJava, javaSource := resolveConfiguredJava(*javaPath, instanceCfg)
	if resolvedJava == "" {
		if strings.TrimSpace(instanceCfg.Java) != "" {
			fmt.Fprintf(os.Stdout, "[run] 实例配置中的 Java 路径已失效，重新自动匹配: %s\n", instanceCfg.Java)
		}
		spec := runtime.Spec{}
		if inst.Vanilla != nil && inst.Vanilla.JavaVersion != nil {
			spec.Component = inst.Vanilla.JavaVersion.Component
			spec.MajorVersion = inst.Vanilla.JavaVersion.MajorVersion
		}
		resolvedJava, err = runtime.Ensure(ctx, spec, runtime.Options{
			Progress: func(msg string) {
				fmt.Fprintf(os.Stdout, "[Runtime] %s\n", msg)
			},
		})
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		javaSource = javaSourceRuntime
	}
	fmt.Fprintf(os.Stdout, "[run] Java运行时来源：%s\n", javaSource)
	if *debug {
		fmt.Fprintf(os.Stdout, "[Runtime] Java 路径: %s\n", resolvedJava)
	}

	assetsDir, err := assets.DefaultDir()
	if err != nil {
		return err
	}
	if err := assets.Ensure(ctx, assets.IndexSpec{
		ID:   inst.Vanilla.AssetIndex.ID,
		URL:  inst.Vanilla.AssetIndex.URL,
		SHA1: inst.Vanilla.AssetIndex.SHA1,
		Size: inst.Vanilla.AssetIndex.Size,
	}, assets.Options{
		Progress: func(msg string) {
			fmt.Fprintf(os.Stdout, "[Assets] %s\n", msg)
		},
	}); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}

	// 崩溃归档：启动前记录已有日志，游戏退出后只处理本次新增或变化的 crash-*.txt。
	// 目录创建或归档失败只告警，不影响启动。
	if mkErr := crash.EnsureArchiveDir(*dir); mkErr != nil {
		fmt.Fprintf(os.Stderr, "[Crash] 归档崩溃日志失败: %v\n", mkErr)
	}
	crashBaseline := crash.TakeSnapshot(*dir)

	err = launch.Run(ctx, launch.Options{
		InstanceDir:       *dir,
		Username:          name,
		UUID:              uuid,
		JavaPath:          resolvedJava,
		AssetsDir:         assetsDir,
		Memory:            mem,
		ExtraJVMArgs:      jvmArgs,
		ExtraGameArgs:     gameArgs,
		PreferHighPerfGPU: preferHighPerfGPU,
		Debug:             *debug,
		Out:               os.Stdout,
		ErrOut:            os.Stderr,
	})

	// 仅在 Java 进程成功运行并正常退出后回填路径；启动失败或中途崩溃保持配置原样。
	// --java 为命令行临时覆盖，不写入实例配置。
	if err == nil && shouldPersistJava(javaSource) {
		changed, saveErr := persistInstanceJava(*dir, resolvedJava)
		switch {
		case saveErr != nil:
			fmt.Fprintf(os.Stderr, "[run] 警告: 保存Java路径到实例配置失败: %v\n", saveErr)
		case changed:
			fmt.Fprintf(os.Stdout, "[run] 已自动保存Java路径到实例配置: %s\n", resolvedJava)
		}
	}

	// 游戏进程退出（无论正常还是崩溃）后归档并诊断本次崩溃日志；没有新日志时保持静默。
	reports, crashWarnings := crash.Collect(crash.Options{InstanceDir: *dir, Baseline: crashBaseline})
	for _, w := range crashWarnings {
		fmt.Fprintf(os.Stderr, "[Crash] 归档崩溃日志失败: %s\n", w)
	}
	for _, r := range reports {
		if *debug {
			fmt.Fprintf(os.Stdout, "[Crash] 扫描到崩溃日志: %s\n", r.Source)
			if r.Archived != "" {
				fmt.Fprintf(os.Stdout, "[Crash] 已归档: %s\n", r.Archived)
			}
		}
		for _, d := range r.Diagnoses {
			fmt.Fprintf(os.Stdout, "[Crash] %s\n", d)
		}
	}

	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// resolveMemory 读取本机物理内存并生成 JVM 堆参数。
// 读取失败时非自动模式只做最小值校验；自动模式无法计算，直接报错。
func resolveMemory(value string, auto, valueSet bool, out, errOut io.Writer) (*launch.Memory, error) {
	if out == nil {
		out = io.Discard
	}
	if errOut == nil {
		errOut = io.Discard
	}
	total, available, detectErr := memory.SystemMemory()
	if auto && (detectErr != nil || total <= 0) {
		if detectErr != nil {
			return nil, fmt.Errorf("自动内存模式无法读取物理内存: %w", detectErr)
		}
		return nil, errors.New("自动内存模式无法读取物理内存")
	}
	if detectErr != nil {
		fmt.Fprintf(errOut, "[Launch] 警告: 无法读取物理内存（%v），跳过 90%% 上限校验\n", detectErr)
	}
	return buildMemory(value, auto, valueSet, total, available, out)
}

// buildMemory 是 resolveMemory 的纯计算部分，便于用固定内存总量做单元测试。
func buildMemory(value string, auto, valueSet bool, total, available int64, out io.Writer) (*launch.Memory, error) {
	if out == nil {
		out = io.Discard
	}
	var maxBytes int64
	if auto {
		if total <= 0 {
			return nil, errors.New("自动内存模式无法读取物理内存")
		}
		if valueSet {
			fmt.Fprintf(out, "[Launch] 自动内存模式已开启，忽略 --memory=%s\n", value)
		}
		maxBytes = memory.AutoMax(total)
		free := "未知"
		if available > 0 {
			free = memory.Format(available)
		}
		fmt.Fprintf(out, "[Launch] 自动内存模式：物理内存 %s（空闲 %s），推荐 Xmx=%s\n",
			memory.Format(total), free, memory.Format(maxBytes))
	} else {
		var err error
		maxBytes, err = memory.Parse(value)
		if err != nil {
			return nil, err
		}
	}
	if err := memory.Validate(maxBytes, total); err != nil {
		return nil, err
	}
	alloc := memory.NewAllocation(maxBytes)
	return &launch.Memory{Min: alloc.Xms(), Max: alloc.Xmx()}, nil
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, " ") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// selectAccount 解析本次启动使用的离线用户名、UUID 与来源。
// 优先级：--account > 显式 --username > 实例配置 default_account；均未指定时报错。
// storeErr 非空说明账号文件不可用，回退临时离线账号。
func selectAccount(accountName, username string, usernameSet bool, defaultAccount string,
	store *account.Store, storeErr error, out, errOut io.Writer) (name, uuid, source string, err error) {
	if out == nil {
		out = io.Discard
	}
	if errOut == nil {
		errOut = io.Discard
	}
	if storeErr == nil && store == nil {
		storeErr = errors.New("账号存储不可用")
	}

	// --account 与实例默认账号都按“已保存账号”解析；显式 --username 保留原有临时账号能力。
	choice := config.EffectiveAccount(config.Config{DefaultAccount: defaultAccount}, accountName)
	savedName := choice.Name
	source = choice.Source
	if !choice.Set() {
		if !usernameSet || username == "" {
			return "", "", "", errors.New("未指定账号：请使用 --account <账号>，或先执行 mtl config set-default-account 绑定实例默认账号")
		}
		source = "命令行 --username"
	}

	if storeErr != nil || store == nil {
		fallback := savedName
		if usernameSet && username != "" {
			fallback = username
		}
		if fallback == "" {
			fallback = username
		}
		if fallback == "" {
			fallback = "Player"
		}
		if err := account.ValidateName(fallback); err != nil {
			return "", "", "", fmt.Errorf("账号文件不可用（%v），且回退用户名非法: %w", storeErr, err)
		}
		uuid = account.OfflineUUID(fallback)
		fmt.Fprintf(errOut, "[Account] 账号文件不可用: %v；回退临时离线账号 %s (%s)\n", storeErr, fallback, uuid)
		return fallback, uuid, source, nil
	}

	if savedName != "" {
		a, ok := store.Find(savedName)
		if !ok {
			if source == "实例默认账号" {
				return "", "", "", fmt.Errorf("实例默认账号 %q 不存在，请先执行: mtl account add %s，或重新执行 mtl config set-default-account 绑定其他账号", savedName, savedName)
			}
			return "", "", "", fmt.Errorf("离线账号 %q 不存在，请先执行: mtl account add %s", savedName, savedName)
		}
		fmt.Fprintf(out, "[Account] 使用离线账号: %s (%s)\n", a.Name, a.UUID)
		return a.Name, a.UUID, source, nil
	}

	if username == "" {
		username = "Player"
	}
	if err := account.ValidateName(username); err != nil {
		return "", "", "", err
	}
	if a, ok := store.Find(username); ok {
		fmt.Fprintf(out, "[Account] 使用离线账号: %s (%s)\n", a.Name, a.UUID)
		return a.Name, a.UUID, source, nil
	}
	uuid = account.OfflineUUID(username)
	fmt.Fprintf(out, "[Account] 临时离线账号: %s (%s)\n", username, uuid)
	return username, uuid, source, nil
}

func accountCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: mtl account <list|add|rm> [name]")
	}
	switch args[0] {
	case "list":
		return accountList(args[1:])
	case "add":
		return accountAdd(args[1:])
	case "rm":
		return accountRemove(args[1:])
	default:
		return fmt.Errorf("未知 account 子命令 %q（可用: list、add、rm）", args[0])
	}
}

func accountList(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("用法: mtl account list")
	}
	store, err := account.OpenDefault()
	if err != nil {
		return err
	}
	list := store.List()
	if len(list) == 0 {
		fmt.Println("[Account] 没有离线账号，可用 mtl account add <name> 添加")
		return nil
	}
	fmt.Printf("[Account] 共 %d 个离线账号（%s）\n", len(list), store.Path())
	for _, a := range list {
		fmt.Printf("[Account] %-16s %s\n", a.Name, a.UUID)
	}
	return nil
}

func accountAdd(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("用法: mtl account add <name>")
	}
	store, err := account.OpenDefault()
	if err != nil {
		return err
	}
	a, created, err := store.Add(args[0])
	if err != nil {
		return err
	}
	if !created {
		fmt.Printf("[Account] 账号已存在: %s (%s)\n", a.Name, a.UUID)
		return nil
	}
	fmt.Printf("[Account] 已添加: %s (%s)\n", a.Name, a.UUID)
	return nil
}

func accountRemove(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("用法: mtl account rm <name>")
	}
	store, err := account.OpenDefault()
	if err != nil {
		return err
	}
	a, err := store.Remove(args[0])
	if err != nil {
		return err
	}
	fmt.Printf("[Account] 已删除: %s (%s)\n", a.Name, a.UUID)
	return nil
}

func inspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	extractDir := fs.String("extract", "", "同时解压 overrides 到指定目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("用法: mtl inspect [--extract <dir>] <pack.mrpack>")
	}

	p, err := mrpack.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer p.Close()

	idx := p.Index
	loaderType, loaderVersion := idx.Loader()
	clientCount := len(idx.ClientFiles())
	fmt.Printf("名称: %s\n", idx.Name)
	fmt.Printf("版本ID: %s\n", idx.VersionID)
	fmt.Printf("Minecraft: %s\n", idx.Dependencies[mrpack.DepMinecraft])
	fmt.Printf("Loader: %s %s\n", loaderType, loaderVersion)
	fmt.Printf("文件: %d（客户端 %d，忽略 %d）\n", len(idx.Files), clientCount, len(idx.Files)-clientCount)

	if *extractDir != "" {
		if err := p.ExtractOverrides(*extractDir); err != nil {
			return err
		}
		fmt.Printf("overrides 已解压到 %s\n", *extractDir)
	}
	return nil
}
