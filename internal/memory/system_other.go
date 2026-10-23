//go:build !windows && !linux && !darwin

package memory

import "errors"

// SystemMemory 在未适配的平台返回错误，调用方回退为只做最小值校验。
func SystemMemory() (total, available int64, err error) {
	return 0, 0, errors.New("当前平台不支持读取物理内存")
}
