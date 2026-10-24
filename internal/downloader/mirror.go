package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Mirrors 是下载镜像源配置，空字段表示使用官方原始地址。
// 优先级由调用方决定（命令行 > 环境变量 > 全局配置 > 官方源）。
type Mirrors struct {
	// Mojang 是 Mojang 资源根镜像，统一作用于版本元信息、libraries 与 assets。
	Mojang string
	// Assets 仅覆盖 assets 对象下载；为空时回落到 Mojang。
	Assets string
	// Libraries 仅覆盖 Mojang libraries 下载；为空时回落到 Mojang。
	Libraries string
	// VersionMeta 仅覆盖版本清单、版本 json、assetIndex 等元信息；为空时回落到 Mojang。
	VersionMeta string
	// Mod 是 mod 下载源前缀，作用于 Modrinth 等非 Mojang/Fabric/Forge 域名。
	Mod string
	// Fabric 是 Fabric meta / maven 镜像。
	Fabric string
	// Forge 是 Forge maven 镜像。
	Forge string
}

func (m Mirrors) normalized() Mirrors {
	m.Mojang = normalizeMirror(m.Mojang)
	m.Assets = normalizeMirror(m.Assets)
	m.Libraries = normalizeMirror(m.Libraries)
	m.VersionMeta = normalizeMirror(m.VersionMeta)
	m.Mod = normalizeMirror(m.Mod)
	m.Fabric = normalizeMirror(m.Fabric)
	m.Forge = normalizeMirror(m.Forge)
	return m
}

// Candidates 返回某个原始地址的候选下载顺序：镜像地址在前，原始地址在后。
// 未配置对应镜像或镜像与原始地址相同时，只返回原始地址。
func (m Mirrors) Candidates(raw string) []string {
	mirror := m.mirrorFor(raw)
	if mirror == "" {
		return []string{raw}
	}
	rewritten, err := RewriteURL(raw, mirror)
	if err != nil || rewritten == raw {
		return []string{raw}
	}
	return []string{rewritten, raw}
}

// mirrorFor 按域名选择镜像：Mojang 的三类资源可分别覆盖，其余未知域名视为 mod 源。
func (m Mirrors) mirrorFor(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	switch strings.ToLower(u.Hostname()) {
	case "launchermeta.mojang.com", "piston-meta.mojang.com", "piston-data.mojang.com":
		return firstNonEmpty(m.VersionMeta, m.Mojang)
	case "resources.download.minecraft.net":
		return firstNonEmpty(m.Assets, m.Mojang)
	case "libraries.minecraft.net":
		return firstNonEmpty(m.Libraries, m.Mojang)
	case "meta.fabricmc.net", "maven.fabricmc.net":
		return m.Fabric
	case "maven.minecraftforge.net", "files.minecraftforge.net":
		return m.Forge
	default:
		return m.Mod
	}
}

// RewriteURL 只替换域名（以及镜像自带的前缀路径），保留原始资源路径与查询参数。
// 例：https://cdn.example.com/a/b.jar + https://mirror.example/mc
//
//	-> https://mirror.example/mc/a/b.jar
func RewriteURL(raw, mirror string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("原始下载地址非法 %q: %w", raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("原始下载地址非法 %q", raw)
	}
	m, err := url.Parse(mirror)
	if err != nil {
		return "", fmt.Errorf("镜像地址非法 %q: %w", mirror, err)
	}
	if (m.Scheme != "http" && m.Scheme != "https") || m.Host == "" {
		return "", fmt.Errorf("镜像地址非法 %q", mirror)
	}

	u.Scheme = m.Scheme
	u.Host = m.Host
	u.User = nil
	u.Fragment = ""
	if prefix := strings.TrimRight(m.Path, "/"); prefix != "" {
		u.Path = prefix + "/" + strings.TrimLeft(u.Path, "/")
		u.RawPath = ""
	}
	return u.String(), nil
}

// NormalizeMirror 校验并规范化镜像地址：去除首尾空白与末尾斜杠。
// 空字符串合法，表示不启用镜像。
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

func normalizeMirror(raw string) string {
	v, err := NormalizeMirror(raw)
	if err != nil {
		return ""
	}
	return v
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// RequestOptions 是 GetBytes 的配置。与 Options 不同，Retries 使用精确语义：
// 0 表示不重试，调用方应传入已归一化的重试次数。
type RequestOptions struct {
	HTTPClient *http.Client
	Retries    int
	RetryDelay time.Duration
	Timeout    time.Duration
	Mirrors    Mirrors
}

// GetBytes 下载有大小上限的小文件（版本清单、sidecar 哈希等），支持镜像回退与重试。
func GetBytes(ctx context.Context, rawURL string, limit int64, opts RequestOptions) ([]byte, error) {
	if err := validateHTTPURL(rawURL); err != nil {
		return nil, err
	}
	client := opts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	delay := opts.RetryDelay
	if delay <= 0 {
		delay = defaultRetryDelay
	}
	mirrors := opts.Mirrors.normalized()
	candidates := mirrors.Candidates(rawURL)
	attempts := opts.Retries + 1
	if attempts < len(candidates) {
		attempts = len(candidates)
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, retryDelay(delay, attempt-1)); err != nil {
				return nil, err
			}
		}
		raw := candidates[attempt%len(candidates)]
		data, err := getBytesOnce(ctx, client, raw, limit, opts.Timeout)
		if err == nil {
			return data, nil
		}
		lastErr = fmt.Errorf("%s: %w", raw, err)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("请求失败（共尝试 %d 次）: %w", attempts, lastErr)
}

func getBytesOnce(ctx context.Context, client *http.Client, rawURL string, limit int64, timeout time.Duration) ([]byte, error) {
	attemptCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		attemptCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp)
	}
	if resp.ContentLength > limit {
		return nil, fmt.Errorf("响应超过上限 %d 字节", limit)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("响应超过上限 %d 字节", limit)
	}
	return data, nil
}
