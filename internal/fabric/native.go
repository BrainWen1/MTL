package fabric

import "strings"

// nativeClassifierArch 判断 maven classifier 是否为 native，并返回其目标架构。
// 无架构后缀的 natives-<os> 按 64 位（amd64）处理；无法识别的命名不参与过滤。
func nativeClassifierArch(classifier string) (arch string, isNative bool) {
	c := strings.ToLower(classifier)
	if !strings.HasPrefix(c, "natives-") {
		return "", false
	}
	switch {
	case strings.HasSuffix(c, "-arm64"), strings.HasSuffix(c, "-aarch64"):
		return "arm64", true
	case strings.HasSuffix(c, "-x86_64"), strings.HasSuffix(c, "-amd64"):
		return "amd64", true
	case strings.HasSuffix(c, "-x86"), strings.HasSuffix(c, "-32"):
		return "386", true
	case strings.HasSuffix(c, "-arm32"):
		return "arm", true
	case strings.HasSuffix(c, "-64"):
		return "amd64", true
	}
	for _, osName := range []string{"windows", "linux", "macos", "osx"} {
		if c == "natives-"+osName {
			return "amd64", true
		}
	}
	return "", false
}

// libraryClassifier 从 group:artifact:version[:classifier][@ext] 中提取 classifier。
func libraryClassifier(name string) string {
	if i := strings.LastIndexByte(name, '@'); i >= 0 {
		name = name[:i]
	}
	parts := strings.Split(name, ":")
	if len(parts) < 4 {
		return ""
	}
	return parts[3]
}
