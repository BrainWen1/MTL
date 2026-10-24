// Package fabric 安装指定 Minecraft + Fabric Loader 版本的原生版本文件：
// Fabric profile/libraries 来自 Fabric 官方 API，vanilla 版本 json/client jar/libraries
// 来自 Mojang 官方 meta。只负责补齐版本文件，不生成启动命令。
package fabric

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"time"

	"mtl/internal/mrpack"
)

const (
	defaultMetaBase        = "https://meta.fabricmc.net"
	defaultMojangManifest  = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"
	maxVersionSegmentBytes = 128
)

var mavenPartPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)

type Options struct {
	// MetaBaseURL 是 Fabric meta 根地址，空值使用官方地址（测试可注入）。
	MetaBaseURL string
	// MojangManifestURL 是 Mojang 版本清单地址，空值使用官方地址。
	MojangManifestURL string
	// Concurrency 是同时下载的文件数，<=0 时默认 4。
	Concurrency int
	// Retries 是失败后的额外重试次数，0 时默认 3；负数表示不重试。
	Retries int
	// RetryDelay 是首次重试前的等待时间，按指数退避，<=0 时默认 500ms。
	RetryDelay time.Duration
	// HTTPClient 为空时使用 http.DefaultClient，调用方应通过 ctx 控制超时。
	HTTPClient *http.Client
	// TargetOS / TargetArch 用于跨平台打包，空值使用本机 runtime.GOOS/GOARCH。
	// 值采用 Go 的 GOOS/GOARCH 命名（darwin 表示 macOS）。
	TargetOS   string
	TargetArch string
	// Progress 接收进度消息（不含 "[Fabric]" 前缀），调用已串行化。
	Progress func(string)
}

type Result struct {
	ProfileID  string // Fabric 版本目录 id
	VersionID  string // vanilla 版本 id
	Files      int    // 计划下载的文件总数
	Downloaded int
	Skipped    int
}

type fabricProfile struct {
	ID           string          `json:"id"`
	InheritsFrom string          `json:"inheritsFrom"`
	Libraries    []fabricLibrary `json:"libraries"`
}

type fabricLibrary struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA512 string `json:"sha512"`
	Size   int64  `json:"size"`
}

