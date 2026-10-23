//go:build windows

package launch

import (
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"
)

// Windows 没有公开的、可对“子进程”设置 GPU 偏好的用户态 API：
// D3DKMTSetProcessGpuPreference 并非 gdi32 的公开导出，SetProcessInformation 也没有
// 公开的 ProcessGpuPreference 信息类。系统“显示卡”设置本身是把按可执行文件路径的
// 偏好写入 HKCU 的 UserGpuPreferences；Windows 在创建进程时读取它，因此这里采用
// 与系统设置相同的机制，在启动 Java 之前完成标记。
const (
	// userGpuPreferencesKey 与 Windows“设置 → 系统 → 显示 → 显示卡”写入的位置一致。
	// 值名是可执行文件绝对路径，数据形如 "GpuPreference=2;"（2 = 高性能）。
	userGpuPreferencesKey = `Software\Microsoft\DirectX\UserGpuPreferences`
	highPerfGPUValue      = "GpuPreference=2;"
)

var (
	modadvapi32         = syscall.NewLazyDLL("advapi32.dll")
	procRegCreateKeyExW = modadvapi32.NewProc("RegCreateKeyExW")
	procRegSetValueExW  = modadvapi32.NewProc("RegSetValueExW")
	procRegCloseKey     = modadvapi32.NewProc("RegCloseKey")
)

// highPerfGPUPreference 返回写入注册表的“值名 -> 数据”。
// 值名必须是 Java 可执行文件的绝对路径，Windows 在创建进程时按该路径匹配偏好。
func highPerfGPUPreference(javaPath string) (name, data string, err error) {
	abs, err := filepath.Abs(javaPath)
	if err != nil {
		return "", "", fmt.Errorf("解析 Java 路径失败: %w", err)
	}
	return filepath.Clean(abs), highPerfGPUValue, nil
}

// applyHighPerfGPUPreference 为 Java 可执行文件写入系统级高性能 GPU 首选项。
// 返回 supported=true 表示当前平台支持该能力；错误只向上传递，由调用方告警后继续启动。
func applyHighPerfGPUPreference(javaPath string) (supported bool, err error) {
	name, data, err := highPerfGPUPreference(javaPath)
	if err != nil {
		return true, err
	}
	if err := writeUserGpuPreference(name, data); err != nil {
		return true, err
	}
	return true, nil
}

// writeUserGpuPreference 向 HKCU 写入 UserGpuPreferences 字符串值。
// 使用 HKCU 无需管理员权限；重复写入相同值保持幂等。
func writeUserGpuPreference(name, data string) error {
	subKey, err := syscall.UTF16PtrFromString(userGpuPreferencesKey)
	if err != nil {
		return err
	}

	var key syscall.Handle
	var disposition uint32
	ret, _, _ := procRegCreateKeyExW.Call(
		uintptr(syscall.HKEY_CURRENT_USER),
		uintptr(unsafe.Pointer(subKey)),
		0,
		0,
		0,
		uintptr(syscall.KEY_QUERY_VALUE|syscall.KEY_SET_VALUE),
		0,
		uintptr(unsafe.Pointer(&key)),
		uintptr(unsafe.Pointer(&disposition)),
	)
	if ret != 0 {
		return fmt.Errorf("打开注册表项 %s 失败: %w", userGpuPreferencesKey, syscall.Errno(ret))
	}
	defer procRegCloseKey.Call(uintptr(key))

	valueName, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	valueData, err := syscall.UTF16FromString(data)
	if err != nil {
		return err
	}

	ret, _, _ = procRegSetValueExW.Call(
		uintptr(key),
		uintptr(unsafe.Pointer(valueName)),
		0,
		uintptr(syscall.REG_SZ),
		uintptr(unsafe.Pointer(&valueData[0])),
		uintptr(len(valueData)*2),
	)
	if ret != 0 {
		return fmt.Errorf("写入 GPU 首选项 %s 失败: %w", name, syscall.Errno(ret))
	}
	return nil
}
