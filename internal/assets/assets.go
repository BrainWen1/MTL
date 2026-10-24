// Package assets 负责下载 Minecraft 原版资源到 mtl 独立缓存目录。
package assets

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"mtl/internal/downloader"
	"mtl/internal/mrpack"
)

const (
	defaultObjectsBase = "https://resources.download.minecraft.net/"
	maxIndexBytes      = 16 << 20
)

// IndexSpec 是版本 json 中的 assetIndex 信息。
type IndexSpec struct {
	ID   string
	URL  string
	SHA1 string
	Size int64
}

type Options struct {
	// Dir 为空时使用 DefaultDir；预留手动指定 assetsDir。
	Dir string
	// Concurrency 是并发下载数，<=0 时使用 downloader 默认值。
	Concurrency int
	// Retries 是失败后的额外重试次数，0 使用 downloader 默认值；负数表示不重试。
	Retries int
	// RetryDelay 是首次重试等待时间，<=0 使用 downloader 默认值。
	RetryDelay time.Duration
	// HTTPClient 为空时使用 http.DefaultClient。
	HTTPClient *http.Client
	// Timeout 是单文件下载超时；<=0 表示不限制。
	Timeout time.Duration
	// Mirrors 是镜像源配置，空值使用官方资源地址。
	Mirrors downloader.Mirrors
	// LogMode 控制资源下载日志粒度；默认 summary。
	LogMode downloader.LogMode
	// ObjectsBaseURL 为空时使用官方资源 CDN。
	ObjectsBaseURL string
	// Progress 接收进度消息（不含 "[Assets]" 前缀），调用已串行化。
	Progress func(string)
}

type assetIndex struct {
	Objects map[string]assetObject `json:"objects"`
}

type assetObject struct {
	Hash string `json:"hash"`
	Size int64  `json:"size"`
}

// DefaultDir 返回平台相关的 assets 缓存目录。
func DefaultDir() (string, error) {
	if runtime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return "", errors.New("未找到 APPDATA 环境变量，无法确定 assets 缓存目录")
		}
		return filepath.Join(appData, "mtl", "cache", "assets"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法确定用户主目录: %w", err)
	}
	return filepath.Join(home, ".cache", "mtl", "assets"), nil
}

