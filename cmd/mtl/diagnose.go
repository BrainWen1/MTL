package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"mtl/internal/config"
	"mtl/internal/diagnose"
	"mtl/internal/lastrun"
	"mtl/internal/mrpack"
)

// diagnoseOptions 是 mtl diagnose 的参数。
type diagnoseOptions struct {
	root string
	last bool
	name string
}

// diagnoseCommand 实现 mtl diagnose：诊断指定实例或上一次运行的实例。
func diagnoseCommand(args []string) error {
	opts, err := parseDiagnoseOptions(args)
	if err != nil {
		return err
	}

	var (
		dir  string
		name string
		rec  *lastrun.Record
	)
	if opts.last {
		r, loadErr := lastrun.Load()
		if loadErr != nil {
			if errors.Is(loadErr, os.ErrNotExist) {
				return errors.New("没有可诊断的上次运行记录，请先执行 mtl run；或使用 mtl diagnose <实例名>")
			}
			return loadErr
		}
		dir = r.InstanceDir
		name = r.InstanceName
		if name == "" {
			name = filepath.Base(filepath.Clean(dir))
		}
		rec = &r
		if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
			if statErr == nil {
				statErr = errors.New("路径不是目录")
			}
			return fmt.Errorf("上次运行的实例目录已不可用: %s（%v）", dir, statErr)
		}
	} else {
		dir = opts.root
		name = opts.name
	}

	dir, err = resolveDiagnoseDir(dir, name, opts.last)
	if err != nil {
		return err
	}
	return printDiagnoseReport(dir, name, rec)
}

func parseDiagnoseOptions(args []string) (diagnoseOptions, error) {
	const usage = "用法: mtl diagnose <实例名> [--dir <实例根目录>]；或 mtl diagnose --last"
	opts := diagnoseOptions{root: defaultInstancesRoot}
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--last":
			opts.last = true
		case a == "--dir":
			if i+1 >= len(args) {
				return opts, errors.New("--dir 缺少参数值")
			}
			i++
			opts.root = args[i]
		case strings.HasPrefix(a, "--dir="):
			opts.root = strings.TrimPrefix(a, "--dir=")
		case strings.HasPrefix(a, "-"):
			return opts, fmt.Errorf("未知参数 %q（%s）", a, usage)
		default:
			positional = append(positional, a)
		}
	}
	if strings.TrimSpace(opts.root) == "" {
		return opts, errors.New("--dir 不能为空")
	}
	if opts.last {
		if len(positional) != 0 {
			return opts, errors.New("--last 不能与实例名同时使用；" + usage)
		}
		return opts, nil
	}
	if len(positional) != 1 {
		return opts, errors.New(usage)
	}
	opts.name = positional[0]
	return opts, nil
}

// resolveDiagnoseDir 解析实例名到目录。与 mtl info 共用单层目录名规则，
// 但不要求 .mtl/config.json 存在，避免配置损坏时无法诊断日志。
func resolveDiagnoseDir(root, name string, last bool) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("实例根目录为空")
	}
	if last {
		return filepath.Abs(root)
	}
	name = strings.TrimSpace(name)
	if err := mrpack.ValidateRelPath(name); err != nil {
		return "", fmt.Errorf("实例名 %q 非法: %w", name, err)
	}
	if strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("实例名 %q 必须是单个目录名", name)
	}
	dir, err := mrpack.SafeJoin(root, name)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("实例不存在: %s", dir)
		}
		return "", fmt.Errorf("读取实例目录失败: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("实例路径不是目录: %s", dir)
	}
	return filepath.Abs(dir)
}

func printDiagnoseReport(dir, name string, rec *lastrun.Record) error {
	logPath, err := instanceLogForDiagnose(dir, rec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[Diagnose] 警告: 查找运行日志失败: %v\n", err)
	}
	crashPath, err := diagnose.LatestCrashFile(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[Diagnose] 警告: 查找崩溃报告失败: %v\n", err)
	}

	logText, readErr := diagnose.ReadTextTail(logPath, diagnose.MaxLogBytes)
	if readErr != nil {
		fmt.Fprintf(os.Stderr, "[Diagnose] 警告: 读取运行日志失败: %v\n", readErr)
		logText = ""
	}
	crashText, readErr := diagnose.ReadTextTail(crashPath, diagnose.MaxCrashBytes)
	if readErr != nil {
		fmt.Fprintf(os.Stderr, "[Diagnose] 警告: 读取崩溃报告失败: %v\n", readErr)
		crashText = ""
	}

	var exitCode *int
	if rec != nil && sameInstanceDir(rec.InstanceDir, dir) {
		exitCode = rec.ExitCode
	}
	rep := diagnose.Analyze(diagnose.Input{
		InstanceName: name,
		InstanceDir:  dir,
		LogFile:      logPath,
		CrashFile:    crashPath,
		Log:          logText,
		Crash:        crashText,
		ExitCode:     exitCode,
		CrashStale:   crashPath != "" && !diagnose.CrashIsCurrent(crashPath, logPath, 0),
	})

	cfg, cfgErr := config.Load(dir)
	if cfgErr != nil {
		fmt.Fprintf(os.Stderr, "[Diagnose] 警告: %v；诊断继续\n", cfgErr)
	}
	printDiagnoseHeader(name, dir, cfg, rep, logPath, crashPath)
	printDiagnoseFindings(rep)
	return nil
}

