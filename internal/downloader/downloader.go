// Package downloader 按清单下载文件，并在写入目标路径前强制校验 sha1/sha512。
package downloader

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
	defaultConcurrency = 4
	defaultRetries     = 3
	defaultRetryDelay  = 500 * time.Millisecond
	maxRetryDelay      = 30 * time.Second
	// 防止畸形包声明超大文件耗尽磁盘；正规整合包文件远小于该值。
	maxFileBytes int64 = 4 << 30
)

// ErrHashMismatch 表示下载内容与清单声明的哈希不一致。
var ErrHashMismatch = errors.New("sha 校验失败")

type Options struct {
	// Concurrency 是同时下载的文件数，<=0 时默认 4。
	Concurrency int
	// Retries 是失败后的额外重试次数，0 时默认 3；负数表示不重试。
	Retries int
	// RetryDelay 是首次重试前的等待时间，按指数退避，<=0 时默认 500ms。
	RetryDelay time.Duration
	// HTTPClient 为空时使用 http.DefaultClient。默认客户端没有整体超时，
	// 调用方应通过 ctx 控制最长等待时间。
	HTTPClient *http.Client
	// Progress 在每个文件处理完成后调用，done 为已完成数（含跳过），total 为文件总数。
	// Progress 回调之间已串行化，但不要在其中执行耗时操作。
	Progress Progress
	// OnStart 在确认需要下载、发起请求前调用；已存在被跳过的文件不会触发。
	// OnStart 回调之间已串行化，但与 Progress 可能并发。
	OnStart func(Result)
}

// Progress 是下载进度回调。
type Progress func(done, total int, res Result)

// Result 描述单个文件的处理结果；顺序与传入的 files 一致。
type Result struct {
	File    mrpack.File
	Target  string
	Skipped bool // 目标文件已存在且哈希正确，未发起下载
}

// Download 把 files 下载到 root 下。每个文件先写同目录临时文件并计算哈希
// （有 sha512 用 sha512，否则用 sha1），校验通过后原子改名到目标路径；
// 任一下载失败会取消其余任务并返回首个错误。
func Download(ctx context.Context, root string, files []mrpack.File, opts Options) ([]Result, error) {
	if root == "" {
		return nil, errors.New("实例目录为空")
	}
	if len(files) == 0 {
		return []Result{}, nil
	}
	o := opts.normalize()
	if o.onStart != nil {
		fn := o.onStart
		var startMu sync.Mutex
		o.onStart = func(res Result) {
			startMu.Lock()
			defer startMu.Unlock()
			fn(res)
		}
	}
	workers := min(o.concurrency, len(files))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make([]Result, len(files))
	jobs := make(chan int)
	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		progressMu sync.Mutex
		firstErr   error
		done       int
	)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				res, err := downloadOne(ctx, root, files[idx], o)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					mu.Unlock()
					continue
				}
				results[idx] = res
				if o.progress != nil {
					progressMu.Lock()
					done++
					o.progress(done, len(files), res)
					progressMu.Unlock()
				}
			}
		}()
	}

