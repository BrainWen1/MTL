package launch

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"mtl/internal/mrpack"
)

var mavenPartPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)

// rulesAllow 使用 Mojang 规则语义：无规则默认允许，最后一条命中的规则决定结果。
func rulesAllow(rules []Rule, osName, arch string) bool {
	if len(rules) == 0 {
		return true
	}
	allowed := false
	for _, r := range rules {
		if !ruleApplies(r, osName, arch) {
			continue
		}
		allowed = r.Action == "allow"
	}
	return allowed
}

func ruleApplies(r Rule, osName, arch string) bool {
	if r.OS != nil {
		if r.OS.Name != "" && r.OS.Name != osName {
			return false
		}
		if r.OS.Arch != "" && r.OS.Arch != arch {
			return false
		}
	}
	// 未实现 demo 等启动特性，统一视为未启用。
	for _, on := range r.Features {
		if on {
			return false
		}
	}
	return true
}

func ruleOS(goos, goarch string) (name, arch string) {
	switch goos {
	case "windows":
		name = "windows"
	case "darwin":
		name = "osx"
	default:
		name = "linux"
	}
	arch = goarch
	switch goarch {
	case "386":
		arch = "x86"
	case "arm64":
		arch = "aarch64"
	}
	return name, arch
}

func archBits(goarch string) string {
	if goarch == "386" {
		return "32"
	}
	return "64"
}

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

// nativeClassifierArch 返回 native classifier 的目标架构。
// 无架构后缀的 natives-<os> 按 amd64 处理；无法识别时返回 ok=false。
func nativeClassifierArch(classifier string) (arch string, ok bool) {
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

// mavenRelPath 把 group:artifact:version[:classifier][@ext] 转成实例内 libraries/ 路径。
// extraClassifier 用于旧版 natives 指定额外 classifier。
func mavenRelPath(name, extraClassifier string) (string, error) {
	if name == "" {
		return "", errors.New("maven 坐标为空")
	}
	ext := "jar"
	if i := strings.LastIndexByte(name, '@'); i >= 0 {
		ext = name[i+1:]
		name = name[:i]
	}
	parts := strings.Split(name, ":")
	if len(parts) != 3 && len(parts) != 4 {
		return "", fmt.Errorf("maven 坐标 %q 段数非法", name)
	}
	group, artifact, version := parts[0], parts[1], parts[2]
	classifier := extraClassifier
	if len(parts) == 4 {
		classifier = parts[3]
	}

	groupParts := strings.Split(group, ".")
	for _, p := range groupParts {
		if p == "" || p == "." || p == ".." || !mavenPartPattern.MatchString(p) {
			return "", fmt.Errorf("maven 坐标 %q 的 group 非法", name)
		}
	}
	if !mavenPartPattern.MatchString(artifact) || !mavenPartPattern.MatchString(version) || !mavenPartPattern.MatchString(ext) {
		return "", fmt.Errorf("maven 坐标 %q 含非法字段", name)
	}
	if classifier != "" && !mavenPartPattern.MatchString(classifier) {
		return "", fmt.Errorf("maven 坐标 %q 的 classifier %q 非法", name, classifier)
	}

	rel := strings.Join(groupParts, "/") + "/" + artifact + "/" + version + "/" + artifact + "-" + version
	if classifier != "" {
		rel += "-" + classifier
	}
	rel += "." + ext
	full := "libraries/" + rel
	if err := mrpack.ValidateRelPath(full); err != nil {
		return "", fmt.Errorf("maven 坐标 %q 生成的路径非法: %w", name, err)
	}
	return full, nil
}
