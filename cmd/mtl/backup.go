package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mtl/internal/backup"
	"mtl/internal/logging"
	"mtl/internal/mrpack"
)

// backupCommand 实现 mtl backup：只读扫描实例并生成 zip 备份。
func backupCommand(args []string) error {
	opts, err := parseBackupOptions(args)
	if err != nil {
		return err
	}
	dir, err := resolveNamedInstance(opts.root, opts.instance)
	if err != nil {
		return err
	}

	plan, err := backup.BuildPlan(dir)
	if err != nil {
		return err
	}
	logger := newBackupLogger(opts.debug || opts.dryRun)
	logger.Info("Backup", "实例: %s（%s）", opts.instance, dir)
	if len(plan.Include) == 0 {
		logger.Info("Backup", "范围: 无可打包内容")
	} else {
		logger.Info("Backup", "范围: %s", strings.Join(plan.Include, ", "))
	}
	if opts.debug || opts.dryRun {
		for _, e := range plan.Entries {
			if e.Dir {
				logger.Debug("Backup", "目录 %s/", e.Rel)
				continue
			}
			logger.Debug("Backup", "文件 %s（%s）", e.Rel, formatBytes(e.Size))
		}
		for _, sk := range plan.Skipped {
			logger.Debug("Backup", "跳过 %s（%s）", sk.Path, sk.Reason)
		}
	}
	if len(plan.Skipped) > 0 {
		logger.Info("Backup", "已排除 %d 个软链接/外部文件或特殊条目", len(plan.Skipped))
	}

	if opts.dryRun {
		logger.Info("Backup", "预览: 共 %d 个文件，%s；未生成备份包",
			plan.FileCount, formatBytes(plan.TotalBytes))
		return nil
	}

	res, err := backup.WriteArchive(plan, opts.instance, backup.WriteOptions{
		Out:  opts.out,
		Name: opts.name,
		Now:  time.Now(),
	})
	if err != nil {
		return err
	}
	logger.Info("Backup", "已生成: %s", res.Path)
	logger.Info("Backup", "包含 %d 个文件、%d 个目录，原始大小 %s，压缩包 %s",
		res.FileCount, res.DirCount, formatBytes(res.TotalBytes), formatBytes(res.ArchiveSize))
	if res.Metadata.Name != "" {
		logger.Info("Backup", "备注: %s", res.Metadata.Name)
	}
	return nil
}

type backupOptions struct {
	root     string
	instance string
	out      string
	name     string
	dryRun   bool
	debug    bool
}

const backupUsage = "用法: mtl backup <实例名> [--dir <实例根目录>] [--out <zip路径>] [--name <备注>] [--dry-run] [--debug]"

func parseBackupOptions(args []string) (backupOptions, error) {
	opts := backupOptions{root: defaultInstancesRoot}
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dir":
			if i+1 >= len(args) {
				return opts, errors.New("--dir 缺少参数值")
			}
			i++
			opts.root = args[i]
		case strings.HasPrefix(a, "--dir="):
			opts.root = strings.TrimPrefix(a, "--dir=")
		case a == "--out":
			if i+1 >= len(args) {
				return opts, errors.New("--out 缺少参数值")
			}
			i++
			opts.out = args[i]
		case strings.HasPrefix(a, "--out="):
			opts.out = strings.TrimPrefix(a, "--out=")
		case a == "--name":
			if i+1 >= len(args) {
				return opts, errors.New("--name 缺少参数值")
			}
			i++
			opts.name = args[i]
		case strings.HasPrefix(a, "--name="):
			opts.name = strings.TrimPrefix(a, "--name=")
		case a == "--dry-run":
			opts.dryRun = true
		case a == "--debug":
			opts.debug = true
		case strings.HasPrefix(a, "-"):
			return opts, fmt.Errorf("未知参数 %q（%s）", a, backupUsage)
		default:
			positional = append(positional, a)
		}
	}
	if strings.TrimSpace(opts.root) == "" {
		return opts, errors.New("--dir 不能为空")
	}
	if len(positional) != 1 {
		return opts, errors.New(backupUsage)
	}
	opts.instance = strings.TrimSpace(positional[0])
	if err := validateSingleInstanceName(opts.instance); err != nil {
		return opts, err
	}
	return opts, nil
}

