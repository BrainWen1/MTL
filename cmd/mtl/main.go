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

	"mtl/internal/downloader"
	"mtl/internal/fabric"
	"mtl/internal/launch"
	"mtl/internal/mrpack"
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
	fmt.Fprintln(os.Stderr, "  mtl run --dir <实例目录> [--username <名字>] [--java <java路径>] [--jvm-arg <参数>]... [--debug]")
	fmt.Fprintln(os.Stderr, "  mtl inspect [--extract <dir>] <pack.mrpack>")
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
	username := fs.String("username", "Player", "离线模式用户名")
	javaPath := fs.String("java", "", "java 可执行文件完整路径，默认从 PATH 查找")
	debug := fs.Bool("debug", false, "打印完整启动命令")
	var extraJVM stringList
	fs.Var(&extraJVM, "jvm-arg", "追加 JVM 参数，可重复指定")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("用法: mtl run --dir <实例目录> [--username <名字>] [--java <java路径>] [--jvm-arg <参数>]... [--debug]")
	}
	if *dir == "" {
		return fmt.Errorf("必须用 --dir 指定实例目录")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := launch.Run(ctx, launch.Options{
		InstanceDir:  *dir,
		Username:     *username,
		JavaPath:     *javaPath,
		ExtraJVMArgs: extraJVM,
		Debug:        *debug,
		Out:          os.Stdout,
		ErrOut:       os.Stderr,
	})
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, " ") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
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
