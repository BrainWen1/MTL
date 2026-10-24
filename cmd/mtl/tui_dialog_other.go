//go:build !windows

package main

import "errors"

// nativeOpenMrpackDialog 当前仅实现 Windows 原生文件选择框。
// 其他平台保留 TUI 其他功能，并给出可执行的替代命令。
func nativeOpenMrpackDialog() (string, error) {
	return "", errors.New("系统文件选择框目前仅支持 Windows，请改用 mtl install <pack.mrpack> 导入")
}
