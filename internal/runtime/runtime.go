// Package runtime 按 Mojang 官方 runtime 元信息下载 Java 运行时到 mtl 独立缓存。
// 运行时只通过完整绝对路径交给 mtl 启动的子进程，不写入 PATH、不修改系统环境。
package runtime

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"mtl/internal/downloader"
	"mtl/internal/mrpack"
)

const (
	defaultMetaURL = "https://launchermeta.mojang.com/v1/products/java-runtime/2ec0cc96c44e5a76b9c8b7c39df7210883d12871/all.json"
	maxMetaBytes   = 512 << 10
	maxManifest    = 8 << 20
	markerName     = ".mtl-runtime.json"

	defaultConcurrency = 4
	defaultRetries     = 3
	defaultRetryDelay  = 500 * time.Millisecond
	maxRetryDelay      = 30 * time.Second
)

// Spec 描述版本 json 里的 Java 需求。
type Spec struct {
	// Component 是 javaVersion.component，例如 java-runtime-gamma。
	Component string
	// MajorVersion 仅用于 Component 缺失时推断官方 runtime。
	MajorVersion int
}

type Options struct {
	// Dir 为空时使用 DefaultDir；预留手动指定 runtime 缓存目录。
	Dir string
	// TargetOS / TargetArch 采用 Go 命名（darwin 表示 macOS），空值使用本机平台。
	TargetOS   string
	TargetArch string
	// MetaURL 为空时使用 Mojang 官方 all.json（测试可注入）。
	MetaURL string
	// Concurrency 是同时下载的文件数，<=0 时默认 4。
	Concurrency int
	// Retries 是失败后的额外重试次数，0 时默认 3；负数表示不重试。
	Retries int
	// RetryDelay 是首次重试前的等待时间，按指数退避，<=0 时默认 500ms。
	RetryDelay time.Duration
	// Timeout 是单文件下载超时；<=0 表示不限制。
	Timeout time.Duration
	// HTTPClient 为空时使用 http.DefaultClient。
	HTTPClient *http.Client
	// Mirrors 是镜像源配置；空值使用 Mojang 官方地址。
	Mirrors downloader.Mirrors
	// Progress 接收进度消息（不含 "[Runtime]" 前缀），调用已串行化。
	Progress func(string)
}

type options struct {
	dir         string
	targetOS    string
	targetArch  string
	metaURL     string
	concurrency int
	retries     int
	retryDelay  time.Duration
	timeout     time.Duration
	httpClient  *http.Client
	mirrors     downloader.Mirrors
	progress    func(string)
}

func (o Options) normalize() options {
	n := options{
		dir:         o.Dir,
		targetOS:    o.TargetOS,
		targetArch:  o.TargetArch,
		metaURL:     o.MetaURL,
		concurrency: o.Concurrency,
		retries:     o.Retries,
		retryDelay:  o.RetryDelay,
		timeout:     o.Timeout,
		httpClient:  o.HTTPClient,
		mirrors:     o.Mirrors,
		progress:    o.Progress,
	}
	if n.targetOS == "" {
		n.targetOS = goruntime.GOOS
	}
	if n.targetArch == "" {
		n.targetArch = goruntime.GOARCH
	}
	if n.metaURL == "" {
		n.metaURL = defaultMetaURL
	}
	if n.concurrency <= 0 {
		n.concurrency = defaultConcurrency
	}
	switch {
	case n.retries == 0:
		n.retries = defaultRetries
	case n.retries < 0:
		n.retries = 0
	}
	if n.retryDelay <= 0 {
		n.retryDelay = defaultRetryDelay
	}
	if n.httpClient == nil {
		n.httpClient = http.DefaultClient
	}
	return n
}

// DefaultDir 返回平台相关的 Java 运行时缓存目录。
func DefaultDir() (string, error) {
	if goruntime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return "", errors.New("未找到 APPDATA 环境变量，无法确定 runtime 缓存目录")
		}
		return filepath.Join(appData, "mtl", "cache", "runtime"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法确定用户主目录: %w", err)
	}
	return filepath.Join(home, ".cache", "mtl", "runtime"), nil
}

