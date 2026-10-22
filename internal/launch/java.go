package launch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"time"
)

var javaVersionPattern = regexp.MustCompile(`version "(\d+)(?:\.(\d+))?`)

// findJava 优先使用显式指定的 java 路径，否则只在 PATH 中查找。
// 后续可在此扩展自动下载 JDK。
func findJava(ctx context.Context, requiredMajor int, override string) (path string, major int, err error) {
	if override != "" {
		abs, absErr := filepath.Abs(override)
		if absErr != nil {
			return "", 0, fmt.Errorf("指定的java不存在: %s", override)
		}
		info, statErr := os.Stat(abs)
		if statErr != nil || !info.Mode().IsRegular() {
			return "", 0, fmt.Errorf("指定的java不存在: %s", override)
		}
		if !isExecutable(info) {
			return "", 0, fmt.Errorf("指定的java不可执行: %s", override)
		}
		path = abs
	} else {
		path, err = exec.LookPath("java")
		if err != nil {
			return "", 0, fmt.Errorf("未找到系统 PATH 中的 java: %w", err)
		}
		if abs, absErr := filepath.Abs(path); absErr == nil {
			path = abs
		}
	}

	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, path, "-version").CombinedOutput()
	if err != nil {
		return "", 0, fmt.Errorf("执行 %s -version 失败: %w", path, err)
	}
	major, err = parseJavaMajor(string(out))
	if err != nil {
		return "", 0, err
	}
	if requiredMajor > 0 && major < requiredMajor {
		return "", 0, fmt.Errorf("系统 Java 版本 %d 低于版本要求 %d", major, requiredMajor)
	}
	return path, major, nil
}

// isExecutable 在 Windows 上只要求是普通文件（执行位无意义），POSIX 上检查执行位。
func isExecutable(info os.FileInfo) bool {
	if !info.Mode().IsRegular() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode().Perm()&0o111 != 0
}

// parseJavaMajor 兼容 Java 8 的 "1.8.0_392" 与 9+ 的 "17.0.10"。
func parseJavaMajor(versionOutput string) (int, error) {
	m := javaVersionPattern.FindStringSubmatch(versionOutput)
	if m == nil {
		return 0, fmt.Errorf("无法识别 java -version 输出: %q", firstLine(versionOutput))
	}
	major, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, err
	}
	if major == 1 && m[2] != "" {
		minor, err := strconv.Atoi(m[2])
		if err != nil {
			return 0, err
		}
		return minor, nil
	}
	return major, nil
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' || s[i] == '\r' {
			return s[:i]
		}
	}
	return s
}