// instanceLogForDiagnose 优先使用上次运行记录中的日志，其次选择最新 run 日志。
func instanceLogForDiagnose(dir string, rec *lastrun.Record) (string, error) {
	if rec != nil && sameInstanceDir(rec.InstanceDir, dir) && rec.LogFile != "" {
		if info, err := os.Stat(rec.LogFile); err == nil && info.Mode().IsRegular() {
			return rec.LogFile, nil
		}
	}
	return diagnose.LatestLogFile(dir)
}

func sameInstanceDir(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA == nil {
		a = aa
	}
	if errB == nil {
		b = bb
	}
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func printDiagnoseHeader(name, dir string, cfg config.Config, rep diagnose.Report, logPath, crashPath string) {
	fmt.Printf("[Diagnose] 实例: %s\n", name)
	fmt.Printf("[Diagnose] 目录: %s\n", dir)
	if cfg.LoaderType != "" {
		fmt.Printf("[Diagnose] 版本: %s %s\n", cfg.LoaderType, cfg.LoaderVersion)
	}
	fmt.Printf("[Diagnose] 运行日志: %s\n", displayPath(logPath))
	fmt.Printf("[Diagnose] 崩溃报告: %s\n", displayPath(crashPath))
	fmt.Printf("[Diagnose] 退出码: %s\n", displayExitCode(rep.ExitCode))

	status := "未崩溃 / 正常结束"
	if rep.Crashed {
		status = "已崩溃 / 异常退出"
	}
	fmt.Printf("[Diagnose] 摘要: %s；退出原因预判：%s；置信度 %d%%\n",
		status, rep.Reason, rep.Confidence)
	for _, note := range rep.Notes {
		fmt.Printf("[Diagnose] 提示: %s\n", note)
	}
}

func printDiagnoseFindings(rep diagnose.Report) {
	if len(rep.Findings) == 0 {
		fmt.Println("[Diagnose] 可疑问题: 未发现明显问题")
	} else {
		fmt.Printf("[Diagnose] 可疑问题（%d 项）:\n", len(rep.Findings))
		for i, f := range rep.Findings {
			fmt.Printf("[Diagnose]   %d. [%s] %s（置信度 %d%%）\n", i+1, f.Severity, f.Title, f.Confidence)
			if f.Detail != "" {
				fmt.Printf("[Diagnose]      说明: %s\n", f.Detail)
			}
			if len(f.Advice) > 0 {
				fmt.Println("[Diagnose]      建议:")
				for _, a := range f.Advice {
					fmt.Printf("[Diagnose]        - %s\n", a)
				}
			}
			if len(f.Evidence) > 0 {
				fmt.Println("[Diagnose]      关键行:")
				for _, line := range f.Evidence {
					fmt.Printf("[Diagnose]        > %s\n", line)
				}
			}
		}
	}

	// 问题证据用于定位具体命中的关键词；崩溃栈关键行只在异常时汇总，避免正常日志刷屏。
	if len(rep.Highlights) > 0 && rep.Crashed {
		fmt.Println("[Diagnose] 崩溃栈关键行:")
		for _, line := range rep.Highlights {
			fmt.Printf("[Diagnose]   > %s\n", line)
		}
	}
	fmt.Println("[Diagnose] 说明: 诊断仅基于本地日志关键词匹配，复杂问题请查看完整日志或崩溃报告")
}

func displayPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return "未找到"
	}
	return path
}

func displayExitCode(code *int) string {
	if code == nil {
		return "未知（可能未启动 Java 进程）"
	}
	if *code == -1 {
		return "强制结束（-1）"
	}
	return fmt.Sprintf("%d", *code)
}
