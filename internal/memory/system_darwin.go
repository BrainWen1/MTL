//go:build darwin

package memory

import (
	"fmt"
	"strconv"
	"strings"
	"syscall"
)

// SystemMemory 读取 hw.memsize；macOS 不提供等价的可用内存查询，available 返回 0。
func SystemMemory() (total, available int64, err error) {
	raw, err := syscall.Sysctl("hw.memsize")
	if err != nil {
		return 0, 0, fmt.Errorf("读取 hw.memsize 失败: %w", err)
	}
	total, err = strconv.ParseInt(strings.Trim(raw, "\x00"), 10, 64)
	if err != nil || total <= 0 {
		return 0, 0, fmt.Errorf("解析 hw.memsize 失败: %q", raw)
	}
	return total, 0, nil
}
