//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

// nativeOpenMrpackDialog 调用 Windows 自带的文件选择对话框。
// PowerShell 只执行这里固定的内联脚本，不读取用户脚本文件；
// 输出统一为 UTF-8，避免中文路径在默认控制台代码页下乱码。
func nativeOpenMrpackDialog() (string, error) {
	const script = `$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding -ArgumentList $false
Add-Type -AssemblyName System.Windows.Forms | Out-Null
$dialog = New-Object System.Windows.Forms.OpenFileDialog
$dialog.Title = '选择 mrpack 整合包'
$dialog.Filter = 'mrpack 整合包 (*.mrpack)|*.mrpack'
$dialog.FilterIndex = 1
$dialog.CheckFileExists = $true
$dialog.CheckPathExists = $true
$dialog.Multiselect = $false
$dialog.RestoreDirectory = $true
if ($dialog.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
    [Console]::Out.Write($dialog.FileName)
}
`
	cmd := exec.Command("powershell.exe",
		"-NoLogo",
		"-NoProfile",
		"-STA",
		"-Command", script,
	)
	// 不弹出额外的控制台窗口；文件选择框本身仍是系统原生窗口。
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			detail := strings.TrimSpace(string(exitErr.Stderr))
			if detail != "" {
				if len(detail) > 500 {
					detail = detail[:500] + "…"
				}
				return "", fmt.Errorf("系统文件选择框异常: %s", detail)
			}
		}
		return "", fmt.Errorf("无法打开系统文件选择框: %w", err)
	}
	path := strings.TrimSpace(string(out))
	return path, nil
}
