//go:build linux

package memory

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// SystemMemory 从 /proc/meminfo 读取物理内存总量与可用量（字节）。
func SystemMemory() (total, available int64, err error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, fmt.Errorf("读取 /proc/meminfo 失败: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, parseErr := strconv.ParseInt(fields[1], 10, 64)
		if parseErr != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = value * 1024
		case "MemAvailable:":
			available = value * 1024
		}
	}
	if total <= 0 {
		return 0, 0, errors.New("/proc/meminfo 中未找到 MemTotal")
	}
	return total, available, nil
}
