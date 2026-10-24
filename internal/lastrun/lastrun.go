// Package lastrun 记录最近一次 mtl run 的实例、日志与退出码，供 mtl diagnose --last 使用。
// 记录文件与全局配置放在同一目录，不写入实例、runtime 或 assets 缓存。
package lastrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mtl/internal/config"
)

const (
	// FileName 是最近一次运行记录的文件名。
	FileName = "last-run.json"
	// EnvPath 可显式指定记录文件路径（测试与高级用法）。
	EnvPath = "MTL_LAST_RUN_PATH"
)

// Record 描述一次 mtl run 的起止状态。
type Record struct {
	InstanceDir      string     `json:"instance_dir"`
	InstanceName     string     `json:"instance_name,omitempty"`
	MinecraftVersion string     `json:"minecraft_version,omitempty"`
	LogFile          string     `json:"log_file,omitempty"`
	StartedAt        time.Time  `json:"started_at"`
	EndedAt          *time.Time `json:"ended_at,omitempty"`
	// ExitCode 为 Java 子进程退出码；尚未启动或启动前失败时为空。
	// -1 表示进程被强制结束。
	ExitCode   *int `json:"exit_code,omitempty"`
	ProcessPID int  `json:"process_pid,omitempty"`
}

// Path 返回最近运行记录的路径：与全局配置同目录；可通过 MTL_LAST_RUN_PATH 覆盖。
func Path() (string, error) {
	if p := strings.TrimSpace(os.Getenv(EnvPath)); p != "" {
		return p, nil
	}
	globalPath, err := config.GlobalPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(globalPath), FileName), nil
}

// Load 读取记录。文件不存在时返回 os.ErrNotExist，调用方据此给出友好提示。
func Load() (Record, error) {
	path, err := Path()
	if err != nil {
		return Record{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Record{}, fmt.Errorf("没有可诊断的上次运行记录: %w", err)
		}
		return Record{}, fmt.Errorf("读取上次运行记录 %s 失败: %w", path, err)
	}
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return Record{}, fmt.Errorf("上次运行记录 %s 损坏: %w", path, err)
	}
	rec.InstanceDir = strings.TrimSpace(rec.InstanceDir)
	rec.InstanceName = strings.TrimSpace(rec.InstanceName)
	rec.MinecraftVersion = strings.TrimSpace(rec.MinecraftVersion)
	rec.LogFile = strings.TrimSpace(rec.LogFile)
	if rec.InstanceDir == "" {
		return Record{}, fmt.Errorf("上次运行记录 %s 缺少 instance_dir", path)
	}
	return rec, nil
}

// Save 原子写入记录，目录不存在时自动创建，重复写入相同内容结果一致。
func Save(rec Record) error {
	if strings.TrimSpace(rec.InstanceDir) == "" {
		return errors.New("上次运行记录缺少实例目录")
	}
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建运行记录目录失败: %w", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("编码运行记录失败: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".last-run-*.tmp")
	if err != nil {
		return fmt.Errorf("创建运行记录临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("设置运行记录权限失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入运行记录失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭运行记录失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("保存运行记录失败: %w", err)
	}
	return nil
}