// restoreCommand 实现 mtl restore：列出备份或从 zip 安全还原实例。
func restoreCommand(args []string) error {
	opts, err := parseRestoreOptions(args)
	if err != nil {
		return err
	}
	logger := newBackupLogger(opts.debug)
	if opts.list {
		return listBackups(logger, opts.debug)
	}

	info, err := backup.ReadInfo(opts.zip)
	if err != nil {
		return err
	}
	target, err := resolveRestoreTarget(opts.root, opts.target)
	if err != nil {
		return err
	}

	logger.Info("Restore", "备份: %s（%s）", info.Path, formatBytes(info.Size))
	logger.Info("Restore", "来源实例: %s", info.Metadata.Instance)
	logger.Info("Restore", "备份时间: %s", info.Metadata.CreatedAt.Local().Format("2006-01-02 15:04:05"))
	if info.Metadata.Name != "" {
		logger.Info("Restore", "备注: %s", info.Metadata.Name)
	}
	logger.Info("Restore", "包含: %s", joinOrDash(info.Metadata.Include))

	targetExists := false
	if stat, statErr := os.Stat(target); statErr == nil {
		if !stat.IsDir() {
			return fmt.Errorf("目标路径不是目录: %s", target)
		}
		targetExists = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("检查目标实例失败: %w", statErr)
	}
	if targetExists {
		logger.Info("Restore", "目标: %s（已存在%s）", target, forceHint(opts.force))
	} else {
		logger.Info("Restore", "目标: %s（将新建）", target)
	}

	res, err := backup.Restore(info.Path, target, backup.RestoreOptions{
		Force: opts.force,
		Now:   time.Now(),
		OnEntry: func(e backup.Entry) {
			if e.Dir {
				logger.Debug("Restore", "目录 %s/", e.Rel)
				return
			}
			logger.Debug("Restore", "文件 %s（%s）", e.Rel, formatBytes(e.Size))
		},
	})
	if err != nil {
		return err
	}
	for _, r := range res.Renamed {
		logger.Info("Restore", "旧内容已保留: %s -> %s", filepath.Base(r.From), filepath.Base(r.To))
	}
	logger.Info("Restore", "还原完成: %s（%d 个文件，%s）",
		res.TargetDir, res.Files, formatBytes(res.Bytes))
	return nil
}

type restoreOptions struct {
	root   string
	zip    string
	target string
	list   bool
	force  bool
	debug  bool
}

const restoreUsage = "用法: mtl restore <备份zip路径> --target <实例名> [--dir <实例根目录>] [--force] [--debug]；或 mtl restore --list"

func parseRestoreOptions(args []string) (restoreOptions, error) {
	opts := restoreOptions{root: defaultInstancesRoot}
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dir":
			if i+1 >= len(args) {
				return opts, errors.New("--dir 缺少参数值")
			}
			i++
			opts.root = args[i]
		case strings.HasPrefix(a, "--dir="):
			opts.root = strings.TrimPrefix(a, "--dir=")
		case a == "--target":
			if i+1 >= len(args) {
				return opts, errors.New("--target 缺少参数值")
			}
			i++
			opts.target = args[i]
		case strings.HasPrefix(a, "--target="):
			opts.target = strings.TrimPrefix(a, "--target=")
		case a == "--list":
			opts.list = true
		case a == "--force":
			opts.force = true
		case a == "--debug":
			opts.debug = true
		case strings.HasPrefix(a, "-"):
			return opts, fmt.Errorf("未知参数 %q（%s）", a, restoreUsage)
		default:
			positional = append(positional, a)
		}
	}
	if strings.TrimSpace(opts.root) == "" {
		return opts, errors.New("--dir 不能为空")
	}

	if opts.list {
		if len(positional) != 0 || strings.TrimSpace(opts.target) != "" || opts.force {
			return opts, errors.New("--list 不能与备份路径、--target 或 --force 同时使用")
		}
		return opts, nil
	}
	if len(positional) != 1 {
		return opts, errors.New(restoreUsage)
	}
	opts.zip = strings.TrimSpace(positional[0])
	opts.target = strings.TrimSpace(opts.target)
	if opts.target == "" {
		return opts, errors.New("必须用 --target 指定目标实例名")
	}
	if err := validateSingleInstanceName(opts.target); err != nil {
		return opts, err
	}
	return opts, nil
}

