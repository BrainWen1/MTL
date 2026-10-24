package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"mtl/internal/assets"
	"mtl/internal/config"
	"mtl/internal/loader"
)

const shutdownGrace = 10 * time.Second

type Options struct {
	InstanceDir string
	Username    string
	// UUID 为空时按离线算法从 Username 推导；账号模块可传入预计算的离线 UUID。
	UUID   string
	Debug  bool
	Out    io.Writer
	ErrOut io.Writer
	// JavaPath 为空时使用系统 PATH 中的 java。
	JavaPath string
	// AssetsDir 为空时使用 mtl 默认 assets 缓存目录；预留手动覆盖（暂无 CLI 参数）。
	AssetsDir string
	// Memory 非空时注入 -Xms/-Xmx；为空时使用 JVM 默认堆大小。
	Memory *Memory
	// ExtraJVMArgs 是 --jvm-arg 传入的自定义 JVM 参数（对应 PCL 高级选项的“JVM 参数”）。
	// 位于固定 JVM 参数和内存参数之后，可覆盖内存等配置；不会被当作游戏参数。
	ExtraJVMArgs []string
	// ExtraGameArgs 是 --game-arg 传入的游戏参数（对应 PCL 高级选项的“游戏参数”）。
	// 只拼在主类之后，绝不会进入 JVM 参数区。
	ExtraGameArgs []string
	// PreferHighPerfGPU 开启后：
	//   1) 在 Windows 上为 Java 可执行文件设置系统级高性能 GPU 首选项（非 Windows 静默跳过）；
	//   2) 追加 -Dsun.java2d.d3d=true JVM 参数（已存在则不重复追加）。
	// 系统首选项设置失败只告警，不阻断启动。
	PreferHighPerfGPU bool
}

// d3dJVMArg 是启用高性能 GPU 调度时追加的 JVM 参数。
// 它只负责让 Java2D 走 D3D 渲染，GPU 偏好本身由操作系统层设置。
const d3dJVMArg = "-Dsun.java2d.d3d=true"

// ensureD3DArg 在启用高性能 GPU 时追加 D3D 参数；已有相同参数时不重复追加。
func ensureD3DArg(jvm []string, enabled bool) ([]string, bool) {
	if !enabled {
		return jvm, false
	}
	if slices.Contains(jvm, d3dJVMArg) {
		return jvm, false
	}
	return append(jvm, d3dJVMArg), true
}

// Memory 是注入给 JVM 的堆参数，值形如 "4G"、"512M"。
// Java 不支持运行时调整 -Xmx，因此仅在启动前生效。
type Memory struct {
	Min string
	Max string
}

func (m *Memory) jvmArgs() []string {
	var args []string
	if m.Min != "" {
		args = append(args, "-Xms"+m.Min)
	}
	if m.Max != "" {
		args = append(args, "-Xmx"+m.Max)
	}
	return args
}

// composeJVM 按 PCL 高级选项的顺序拼接 JVM 参数：
// 固定 JVM 参数 -> -Xms/-Xmx -> 自定义 --jvm-arg。
// 自定义参数放在内存参数之后，用户可覆盖 -Xmx 等配置。
func composeJVM(base []string, m *Memory, extra []string) (jvm, memArgs []string) {
	jvm = base
	if m != nil {
		memArgs = m.jvmArgs()
		jvm = append(jvm, memArgs...)
	}
	jvm = append(jvm, filterArgs(extra)...)
	return jvm, memArgs
}

// filterArgs 只过滤空参数，其余原样透传：不解析、不重组、不追加任何默认参数。
func filterArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, 0, len(args))
	for _, a := range args {
		if strings.TrimSpace(a) != "" {
			out = append(out, a)
		}
	}
	return out
}

