package main

import (
	"fmt"
	"os"
	"strings"

	"mtl/internal/config"
	"mtl/internal/logging"
)

// resolveRunLogLevel 解析 run 的终端日志级别。
// 优先级：--log > --debug > 环境变量 MTL_LOG_LEVEL > 全局配置 log_level > info。
// 日志文件始终由 logging 包记录 debug 及以上，不受该返回值影响。
func resolveRunLogLevel(cliValue string, debug bool) (logging.Level, error) {
	if v := strings.TrimSpace(cliValue); v != "" {
		level, err := logging.ParseLevel(v)
		if err != nil {
			return logging.LevelInfo, fmt.Errorf("--log %q 无效（可用: debug、info、warn、error）", cliValue)
		}
		return level, nil
	}
	if debug {
		return logging.LevelDebug, nil
	}

	cfg, err := config.LoadGlobal()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[Log] 警告: %v；使用默认 info\n", err)
	}
	cfg = config.GlobalFromEnv(cfg)
	level, err := logging.ParseLevel(cfg.LogLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[Log] 警告: %v；使用默认 info\n", err)
		return logging.LevelInfo, nil
	}
	return level, nil
}
