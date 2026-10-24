// Package config 顶层全局配置（%APPDATA%/mtl/config.json、~/.config/mtl/config.json）。
// 与实例配置分离：镜像源、下载重试/超时/日志模式对所有实例生效。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"mtl/internal/logging"
)

const (
	// GlobalFileName 是全局配置文件名。
	GlobalFileName = "config.json"
	// EnvGlobalConfig 可显式指定全局配置文件路径（测试与高级用法）。
	EnvGlobalConfig = "MTL_CONFIG_PATH"

	// LogModeSummary 只输出批次进度与失败清单（默认）。
	LogModeSummary = "summary"
	// LogModeVerbose 逐个文件输出下载进度。
	LogModeVerbose = "verbose"
)

// Global 对应全局配置文件结构。字段名使用 snake_case，便于直接编辑。
type Global struct {
	Mirrors  GlobalMirrors  `json:"mirrors"`
	Download GlobalDownload `json:"download"`
	// DefaultJava 是 mtl java use 设置的全局默认 Java 运行时标识（runtime 缓存目录名）。
	// 仅在命令行未传 --java 且实例配置未指定 java 时生效；不保存可执行文件绝对路径。
	DefaultJava string `json:"default_java,omitempty"`
	// LogLevel 是终端日志级别：debug / info / warn / error，默认 info。
	// 实例日志文件始终记录 debug 及以上，不受该选项影响。
	LogLevel string `json:"log_level,omitempty"`
}

// GlobalMirrors 是各类下载源的镜像配置；空字符串表示使用官方源。
type GlobalMirrors struct {
	// Mojang 是 Mojang 资源根镜像，作用于 assets、libraries 与版本元信息。
	Mojang string `json:"mojang,omitempty"`
	// Assets / Libraries / VersionMeta 可单独覆盖 Mojang 根镜像。
	Assets      string `json:"assets,omitempty"`
	Libraries   string `json:"libraries,omitempty"`
	VersionMeta string `json:"version_meta,omitempty"`
	// Mod 是 mod 下载源前缀（Modrinth 等非 Mojang 域名）。
	Mod string `json:"mod,omitempty"`
	// Fabric / Forge 分别是 Fabric meta/maven 与 Forge maven 镜像。
	Fabric string `json:"fabric,omitempty"`
	Forge  string `json:"forge,omitempty"`
}

// GlobalDownload 是下载行为配置。
type GlobalDownload struct {
	// LogMode 为 summary（默认）或 verbose；--debug 会强制 verbose。
	LogMode string `json:"log_mode,omitempty"`
	// Retries 是失败后的额外重试次数：0 使用默认 3，-1 表示不重试。
	Retries int `json:"retries,omitempty"`
	// Timeout 是单文件下载超时，例如 30s、2m；空字符串表示不限制。
	Timeout string `json:"timeout,omitempty"`
	// Concurrency 是并发下载数，0 使用默认值。
	Concurrency int `json:"concurrency,omitempty"`
}

// DefaultGlobal 返回全局配置默认值。
func DefaultGlobal() Global {
	return Global{
		Download: GlobalDownload{LogMode: LogModeSummary},
		LogLevel: logging.LevelInfo.Name(),
	}
}

// GlobalPath 返回全局配置文件路径。
// 优先使用 MTL_CONFIG_PATH；Windows 使用 %APPDATA%/mtl/config.json；
// 其他平台使用 os.UserConfigDir()/mtl/config.json（Linux 通常为 ~/.config/mtl）。
func GlobalPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv(EnvGlobalConfig)); p != "" {
		return p, nil
	}
	if runtime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return "", errors.New("未找到 APPDATA 环境变量，无法确定全局配置目录")
		}
		return filepath.Join(appData, "mtl", GlobalFileName), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("无法确定用户配置目录: %w", err)
	}
	return filepath.Join(dir, "mtl", GlobalFileName), nil
}

