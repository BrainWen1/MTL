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
	"mtl/internal/downloader"
	"mtl/internal/fabric"
	"mtl/internal/launch"
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
	case "account":
		return accountCommand(args[1:])
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
	fmt.Fprintln(os.Stderr, "  mtl install --dir <实例目录> [--concurrency N] [--skip-loader] <pack.mrpack>")
	fmt.Fprintln(os.Stderr, "  mtl run --dir <实例目录> [--account <账号>] [--username <名字>] [--memory <内存>] [--memory-auto] [--java <java路径>] [--jvm-arg <参数>]... [--game-arg <参数>]... [--debug]")
	fmt.Fprintln(os.Stderr, "  mtl inspect [--extract <dir>] <pack.mrpack>")
	fmt.Fprintln(os.Stderr, "  mtl account list")
	fmt.Fprintln(os.Stderr, "  mtl account add <name>")
	fmt.Fprintln(os.Stderr, "  mtl account rm <name>")
	fmt.Fprintln(os.Stderr, "  mtl version")
}

func install(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	dir := fs.String("dir", "", "实例目录（必填）")
	concurrency := fs.Int("concurrency", 0, "并发下载数，0 表示使用默认值")
	skipLoader := fs.Bool("skip-loader", false, "跳过 Fabric loader 安装")
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
	// fabric 仅用于测试注入 mock 服务地址；零值使用官方端点。
	fabric fabric.Options
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
		fmt.Fprintln(out, "已跳过 Fabric loader 安装")
		fmt.Fprintf(out, "实例安装完成: %s\n", dir)
		return nil
	}
	if loaderType != mrpack.LoaderFabric {
		return fmt.Errorf("暂不支持 %s loader 的自动安装", loaderType)
	}

	fabOpts := opts.fabric
	fabOpts.Concurrency = opts.concurrency
	fabOpts.Progress = func(msg string) {
		fmt.Fprintf(out, "[Fabric] %s\n", msg)
	}
	res, err := fabric.Install(ctx, dir, idx.Dependencies[mrpack.DepMinecraft], loaderVersion, fabOpts)
	if err != nil {
		return fmt.Errorf("Fabric loader 安装失败: %w", err)
	}
	fmt.Fprintf(out, "[Fabric] 安装完成: %s（下载 %d，跳过 %d）\n", res.ProfileID, res.Downloaded, res.Skipped)
	fmt.Fprintf(out, "实例安装完成: %s\n", dir)
	return nil
}

func runInstance(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	dir := fs.String("dir", "", "实例目录（必填）")
	accountName := fs.String("account", "", "已保存的离线账号；优先于 --username")
	username := fs.String("username", "Player", "离线模式用户名；未保存时临时使用")
	memoryValue := fs.String("memory", memory.DefaultValue, "JVM 最大堆，例如 4G、512M；默认 2G")
	memoryAuto := fs.Bool("memory-auto", false, "按物理内存自动计算 JVM 最大堆；开启后忽略 --memory")
	javaPath := fs.String("java", "", "java 可执行文件完整路径；不传则自动使用 Mojang 官方 Java 运行时")
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
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "username":
			usernameSet = true
		case "memory":
			memorySet = true
		}
	})
	if fs.NArg() != 0 {
		return fmt.Errorf("用法: mtl run --dir <实例目录> [--account <账号>] [--username <名字>] [--memory <内存>] [--memory-auto] [--java <java路径>] [--jvm-arg <参数>]... [--game-arg <参数>]... [--debug]")
	}
	if *dir == "" {
		return fmt.Errorf("必须用 --dir 指定实例目录")
	}

	mem, err := resolveMemory(*memoryValue, *memoryAuto, memorySet, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}

	store, storeErr := account.OpenDefault()
	name, uuid, err := selectAccount(*accountName, *username, usernameSet, store, storeErr, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	inst, err := launch.Load(*dir)
	if err != nil {
		return err
	}

	resolvedJava := *javaPath
	if resolvedJava == "" {
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
		if *debug {
			fmt.Fprintf(os.Stdout, "[Runtime] Java 路径: %s\n", resolvedJava)
		}
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

	err = launch.Run(ctx, launch.Options{
		InstanceDir:   *dir,
		Username:      name,
		UUID:          uuid,
		JavaPath:      resolvedJava,
		AssetsDir:     assetsDir,
		Memory:        mem,
		ExtraJVMArgs:  extraJVM,
		ExtraGameArgs: extraGame,
		Debug:         *debug,
		Out:           os.Stdout,
		ErrOut:        os.Stderr,
	})
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

// selectAccount 解析本次启动使用的离线用户名与 UUID。
// accountName 非空时优先查已保存账号；storeErr 非空说明账号文件不可用，回退临时账号。
func selectAccount(accountName, username string, usernameSet bool, store *account.Store, storeErr error, out, errOut io.Writer) (string, string, error) {
	if out == nil {
		out = io.Discard
	}
	if errOut == nil {
		errOut = io.Discard
	}
	if storeErr == nil && store == nil {
		storeErr = errors.New("账号存储不可用")
	}
	if storeErr != nil || store == nil {
		name := username
		if accountName != "" && !usernameSet {
			name = accountName
		}
		if name == "" {
			name = "Player"
		}
		if err := account.ValidateName(name); err != nil {
			return "", "", fmt.Errorf("账号文件不可用（%v），且回退用户名非法: %w", storeErr, err)
		}
		uuid := account.OfflineUUID(name)
		fmt.Fprintf(errOut, "[Account] 账号文件不可用: %v；回退临时离线账号 %s (%s)\n", storeErr, name, uuid)
		return name, uuid, nil
	}

	if accountName != "" {
		a, ok := store.Find(accountName)
		if !ok {
			return "", "", fmt.Errorf("离线账号 %q 不存在，请先执行: mtl account add %s", accountName, accountName)
		}
		fmt.Fprintf(out, "[Account] 使用离线账号: %s (%s)\n", a.Name, a.UUID)
		return a.Name, a.UUID, nil
	}

	if username == "" {
		username = "Player"
	}
	if err := account.ValidateName(username); err != nil {
		return "", "", err
	}
	if a, ok := store.Find(username); ok {
		fmt.Fprintf(out, "[Account] 使用离线账号: %s (%s)\n", a.Name, a.UUID)
		return a.Name, a.UUID, nil
	}
	uuid := account.OfflineUUID(username)
	fmt.Fprintf(out, "[Account] 临时离线账号: %s (%s)\n", username, uuid)
	return username, uuid, nil
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
