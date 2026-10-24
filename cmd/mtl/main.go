package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"mtl/internal/account"
	"mtl/internal/assets"
	"mtl/internal/config"
	"mtl/internal/crash"
	"mtl/internal/downloader"
	"mtl/internal/fabric"
	"mtl/internal/forge"
	"mtl/internal/lastrun"
	"mtl/internal/launch"
	"mtl/internal/loader"
	"mtl/internal/logging"
	"mtl/internal/memory"
	"mtl/internal/mrpack"
	"mtl/internal/runtime"
	"mtl/internal/template"
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
	case "diagnose":
		return diagnoseCommand(args[1:])
	case "account":
		return accountCommand(args[1:])
	case "config":
		return configCommand(args[1:])
	case "mod":
		return modCommand(args[1:])
	case "java":
		return javaCommand(args[1:])
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
	fmt.Fprintln(os.Stderr, "  mtl install --dir <实例目录> [--concurrency N] [--skip-loader] [--no-prune] [--mirror URL] [--mod-mirror URL] [--retries N] [--timeout 30s] [--log-mode summary|verbose] [--debug] <pack.mrpack>（整合包模式，支持 Fabric / Forge 1.20.1+）")
	fmt.Fprintln(os.Stderr, "  mtl install --dir <实例目录> --mc <版本号> [--loader fabric|forge] [--loader-version <版本>] [--concurrency N] [--mirror URL] [--mod-mirror URL] [--retries N] [--timeout 30s] [--log-mode summary|verbose] [--debug]（纯净模板模式）")
	fmt.Fprintln(os.Stderr, "  mtl run --dir <实例目录> [--account <账号>] [--username <名字>] [--memory <内存>] [--memory-auto] [--highperf-gpu|--no-highperf-gpu] [--java <java路径>] [--jvm-arg <参数>]... [--game-arg <参数>]... [--mirror URL] [--mod-mirror URL] [--retries N] [--timeout 30s] [--log <debug|info|warn|error>] [--log-mode summary|verbose] [--debug]")
	fmt.Fprintln(os.Stderr, "  mtl inspect [--extract <dir>] <pack.mrpack>")
	fmt.Fprintln(os.Stderr, "  mtl list [--dir <实例根目录>]（默认 ./instances）")
	fmt.Fprintln(os.Stderr, "  mtl info [--dir <实例根目录>] <实例名>")
	fmt.Fprintln(os.Stderr, "  mtl diagnose [--dir <实例根目录>] <实例名>（诊断最近日志与崩溃报告）")
	fmt.Fprintln(os.Stderr, "  mtl diagnose --last（诊断上一次运行的实例）")
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
	fmt.Fprintln(os.Stderr, "  mtl config global（显示全局配置路径与内容）")
	fmt.Fprintln(os.Stderr, "  mtl config set-mirror <mojang|assets|libraries|version-meta|mod|fabric|forge> <镜像地址>")
	fmt.Fprintln(os.Stderr, "  mtl config set-log-level <debug|info|warn|error>（全局终端日志级别）")
	fmt.Fprintln(os.Stderr, "  mtl mod list --dir <实例目录> [--debug]")
	fmt.Fprintln(os.Stderr, "  mtl mod enable|disable|rm <mod-id/文件名> --dir <实例目录>")
	fmt.Fprintln(os.Stderr, "  mtl java list [--debug]")
	fmt.Fprintln(os.Stderr, "  mtl java use <runtime-id>")
	fmt.Fprintln(os.Stderr, "  mtl java prune")
	fmt.Fprintln(os.Stderr, "  mtl version")
}