// LoadGlobal 读取全局配置。文件不存在时返回默认值且不报错；
// 文件损坏或字段非法时返回可用的规范化结果和错误，调用方应告警后继续。
func LoadGlobal() (Global, error) {
	def := DefaultGlobal()
	path, err := GlobalPath()
	if err != nil {
		return def, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return def, nil
	}
	if err != nil {
		return def, fmt.Errorf("读取全局配置 %s 失败: %w", path, err)
	}
	var cfg Global
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return def, fmt.Errorf("全局配置 %s 损坏: %w", path, err)
	}
	norm, normErr := NormalizeGlobal(cfg)
	if normErr != nil {
		return norm, fmt.Errorf("全局配置 %s 无效: %w", path, normErr)
	}
	return norm, nil
}

// SaveGlobal 校验并原子写入全局配置，目录不存在时自动创建。
func SaveGlobal(cfg Global) error {
	cfg, err := NormalizeGlobal(cfg)
	if err != nil {
		return err
	}
	path, err := GlobalPath()
	if err != nil {
		return err
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("创建全局配置目录失败: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("编码全局配置失败: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(parent, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("创建全局配置临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("设置全局配置权限失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入全局配置失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭全局配置失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("保存全局配置失败: %w", err)
	}
	return nil
}

// NormalizeGlobal 规范化镜像地址与下载参数，并返回所有非法字段的错误。
// 即使返回错误，cfg 中合法字段仍会被保留，调用方可以继续使用。
func NormalizeGlobal(cfg Global) (Global, error) {
	var problems []string
	normalize := func(name string, v *string) {
		n, err := NormalizeMirror(*v)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", name, err))
			*v = ""
			return
		}
		*v = n
	}
	normalize("mirrors.mojang", &cfg.Mirrors.Mojang)
	normalize("mirrors.assets", &cfg.Mirrors.Assets)
	normalize("mirrors.libraries", &cfg.Mirrors.Libraries)
	normalize("mirrors.version_meta", &cfg.Mirrors.VersionMeta)
	normalize("mirrors.mod", &cfg.Mirrors.Mod)
	normalize("mirrors.fabric", &cfg.Mirrors.Fabric)
	normalize("mirrors.forge", &cfg.Mirrors.Forge)

	cfg.Download.LogMode = strings.ToLower(strings.TrimSpace(cfg.Download.LogMode))
	switch cfg.Download.LogMode {
	case "":
		cfg.Download.LogMode = LogModeSummary
	case LogModeSummary, LogModeVerbose:
	default:
		problems = append(problems, fmt.Sprintf("download.log_mode %q 无效（可用: summary、verbose）", cfg.Download.LogMode))
		cfg.Download.LogMode = LogModeSummary
	}
	if cfg.Download.Retries < -1 {
		problems = append(problems, fmt.Sprintf("download.retries %d 无效（-1 表示不重试，0 表示默认）", cfg.Download.Retries))
		cfg.Download.Retries = 0
	}
	if cfg.Download.Concurrency < 0 {
		problems = append(problems, fmt.Sprintf("download.concurrency %d 不能为负数", cfg.Download.Concurrency))
		cfg.Download.Concurrency = 0
	}
	cfg.Download.Timeout = strings.TrimSpace(cfg.Download.Timeout)
	if cfg.Download.Timeout != "" {
		d, err := time.ParseDuration(cfg.Download.Timeout)
		if err != nil || d < 0 {
			problems = append(problems, fmt.Sprintf("download.timeout %q 无效（例如 30s、2m）", cfg.Download.Timeout))
			cfg.Download.Timeout = ""
		}
	}
	cfg.DefaultJava = strings.TrimSpace(cfg.DefaultJava)
	if err := ValidateDefaultJava(cfg.DefaultJava); err != nil {
		problems = append(problems, err.Error())
		cfg.DefaultJava = ""
	}
	level, levelErr := logging.ParseLevel(cfg.LogLevel)
	if levelErr != nil {
		problems = append(problems, levelErr.Error())
		level = logging.LevelInfo
	}
	cfg.LogLevel = level.Name()
	if len(problems) > 0 {
		return cfg, errors.New(strings.Join(problems, "；"))
	}
	return cfg, nil
}

// ValidateDefaultJava 校验全局默认 Java 运行时标识：空字符串表示未设置；
// 非空时只允许单层目录名，避免被误用为路径或包含跨平台非法字符。
func ValidateDefaultJava(id string) error {
	if id == "" {
		return nil
	}
	if id == "." || id == ".." {
		return fmt.Errorf("default_java %q 非法", id)
	}
	if strings.HasSuffix(id, ".") || strings.HasSuffix(id, " ") {
		return fmt.Errorf("default_java %q 非法：不能以点或空格结尾", id)
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("default_java %q 非法：包含控制字符", id)
		}
		if strings.ContainsRune(`/\:*?"<>|`, r) {
			return fmt.Errorf("default_java %q 非法：包含路径保留字符 %q", id, r)
		}
	}
	return nil
}

// NormalizeMirror 校验并规范化镜像地址；空字符串表示不启用镜像。
func NormalizeMirror(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("镜像地址非法 %q: %w", raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("镜像地址必须是以 http:// 或 https:// 开头的完整地址: %q", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("镜像地址不能包含查询参数或锚点: %q", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

// GlobalFromEnv 应用环境变量覆盖，返回新的全局配置。
// 支持 MTL_MOJANG_MIRROR / MTL_ASSETS_MIRROR / MTL_LIBRARIES_MIRROR /
// MTL_VERSION_META_MIRROR / MTL_MOD_MIRROR / MTL_FABRIC_MIRROR / MTL_FORGE_MIRROR /
// MTL_DOWNLOAD_LOG / MTL_DOWNLOAD_RETRIES / MTL_DOWNLOAD_TIMEOUT / MTL_DOWNLOAD_CONCURRENCY /
// MTL_LOG_LEVEL。
func GlobalFromEnv(cfg Global) Global {
	set := func(env string, dst *string) {
		if v, ok := os.LookupEnv(env); ok {
			*dst = strings.TrimSpace(v)
		}
	}
	set("MTL_MOJANG_MIRROR", &cfg.Mirrors.Mojang)
	set("MTL_ASSETS_MIRROR", &cfg.Mirrors.Assets)
	set("MTL_LIBRARIES_MIRROR", &cfg.Mirrors.Libraries)
	set("MTL_VERSION_META_MIRROR", &cfg.Mirrors.VersionMeta)
	set("MTL_MOD_MIRROR", &cfg.Mirrors.Mod)
	set("MTL_FABRIC_MIRROR", &cfg.Mirrors.Fabric)
	set("MTL_FORGE_MIRROR", &cfg.Mirrors.Forge)
	set("MTL_DOWNLOAD_LOG", &cfg.Download.LogMode)
	set("MTL_DOWNLOAD_TIMEOUT", &cfg.Download.Timeout)
	// 空日志级别没有意义，留空时保留全局配置中的值。
	if v := strings.TrimSpace(os.Getenv("MTL_LOG_LEVEL")); v != "" {
		cfg.LogLevel = v
	}
	if v := strings.TrimSpace(os.Getenv("MTL_DOWNLOAD_RETRIES")); v != "" {
		if n, err := parseInt(v); err == nil {
			cfg.Download.Retries = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("MTL_DOWNLOAD_CONCURRENCY")); v != "" {
		if n, err := parseInt(v); err == nil {
			cfg.Download.Concurrency = n
		}
	}
	return cfg
}

func parseInt(v string) (int, error) {
	var n int
	_, err := fmt.Sscanf(v, "%d", &n)
	return n, err
}

// FormatGlobal 返回便于打印的缩进 JSON。
func FormatGlobal(cfg Global) string {
	if norm, err := NormalizeGlobal(cfg); err == nil {
		cfg = norm
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}
