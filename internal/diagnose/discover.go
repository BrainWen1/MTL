// Package diagnose 离线解析实例的运行日志与崩溃报告，输出结构化诊断报告。
// 只读本地文本，不联网、不启动游戏，任何文件缺失都降级为提示。
package diagnose

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mtl/internal/mrpack"
)

const (
	// MaxLogBytes 限制读取日志尾部的大小，避免超大 latest.log 拖慢诊断。
	MaxLogBytes int64 = 4 << 20
	// MaxCrashBytes 限制读取单个崩溃报告的大小。
	MaxCrashBytes int64 = 8 << 20
	// archiveSummaryMarker 是 mtl 归档崩溃文件顶部诊断摘要与原文的分隔行。
	archiveSummaryMarker = "===== 以下为原始崩溃日志 ====="
	// archiveDirRel 是 mtl 自己归档崩溃报告的目录。
	archiveDirRel = "crash_reports"
)

// rawCrashDirRels 是 Minecraft 原版生成的崩溃报告目录，优先于 mtl 归档目录。
var rawCrashDirRels = []string{
	".minecraft/crash-reports",
	"crash-reports",
}

type candidate struct {
	path    string
	modTime int64
	name    string
}

// LatestLogFile 返回实例 logs 目录中最近一次运行的 mtl 日志。
// 优先 run-*.log；没有时回退到最新的任意 .log（例如 latest.log）。
// 目录不存在时返回空字符串和 nil，不视为错误。
func LatestLogFile(instanceDir string) (string, error) {
	dir, err := mrpack.SafeJoin(instanceDir, "logs")
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}

	var runLogs, otherLogs []candidate
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		c := candidate{
			path:    filepath.Join(dir, e.Name()),
			modTime: info.ModTime().UnixNano(),
			name:    e.Name(),
		}
		if strings.HasPrefix(strings.ToLower(e.Name()), "run-") {
			runLogs = append(runLogs, c)
		} else {
			otherLogs = append(otherLogs, c)
		}
	}
	if len(runLogs) > 0 {
		return newest(runLogs).path, nil
	}
	if len(otherLogs) > 0 {
		return newest(otherLogs).path, nil
	}
	return "", nil
}

// LatestCrashFile 返回实例最近的一份 crash-*.txt。
// 优先 Minecraft 原版 crash-reports，找不到时才使用 mtl 的 crash_reports 归档。
func LatestCrashFile(instanceDir string) (string, error) {
	for _, rel := range rawCrashDirRels {
		files, err := crashCandidates(instanceDir, rel)
		if err != nil {
			return "", err
		}
		if len(files) > 0 {
			return newest(files).path, nil
		}
	}
	files, err := crashCandidates(instanceDir, archiveDirRel)
	if err != nil {
		return "", err
	}
	if len(files) > 0 {
		return newest(files).path, nil
	}
	return "", nil
}

func crashCandidates(instanceDir, rel string) ([]candidate, error) {
	dir, err := mrpack.SafeJoin(instanceDir, rel)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var files []candidate
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		lower := strings.ToLower(e.Name())
		if !strings.HasPrefix(lower, "crash-") || !strings.HasSuffix(lower, ".txt") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, candidate{
			path:    filepath.Join(dir, e.Name()),
			modTime: info.ModTime().UnixNano(),
			name:    e.Name(),
		})
	}
	return files, nil
}

// newest 按修改时间倒序，时间相同时按文件名倒序，保证结果稳定。
func newest(files []candidate) candidate {
	sort.Slice(files, func(i, j int) bool {
		if files[i].modTime != files[j].modTime {
			return files[i].modTime > files[j].modTime
		}
		return files[i].name > files[j].name
	})
	return files[0]
}

// CrashIsCurrent 判断崩溃报告是否属于最近一次运行。
// 每次运行都有独立日志，日志修改时间接近运行结束时间；崩溃报告在运行期间生成，
// 因此与日志修改时间相差不大。旧崩溃报告通常明显早于新日志。
// 日志缺失时无法判断，保守返回 true（只能基于崩溃报告分析）。
func CrashIsCurrent(crashPath, logPath string, tolerance time.Duration) bool {
	if strings.TrimSpace(crashPath) == "" {
		return false
	}
	if strings.TrimSpace(logPath) == "" {
		return true
	}
	if tolerance <= 0 {
		tolerance = 10 * time.Minute
	}
	crashInfo, err := os.Stat(crashPath)
	if err != nil {
		return false
	}
	logInfo, err := os.Stat(logPath)
	if err != nil {
		return true
	}
	return !crashInfo.ModTime().Before(logInfo.ModTime().Add(-tolerance))
}

// ReadTextTail 读取文本文件末尾最多 maxBytes 字节。路径为空返回空字符串。
// 从中间截断时丢弃第一行不完整内容，避免输出半行日志。
func ReadTextTail(path string, maxBytes int64) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	var data []byte
	truncated := false
	if maxBytes > 0 && info.Size() > maxBytes {
		if _, err := f.Seek(info.Size()-maxBytes, io.SeekStart); err != nil {
			return "", err
		}
		truncated = true
	}
	data, err = io.ReadAll(io.LimitReader(f, maxBytes))
	if err != nil {
		return "", err
	}
	text := strings.ToValidUTF8(string(data), "")
	if truncated {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	return text, nil
}

// StripArchiveSummary 去掉 mtl 归档文件顶部的诊断摘要，只保留原始崩溃内容。
// 摘要里的中文提示可能包含诊断关键词，直接参与匹配会造成误报。
func StripArchiveSummary(content string) string {
	_, rest, found := strings.Cut(content, archiveSummaryMarker)
	if !found {
		return content
	}
	return strings.TrimLeft(rest, "\r\n")
}
