package forge

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"mtl/internal/downloader"
	"mtl/internal/mrpack"
)

const (
	defaultMavenBase = "https://maven.minecraftforge.net"
	maxSidecarBytes  = 4096
	maxOutputBytes   = 256 << 10
	defaultRetries   = 3
)

// DefaultCacheDir 返回 Forge installer 的全局缓存目录。
// 与 runtime/assets 一样放在 mtl 缓存根目录下，和实例目录隔离。
func DefaultCacheDir() (string, error) {
	if runtime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return "", errors.New("未找到 APPDATA 环境变量，无法确定 Forge 缓存目录")
		}
		return filepath.Join(appData, "mtl", "cache", "forge"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法确定用户主目录: %w", err)
	}
	return filepath.Join(home, ".cache", "mtl", "forge"), nil
}

func (l *Loader) mavenBase() string {
	if b := strings.TrimSpace(l.opts.MavenBaseURL); b != "" {
		return b
	}
	return defaultMavenBase
}

// installerURL 返回 Forge 官方 Maven 的 installer 地址。
func (l *Loader) installerURL(fullVersion string) (string, error) {
	base := strings.TrimRight(l.mavenBase(), "/")
	raw := base + "/net/minecraftforge/forge/" + url.PathEscape(fullVersion) +
		"/forge-" + url.PathEscape(fullVersion) + "-installer.jar"
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("Forge installer 地址非法: %q", raw)
	}
	return raw, nil
}

// ensureInstaller 下载并校验 Forge installer 到全局缓存，返回本地绝对路径。
// 已存在且 sha1 正确的文件由 downloader 直接跳过，满足幂等要求。
func (l *Loader) ensureInstaller(ctx context.Context, fullVersion string) (string, bool, error) {
	rawURL, err := l.installerURL(fullVersion)
	if err != nil {
		return "", false, err
	}

	l.report("拉取 Forge installer 校验信息: %s", fullVersion)
	sidecar, err := l.getBytes(ctx, rawURL+".sha1", maxSidecarBytes)
	if err != nil {
		return "", false, fmt.Errorf("获取 Forge installer sha1 失败: %w", err)
	}
	sum, err := parseSHA1Sidecar(sidecar)
	if err != nil {
		return "", false, fmt.Errorf("Forge installer sha1 无效: %w", err)
	}

	cacheDir := l.opts.CacheDir
	if cacheDir == "" {
		cacheDir, err = DefaultCacheDir()
		if err != nil {
			return "", false, err
		}
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", false, fmt.Errorf("创建 Forge 缓存目录失败: %w", err)
	}

	name := "forge-" + fullVersion + "-installer.jar"
	l.report("下载 Forge installer: %s", fullVersion)
	results, err := downloader.Download(ctx, cacheDir, []mrpack.File{{
		Path:      name,
		Hashes:    mrpack.Hashes{SHA1: sum},
		Downloads: []string{rawURL},
	}}, downloader.Options{
		Concurrency: 1,
		Retries:     l.retries(),
		RetryDelay:  l.opts.RetryDelay,
		Timeout:     l.opts.Timeout,
		HTTPClient:  l.opts.HTTPClient,
		Mirrors:     l.opts.Mirrors,
	})
	if err != nil {
		return "", false, fmt.Errorf("下载 Forge installer 失败: %w", err)
	}
	skipped := len(results) == 1 && results[0].Skipped
	if skipped {
		l.report("Forge installer 已缓存，跳过下载")
	}
	p, err := mrpack.SafeJoin(cacheDir, name)
	if err != nil {
		return "", false, err
	}
	return p, skipped, nil
}

func parseSHA1Sidecar(data []byte) (string, error) {
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "", errors.New("校验文件为空")
	}
	sum := strings.ToLower(fields[0])
	b, err := hex.DecodeString(sum)
	if err != nil || len(b) != 20 {
		return "", errors.New("不是合法的 sha1")
	}
	return sum, nil
}

// runInstallerCommand 以非 GUI 模式执行 installer，并把输出限制在固定上限内。
func runInstallerCommand(ctx context.Context, javaPath, installerPath, instanceDir string) (string, error) {
	buf := &cappedBuffer{limit: maxOutputBytes}
	cmd := exec.CommandContext(ctx, javaPath, "-jar", installerPath, "--installClient", instanceDir)
	cmd.Dir = instanceDir
	cmd.Stdout = buf
	cmd.Stderr = buf
	err := cmd.Run()
	return buf.String(), err
}

func (l *Loader) runInstaller(ctx context.Context, javaPath, installerPath, instanceDir string) (string, error) {
	if l.installerRunner != nil {
		return l.installerRunner(ctx, javaPath, installerPath, instanceDir)
	}
	return runInstallerCommand(ctx, javaPath, installerPath, instanceDir)
}

// cappedBuffer 只保留最后 limit 字节，避免 installer 输出占满内存。
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n >= b.limit {
		b.buf.Reset()
		b.buf.Write(p[n-b.limit:])
		return n, nil
	}
	if b.buf.Len()+n > b.limit {
		drop := b.buf.Len() + n - b.limit
		b.buf.Next(drop)
	}
	b.buf.Write(p)
	return n, nil
}

func (b *cappedBuffer) String() string { return b.buf.String() }

// outputTail 把 installer 输出整理成可读的错误补充信息。
func outputTail(output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	const keep = 2000
	if len(output) > keep {
		output = "…" + output[len(output)-keep:]
	}
	return "\ninstaller 输出（末尾）:\n" + output
}

// getBytes 拉取无独立清单的元数据（如 .sha1 sidecar），复用公共下载层的
// 镜像回退与重试。
func (l *Loader) getBytes(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("非法下载地址 %q", rawURL)
	}
	return downloader.GetBytes(ctx, rawURL, limit, downloader.RequestOptions{
		HTTPClient: l.httpClient(),
		Retries:    l.retries(),
		RetryDelay: l.opts.RetryDelay,
		Timeout:    l.opts.Timeout,
		Mirrors:    l.opts.Mirrors,
	})
}

func (l *Loader) httpClient() *http.Client {
	if l.opts.HTTPClient != nil {
		return l.opts.HTTPClient
	}
	return http.DefaultClient
}

func (l *Loader) retries() int {
	switch {
	case l.opts.Retries == 0:
		return defaultRetries
	case l.opts.Retries < 0:
		return 0
	default:
		return l.opts.Retries
	}
}
