package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

const shutdownGrace = 10 * time.Second

type Options struct {
	InstanceDir  string
	Username     string
	ExtraJVMArgs []string
	Debug        bool
	Out          io.Writer
	ErrOut       io.Writer
	// JavaPath 为空时使用系统 PATH 中的 java。
	JavaPath string
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

	username := opts.Username
	if username == "" {
		username = "Player"
	}
	jvm, game, err := l.buildArgs(nativeDir, username, opts.ExtraJVMArgs)
	if err != nil {
		return err
	}
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
