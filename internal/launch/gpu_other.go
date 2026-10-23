//go:build !windows

package launch

// applyHighPerfGPUPreference 在非 Windows 平台静默跳过：不执行任何系统调用，也不报错。
func applyHighPerfGPUPreference(javaPath string) (supported bool, err error) {
	_ = javaPath
	return false, nil
}