func install(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	dir := fs.String("dir", "", "实例目录（必填）")
	concurrency := fs.Int("concurrency", 0, "并发下载数，0 表示使用配置/默认值")
	skipLoader := fs.Bool("skip-loader", false, "跳过加载器（Fabric/Forge）安装（仅 mrpack 模式）")
	noPrune := fs.Bool("no-prune", false, "增量更新时不删除旧版整合包遗留的托管文件（仅 mrpack 模式）")
	mcVersion := fs.String("mc", "", "Minecraft 版本号，例如 1.20.1（纯净模板模式必填）")
	loaderType := fs.String("loader", "", "加载器类型：fabric 或 forge；省略表示纯原版")
	loaderVersion := fs.String("loader-version", "", "加载器版本，必须与 --loader 一起使用")
	debug := fs.Bool("debug", false, "输出每个文件的下载详情（等价 --log-mode verbose）")
	dl := addDownloadFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	if fs.NArg() > 1 {
		return fmt.Errorf("用法: mtl install --dir <实例目录> [--concurrency N] [--skip-loader] [--no-prune] <pack.mrpack>；或 mtl install --dir <实例目录> --mc <版本号> [--loader fabric|forge] [--loader-version <版本>]")
	}
	if *dir == "" {
		return fmt.Errorf("必须用 --dir 指定实例目录")
	}

	settings, err := resolveDownloadSettings(fs, dl, *concurrency, *debug)
	if err != nil {
		return err
	}
	if *debug {
		fmt.Fprintf(os.Stdout, "[Download] 日志模式: %s，并发: %d，重试: %d\n",
			logModeName(settings.LogMode), settings.Concurrency, settings.Retries)
	}

	if fs.NArg() == 1 {
		if explicit["mc"] || explicit["loader"] || explicit["loader-version"] {
			return fmt.Errorf("mrpack 文件与 --mc / --loader / --loader-version 不能同时使用，请二选一")
		}
		return installPack(fs.Arg(0), *dir, installOptions{
			concurrency: *concurrency,
			skipLoader:  *skipLoader,
			noPrune:     *noPrune,
			download:    settings,
		}, os.Stdout)
	}

	if explicit["skip-loader"] {
		return fmt.Errorf("--skip-loader 仅适用于 mrpack 安装；纯净模板模式请省略 --loader 安装纯原版")
	}
	if explicit["no-prune"] {
		return fmt.Errorf("--no-prune 仅适用于 mrpack 安装；纯净模板实例不生成 pack-lock")
	}
	if strings.TrimSpace(*mcVersion) == "" {
		return fmt.Errorf("纯净模板模式必须用 --mc 指定 Minecraft 版本，或传入 mrpack 文件")
	}
	if explicit["loader-version"] && strings.TrimSpace(*loaderType) == "" {
		return fmt.Errorf("--loader-version 必须与 --loader 一起使用")
	}
	if strings.TrimSpace(*loaderType) != "" && strings.TrimSpace(*loaderVersion) == "" {
		return fmt.Errorf("使用 --loader 时必须同时指定 --loader-version")
	}
	return installTemplate(*dir, template.Options{
		MinecraftVersion: strings.TrimSpace(*mcVersion),
		LoaderType:       *loaderType,
		LoaderVersion:    *loaderVersion,
		Fabric: fabric.Options{
			Concurrency: settings.Concurrency,
			Retries:     settings.Retries,
			Timeout:     settings.Timeout,
			Mirrors:     settings.Mirrors,
			LogMode:     settings.LogMode,
			Progress: func(msg string) {
				fmt.Fprintf(os.Stdout, "[Fabric] %s\n", msg)
			},
		},
		Forge: forge.Options{
			Concurrency: settings.Concurrency,
			Retries:     settings.Retries,
			Timeout:     settings.Timeout,
			Mirrors:     settings.Mirrors,
			LogMode:     settings.LogMode,
			Progress: func(msg string) {
				fmt.Fprintf(os.Stdout, "[Forge] %s\n", msg)
			},
		},
		Assets: assets.Options{
			Concurrency: settings.Concurrency,
			Retries:     settings.Retries,
			Timeout:     settings.Timeout,
			Mirrors:     settings.Mirrors,
			LogMode:     settings.LogMode,
			Progress: func(msg string) {
				fmt.Fprintf(os.Stdout, "[Assets] %s\n", msg)
			},
		},
		Progress: func(msg string) {
			fmt.Fprintf(os.Stdout, "[Template] %s\n", msg)
		},
	}, os.Stdout)
}

