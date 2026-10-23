//go:build windows

package memory

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

// memoryStatusEx 对应 Windows 的 MEMORYSTATUSEX 结构。
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// SystemMemory 返回物理内存总量与当前可用量（字节）。
func SystemMemory() (total, available int64, err error) {
	var st memoryStatusEx
	st.Length = uint32(unsafe.Sizeof(st))
	r, _, callErr := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&st)))
	if r == 0 {
		return 0, 0, fmt.Errorf("读取物理内存失败: %v", callErr)
	}
	return int64(st.TotalPhys), int64(st.AvailPhys), nil
}