// Ensure 确保指定 Mojang 官方 Java 运行时可用，返回 java 可执行文件绝对路径。
// 已存在且与当前官方清单一致、全部文件 sha1 校验通过时直接复用。
func Ensure(ctx context.Context, spec Spec, opts Options) (string, error) {
	o := opts.normalize()

	var logMu sync.Mutex
	logf := func(format string, args ...any) {
		if o.progress == nil {
			return
		}
		msg := fmt.Sprintf(format, args...)
		logMu.Lock()
		defer logMu.Unlock()
		o.progress(msg)
	}

	component := strings.TrimSpace(spec.Component)
	if component == "" {
		c, ok := componentForMajor(spec.MajorVersion)
		if !ok {
			return "", fmt.Errorf("版本 json 未提供可用的 Java 运行时要求（component %q，major %d），请使用 --java 指定",
				spec.Component, spec.MajorVersion)
		}
		component = c
	}
	if err := validateComponent(component); err != nil {
		return "", err
	}
	platform, err := platformKey(o.targetOS, o.targetArch)
	if err != nil {
		return "", err
	}

	dir := o.dir
	if dir == "" {
		dir, err = DefaultDir()
		if err != nil {
			return "", err
		}
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("解析 runtime 缓存目录失败: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", friendly("创建 runtime 缓存目录失败", err)
	}
	finalDir, err := mrpack.SafeJoin(dir, component)
	if err != nil {
		return "", fmt.Errorf("运行时目录非法: %w", err)
	}

	logf("正在检查 Java 运行时：%s（%s）", component, platform)

	// 本地标记只有在 component/平台匹配、且 java 可执行文件 sha1 正确时才可信。
	markerOK := false
	var local marker
	var localJava string
	if m, ok := readMarker(finalDir, component, platform); ok {
		if p, err := mrpack.SafeJoin(finalDir, m.Java); err == nil && validJavaFile(p, m.JavaSHA1) {
			markerOK = true
			local = m
			localJava = p
		}
	}

	entry, err := o.fetchEntry(ctx, platform, component)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if markerOK {
			logf("无法获取 Java 运行时元信息，使用本地缓存：%s", component)
			return localJava, nil
		}
		return "", err
	}

	art := entry.Manifest
	if !validSHA1(art.SHA1) {
		return "", fmt.Errorf("官方运行时清单 %q 的 sha1 非法", component)
	}
	manifestRel := "manifests/" + component + "-" + strings.ToLower(art.SHA1) + ".json"
	m, err := o.loadManifest(ctx, dir, manifestRel, art)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if markerOK {
			logf("无法获取 Java 运行时清单，使用本地缓存：%s", component)
			return localJava, nil
		}
		return "", err
	}
	pl, err := buildPlan(m, platform)
	if err != nil {
		return "", fmt.Errorf("Java 运行时清单非法: %w", err)
	}

	if markerOK && strings.EqualFold(local.ManifestSHA1, art.SHA1) {
		if err := pl.verify(finalDir); err == nil {
			logf("Java 运行时校验通过，跳过下载：%s", component)
			return localJava, nil
		}
		logf("本地 Java 运行时文件不完整，重新安装：%s", component)
	}

	javaPath, err := pl.install(ctx, dir, component, platform, finalDir, art.SHA1, entry.Version.Name, o, logf)
	if err != nil {
		return "", err
	}
	logf("Java 运行时安装完成：%s（%s）", component, entry.Version.Name)
	return javaPath, nil
}

// componentForMajor 在版本 json 未提供 component 时按 Java 主版本推断官方组件。
func componentForMajor(major int) (string, bool) {
	switch major {
	case 8:
		return "jre-legacy", true
	case 16:
		return "java-runtime-alpha", true
	case 17:
		return "java-runtime-gamma", true
	case 21:
		return "java-runtime-delta", true
	case 25:
		return "java-runtime-epsilon", true
	default:
		return "", false
	}
}

