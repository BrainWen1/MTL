package fabric

import (
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mtl/internal/mrpack"
)

const (
	// 与 downloader 保持一致的并发/重试模型。
	defaultConcurrency = 4
	defaultRetries     = 3
	defaultRetryDelay  = 500 * time.Millisecond
	maxRetryDelay      = 30 * time.Second

	maxArtifactBytes   int64 = 4 << 30
	maxMetadataBytes   int64 = 8 << 20
	maxSidecarHashSize int64 = 4096
)

type hashKind int

const (
	hashSHA1 hashKind = iota
	hashSHA512
)

var errHashMismatch = errors.New("sha 校验失败")

type artifact struct {
	relPath string // 实例目录内相对路径
	url     string
	kind    hashKind
	sum     string
	size    int64
	display string // 进度输出用名称
	label   string // library / game jar
}

// reporter 串行化进度回调，调用方无需自己加锁。
type reporter struct {
	mu sync.Mutex
	fn func(string)
}

func (r *reporter) report(msg string) {
	if r == nil || r.fn == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fn(msg)
}

type options struct {
	metaBase    string
	manifestURL string
	httpClient  *http.Client
	concurrency int
	retries     int
	retryDelay  time.Duration
	progress    *reporter
}

func newHash(k hashKind) hash.Hash {
	if k == hashSHA1 {
		return sha1.New()
	}
	return sha512.New()
}

func hashHexLen(k hashKind) int {
	if k == hashSHA1 {
		return 40
	}
	return 128
}

func sumHex(k hashKind, data []byte) string {
	h := newHash(k)
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func validateHash(k hashKind, sum string) error {
	b, err := hex.DecodeString(sum)
	if err != nil || len(b)*2 != hashHexLen(k) {
		return errors.New("不是合法的十六进制哈希")
	}
	return nil
}

func validateDownloadURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("非法下载地址 %q", raw)
	}
	return nil
}

// downloadArtifacts 与 downloader.Download 语义一致：并发下载、失败取消其余任务、
// 临时文件校验通过后原子改名；已存在且哈希正确的文件跳过。
func downloadArtifacts(ctx context.Context, root string, arts []artifact, o options) (downloaded, skipped int, err error) {
	if len(arts) == 0 {
		return 0, 0, nil
	}
	// 先集中校验路径，避免下载到一半才发现逃逸路径。
	for _, a := range arts {
		if _, err := mrpack.SafeJoin(root, a.relPath); err != nil {
			return 0, 0, fmt.Errorf("文件 %q 路径非法: %w", a.relPath, err)
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	total := len(arts)
	jobs := make(chan int)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		done     int
	)
	workers := min(o.concurrency, total)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				a := arts[idx]
				wasSkipped, err := downloadOneArtifact(ctx, root, a, o)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					mu.Unlock()
					continue
				}
				mu.Lock()
				done++
				if wasSkipped {
					skipped++
				} else {
					downloaded++
				}
				msg := fmt.Sprintf("下载%s %s (%d/%d)", a.label, a.display, done, total)
				if wasSkipped {
					msg = fmt.Sprintf("跳过%s %s（已存在） (%d/%d)", a.label, a.display, done, total)
				}
				mu.Unlock()
				o.progress.report(msg)
			}
		}()
	}

feed:
	for i := range arts {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()

	if firstErr != nil {
		return 0, 0, firstErr
	}
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	return downloaded, skipped, nil
}

func downloadOneArtifact(ctx context.Context, root string, a artifact, o options) (bool, error) {
	target, err := mrpack.SafeJoin(root, a.relPath)
	if err != nil {
		return false, fmt.Errorf("文件 %q 路径非法: %w", a.relPath, err)
	}
	if validExistingArtifact(target, a) {
		return true, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return false, fmt.Errorf("文件 %q 创建目录失败: %w", a.relPath, err)
	}
	if err := fetchArtifact(ctx, o, a, target); err != nil {
		return false, fmt.Errorf("%s %s 下载失败（共尝试 %d 次）: %w", a.label, a.display, o.retries+1, err)
	}
	return false, nil
}

func fetchArtifact(ctx context.Context, o options, a artifact, target string) error {
	var lastErr error
	for attempt := 0; attempt <= o.retries; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, retryDelay(o.retryDelay, attempt-1)); err != nil {
				return err
			}
		}
		err := fetchArtifactOnce(ctx, o.httpClient, a, target)
		if err == nil {
			return nil
		}
		lastErr = fmt.Errorf("%s: %w", a.url, err)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return lastErr
}

func fetchArtifactOnce(ctx context.Context, client *http.Client, a artifact, target string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	limit := a.size
	if limit <= 0 || limit > maxArtifactBytes {
		limit = maxArtifactBytes
	}
	if resp.ContentLength > limit {
		return fmt.Errorf("响应大小 %d 超过上限 %d", resp.ContentLength, limit)
	}

	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".mtl-fabric-dl-*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			os.Remove(tmpName)
		}
	}()

	h := newHash(a.kind)
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, limit+1))
	if err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if n > limit {
		return fmt.Errorf("文件大小超过上限 %d 字节", limit)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, a.sum) {
		return fmt.Errorf("%w: 得到 %s，期望 %s", errHashMismatch, got, a.sum)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("移动到目标路径失败: %w", err)
	}
	committed = true
	return nil
}

func validExistingArtifact(path string, a artifact) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h := newHash(a.kind)
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), a.sum)
}

// getBytes 拉取无独立校验和的元数据（大小受限），仍沿用重试与退避模型。
func (o options) getBytes(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	if err := validateDownloadURL(rawURL); err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt <= o.retries; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, retryDelay(o.retryDelay, attempt-1)); err != nil {
				return nil, err
			}
		}
		data, err := httpGet(ctx, o.httpClient, rawURL, limit)
		if err == nil {
			return data, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("请求失败（共尝试 %d 次）: %w", o.retries+1, lastErr)
}

func httpGet(ctx context.Context, client *http.Client, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
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

func retryDelay(base time.Duration, attempt int) time.Duration {
	d := base
	for i := 0; i < attempt && d < maxRetryDelay; i++ {
		d *= 2
	}
	if d > maxRetryDelay {
		d = maxRetryDelay
	}
	return d
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// writeFileAtomic 用于写入版本 json：先写同目录临时文件再改名。
func writeFileAtomic(root, rel string, data []byte) error {
	target, err := mrpack.SafeJoin(root, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".mtl-fabric-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return err
	}
	committed = true
	return nil
}