func Run(ctx context.Context, opts Options) error {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	errOut := opts.ErrOut
	if errOut == nil {
		errOut = io.Discard
	}
	logf := func(format string, args ...any) {
		fmt.Fprintf(out, "[Launcher] "+format+"\n", args...)
	}

	l, err := Load(opts.InstanceDir)
	if err != nil {
		return err
	}
	logf("实例: %s", l.Dir)
	logf("版本: %s（Minecraft %s）", l.ProfileID, l.GameVersion)

	// 加载器类型优先取实例配置；旧实例没有该字段时按 profile id 推断（默认 Fabric）。
	instCfg, _ := config.Load(l.Dir)
	loaderType := instCfg.LoaderType
	if loaderType == "" {
		loaderType = loader.Detect(l.ProfileID)
	}
	instance := &config.Instance{
		Dir:              l.Dir,
		MinecraftVersion: l.GameVersion,
		LoaderType:       loaderType,
		LoaderVersion:    instCfg.LoaderVersion,
	}
	ld, err := loader.NewLoader(loaderType, l.GameVersion)
	if err != nil {
		return err
	}
	loaderArgs, err := ld.GetLaunchArgs(ctx, instance)
	if err != nil {
		return err
	}
	if opts.Debug {
		fmt.Fprintf(out, "[Loader] 加载器: %s（Minecraft %s）\n", ld.Name(), l.GameVersion)
	}

	javaPath, javaMajor, err := findJava(ctx, l.JavaMajor, opts.JavaPath)
	if err != nil {
		return err
	}
	if l.JavaMajor > 0 {
		logf("Java: %s（版本 %d，要求 ≥ %d）", javaPath, javaMajor, l.JavaMajor)
	} else {
		logf("Java: %s（版本 %d）", javaPath, javaMajor)
	}

	entries := l.ClassPathEntries()
	for _, p := range entries {
		info, err := os.Stat(p)
		if err != nil || info.IsDir() {
			return fmt.Errorf("缺少启动文件: %s", p)
		}
	}
	nativeJars := l.nativeJars()
	for _, p := range nativeJars {
		info, err := os.Stat(p)
		if err != nil || info.IsDir() {
			return fmt.Errorf("缺少 native 文件: %s", p)
		}
	}

	nativeDir, err := extractNatives(nativeJars)
	if err != nil {
		return err
	}
	defer os.RemoveAll(nativeDir)
	logf("Native 解压目录: %s（%d 个 native jar）", nativeDir, len(nativeJars))
	libCount := len(entries) - len(l.Mods) - 1
	logf("ClassPath: %d 个 jar（libraries %d，mods %d，client 1）",
		len(entries), libCount, len(l.Mods))

	assetsDir := opts.AssetsDir
	if assetsDir == "" {
		assetsDir, err = assets.DefaultDir()
		if err != nil {
			return err
		}
	}
	if assetsDir, err = filepath.Abs(assetsDir); err != nil {
		return fmt.Errorf("解析 assets 目录失败: %w", err)
	}

	username := opts.Username
	if username == "" {
		username = "Player"
	}
	uuid := opts.UUID
	if uuid == "" {
		uuid = offlineUUID(username)
	}
	// 固定参数在 buildArgs 中生成；自定义 JVM 参数由 composeJVM 排在内存参数之后。
	jvm, game, err := l.buildArgs(nativeDir, assetsDir, username, uuid, nil)
	if err != nil {
		return err
	}
	// 加载器额外参数位于固定 JVM 参数之后、内存与用户自定义参数之前。
	jvm = append(jvm, loaderArgs...)
	jvm, memArgs := composeJVM(jvm, opts.Memory, opts.ExtraJVMArgs)
	jvm, d3dAdded := ensureD3DArg(jvm, opts.PreferHighPerfGPU)
	if len(memArgs) > 0 {
		fmt.Fprintf(out, "[Launch] JVM内存参数：%s\n", strings.Join(memArgs, " "))
	}
	// 游戏参数固定放在主类之后，与 JVM 参数严格分离。
	game = append(game, filterArgs(opts.ExtraGameArgs)...)
	args := append(jvm, l.MainClass)
	args = append(args, game...)

	if opts.Debug {
		logf("高性能GPU调度: %v；操作系统: %s", opts.PreferHighPerfGPU, runtime.GOOS)
		switch {
		case !opts.PreferHighPerfGPU:
			logf("高性能GPU: 未启用，不追加 %s", d3dJVMArg)
		case d3dAdded:
			logf("高性能GPU: 已追加 JVM 参数 %s", d3dJVMArg)
		default:
			logf("高性能GPU: JVM 参数 %s 已存在，不重复追加", d3dJVMArg)
		}
	}

	// 在创建 Java 进程之前设置系统 GPU 首选项。失败仅告警，不阻断游戏启动。
	if opts.PreferHighPerfGPU {
		supported, gpuErr := applyHighPerfGPUPreference(javaPath)
		switch {
		case gpuErr != nil:
			fmt.Fprintf(errOut, "[Launch] 警告: 设置高性能GPU优先失败（游戏仍会启动）: %v\n", gpuErr)
			if opts.Debug {
				logf("高性能GPU: 系统 GPU 偏好设置失败，已跳过")
			}
		case !supported:
			if opts.Debug {
				logf("高性能GPU: 当前平台不支持系统 GPU 偏好设置，已跳过")
			}
		default:
			logf("高性能GPU: 已启用 Windows 高性能显卡首选项")
			if opts.Debug {
				logf("高性能GPU: 已为 %s 写入 GPU 首选项", javaPath)
			}
		}
	}

	cmd := exec.Command(javaPath, args...)
	cmd.Dir = l.Dir
	cmd.Stdout = out
	cmd.Stderr = errOut

	if opts.Debug {
		logf("命令: %s", formatCommand(append([]string{javaPath}, args...)))
	} else {
		logf("启动: %s（调试模式可查看完整命令：--debug）", l.MainClass)
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	return runProcess(ctx, cmd, logf)
}

func runProcess(ctx context.Context, cmd *exec.Cmd, logf func(string, ...any)) error {
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 Java 进程失败: %w", err)
	}
	logf("游戏进程 PID: %d", cmd.Process.Pid)

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		return exitStatus(err, logf)
	case <-ctx.Done():
		logf("收到退出信号，正在通知游戏进程...")
		if err := cmd.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
			logf("无法向游戏进程转发中断信号: %v（%s 后仍未退出将强制结束）", err, shutdownGrace)
		}
		select {
		case <-done:
			logf("游戏进程已退出")
			return nil
		case <-time.After(shutdownGrace):
			logf("游戏进程未在 %s 内退出，强制结束", shutdownGrace)
			_ = cmd.Process.Kill()
			<-done
			return errors.New("游戏进程被强制结束")
		}
	}
}

func exitStatus(err error, logf func(string, ...any)) error {
	if err == nil {
		logf("游戏进程正常退出")
		return nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		logf("游戏进程异常退出（退出码 %d）", ee.ExitCode())
		return fmt.Errorf("游戏进程异常退出（退出码 %d）", ee.ExitCode())
	}
	return fmt.Errorf("等待游戏进程失败: %w", err)
}