func validateComponent(component string) error {
	if err := mrpack.ValidateRelPath(component); err != nil {
		return fmt.Errorf("Java 运行时组件 %q 非法: %w", component, err)
	}
	if strings.ContainsAny(component, `/\`) {
		return fmt.Errorf("Java 运行时组件 %q 非法", component)
	}
	return nil
}

// platformKey 把 GOOS/GOARCH 映射到 Mojang all.json 的平台键。
func platformKey(goos, goarch string) (string, error) {
	switch goos {
	case "windows":
		switch goarch {
		case "amd64":
			return "windows-x64", nil
		case "386":
			return "windows-x86", nil
		case "arm64":
			return "windows-arm64", nil
		}
	case "darwin":
		switch goarch {
		case "amd64":
			return "mac-os", nil
		case "arm64":
			return "mac-os-arm64", nil
		}
	case "linux":
		switch goarch {
		case "amd64":
			return "linux", nil
		case "386":
			return "linux-i386", nil
		}
	}
	return "", fmt.Errorf("当前系统（%s-%s）没有可用的官方 Java 运行时", goos, goarch)
}

type metaIndex map[string]map[string][]metaEntry

type metaEntry struct {
	Availability struct {
		Progress int `json:"progress"`
	} `json:"availability"`
	Manifest metaArtifact `json:"manifest"`
	Version  struct {
		Name string `json:"name"`
	} `json:"version"`
}

type metaArtifact struct {
	SHA1 string `json:"sha1"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

func (o options) fetchEntry(ctx context.Context, platform, component string) (metaEntry, error) {
	raw, err := o.getBytes(ctx, o.metaURL, maxMetaBytes)
	if err != nil {
		return metaEntry{}, friendly("下载 Java 运行时元信息失败", err)
	}
	var idx metaIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		return metaEntry{}, fmt.Errorf("解析 Java 运行时元信息失败: %w", err)
	}
	entries := idx[platform][component]
	if len(entries) == 0 {
		return metaEntry{}, fmt.Errorf("当前系统（%s）没有可用的官方 Java 运行时组件 %s", platform, component)
	}
	for _, e := range entries {
		if e.Availability.Progress == 100 {
			return e, nil
		}
	}
	return metaEntry{}, fmt.Errorf("官方 Java 运行时组件 %s 尚未完整提供（%s）", component, platform)
}

type manifest struct {
	Files map[string]manifestEntry `json:"files"`
}

type manifestEntry struct {
	Type       string                      `json:"type"`
	Executable bool                        `json:"executable"`
	Target     string                      `json:"target"`
	Downloads  map[string]manifestDownload `json:"downloads"`
}

type manifestDownload struct {
	SHA1 string `json:"sha1"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

type marker struct {
	Component    string `json:"component"`
	Platform     string `json:"platform"`
	ManifestSHA1 string `json:"manifestSha1"`
	Version      string `json:"version"`
	Java         string `json:"java"`
	JavaSHA1     string `json:"javaSha1"`
}

// loadManifest 下载并校验官方运行时清单；已存在且 sha1 正确时由 downloader 跳过。
func (o options) loadManifest(ctx context.Context, dir, rel string, art metaArtifact) (*manifest, error) {
	if art.Size <= 0 || art.Size > maxManifest {
		return nil, fmt.Errorf("运行时清单大小 %d 非法", art.Size)
	}
	if err := validateHTTPURL(art.URL); err != nil {
		return nil, fmt.Errorf("运行时清单地址非法: %w", err)
	}
	_, err := downloader.Download(ctx, dir, []mrpack.File{{
		Path:      rel,
		Hashes:    mrpack.Hashes{SHA1: strings.ToLower(art.SHA1)},
		Downloads: []string{art.URL},
		FileSize:  art.Size,
	}}, downloader.Options{
		Concurrency: 1,
		Retries:     o.retries,
		RetryDelay:  o.retryDelay,
		Timeout:     o.timeout,
		HTTPClient:  o.httpClient,
		Mirrors:     o.mirrors,
	})
	if err != nil {
		return nil, friendly("下载 Java 运行时清单失败", err)
	}

	p, err := mrpack.SafeJoin(dir, rel)
	if err != nil {
		return nil, err
	}
	data, err := readLimited(p, maxManifest)
	if err != nil {
		return nil, friendly("读取 Java 运行时清单失败", err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("解析 Java 运行时清单失败: %w", err)
	}
	return &m, nil
}

type linkEntry struct {
	rel      string
	target   string
	resolved string
}

type plan struct {
	files     []mrpack.File
	dirs      []string
	links     []linkEntry
	execFiles []string
	javaRel   string
	javaSHA1  string
}

// buildPlan 把官方清单转成下载/创建计划，所有路径在这里完成逃逸校验。
func buildPlan(m *manifest, platform string) (*plan, error) {
	if m == nil || len(m.Files) == 0 {
		return nil, errors.New("清单中没有文件")
	}
	keys := make([]string, 0, len(m.Files))
	for k := range m.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	p := &plan{}
	var javaCandidates []string
	for _, rel := range keys {
		if err := mrpack.ValidateRelPath(rel); err != nil {
			return nil, fmt.Errorf("文件路径 %q 非法: %w", rel, err)
		}
		if rel == markerName {
			return nil, fmt.Errorf("清单文件路径 %q 与内部标记冲突", rel)
		}
		e := m.Files[rel]
		switch e.Type {
		case "directory":
			p.dirs = append(p.dirs, rel)
		case "file":
			raw, ok := e.Downloads["raw"]
			if !ok || raw.URL == "" || raw.SHA1 == "" {
				return nil, fmt.Errorf("文件 %q 缺少 raw 下载信息", rel)
			}
			if !validSHA1(raw.SHA1) {
				return nil, fmt.Errorf("文件 %q 的 sha1 非法", rel)
			}
			if raw.Size < 0 {
				return nil, fmt.Errorf("文件 %q 的 size 非法", rel)
			}
			if err := validateHTTPURL(raw.URL); err != nil {
				return nil, fmt.Errorf("文件 %q: %w", rel, err)
			}
			p.files = append(p.files, mrpack.File{
				Path:      rel,
				Hashes:    mrpack.Hashes{SHA1: strings.ToLower(raw.SHA1)},
				Downloads: []string{raw.URL},
				FileSize:  raw.Size,
			})
			if e.Executable {
				p.execFiles = append(p.execFiles, rel)
				if isJavaExecutable(rel, platform) {
					javaCandidates = append(javaCandidates, rel)
				}
			}
		case "link":
			resolved, err := safeLinkTarget(rel, e.Target)
			if err != nil {
				return nil, err
			}
			p.links = append(p.links, linkEntry{rel: rel, target: e.Target, resolved: resolved})
		case "":
			return nil, fmt.Errorf("文件 %q 缺少 type", rel)
		default:
			return nil, fmt.Errorf("文件 %q 的类型 %q 不受支持", rel, e.Type)
		}
	}
	if len(javaCandidates) == 0 {
		return nil, errors.New("官方 Java 运行时清单中未找到 java 可执行文件")
	}
	sort.Strings(javaCandidates)
	p.javaRel = javaCandidates[0]
	p.javaSHA1 = strings.ToLower(m.Files[p.javaRel].Downloads["raw"].SHA1)
	return p, nil
}

func isJavaExecutable(rel, platform string) bool {
	if strings.HasPrefix(platform, "windows-") {
		return path.Base(rel) == "java.exe"
	}
	return path.Base(rel) == "java"
}

// safeLinkTarget 允许指向运行时目录内部，拒绝绝对路径和任何逃逸。
func safeLinkTarget(rel, target string) (string, error) {
	if target == "" {
		return "", fmt.Errorf("链接 %q 缺少 target", rel)
	}
	if strings.ContainsRune(target, 0) || strings.Contains(target, `\`) ||
		strings.HasPrefix(target, "/") || strings.Contains(target, ":") {
		return "", fmt.Errorf("链接 %q 的 target %q 非法", rel, target)
	}
	resolved := path.Clean(path.Join(path.Dir(rel), target))
	if resolved == "." || resolved == ".." || strings.HasPrefix(resolved, "../") {
		return "", fmt.Errorf("链接 %q 的 target %q 逃逸运行时目录", rel, target)
	}
	if err := mrpack.ValidateRelPath(resolved); err != nil {
		return "", fmt.Errorf("链接 %q 的 target %q 非法: %w", rel, target, err)
	}
	return resolved, nil
}

// verify 校验本地运行时与官方清单完全一致。
func (p *plan) verify(root string) error {
	for _, rel := range p.dirs {
		target, err := mrpack.SafeJoin(root, rel)
		if err != nil {
			return err
		}
		info, err := os.Stat(target)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("目录 %q 缺失", rel)
		}
	}
	for _, f := range p.files {
		target, err := mrpack.SafeJoin(root, f.Path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(target)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("文件 %q 缺失", f.Path)
		}
		got, err := fileSHA1(target)
		if err != nil || !strings.EqualFold(got, f.Hashes.SHA1) {
			return fmt.Errorf("文件 %q 的 sha1 不匹配", f.Path)
		}
	}
	for _, l := range p.links {
		target, err := mrpack.SafeJoin(root, l.rel)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(target); err != nil {
			return fmt.Errorf("链接 %q 缺失", l.rel)
		}
	}
	javaPath, err := mrpack.SafeJoin(root, p.javaRel)
	if err != nil {
		return err
	}
	if !validJavaFile(javaPath, p.javaSHA1) {
		return errors.New("java 可执行文件校验失败")
	}
	return nil
}

// install 先在 staging 目录完整下载并校验，最后原子替换正式目录。
// staging 使用固定路径，失败或 Ctrl+C 后再次执行可复用其中的 *.tmp 实现续传。
func (p *plan) install(ctx context.Context, root, component, platform, finalDir, manifestSHA1, version string, o options, logf func(string, ...any)) (string, error) {
	staging := filepath.Join(root, ".staging-"+component+"-"+platform)
	if info, err := os.Lstat(staging); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("Java 运行时临时目录 %q 不是普通目录", staging)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", friendly("检查 Java 运行时临时目录失败", err)
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return "", friendly("创建 Java 运行时临时目录失败", err)
	}
	_ = os.Chmod(staging, 0o755)

	for _, rel := range p.dirs {
		target, err := mrpack.SafeJoin(staging, rel)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			return "", friendly(fmt.Sprintf("创建目录 %q 失败", rel), err)
		}
	}

	logf("开始下载 Java 运行时文件：共 %d 个", len(p.files))
	if len(p.files) > 0 {
		lastPct := -10
		_, err := downloader.Download(ctx, staging, p.files, downloader.Options{
			Concurrency: o.concurrency,
			Retries:     o.retries,
			RetryDelay:  o.retryDelay,
			Timeout:     o.timeout,
			HTTPClient:  o.httpClient,
			Mirrors:     o.mirrors,
			Progress: func(done, total int, _ downloader.Result) {
				pct := done * 100 / total
				if pct >= lastPct+10 || done == total {
					lastPct = pct
					logf("下载 Java 运行时文件：%d/%d（%d%%）", done, total, pct)
				}
			},
			FileProgress: func(filePath string, written, total int64) {
				if total <= 0 {
					return
				}
				logf("下载 Java 运行时文件：%s（%d/%d，%d%%）",
					path.Base(filePath), written, total, written*100/total)
			},
		})
		if err != nil {
			return "", friendly("下载 Java 运行时文件失败", err)
		}
	}

	// downloader 统一写 0644，POSIX 上需要补回官方清单声明的执行位。
	for _, rel := range p.execFiles {
		target, err := mrpack.SafeJoin(staging, rel)
		if err != nil {
			return "", err
		}
		if err := os.Chmod(target, 0o755); err != nil {
			return "", friendly(fmt.Sprintf("设置 %q 执行权限失败", rel), err)
		}
	}

	for _, l := range p.links {
		target, err := mrpack.SafeJoin(staging, l.rel)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", friendly(fmt.Sprintf("创建链接目录 %q 失败", l.rel), err)
		}
		// 固定 staging 可能保留上次失败时创建的链接，先移除再重建。
		if _, statErr := os.Lstat(target); statErr == nil {
			if err := os.Remove(target); err != nil {
				return "", friendly(fmt.Sprintf("清理旧链接 %q 失败", l.rel), err)
			}
		}
		if err := os.Symlink(l.target, target); err != nil {
			// Windows 创建符号链接通常需要额外权限；官方 Windows 清单不含 link，
			// 这里仅作为其他平台清单的兜底。
			src, joinErr := mrpack.SafeJoin(staging, l.resolved)
			if joinErr == nil && copyRegularFile(src, target) == nil {
				continue
			}
			return "", friendly(fmt.Sprintf("创建链接 %q 失败", l.rel), err)
		}
	}

	if err := writeMarker(staging, marker{
		Component:    component,
		Platform:     platform,
		ManifestSHA1: strings.ToLower(manifestSHA1),
		Version:      version,
		Java:         p.javaRel,
		JavaSHA1:     p.javaSHA1,
	}); err != nil {
		return "", friendly("写入 Java 运行时标记失败", err)
	}
	if err := swapDir(staging, finalDir); err != nil {
		return "", err
	}
	_ = os.Chmod(finalDir, 0o755)

	javaPath, err := mrpack.SafeJoin(finalDir, p.javaRel)
	if err != nil {
		return "", err
	}
	if !validJavaFile(javaPath, p.javaSHA1) {
		return "", errors.New("安装后的 Java 运行时校验失败")
	}
	return javaPath, nil
}

func writeMarker(dir string, m marker) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, markerName+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, markerName)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// swapDir 用已完成的 staging 替换正式目录；替换失败时尽量恢复旧目录。
func swapDir(staging, finalDir string) error {
	info, err := os.Lstat(finalDir)
	switch {
	case err == nil:
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("运行时目录 %q 不是普通目录", finalDir)
		}
		backup := finalDir + fmt.Sprintf(".old-%d", time.Now().UnixNano())
		if err := os.Rename(finalDir, backup); err != nil {
			return friendly("替换旧 Java 运行时失败", err)
		}
		if err := os.Rename(staging, finalDir); err != nil {
			_ = os.Rename(backup, finalDir)
			return friendly("安装 Java 运行时失败", err)
		}
		_ = os.RemoveAll(backup)
		return nil
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Rename(staging, finalDir); err != nil {
			return friendly("安装 Java 运行时失败", err)
		}
		return nil
	default:
		return friendly("检查 Java 运行时目录失败", err)
	}
}

func copyRegularFile(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("链接目标 %q 不是普通文件", src)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

func readMarker(dir, component, platform string) (marker, bool) {
	p := filepath.Join(dir, markerName)
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return marker{}, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return marker{}, false
	}
	var m marker
	if err := json.Unmarshal(data, &m); err != nil {
		return marker{}, false
	}
	if m.Component != component || m.Platform != platform ||
		m.ManifestSHA1 == "" || m.Java == "" || !validSHA1(m.JavaSHA1) {
		return marker{}, false
	}
	return m, true
}

func validJavaFile(path, wantSHA1 string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if goruntime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return false
	}
	if wantSHA1 == "" {
		return true
	}
	got, err := fileSHA1(path)
	return err == nil && strings.EqualFold(got, wantSHA1)
}

func fileSHA1(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("文件超过 %d 字节上限", limit)
	}
	return data, nil
}

func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("非法下载地址 %q", raw)
	}
	return nil
}

func validSHA1(h string) bool {
	b, err := hex.DecodeString(h)
	return err == nil && len(b) == sha1.Size
}

func (o options) getBytes(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	if err := validateHTTPURL(rawURL); err != nil {
		return nil, err
	}
	// RequestOptions.Retries 使用精确语义：o.retries 已完成归一化。
	return downloader.GetBytes(ctx, rawURL, limit, downloader.RequestOptions{
		HTTPClient: o.httpClient,
		Retries:    o.retries,
		RetryDelay: o.retryDelay,
		Timeout:    o.timeout,
		Mirrors:    o.mirrors,
	})
}

// friendly 补充权限/磁盘空间的可读提示。
func friendly(action string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%s：权限不足，请检查 runtime 缓存目录写入权限（%w）", action, err)
	}
	msg := strings.ToLower(err.Error())
	if errors.Is(err, syscall.ENOSPC) ||
		strings.Contains(msg, "no space left") ||
		strings.Contains(msg, "not enough space") ||
		strings.Contains(msg, "disk full") {
		return fmt.Errorf("%s：磁盘空间不足（%w）", action, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}