func (o Options) normalize() options {
	n := options{
		metaBase:    o.MetaBaseURL,
		manifestURL: o.MojangManifestURL,
		httpClient:  o.HTTPClient,
		concurrency: o.Concurrency,
		retries:     o.Retries,
		retryDelay:  o.RetryDelay,
		targetOS:    o.TargetOS,
		targetArch:  o.TargetArch,
	}
	if n.targetOS == "" {
		n.targetOS = runtime.GOOS
	}
	if n.targetArch == "" {
		n.targetArch = runtime.GOARCH
	}
	if n.metaBase == "" {
		n.metaBase = defaultMetaBase
	}
	if n.manifestURL == "" {
		n.manifestURL = defaultMojangManifest
	}
	if n.httpClient == nil {
		n.httpClient = http.DefaultClient
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
	if o.Progress != nil {
		n.progress = &reporter{fn: o.Progress}
	}
	return n
}

// Install 在 instanceDir 中按 Minecraft 原生布局补齐：
//
//	versions/<fabric-id>/<fabric-id>.json
//	versions/<mc>/<mc>.json
//	versions/<mc>/<mc>.jar
//	libraries/<maven path>
//
// 失败时不会回滚已下载文件，也不会删除实例中的其他内容。
func Install(ctx context.Context, instanceDir, gameVersion, loaderVersion string, opts Options) (Result, error) {
	if instanceDir == "" {
		return Result{}, errors.New("实例目录为空")
	}
	if err := validateVersionSegment("Minecraft 版本", gameVersion); err != nil {
		return Result{}, err
	}
	if err := validateVersionSegment("fabric-loader 版本", loaderVersion); err != nil {
		return Result{}, err
	}
	o := opts.normalize()
	if err := validateTargetPlatform(o.targetOS, o.targetArch); err != nil {
		return Result{}, err
	}

	profileID := fmt.Sprintf("fabric-loader-%s-%s", loaderVersion, gameVersion)
	profileURL := fmt.Sprintf("%s/v2/versions/loader/%s/%s/profile/json",
		strings.TrimRight(o.metaBase, "/"), url.PathEscape(gameVersion), url.PathEscape(loaderVersion))

	o.progress.report("拉取loader元信息")
	profileRaw, err := o.getBytes(ctx, profileURL, maxMetadataBytes)
	if err != nil {
		return Result{}, fmt.Errorf("获取 Fabric loader profile 失败: %w", err)
	}
	var profile fabricProfile
	if err := json.Unmarshal(profileRaw, &profile); err != nil {
		return Result{}, fmt.Errorf("解析 Fabric loader profile 失败: %w", err)
	}
	if profile.ID != profileID {
		return Result{}, fmt.Errorf("Fabric profile id 异常: %q，期望 %q", profile.ID, profileID)
	}
	if profile.InheritsFrom != gameVersion {
		return Result{}, fmt.Errorf("Fabric profile inheritsFrom 异常: %q，期望 %q", profile.InheritsFrom, gameVersion)
	}
	if len(profile.Libraries) == 0 {
		return Result{}, errors.New("Fabric profile 缺少 libraries")
	}

	o.progress.report("拉取 vanilla 版本元信息")
	vanillaRaw, err := fetchVanillaVersion(ctx, gameVersion, o)
	if err != nil {
		return Result{}, err
	}
	var vanilla mojangVersion
	if err := json.Unmarshal(vanillaRaw, &vanilla); err != nil {
		return Result{}, fmt.Errorf("解析 Minecraft %s 版本 json 失败: %w", gameVersion, err)
	}
	if vanilla.ID != "" && vanilla.ID != gameVersion {
		return Result{}, fmt.Errorf("Minecraft 版本 json id 异常: %q，期望 %q", vanilla.ID, gameVersion)
	}

	seen := make(map[string]artifact)
	var arts []artifact
	add := func(a artifact) error {
		if prev, ok := seen[a.relPath]; ok {
			if prev.kind != a.kind || !strings.EqualFold(prev.sum, a.sum) {
				return fmt.Errorf("文件路径冲突: %s", a.relPath)
			}
			return nil
		}
		seen[a.relPath] = a
		arts = append(arts, a)
		return nil
	}

	for _, lib := range profile.Libraries {
		a, err := fabricArtifact(ctx, lib, o)
		if err != nil {
			return Result{}, fmt.Errorf("Fabric library %q: %w", lib.Name, err)
		}
		if err := add(a); err != nil {
			return Result{}, err
		}
	}

	vanillaArts, err := vanillaArtifacts(vanilla, o.targetOS, o.targetArch, o.progress)
	if err != nil {
		return Result{}, err
	}
	for _, a := range vanillaArts {
		if err := add(a); err != nil {
			return Result{}, err
		}
	}

	client := vanilla.Downloads.Client
	if err := validateDownloadURL(client.URL); err != nil {
		return Result{}, fmt.Errorf("Minecraft client jar: %w", err)
	}
	if err := validateHash(hashSHA1, client.SHA1); err != nil {
		return Result{}, fmt.Errorf("Minecraft client jar sha1 无效: %w", err)
	}
	if client.Size < 0 || client.Size > maxArtifactBytes {
		return Result{}, fmt.Errorf("Minecraft client jar 声明大小 %d 非法", client.Size)
	}
	if err := add(artifact{
		relPath: "versions/" + gameVersion + "/" + gameVersion + ".jar",
		url:     client.URL,
		kind:    hashSHA1,
		sum:     client.SHA1,
		size:    client.Size,
		display: gameVersion,
		label:   "game jar",
	}); err != nil {
		return Result{}, err
	}

	downloaded, skipped, err := downloadArtifacts(ctx, instanceDir, arts, o)
	if err != nil {
		return Result{}, err
	}

	o.progress.report("写入版本 json")
	if err := writeFileAtomic(instanceDir, "versions/"+gameVersion+"/"+gameVersion+".json", vanillaRaw); err != nil {
		return Result{}, fmt.Errorf("写入 Minecraft 版本 json 失败: %w", err)
	}
	if err := writeFileAtomic(instanceDir, "versions/"+profileID+"/"+profileID+".json", profileRaw); err != nil {
		return Result{}, fmt.Errorf("写入 Fabric 版本 json 失败: %w", err)
	}

	return Result{
		ProfileID:  profileID,
		VersionID:  gameVersion,
		Files:      len(arts),
		Downloaded: downloaded,
		Skipped:    skipped,
	}, nil
}

// VanillaResult 描述一次原版文件准备的结果。
type VanillaResult struct {
	VersionID     string
	Files         int
	Downloaded    int
	Skipped       int
	JavaComponent string
	JavaMajor     int
}

// EnsureVanilla 只准备原版文件：versions/<mc>/<mc>.json、client jar 和 vanilla libraries。
// 它复用与 Install 相同的下载、校验逻辑，供 Forge 等需要继承原版版本的加载器使用；
// 已存在且哈希正确的文件会被跳过，可安全重复执行。
func EnsureVanilla(ctx context.Context, instanceDir, gameVersion string, opts Options) (VanillaResult, error) {
	if instanceDir == "" {
		return VanillaResult{}, errors.New("实例目录为空")
	}
	if err := validateVersionSegment("Minecraft 版本", gameVersion); err != nil {
		return VanillaResult{}, err
	}
	o := opts.normalize()
	if err := validateTargetPlatform(o.targetOS, o.targetArch); err != nil {
		return VanillaResult{}, err
	}

	o.progress.report("拉取 vanilla 版本元信息")
	vanillaRaw, err := fetchVanillaVersion(ctx, gameVersion, o)
	if err != nil {
		return VanillaResult{}, err
	}
	var vanilla mojangVersion
	if err := json.Unmarshal(vanillaRaw, &vanilla); err != nil {
		return VanillaResult{}, fmt.Errorf("解析 Minecraft %s 版本 json 失败: %w", gameVersion, err)
	}
	if vanilla.ID != "" && vanilla.ID != gameVersion {
		return VanillaResult{}, fmt.Errorf("Minecraft 版本 json id 异常: %q，期望 %q", vanilla.ID, gameVersion)
	}

	vanillaArts, err := vanillaArtifacts(vanilla, o.targetOS, o.targetArch, o.progress)
	if err != nil {
		return VanillaResult{}, err
	}

	seen := make(map[string]artifact)
	var arts []artifact
	add := func(a artifact) error {
		if prev, ok := seen[a.relPath]; ok {
			if prev.kind != a.kind || !strings.EqualFold(prev.sum, a.sum) {
				return fmt.Errorf("文件路径冲突: %s", a.relPath)
			}
			return nil
		}
		seen[a.relPath] = a
		arts = append(arts, a)
		return nil
	}
	for _, a := range vanillaArts {
		if err := add(a); err != nil {
			return VanillaResult{}, err
		}
	}

	client := vanilla.Downloads.Client
	if err := validateDownloadURL(client.URL); err != nil {
		return VanillaResult{}, fmt.Errorf("Minecraft client jar: %w", err)
	}
	if err := validateHash(hashSHA1, client.SHA1); err != nil {
		return VanillaResult{}, fmt.Errorf("Minecraft client jar sha1 无效: %w", err)
	}
	if client.Size < 0 || client.Size > maxArtifactBytes {
		return VanillaResult{}, fmt.Errorf("Minecraft client jar 声明大小 %d 非法", client.Size)
	}
	if err := add(artifact{
		relPath: "versions/" + gameVersion + "/" + gameVersion + ".jar",
		url:     client.URL,
		kind:    hashSHA1,
		sum:     client.SHA1,
		size:    client.Size,
		display: gameVersion,
		label:   "game jar",
	}); err != nil {
		return VanillaResult{}, err
	}

	downloaded, skipped, err := downloadArtifacts(ctx, instanceDir, arts, o)
	if err != nil {
		return VanillaResult{}, err
	}

	o.progress.report("写入 Minecraft 版本 json")
	if err := writeFileAtomic(instanceDir, "versions/"+gameVersion+"/"+gameVersion+".json", vanillaRaw); err != nil {
		return VanillaResult{}, fmt.Errorf("写入 Minecraft 版本 json 失败: %w", err)
	}

	res := VanillaResult{
		VersionID:  gameVersion,
		Files:      len(arts),
		Downloaded: downloaded,
		Skipped:    skipped,
	}
	if vanilla.JavaVersion != nil {
		res.JavaComponent = vanilla.JavaVersion.Component
		res.JavaMajor = vanilla.JavaVersion.MajorVersion
	}
	return res, nil
}

func fabricArtifact(ctx context.Context, lib fabricLibrary, o options) (artifact, error) {
	rel, err := mavenLibraryPath(lib.Name)
	if err != nil {
		return artifact{}, err
	}
	if lib.URL == "" {
		return artifact{}, errors.New("缺少 maven 仓库地址")
	}
	rawURL := strings.TrimRight(lib.URL, "/") + "/" + strings.TrimPrefix(rel, "libraries/")
	if err := validateDownloadURL(rawURL); err != nil {
		return artifact{}, err
	}

	sum := lib.SHA512
	if sum == "" {
		// Fabric meta 对 fabric-loader/intermediary 不下发哈希，改取 maven 的 .sha512 sidecar。
		data, err := o.getBytes(ctx, rawURL+".sha512", maxSidecarHashSize)
		if err != nil {
			return artifact{}, fmt.Errorf("获取 sha512 失败: %w", err)
		}
		sum = strings.TrimSpace(string(data))
	}
	if err := validateHash(hashSHA512, sum); err != nil {
		return artifact{}, err
	}
	if lib.Size < 0 || lib.Size > maxArtifactBytes {
		return artifact{}, fmt.Errorf("声明大小 %d 非法", lib.Size)
	}
	return artifact{
		relPath: rel,
		url:     rawURL,
		kind:    hashSHA512,
		sum:     sum,
		size:    lib.Size,
		display: lib.Name,
		label:   "library",
	}, nil
}

// mavenLibraryPath 把 group:artifact:version[:classifier][@ext] 转为实例内路径。
func mavenLibraryPath(name string) (string, error) {
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
	classifier := ""
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

var (
	validTargetOS   = map[string]bool{"windows": true, "linux": true, "darwin": true}
	validTargetArch = map[string]bool{"amd64": true, "arm64": true, "386": true, "arm": true}
)

func validateTargetPlatform(goos, goarch string) error {
	if !validTargetOS[goos] {
		return fmt.Errorf("不支持的目标系统 %q（可选 windows/linux/darwin）", goos)
	}
	if !validTargetArch[goarch] {
		return fmt.Errorf("不支持的目标架构 %q（可选 amd64/arm64/386/arm）", goarch)
	}
	return nil
}

func validateVersionSegment(kind, v string) error {
	if v == "" {
		return fmt.Errorf("%s为空", kind)
	}
	if len(v) > maxVersionSegmentBytes {
		return fmt.Errorf("%s %q 过长", kind, v)
	}
	if strings.ContainsAny(v, `/\`) {
		return fmt.Errorf("%s %q 含路径分隔符", kind, v)
	}
	if err := mrpack.ValidateRelPath(v); err != nil {
		return fmt.Errorf("%s %q 非法: %w", kind, v, err)
	}
	return nil
}
