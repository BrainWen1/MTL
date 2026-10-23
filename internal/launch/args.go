package launch

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

var placeholderPattern = regexp.MustCompile(`\$\{[^}]+\}`)

// buildArgs 合并 vanilla 与 Fabric profile 的 JVM/游戏参数，并完成占位符替换。
func (l *Launch) buildArgs(nativesDir, assetsDir, username, uuid string, extraJVM []string) (jvm, game []string, err error) {
	cp := l.ClassPath()
	versionType := l.Vanilla.Type
	if versionType == "" {
		versionType = "release"
	}
	assetsIndex := l.Vanilla.AssetIndex.ID
	if assetsIndex == "" {
		assetsIndex = l.Vanilla.Assets
	}
	repl := map[string]string{
		"auth_player_name":    username,
		"version_name":        l.ProfileID,
		"game_directory":      l.Dir,
		"assets_root":         assetsDir,
		"assets_index_name":   assetsIndex,
		"auth_uuid":           uuid,
		"auth_access_token":   "0",
		"clientid":            "",
		"auth_xuid":           "",
		"user_type":           "legacy",
		"version_type":        versionType,
		"natives_directory":   nativesDir,
		"launcher_name":       "mtl",
		"launcher_version":    "dev",
		"classpath":           cp,
		"classpath_separator": string(os.PathListSeparator),
		"library_directory":   filepath.Join(l.Dir, "libraries"),
	}

	osName, archName := ruleOS(runtime.GOOS, runtime.GOARCH)
	jvm, err = appendArguments(jvm, l.Vanilla.Arguments.JVM, repl, osName, archName)
	if err != nil {
		return nil, nil, err
	}
	jvm, err = appendArguments(jvm, l.Profile.Arguments.JVM, repl, osName, archName)
	if err != nil {
		return nil, nil, err
	}
	game, err = appendArguments(game, l.Vanilla.Arguments.Game, repl, osName, archName)
	if err != nil {
		return nil, nil, err
	}
	game, err = appendArguments(game, l.Profile.Arguments.Game, repl, osName, archName)
	if err != nil {
		return nil, nil, err
	}

	if !hasJVMArg(jvm, "-Djava.library.path=") {
		jvm = append(jvm, "-Djava.library.path="+nativesDir)
	}
	if !hasClassPathArg(jvm) {
		// 放在用户自定义参数之前，避免额外参数被误当成 classpath。
		jvm = append(jvm, "-cp", cp)
	}
	jvm = append(jvm, extraJVM...)
	game = ensureFlag(game, "--gameDir", l.Dir)
	game = ensureFlag(game, "--username", username)
	game = ensureFlag(game, "--uuid", uuid)
	// 使用 mtl 独立缓存目录，不复用其他启动器的 assets。
	game = ensureFlag(game, "--assetsDir", assetsDir)
	return jvm, game, nil
}

func appendArguments(dst []string, args []Argument, repl map[string]string, osName, arch string) ([]string, error) {
	for _, arg := range args {
		if !rulesAllow(arg.Rules, osName, arch) {
			continue
		}
		for _, raw := range arg.Values {
			value, err := substitute(raw, repl)
			if err != nil {
				return nil, err
			}
			dst = append(dst, value)
		}
	}
	return dst, nil
}

func substitute(s string, repl map[string]string) (string, error) {
	out := s
	for k, v := range repl {
		out = strings.ReplaceAll(out, "${"+k+"}", v)
	}
	if left := placeholderPattern.FindString(out); left != "" {
		return "", fmt.Errorf("不支持的启动参数占位符 %s", left)
	}
	return out, nil
}

func hasJVMArg(args []string, prefix string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return true
		}
	}
	return false
}

func hasClassPathArg(args []string) bool {
	for _, a := range args {
		if a == "-cp" || a == "-classpath" || strings.HasPrefix(a, "-Djava.class.path=") {
			return true
		}
	}
	return false
}

func ensureFlag(args []string, flag, value string) []string {
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return args
		}
	}
	return append(args, flag, value)
}

// offlineUUID 与 Java 的 UUID.nameUUIDFromBytes(("OfflinePlayer:"+name)) 一致。
func offlineUUID(name string) string {
	h := md5.Sum([]byte("OfflinePlayer:" + name))
	h[6] = (h[6] & 0x0f) | 0x30
	h[8] = (h[8] & 0x3f) | 0x80
	return hex.EncodeToString(h[:])
}

func formatCommand(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\"") {
			parts[i] = strconv.Quote(a)
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}