// Ensure 下载 assetIndex 及其全部 objects 到 dir（默认 DefaultDir）。
// 已存在且 sha1 正确的文件会被 downloader 跳过。
func Ensure(ctx context.Context, spec IndexSpec, opts Options) error {
	// OnStart 与 Progress 分属不同回调，下载器只保证同类回调串行，这里统一加锁。
	var logMu sync.Mutex
	logf := func(format string, args ...any) {
		if opts.Progress == nil {
			return
		}
		msg := fmt.Sprintf(format, args...)
		logMu.Lock()
		defer logMu.Unlock()
		opts.Progress(msg)
	}

	if spec.ID == "" || spec.URL == "" || spec.SHA1 == "" {
		return errors.New("版本 json 缺少完整的 assetIndex 信息（id/url/sha1）")
	}
	if err := mrpack.ValidateRelPath(spec.ID); err != nil || strings.ContainsAny(spec.ID, `/\`) {
		return fmt.Errorf("assetIndex id %q 非法", spec.ID)
	}
	if !validSHA1(spec.SHA1) {
		return fmt.Errorf("assetIndex %q 的 sha1 非法", spec.ID)
	}
	if err := validateHTTPURL(spec.URL); err != nil {
		return fmt.Errorf("assetIndex %q: %w", spec.ID, err)
	}
	if spec.Size < 0 {
		return fmt.Errorf("assetIndex %q 的 size 非法", spec.ID)
	}
	if err := validateHTTPURL(objectsBase(opts.ObjectsBaseURL)); err != nil {
		return err
	}

	dir := opts.Dir
	if dir == "" {
		var err error
		dir, err = DefaultDir()
		if err != nil {
			return err
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("解析 assets 缓存目录失败: %w", err)
	}
	dir = abs
	if err := os.MkdirAll(filepath.Join(dir, "indexes"), 0o755); err != nil {
		return friendly("创建 assets/indexes 目录失败", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o755); err != nil {
		return friendly("创建 assets/objects 目录失败", err)
	}

	logf("正在拉取资源索引：%s", spec.ID)
	indexRel := "indexes/" + spec.ID + ".json"
	_, err = downloader.Download(ctx, dir, []mrpack.File{{
		Path:      indexRel,
		Hashes:    mrpack.Hashes{SHA1: spec.SHA1},
		Downloads: []string{spec.URL},
		FileSize:  spec.Size,
	}}, downloader.Options{
		Concurrency: 1,
		Retries:     opts.Retries,
		RetryDelay:  opts.RetryDelay,
		Timeout:     opts.Timeout,
		HTTPClient:  opts.HTTPClient,
		Mirrors:     opts.Mirrors,
	})
	if err != nil {
		return friendly(fmt.Sprintf("下载 assetIndex %s 失败", spec.ID), err)
	}

	raw, err := readIndex(dir, indexRel)
	if err != nil {
		return friendly(fmt.Sprintf("读取 assetIndex %s 失败", spec.ID), err)
	}
	var idx assetIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		return fmt.Errorf("解析 assetIndex %s 失败: %w", spec.ID, err)
	}

	files, pathToKey, err := buildAssetFiles(idx, objectsBase(opts.ObjectsBaseURL))
	if err != nil {
		return fmt.Errorf("assetIndex %s 内容非法: %w", spec.ID, err)
	}
	if len(files) == 0 {
		logf("全部assets资源就绪")
		return nil
	}

	verbose := opts.LogMode == downloader.LogVerbose
	lastPct := -10
	_, err = downloader.Download(ctx, dir, files, downloader.Options{
		Concurrency: opts.Concurrency,
		Retries:     opts.Retries,
		RetryDelay:  opts.RetryDelay,
		Timeout:     opts.Timeout,
		HTTPClient:  opts.HTTPClient,
		Mirrors:     opts.Mirrors,
		OnStart: func(res downloader.Result) {
			if verbose {
				logf("开始下载资源：%s", pathToKey[res.File.Path])
			}
		},
		Progress: func(done, total int, res downloader.Result) {
			if !verbose {
				return
			}
			if res.Skipped {
				logf("已存在，跳过：%s", pathToKey[res.File.Path])
				return
			}
			logf("资源下载完成，校验通过")
		},
		// summary 模式只输出批次进度，避免上千个 asset 逐个刷屏。
		BatchProgress: func(done, total, failed int) {
			if verbose {
				return
			}
			pct := done * 100 / total
			if pct < lastPct+10 && done != total {
				return
			}
			lastPct = pct
			if failed > 0 {
				logf("资源下载进度：%d/%d（%d%%，失败 %d）", done, total, pct, failed)
				return
			}
			logf("资源下载进度：%d/%d（%d%%）", done, total, pct)
		},
		// 资源通常很小；若出现大对象，保留下载百分比。
		FileProgress: func(path string, written, total int64) {
			if total <= 0 {
				return
			}
			logf("资源下载：%s（%d/%d，%d%%）", pathToKey[path], written, total, written*100/total)
		},
	})
	if err != nil {
		return friendly("下载 assets 资源失败", err)
	}
	logf("全部assets资源就绪")
	return nil
}

func objectsBase(override string) string {
	if override == "" {
		return defaultObjectsBase
	}
	return override
}

// buildAssetFiles 把 assetIndex.objects 转成下载清单。
// 同一 hash 只下载一次（不同资源名可能指向同一文件）。
func buildAssetFiles(idx assetIndex, baseURL string) ([]mrpack.File, map[string]string, error) {
	base := strings.TrimRight(baseURL, "/") + "/"
	keys := make([]string, 0, len(idx.Objects))
	for k := range idx.Objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	files := make([]mrpack.File, 0, len(keys))
	pathToKey := make(map[string]string, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		obj := idx.Objects[key]
		hash := strings.ToLower(strings.TrimSpace(obj.Hash))
		if !validSHA1(hash) {
			return nil, nil, fmt.Errorf("资源 %q 的 hash 非法: %q", key, obj.Hash)
		}
		if obj.Size < 0 {
			return nil, nil, fmt.Errorf("资源 %q 的 size 非法", key)
		}
		if seen[hash] {
			continue
		}
		seen[hash] = true

		rel := objectRelPath(hash)
		files = append(files, mrpack.File{
			Path:      rel,
			Hashes:    mrpack.Hashes{SHA1: hash},
			Downloads: []string{base + hash[:2] + "/" + hash},
			FileSize:  obj.Size,
		})
		pathToKey[rel] = key
	}
	return files, pathToKey, nil
}

// objectRelPath 遵循原版布局：objects/<hash 前两位>/<hash>。
func objectRelPath(hash string) string {
	return "objects/" + hash[:2] + "/" + hash
}

func readIndex(dir, rel string) ([]byte, error) {
	p, err := mrpack.SafeJoin(dir, rel)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxIndexBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxIndexBytes {
		return nil, fmt.Errorf("assetIndex 超过 %d 字节上限", maxIndexBytes)
	}
	return data, nil
}

func validSHA1(h string) bool {
	b, err := hex.DecodeString(h)
	return err == nil && len(b) == 20
}

func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("非法下载地址 %q", raw)
	}
	return nil
}

// friendly 补充权限/磁盘空间的可读提示。
func friendly(action string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%s：权限不足，请检查缓存目录写入权限（%w）", action, err)
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