feed:
	for i := range files {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

type options struct {
	concurrency int
	retries     int
	retryDelay  time.Duration
	httpClient  *http.Client
	progress    Progress
	onStart     func(Result)
}

func (o Options) normalize() options {
	n := options{
		concurrency: o.Concurrency,
		retries:     o.Retries,
		retryDelay:  o.RetryDelay,
		httpClient:  o.HTTPClient,
		progress:    o.Progress,
		onStart:     o.OnStart,
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

type hashKind int

const (
	hashSHA1 hashKind = iota
	hashSHA512
)

type hashSpec struct {
	kind hashKind
	sum  string
}

func (s hashSpec) name() string {
	if s.kind == hashSHA1 {
		return "sha1"
	}
	return "sha512"
}

func (s hashSpec) new() hash.Hash {
	if s.kind == hashSHA1 {
		return sha1.New()
	}
	return sha512.New()
}

// hashSpecFor 优先使用 sha512；资产索引等场景只有 sha1。
func hashSpecFor(h mrpack.Hashes) (hashSpec, error) {
	if h.SHA512 != "" {
		if err := validateHashHex(h.SHA512, sha512.Size); err != nil {
			return hashSpec{}, fmt.Errorf("sha512 无效: %w", err)
		}
		return hashSpec{kind: hashSHA512, sum: h.SHA512}, nil
	}
	if h.SHA1 != "" {
		if err := validateHashHex(h.SHA1, sha1.Size); err != nil {
			return hashSpec{}, fmt.Errorf("sha1 无效: %w", err)
		}
		return hashSpec{kind: hashSHA1, sum: h.SHA1}, nil
	}
	return hashSpec{}, errors.New("缺少 sha1/sha512 哈希")
}

func validateHashHex(s string, size int) error {
	b, err := hex.DecodeString(s)
	if err != nil {
		return errors.New("不是合法十六进制")
	}
	if len(b) != size {
		return fmt.Errorf("长度应为 %d 字节", size)
	}
	return nil
}

func downloadOne(ctx context.Context, root string, f mrpack.File, o options) (Result, error) {
	target, err := mrpack.SafeJoin(root, f.Path)
	if err != nil {
		return Result{}, fmt.Errorf("文件 %q 路径非法: %w", f.Path, err)
	}
	res := Result{File: f, Target: target}

	spec, err := hashSpecFor(f.Hashes)
	if err != nil {
		return Result{}, fmt.Errorf("文件 %q 哈希配置错误: %w", f.Path, err)
	}
	if validExistingFile(target, spec) {
		res.Skipped = true
		return res, nil
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return Result{}, fmt.Errorf("文件 %q 创建目录失败: %w", f.Path, err)
	}
	if len(f.Downloads) == 0 {
		return Result{}, fmt.Errorf("文件 %q 没有下载地址", f.Path)
	}
	for _, raw := range f.Downloads {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return Result{}, fmt.Errorf("文件 %q 下载地址非法: %q", f.Path, raw)
		}
	}
	if f.FileSize > maxFileBytes {
		return Result{}, fmt.Errorf("文件 %q 声明大小 %d 超过上限 %d", f.Path, f.FileSize, maxFileBytes)
	}

	limit := maxFileBytes
	if f.FileSize > 0 {
		limit = f.FileSize
	}

	if o.onStart != nil {
		o.onStart(res)
	}

	var lastErr error
	for attempt := 0; attempt <= o.retries; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, retryDelay(o.retryDelay, attempt-1)); err != nil {
				return Result{}, fmt.Errorf("文件 %q 下载已取消: %w", f.Path, err)
			}
		}
		// 多个镜像轮换尝试，避免反复命中同一个坏源。
		rawURL := f.Downloads[attempt%len(f.Downloads)]
		err := fetchOnce(ctx, o.httpClient, rawURL, target, spec, limit)
		if err == nil {
			return res, nil
		}
		lastErr = fmt.Errorf("%s: %w", rawURL, err)
		if ctx.Err() != nil {
			return Result{}, fmt.Errorf("文件 %q 下载已取消: %w", f.Path, ctx.Err())
		}
	}
	return Result{}, fmt.Errorf("文件 %q 下载失败（共尝试 %d 次）: %w", f.Path, o.retries+1, lastErr)
}

// fetchOnce 下载到同目录临时文件，校验通过后才改名；所有失败路径都会删除临时文件。
func fetchOnce(ctx context.Context, client *http.Client, rawURL, target string, spec hashSpec, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
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
	if resp.ContentLength > limit {
		return fmt.Errorf("响应大小 %d 超过上限 %d", resp.ContentLength, limit)
	}

	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".mtl-dl-*")
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

	h := spec.new()
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
	if !strings.EqualFold(got, spec.sum) {
		return fmt.Errorf("%w（%s）: 得到 %s，期望 %s", ErrHashMismatch, spec.name(), got, spec.sum)
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

// validExistingFile 只信任普通文件；符号链接等直接视为无效，走正常下载覆盖。
func validExistingFile(path string, spec hashSpec) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	got, err := fileHash(path, spec)
	return err == nil && strings.EqualFold(got, spec.sum)
}

func fileHash(path string, spec hashSpec) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := spec.new()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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
