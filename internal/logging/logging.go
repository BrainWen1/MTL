// Package logging 提供 mtl 运行期的分级日志：终端 + 实例日志文件。
// 终端级别可配置，日志文件默认记录 DEBUG 及以上，便于事后诊断。
package logging

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mtl/internal/mrpack"
)

// Level 是日志级别，数值越大越严重。
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// ParseLevel 解析日志级别；空字符串按 INFO 处理。
// 同时接受 PCL 风格的 info/warn/error 与常见的 warning、verbose。
func ParseLevel(raw string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "info":
		return LevelInfo, nil
	case "debug", "verbose":
		return LevelDebug, nil
	case "warn", "warning":
		return LevelWarn, nil
	case "error":
		return LevelError, nil
	default:
		return LevelInfo, fmt.Errorf("日志级别 %q 无效（可用: debug、info、warn、error）", raw)
	}
}

// Name 返回小写名称，用于配置文件。
func (l Level) Name() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return "info"
	}
}

// Label 返回固定宽度的终端标签。
func (l Level) Label() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelWarn:
		return "WARN "
	case LevelError:
		return "ERROR"
	default:
		return "INFO "
	}
}

func (l Level) String() string { return l.Name() }

// Options 控制一个 Logger 的输出目标与阈值。
// Level 只影响终端；FileLevel 为空时日志文件记录 DEBUG 及以上。
type Options struct {
	Console    io.Writer
	Level      Level
	File       io.Writer
	FileLevel  *Level
	Now        func() time.Time
	TimeLayout string
}

// Logger 是并发安全的分级日志器。所有写错误都会被忽略，避免日志故障影响启动。
type Logger struct {
	mu           sync.Mutex
	console      io.Writer
	file         io.Writer
	consoleLevel Level
	fileLevel    Level
	now          func() time.Time
	layout       string
}

// New 创建 Logger。Console/File 为空时写入 io.Discard。
func New(opts Options) *Logger {
	l := &Logger{
		console:      opts.Console,
		file:         opts.File,
		consoleLevel: opts.Level,
		fileLevel:    LevelDebug,
		now:          opts.Now,
		layout:       opts.TimeLayout,
	}
	if l.console == nil {
		l.console = io.Discard
	}
	if l.file == nil {
		l.file = io.Discard
	}
	if l.now == nil {
		l.now = time.Now
	}
	if l.layout == "" {
		l.layout = "2006-01-02 15:04:05.000"
	}
	if opts.FileLevel != nil {
		l.fileLevel = *opts.FileLevel
	}
	return l
}

// SetLevel 调整终端日志级别。
func (l *Logger) SetLevel(level Level) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.consoleLevel = level
	l.mu.Unlock()
}

// Level 返回当前终端日志级别。
func (l *Logger) Level() Level {
	if l == nil {
		return LevelInfo
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.consoleLevel
}

// Enabled 报告该级别是否至少会输出到终端或日志文件。
func (l *Logger) Enabled(level Level) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return level >= l.consoleLevel || level >= l.fileLevel
}

// Debug 输出调试日志。
func (l *Logger) Debug(module, format string, args ...any) {
	l.Log(LevelDebug, module, format, args...)
}

// Info 输出信息日志。
func (l *Logger) Info(module, format string, args ...any) { l.Log(LevelInfo, module, format, args...) }

// Warn 输出警告日志。
func (l *Logger) Warn(module, format string, args ...any) { l.Log(LevelWarn, module, format, args...) }

// Error 输出错误日志。
func (l *Logger) Error(module, format string, args ...any) {
	l.Log(LevelError, module, format, args...)
}

// Log 按级别与模块输出一行日志。模块为空时使用 mtl。
func (l *Logger) Log(level Level, module, format string, args ...any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	enabled := level >= l.consoleLevel || level >= l.fileLevel
	l.mu.Unlock()
	if !enabled {
		return
	}

	module = strings.Trim(strings.TrimSpace(module), "[]")
	if module == "" {
		module = "mtl"
	}
	msg := fmt.Sprintf(format, args...)
	if strings.HasSuffix(msg, "\n") {
		msg = strings.TrimRight(msg, "\r\n")
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	line := l.now().Format(l.layout) + " [" + level.Label() + "] [" + module + "] " + msg + "\n"
	// 重新检查阈值：SetLevel 可能在格式化期间被调用。
	if level >= l.consoleLevel {
		_, _ = io.WriteString(l.console, line)
	}
	if level >= l.fileLevel {
		_, _ = io.WriteString(l.file, line)
	}
}

// RawWriter 返回同时写入终端与日志文件的原始输出流，用于游戏 stdout/stderr。
// 原始输出不添加时间戳和级别，始终写入两个目标。
func (l *Logger) RawWriter() io.Writer {
	if l == nil {
		return io.Discard
	}
	return rawWriter{l: l}
}

type rawWriter struct{ l *Logger }

func (w rawWriter) Write(p []byte) (int, error) {
	l := w.l
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.console.Write(p)
	_, _ = l.file.Write(p)
	return len(p), nil
}

// OpenInstanceLog 在 <实例目录>/logs 下创建本次启动独占的日志文件。
// 文件名形如 run-20261024-2230.log；同一分钟内多次启动时追加 -2、-3 后缀，
// 保证每次运行独立成文件，不会无限追加到同一个日志。
func OpenInstanceLog(instanceDir string, now time.Time) (*os.File, string, error) {
	if strings.TrimSpace(instanceDir) == "" {
		return nil, "", errors.New("实例目录为空")
	}
	if now.IsZero() {
		now = time.Now()
	}
	dir, err := mrpack.SafeJoin(instanceDir, "logs")
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", fmt.Errorf("创建日志目录 %s 失败: %w", dir, err)
	}

	base := "run-" + now.Format("20060102-1504")
	for i := 1; i <= 1000; i++ {
		name := base + ".log"
		if i > 1 {
			name = fmt.Sprintf("%s-%d.log", base, i)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("创建日志文件 %s 失败: %w", path, err)
		}
		return f, path, nil
	}
	return nil, "", fmt.Errorf("同一分钟内日志文件过多，无法创建 %s 的日志", base)
}
