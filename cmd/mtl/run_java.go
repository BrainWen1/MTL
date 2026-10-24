package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mtl/internal/config"
	"mtl/internal/runtime"
)

// Java 运行时来源，仅用于 run 日志区分，不影响启动流程。
const (
	javaSourceCLI     = "命令行 --java"
	javaSourceConfig  = "实例配置"
	javaSourceGlobal  = "全局默认（mtl java use）"
	javaSourceRuntime = "全局缓存"
)

// usableJavaPath 校验配置中的 Java 路径是否仍可用，并返回规范化的绝对路径。
// 只检查文件真实存在且是普通文件；版本兼容性统一交给启动阶段的 findJava。
// 返回 false 表示路径已失效（例如用户手动删除了运行时），调用方应重新自动匹配。
func usableJavaPath(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return abs, true
}

// resolveConfiguredJava 按“命令行 --java > 实例配置 java”解析 Java 路径。
// 返回空 path 表示需要调用 runtime.Ensure 从全局缓存匹配或下载。
// 命令行显式指定的路径即使无效也直接返回，由启动阶段报错，绝不静默回退。
func resolveConfiguredJava(cliJava string, cfg config.Config) (path, source string) {
	if cliJava != "" {
		path = cliJava
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		return path, javaSourceCLI
	}
	if p, ok := usableJavaPath(cfg.Java); ok {
		return p, javaSourceConfig
	}
	return "", ""
}

// resolveGlobalJava 解析 `mtl java use` 设置的全局默认运行时。
// 未设置、运行时缺失或校验失败时返回 ok=false，由调用方继续自动匹配；
// 全局配置损坏只告警，不阻断启动。
func resolveGlobalJava() (javaPath, runtimeID string, ok bool) {
	cfg, err := config.LoadGlobal()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[Java] 警告: %v\n", err)
	}
	runtimeID = strings.TrimSpace(cfg.DefaultJava)
	if runtimeID == "" {
		return "", "", false
	}
	inst, err := runtime.Find("", runtimeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[Java] 警告: 全局默认 Java 运行时不可用（%v），回退自动匹配\n", err)
		return "", "", false
	}
	if !inst.Valid {
		fmt.Fprintf(os.Stderr, "[Java] 警告: 全局默认 Java 运行时 %q 校验失败（%s），回退自动匹配\n",
			inst.ID, inst.Err)
		return "", "", false
	}
	return inst.JavaPath, inst.ID, true
}

// shouldPersistJava 报告本次 Java 来源是否允许在游戏成功结束后回填配置。
// 命令行 --java 只对本次启动生效；全局默认同样不回填，否则实例会固化成
// 当前路径，之后 mtl java use 切换全局默认时无法生效。
func shouldPersistJava(source string) bool {
	return source != javaSourceCLI && source != javaSourceGlobal
}

// persistInstanceJava 在游戏成功运行后把本次使用的 Java 绝对路径回填到实例配置。
// 与配置中已有值完全一致时不写文件（幂等）。写入前重新读取配置，
// 避免覆盖游戏运行期间通过 config 子命令产生的其他修改。
func persistInstanceJava(dir, javaPath string) (changed bool, err error) {
	cfg, err := config.Load(dir)
	if err != nil {
		return false, err
	}
	if cfg.Java == javaPath {
		return false, nil
	}
	cfg.Java = javaPath
	if err := config.Save(dir, cfg); err != nil {
		return false, err
	}
	return true, nil
}