// installTemplate 执行不依赖 mrpack 的纯净模板安装，输出与 mrpack 模式一致。
func installTemplate(dir string, opts template.Options, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	res, err := template.Install(ctx, dir, opts)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	if res.LoaderType == "" {
		fmt.Fprintf(out, "实例安装完成: %s（纯原版 Minecraft %s）\n", dir, res.MinecraftVersion)
	} else {
		fmt.Fprintf(out, "实例安装完成: %s（%s %s / Minecraft %s）\n",
			dir, res.LoaderType, res.LoaderVersion, res.MinecraftVersion)
	}
	return nil
}

type installOptions struct {
	concurrency int
	skipLoader  bool
	noPrune     bool
	// download 是下载层参数（镜像、重试、超时、日志模式）。
	download downloadSettings
	// fabric / forge 仅用于测试注入 mock 服务地址；零值使用官方端点。
	fabric fabric.Options
	forge  forge.Options
}

func logModeName(m downloader.LogMode) string {
	if m == downloader.LogVerbose {
		return "verbose"
	}
	return "summary"
}

// installPack 初始化实例目录、下载客户端文件、解压 overrides，最后安装 Fabric loader。
// 下载先于解压，保证 overrides 中的同名文件拥有最终优先级。
func installPack(packPath, dir string, opts installOptions, out io.Writer) error {
	p, err := mrpack.Open(packPath)
	if err != nil {
		return err
	}
	defer p.Close()

	// 兼容测试与旧调用方：未解析出下载设置时回落到 installOptions.concurrency。
	concurrency := opts.download.Concurrency
	if concurrency <= 0 {
		concurrency = opts.concurrency
	}
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

	verbose := opts.download.LogMode == downloader.LogVerbose
	lastPct := -10
	results, err := downloader.Download(ctx, dir, files, downloader.Options{
		Concurrency: concurrency,
		Retries:     opts.download.Retries,
		Timeout:     opts.download.Timeout,
		Mirrors:     opts.download.Mirrors,
		Progress: func(done, total int, res downloader.Result) {
			if !verbose {
				return
			}
			note := ""
			if res.Skipped {
				note = "（已存在，跳过）"
			}
			fmt.Fprintf(out, "[%d/%d] %s%s\n", done, total, res.File.Path, note)
		},
		// summary 模式只输出批次进度，避免大量 mod 文件刷屏。
		BatchProgress: func(done, total, failed int) {
			if verbose {
				return
			}
			pct := done * 100 / total
			if pct < lastPct+10 && done != total {
				return
			}
			lastPct = pct
			if failed > 0 {
				fmt.Fprintf(out, "[Download] 下载进度：%d/%d（%d%%，失败 %d）\n", done, total, pct, failed)
				return
			}
			fmt.Fprintf(out, "[Download] 下载进度：%d/%d（%d%%）\n", done, total, pct)
		},
		// 大文件（mod 等）保留百分比进度。
		FileProgress: func(path string, written, total int64) {
			if total <= 0 {
				return
			}
			fmt.Fprintf(out, "[Download] 下载 %s（%d/%d，%d%%）\n", path, written, total, written*100/total)
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
		if err := updatePackLock(p, packPath, dir, opts.noPrune, out); err != nil {
			return err
		}
		fmt.Fprintf(out, "实例安装完成: %s\n", dir)
		return nil
	}

	mcVersion := idx.Dependencies[mrpack.DepMinecraft]
	loaderOpts := loader.Options{
		Fabric: opts.fabric,
		Forge:  opts.forge,
	}
	loaderOpts.Fabric.Concurrency = concurrency
	loaderOpts.Fabric.Retries = opts.download.Retries
	loaderOpts.Fabric.Timeout = opts.download.Timeout
	loaderOpts.Fabric.Mirrors = opts.download.Mirrors
	loaderOpts.Fabric.LogMode = opts.download.LogMode
	loaderOpts.Fabric.Progress = func(msg string) {
		fmt.Fprintf(out, "[Fabric] %s\n", msg)
	}
	loaderOpts.Forge.Concurrency = concurrency
	loaderOpts.Forge.Retries = opts.download.Retries
	loaderOpts.Forge.Timeout = opts.download.Timeout
	loaderOpts.Forge.Mirrors = opts.download.Mirrors
	loaderOpts.Forge.LogMode = opts.download.LogMode
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
	if err := updatePackLock(p, packPath, dir, opts.noPrune, out); err != nil {
		return err
	}
	fmt.Fprintf(out, "实例安装完成: %s\n", dir)
	return nil
}

// updatePackLock 在整合包文件落盘后生成或更新 .mtl/pack-lock.json。
// 只有同一整合包的旧清单才会触发增量清理；清理对象仅限旧清单记录、
// 新版不再声明、且内容未被用户修改的托管文件。
func updatePackLock(p *mrpack.Pack, packPath, dir string, noPrune bool, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	desired, err := p.ManagedFiles(dir)
	if err != nil {
		return fmt.Errorf("生成整合包锁定清单失败: %w", err)
	}

	old, err := mrpack.LoadLock(dir)
	if err != nil {
		fmt.Fprintf(out, "[Pack] 警告: 旧锁定清单不可用，跳过清理并重新生成: %v\n", err)
		old = nil
	}
	if old != nil && old.PackID != p.Index.Name {
		fmt.Fprintf(out, "[Pack] 警告: 整合包标识由 %q 变为 %q，跳过清理，避免误删其他整合包文件\n", old.PackID, p.Index.Name)
		old = nil
	}

	lock := &mrpack.Lock{
		PackID:      p.Index.Name,
		PackVersion: p.Index.VersionID,
		Files:       desired,
	}
	if source, err := filepath.Abs(packPath); err == nil {
		lock.Source = source
	} else {
		lock.Source = packPath
	}

	if old == nil {
		fmt.Fprintln(out, "[Pack] 未找到可用的旧锁定清单，本次不做清理")
	} else {
		diff := mrpack.DiffFiles(old.Files, desired)
		plan := mrpack.PlanPrune(dir, old, desired)
		fmt.Fprintf(out, "[Pack] 增量检查: 新增 %d，更新 %d，待删除 %d\n",
			len(diff.Added), len(diff.Updated), len(plan.Delete))
		if len(plan.Delete) > 0 {
			fmt.Fprintln(out, "[Pack] 待删除文件（旧版托管、新版不再包含）:")
			for _, rel := range plan.Delete {
				fmt.Fprintf(out, "[Pack]   - %s\n", rel)
			}
		}
		for _, kept := range plan.Keep {
			fmt.Fprintf(out, "[Pack] 保留 %s（%s）\n", kept.Path, kept.Reason)
		}
		if noPrune {
			if len(plan.Delete) > 0 {
				fmt.Fprintf(out, "[Pack] --no-prune 已开启，保留 %d 个旧托管文件（不再纳入锁定清单）\n", len(plan.Delete))
			}
		} else if len(plan.Delete) > 0 {
			n, err := mrpack.ApplyPrune(dir, plan.Delete)
			if err != nil {
				return fmt.Errorf("清理旧托管文件失败: %w", err)
			}
			fmt.Fprintf(out, "[Pack] 已清理 %d 个旧托管文件\n", n)
		}
	}

	if err := mrpack.SaveLock(dir, lock); err != nil {
		return fmt.Errorf("写入整合包锁定清单失败: %w", err)
	}
	fmt.Fprintf(out, "[Pack] 已写入锁定清单: %s（%d 个托管文件）\n", mrpack.LockRelPath, len(desired))
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

func runInstance(args []string) (retErr error) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	dir := fs.String("dir", "", "实例目录（必填）")
	accountName := fs.String("account", "", "已保存的离线账号；优先于实例默认账号和 --username")
	username := fs.String("username", "Player", "离线模式用户名；未保存时临时使用")
	memoryValue := fs.String("memory", memory.DefaultValue, "JVM 最大堆，例如 4G、512M；默认 2G")
	memoryAuto := fs.Bool("memory-auto", false, "按物理内存自动计算 JVM 最大堆；开启后忽略 --memory")
	javaPath := fs.String("java", "", "java 可执行文件完整路径；不传则自动使用 Mojang 官方 Java 运行时")
	highPerfGPU := fs.Bool("highperf-gpu", false, "本次启动强制优先使用高性能显卡（覆盖实例配置，仅 Windows 生效）")
	noHighPerfGPU := fs.Bool("no-highperf-gpu", false, "本次启动强制关闭高性能显卡调度（覆盖实例配置）")
	debug := fs.Bool("debug", false, "打印完整启动命令，并切换为 debug 日志与 verbose 下载日志")
	logLevelFlag := fs.String("log", "", "终端日志级别：debug、info、warn、error；默认读取全局配置")
	dl := addDownloadFlags(fs)
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
		return fmt.Errorf("用法: mtl run --dir <实例目录> [--account <账号>] [--username <名字>] [--memory <内存>] [--memory-auto] [--highperf-gpu|--no-highperf-gpu] [--java <java路径>] [--jvm-arg <参数>]... [--game-arg <参数>]... [--log <debug|info|warn|error>] [--debug]")
	}
	if *dir == "" {
		return fmt.Errorf("必须用 --dir 指定实例目录")
	}
	absDir, err := filepath.Abs(*dir)
	if err != nil {
		return fmt.Errorf("解析实例目录失败: %w", err)
	}
	settings, err := resolveDownloadSettings(fs, dl, 0, *debug)
	if err != nil {
		return err
	}
	level, err := resolveRunLogLevel(*logLevelFlag, *debug)
	if err != nil {
		return err
	}

	// 每次 run 单独创建日志文件；创建失败只告警，终端日志仍然可用。
	var logFile *os.File
	logPath := ""
	dirReady := false
	if info, statErr := os.Stat(absDir); statErr != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "[Log] 警告: 实例目录不可用，跳过日志文件: %s\n", absDir)
	} else {
		dirReady = true
		if f, p, openErr := logging.OpenInstanceLog(absDir, time.Now()); openErr != nil {
			fmt.Fprintf(os.Stderr, "[Log] 警告: 创建实例日志文件失败: %v（仅输出到终端）\n", openErr)
		} else {
			logFile, logPath = f, p
		}
	}
	logger := logging.New(logging.Options{
		Console: os.Stdout,
		Level:   level,
		File:    logFile,
	})

	// 记录本次运行的起止状态，供 mtl diagnose --last 使用。
	rec := lastrun.Record{
		InstanceDir:  absDir,
		InstanceName: filepath.Base(filepath.Clean(absDir)),
		LogFile:      logPath,
		StartedAt:    time.Now(),
	}
	var (
		processExit    *int
		processPID     int
		processStart   time.Time
		processStarted bool
	)
	// 实例目录无效时不写记录，避免一次手误覆盖掉可用于 --last 的干净状态。
	if dirReady {
		if saveErr := lastrun.Save(rec); saveErr != nil {
			logger.Warn("run", "写入上次运行记录失败: %v", saveErr)
		}
	}
	defer func() {
		ended := time.Now()
		rec.EndedAt = &ended
		rec.ExitCode = processExit
		rec.ProcessPID = processPID
		if retErr != nil && !errors.Is(retErr, context.Canceled) {
			logger.Error("run", "启动失败: %v", retErr)
		}
		if dirReady {
			if saveErr := lastrun.Save(rec); saveErr != nil {
				logger.Warn("run", "更新上次运行记录失败: %v", saveErr)
			}
		}
		logger.Info("run", "mtl 运行结束")
		if logFile != nil {
			_ = logFile.Close()
		}
	}()

	logger.Info("run", "实例: %s（%s）", rec.InstanceName, absDir)
	logger.Info("run", "终端日志级别: %s", level.Name())
	if logPath != "" {
		logger.Info("run", "日志文件: %s", logPath)
	}

	// 实例配置优先级低于命令行：--memory 覆盖实例内存设置，--memory-auto 强制自动内存；
	// --highperf-gpu/--no-highperf-gpu 覆盖实例的高性能显卡设置。
	instanceCfg := loadInstanceConfig(absDir)
	memChoice := config.EffectiveMemory(instanceCfg, *memoryValue, memorySet, *memoryAuto)
	preferHighPerfGPU, gpuSource := config.EffectiveHighPerfGPU(instanceCfg, highPerfGPUValue, highPerfCLISet)
	logger.Debug("config", "实例配置文件: %s", configPathForLog(absDir))
	logger.Debug("config", "读取到的实例配置:\n%s", config.Format(instanceCfg))
	logger.Debug("config", "内存来源: %s", memChoice.Source)
	logger.Debug("config", "高性能显卡调度: %v（来源: %s）", preferHighPerfGPU, gpuSource)
	logger.Debug("download", "下载日志模式: %s，并发: %d，重试: %d",
		logModeName(settings.LogMode), settings.Concurrency, settings.Retries)
	mem, err := resolveMemory(memChoice.Value, memChoice.Auto, memChoice.ValueSet, logger.RawWriter(), logger.RawWriter())
	if err != nil {
		return err
	}
	logger.Info("run", "内存配置: -Xms%s -Xmx%s（来源: %s）", mem.Min, mem.Max, memChoice.Source)

	// 配置参数在前，命令行参数追加在后；其余启动流程保持不变。
	jvmArgs := config.MergeArgs(instanceCfg.JVMArgs, extraJVM)
	gameArgs := config.MergeArgs(instanceCfg.GameArgs, extraGame)
	logger.Debug("config", "合并后的 JVM 参数（%d 项）: %q", len(jvmArgs), jvmArgs)
	logger.Debug("config", "合并后的游戏参数（%d 项）: %q", len(gameArgs), gameArgs)

	store, storeErr := account.OpenDefault()
	name, uuid, accountSource, err := selectAccount(*accountName, *username, usernameSet,
		instanceCfg.DefaultAccount, store, storeErr, io.Discard, logger.RawWriter())
	if err != nil {
		return err
	}
	logger.Info("account", "使用离线账号: %s (%s)，来源: %s", name, uuid, accountSource)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	inst, err := launch.Load(absDir)
	if err != nil {
		return err
	}
	rec.MinecraftVersion = inst.GameVersion
	logger.Info("run", "版本: %s（Minecraft %s）", inst.ProfileID, inst.GameVersion)
	if instanceCfg.LoaderType != "" {
		logger.Info("run", "加载器: %s %s", instanceCfg.LoaderType, instanceCfg.LoaderVersion)
	}
	if len(inst.Mods) > 0 {
		logger.Info("mod", "ClassPath 中的 Mod 文件: %d 个", len(inst.Mods))
	}

	// Java 优先级：命令行 --java > 实例配置 java（需仍真实存在）
	// > 全局默认（mtl java use）> 全局缓存自动匹配下载。
	resolvedJava, javaSource := resolveConfiguredJava(*javaPath, instanceCfg)
	if resolvedJava == "" && strings.TrimSpace(instanceCfg.Java) != "" {
		logger.Warn("java", "实例配置中的 Java 路径已失效，重新选择 Java: %s", instanceCfg.Java)
	}
	if resolvedJava == "" {
		if p, id, ok := resolveGlobalJava(); ok {
			resolvedJava = p
			javaSource = javaSourceGlobal
			logger.Debug("java", "全局默认 Java 运行时: %s（%s）", id, p)
		}
	}
	if resolvedJava == "" {
		spec := runtime.Spec{}
		if inst.Vanilla != nil && inst.Vanilla.JavaVersion != nil {
			spec.Component = inst.Vanilla.JavaVersion.Component
			spec.MajorVersion = inst.Vanilla.JavaVersion.MajorVersion
		}
		if rd, rdErr := runtime.DefaultDir(); rdErr == nil {
			logger.Debug("runtime", "缓存目录: %s", rd)
		}
		resolvedJava, err = runtime.Ensure(ctx, spec, runtime.Options{
			Concurrency: settings.Concurrency,
			Retries:     settings.Retries,
			Timeout:     settings.Timeout,
			Mirrors:     settings.Mirrors,
			Progress: func(msg string) {
				logger.Info("runtime", "%s", msg)
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
	logger.Info("java", "运行时来源: %s", javaSource)
	logger.Info("java", "Java 路径: %s", resolvedJava)

	assetsDir, err := assets.DefaultDir()
	if err != nil {
		return err
	}
	logger.Debug("assets", "缓存目录: %s", assetsDir)
	if err := assets.Ensure(ctx, assets.IndexSpec{
		ID:   inst.Vanilla.AssetIndex.ID,
		URL:  inst.Vanilla.AssetIndex.URL,
		SHA1: inst.Vanilla.AssetIndex.SHA1,
		Size: inst.Vanilla.AssetIndex.Size,
	}, assets.Options{
		Concurrency: settings.Concurrency,
		Retries:     settings.Retries,
		Timeout:     settings.Timeout,
		Mirrors:     settings.Mirrors,
		LogMode:     settings.LogMode,
		Progress: func(msg string) {
			logger.Info("assets", "%s", msg)
		},
	}); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}

	// 崩溃归档：启动前记录已有日志，游戏退出后只处理本次新增或变化的 crash-*.txt。
	// 目录创建或归档失败只告警，不影响启动。
	if mkErr := crash.EnsureArchiveDir(absDir); mkErr != nil {
		logger.Warn("crash", "归档崩溃日志失败: %v", mkErr)
	}
	crashBaseline := crash.TakeSnapshot(absDir)

	err = launch.Run(ctx, launch.Options{
		InstanceDir:       absDir,
		Username:          name,
		UUID:              uuid,
		JavaPath:          resolvedJava,
		AssetsDir:         assetsDir,
		Memory:            mem,
		ExtraJVMArgs:      jvmArgs,
		ExtraGameArgs:     gameArgs,
		PreferHighPerfGPU: preferHighPerfGPU,
		Debug:             *debug,
		Out:               logger.RawWriter(),
		ErrOut:            logger.RawWriter(),
		OnProcessStart: func(pid int) {
			processStarted = true
			processPID = pid
			processStart = time.Now()
			logger.Info("process", "子进程 PID: %d，启动时间: %s", pid, processStart.Format("2006-01-02 15:04:05"))
		},
		OnProcessExit: func(code int) {
			c := code
			processExit = &c
			duration := time.Duration(0)
			if processStarted {
				duration = time.Since(processStart)
			}
			logger.Info("process", "子进程退出码: %d，退出时间: %s，运行时长: %s",
				code, time.Now().Format("2006-01-02 15:04:05"), duration.Round(time.Second))
		},
	})

	// 仅在 Java 进程成功运行并正常退出后回填路径；启动失败或中途崩溃保持配置原样。
	// --java 为命令行临时覆盖，不写入实例配置。
	if err == nil && shouldPersistJava(javaSource) {
		changed, saveErr := persistInstanceJava(absDir, resolvedJava)
		switch {
		case saveErr != nil:
			logger.Warn("java", "保存 Java 路径到实例配置失败: %v", saveErr)
		case changed:
			logger.Info("java", "已自动保存 Java 路径到实例配置: %s", resolvedJava)
		}
	}

	// 游戏进程退出（无论正常还是崩溃）后归档并诊断本次崩溃日志；没有新日志时保持静默。
	reports, crashWarnings := crash.Collect(crash.Options{InstanceDir: absDir, Baseline: crashBaseline})
	for _, w := range crashWarnings {
		logger.Warn("crash", "归档崩溃日志失败: %s", w)
	}
	for _, r := range reports {
		logger.Debug("crash", "扫描到崩溃日志: %s", r.Source)
		if r.Archived != "" {
			logger.Debug("crash", "已归档: %s", r.Archived)
		}
		for _, d := range r.Diagnoses {
			logger.Info("crash", "%s", d)
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