func listBackups(logger *logging.Logger, debug bool) error {
	dir, err := backup.DefaultDir()
	if err != nil {
		return err
	}
	items, err := backup.List("")
	if err != nil {
		return err
	}
	logger.Info("Restore", "备份目录: %s", dir)
	if len(items) == 0 {
		logger.Info("Restore", "没有本地备份")
		return nil
	}

	rows := make([][]string, 0, len(items)+1)
	rows = append(rows, []string{"备份文件", "备份时间", "来源实例", "备注", "大小"})
	invalid := 0
	for _, item := range items {
		name := filepath.Base(item.Info.Path)
		if item.Err != "" {
			invalid++
			rows = append(rows, []string{
				name,
				item.Info.ModTime.Local().Format("2006-01-02 15:04:05"),
				"(无效)",
				"-",
				formatBytes(item.Info.Size),
			})
			continue
		}
		rows = append(rows, []string{
			name,
			item.Info.Metadata.CreatedAt.Local().Format("2006-01-02 15:04:05"),
			item.Info.Metadata.Instance,
			orDash(item.Info.Metadata.Name),
			formatBytes(item.Info.Size),
		})
		if debug {
			logger.Debug("Restore", "%s: %s", name, item.Info.Path)
		}
	}
	writeTable(os.Stdout, rows)
	for _, item := range items {
		if item.Err != "" {
			logger.Warn("Restore", "无法读取 %s: %s", filepath.Base(item.Info.Path), item.Err)
		}
	}
	if invalid > 0 {
		logger.Info("Restore", "共 %d 个 zip，其中 %d 个不是有效的 mtl 备份", len(items), invalid)
	} else {
		logger.Info("Restore", "共 %d 个备份", len(items))
	}
	return nil
}

// resolveNamedInstance 解析并校验已存在的实例目录，实例名必须是单层目录名。
func resolveNamedInstance(root, name string) (string, error) {
	dir, err := resolveRestoreTarget(root, name)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("实例不存在: %s", dir)
		}
		return "", fmt.Errorf("读取实例目录失败: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("实例路径不是目录: %s", dir)
	}
	return dir, nil
}

// resolveRestoreTarget 只做路径安全校验，不要求目标已存在。
func resolveRestoreTarget(root, name string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("实例根目录为空")
	}
	if err := validateSingleInstanceName(name); err != nil {
		return "", err
	}
	dir, err := mrpack.SafeJoin(root, name)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("解析实例目录失败: %w", err)
	}
	return abs, nil
}

func validateSingleInstanceName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("实例名为空")
	}
	if err := mrpack.ValidateRelPath(name); err != nil {
		return fmt.Errorf("实例名 %q 非法: %w", name, err)
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("实例名 %q 必须是单个目录名", name)
	}
	return nil
}

func newBackupLogger(debug bool) *logging.Logger {
	level := logging.LevelInfo
	if debug {
		level = logging.LevelDebug
	}
	return logging.New(logging.Options{Console: os.Stdout, Level: level})
}

func forceHint(force bool) string {
	if force {
		return "，将保留被覆盖的旧目录/文件"
	}
	return "，如确认覆盖请加 --force"
}

func joinOrDash(items []string) string {
	if len(items) == 0 {
		return "-"
	}
	return strings.Join(items, ", ")
}
