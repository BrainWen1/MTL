package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"mtl/internal/assets"
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
	jvm, memArgs := composeJVM(jvm, opts.Memory, opts.ExtraJVMArgs)
	if len(memArgs) > 0 {
		fmt.Fprintf(out, "[Launch] JVM内存参数：%s\n", strings.Join(memArgs, " "))
	}
	// 游戏参数固定放在主类之后，与 JVM 参数严格分离。
	game = append(game, filterArgs(opts.ExtraGameArgs)...)
	args := append(jvm, l.MainClass)
	args = append(args, game...)

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
