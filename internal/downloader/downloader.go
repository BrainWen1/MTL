// Package downloader 按清单下载文件，并在写入目标路径前强制校验 sha1/sha512。
//
// 下载层统一提供：
//   - HTTP Range 断点续传（目标文件旁的 *.tmp 保留未完成内容）；
//   - 镜像源重写与官方源自动回退；
//   - 单文件超时、指数退避重试；
//   - 临时文件校验通过后原子改名；
//   - 失败文件汇总，单个文件失败不会中止其余文件。
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
	// bigFileBytes 是输出字节级进度的大文件阈值（mod、runtime 等）。
	bigFileBytes int64 = 8 << 20
	// partialSuffix 是断点续传用的稳定临时文件后缀。
	partialSuffix = ".tmp"
)

// ErrHashMismatch 表示下载内容与清单声明的哈希不一致。
var ErrHashMismatch = errors.New("sha 校验失败")

// LogMode 控制调用方的下载日志粒度。零值为 summary。
type LogMode int

const (
	// LogSummary 只输出批次总进度、失败清单与大文件百分比（默认）。
	LogSummary LogMode = iota
	// LogVerbose 逐个文件输出下载/跳过信息。
	LogVerbose
)

// ErrorKind 是失败分类，便于汇总时区分网络、哈希与本地错误。
type ErrorKind string

const (
	KindNetwork ErrorKind = "网络失败"
	KindHash    ErrorKind = "哈希校验失败"
	KindRange   ErrorKind = "不支持断点续传"
	KindLocal   ErrorKind = "本地文件错误"
)

type Options struct {
	// Concurrency 是同时下载的文件数，<=0 时默认 4。
	Concurrency int
	// Retries 是失败后的额外重试次数，0 时默认 3；负数表示不重试。
	Retries int
	// RetryDelay 是首次重试前的等待时间，按指数退避，<=0 时默认 500ms。
	RetryDelay time.Duration
	// Timeout 是单次下载尝试（含 HEAD 探测）的超时时间；<=0 表示不限制，
	// 由调用方通过 ctx 控制整体超时。
	Timeout time.Duration
	// HTTPClient 为空时使用 http.DefaultClient。默认客户端没有整体超时，
	// 调用方应通过 ctx 或 Timeout 控制最长等待时间。
	HTTPClient *http.Client
	// Mirrors 是镜像源配置；为空时全部使用原始官方地址。
	Mirrors Mirrors
	// Progress 在每个文件成功/跳过后调用，done 为已完成数（含跳过），total 为文件总数。
	// Progress 回调之间已串行化，但不要在其中执行耗时操作。
	Progress Progress
	// BatchProgress 在每个文件处理完成（成功、跳过或失败）后调用，
	// 用于汇总模式输出批次进度，failed 为当前失败数。
	BatchProgress BatchProgress
	// FileProgress 在单个大文件下载过程中按 10% / 1 秒节流触发；
	// written 为含断点前缀的已写入字节数，total 为已知文件大小（未知时为 0）。
	FileProgress FileProgress
	// OnStart 在确认需要下载、发起请求前调用；已存在被跳过的文件不会触发。
	// OnStart 回调之间已串行化，但与 Progress 可能并发。
	OnStart func(Result)
}

// Progress 是下载进度回调。
type Progress func(done, total int, res Result)

// BatchProgress 是批次进度回调，失败文件也计入 done。
type BatchProgress func(done, total, failed int)

// FileProgress 是单个大文件的字节进度回调。
type FileProgress func(path string, written, total int64)

// Result 描述单个文件的处理结果；顺序与传入的 files 一致。
type Result struct {
	File    mrpack.File
	Target  string
	Skipped bool // 目标文件已存在且哈希正确，未发起下载
	Resumed bool // 本次通过 HTTP Range 从临时文件断点续传
	// RangeUnsupported 表示存在未完成临时文件，但服务端不支持 Range，
	// 本次已从头重新下载。
	RangeUnsupported bool
	// Attempts 是实际发起的下载尝试次数（跳过时为 0）。
	Attempts int
	// Bytes 是本次成功下载后目标文件的总字节数。
	Bytes int64
	// Source 是最后一次尝试使用的下载地址。
	Source string
	// Err 非空表示该文件处理失败；失败文件不会阻止同一批次的其他文件。
	Err  error
	Kind ErrorKind
}

// FileFailure 描述一个失败文件，供汇总输出。
type FileFailure struct {
	Path string
	URL  string
	Kind ErrorKind
	Err  error
}

func (f FileFailure) Error() string {
	if f.Path == "" {
		return fmt.Sprintf("%s: %v", f.Kind, f.Err)
	}
	return fmt.Sprintf("%s（%s）: %v", f.Path, f.Kind, f.Err)
}

// BatchError 汇总一批下载中的失败文件。
type BatchError struct {
	Total  int
	Failed []FileFailure
}

// maxReportedFailures 限制错误信息中列出的失败明细，避免刷屏。
const maxReportedFailures = 20

func (e *BatchError) Error() string {
	if e == nil || len(e.Failed) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "下载失败：%d/%d 个文件", len(e.Failed), e.Total)
	for i, f := range e.Failed {
		if i >= maxReportedFailures {
			fmt.Fprintf(&b, "\n  …另有 %d 个失败文件", len(e.Failed)-maxReportedFailures)
			break
		}
		fmt.Fprintf(&b, "\n  - %s", f.Error())
	}
	return b.String()
}

// Unwrap 让 errors.Is/As 能匹配到哈希不一致等底层错误。
func (e *BatchError) Unwrap() []error {
	if e == nil {
		return nil
	}
	out := make([]error, 0, len(e.Failed))
	for _, f := range e.Failed {
		if f.Err != nil {
			out = append(out, f.Err)
		}
	}
	return out
}

// FailedCount 返回失败文件数，便于调用方输出摘要。
func (e *BatchError) FailedCount() int {
	if e == nil {
		return 0
	}
	return len(e.Failed)
}

// Download 把 files 下载到 root 下。每个文件先写同目录的 <目标>.tmp 临时文件，
// 支持 HTTP Range 续传；哈希校验通过后原子改名到目标路径。
// 单个文件失败不会中止其他文件，全部处理完后通过 *BatchError 汇总返回。
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
	if o.fileProgress != nil {
		fn := o.fileProgress
		var fileMu sync.Mutex
		o.fileProgress = func(path string, written, total int64) {
			fileMu.Lock()
			defer fileMu.Unlock()
			fn(path, written, total)
		}
	}
	workers := min(o.concurrency, len(files))

	results := make([]Result, len(files))
	jobs := make(chan int)
	var (
		wg         sync.WaitGroup
		progressMu sync.Mutex
		done       int
		failed     int
	)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				res := downloadOne(ctx, root, files[idx], o)
				results[idx] = res

				progressMu.Lock()
				done++
				if res.Err != nil {
					failed++
				}
				if res.Err == nil && o.progress != nil {
					o.progress(done, len(files), res)
				}
				if o.batchProgress != nil {
					o.batchProgress(done, len(files), failed)
				}
				progressMu.Unlock()
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

	// 用户取消（Ctrl+C）时保留 *.tmp 供下次续传，并让上层按取消处理。
	if err := ctx.Err(); err != nil {
		return results, err
	}

	var failures []FileFailure
	for i := range results {
		res := &results[i]
		if res.Err == nil {
			continue
		}
		if res.Kind == "" {
			res.Kind = classify(res.Err)
		}
		failures = append(failures, FileFailure{
			Path: res.File.Path,
			URL:  res.Source,
			Kind: res.Kind,
			Err:  res.Err,
		})
	}
	if len(failures) > 0 {
		return results, &BatchError{Total: len(files), Failed: failures}
	}
	return results, nil
}

type options struct {
	concurrency   int
	retries       int
	retryDelay    time.Duration
	timeout       time.Duration
	httpClient    *http.Client
	mirrors       Mirrors
	progress      Progress
	batchProgress BatchProgress
	fileProgress  FileProgress
	onStart       func(Result)
}

func (o Options) normalize() options {
	n := options{
		concurrency:   o.Concurrency,
		retries:       o.Retries,
		retryDelay:    o.RetryDelay,
		timeout:       o.Timeout,
		httpClient:    o.HTTPClient,
		mirrors:       o.Mirrors,
		progress:      o.Progress,
		batchProgress: o.BatchProgress,
		fileProgress:  o.FileProgress,
		onStart:       o.OnStart,
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
	if n.timeout < 0 {
		n.timeout = 0
	}
	if n.httpClient == nil {
		n.httpClient = http.DefaultClient
	}
	n.mirrors = n.mirrors.normalized()
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

// localError 标记与网络无关的本地错误（路径、目录、哈希配置等）。
type localError struct{ err error }

func (e *localError) Error() string { return e.err.Error() }
func (e *localError) Unwrap() error { return e.err }

func localErr(format string, args ...any) error {
	return &localError{err: fmt.Errorf(format, args...)}
}

// classify 把下载错误归类，便于汇总时区分网络失败与哈希校验失败。
func classify(err error) ErrorKind {
	var le *localError
	if errors.As(err, &le) {
		return KindLocal
	}
	if errors.Is(err, ErrHashMismatch) {
		return KindHash
	}
	if errors.Is(err, ErrRangeUnsupported) {
		return KindRange
	}
	return KindNetwork
}

func downloadOne(ctx context.Context, root string, f mrpack.File, o options) Result {
	res := Result{File: f}
	target, err := mrpack.SafeJoin(root, f.Path)
	if err != nil {
		res.Err = localErr("文件 %q 路径非法: %w", f.Path, err)
		res.Kind = KindLocal
		return res
	}
	res.Target = target

	spec, err := hashSpecFor(f.Hashes)
	if err != nil {
		res.Err = localErr("文件 %q 哈希配置错误: %w", f.Path, err)
		res.Kind = KindLocal
		return res
	}
	if validExistingFile(target, spec) {
		res.Skipped = true
		return res
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		res.Err = localErr("文件 %q 创建目录失败: %w", f.Path, err)
		res.Kind = KindLocal
		return res
	}
	if len(f.Downloads) == 0 {
		res.Err = localErr("文件 %q 没有下载地址", f.Path)
		res.Kind = KindLocal
		return res
	}
	candidates, err := candidateURLs(f.Downloads, o.mirrors)
	if err != nil {
		res.Err = localErr("文件 %q: %w", f.Path, err)
		res.Kind = KindLocal
		return res
	}
	if f.FileSize > maxFileBytes {
		res.Err = localErr("文件 %q 声明大小 %d 超过上限 %d", f.Path, f.FileSize, maxFileBytes)
		res.Kind = KindLocal
		return res
	}

	if o.onStart != nil {
		o.onStart(res)
	}

	limit := maxFileBytes
	if f.FileSize > 0 {
		limit = f.FileSize
	}

	// 镜像源优先；重试次数不足以覆盖全部候选地址时，至少把每个地址试一次。
	attempts := o.retries + 1
	if attempts < len(candidates) {
		attempts = len(candidates)
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, retryDelay(o.retryDelay, attempt-1)); err != nil {
				res.Err = fmt.Errorf("文件 %q 下载已取消: %w", f.Path, err)
				res.Kind = KindNetwork
				res.Attempts = attempt
				return res
			}
		}
		rawURL := candidates[attempt%len(candidates)]
		res.Source = rawURL
		stats, err := fetchOnce(ctx, o.httpClient, rawURL, target, spec, limit, o.timeout,
			func(written, total int64) {
				if o.fileProgress != nil {
					o.fileProgress(f.Path, written, total)
				}
			})
		if err == nil {
			res.Resumed = stats.Resumed
			res.RangeUnsupported = stats.RangeUnsupported
			res.Bytes = stats.Bytes
			res.Attempts = attempt + 1
			return res
		}
		lastErr = err
		if ctx.Err() != nil {
			res.Err = fmt.Errorf("文件 %q 下载已取消: %w", f.Path, ctx.Err())
			res.Kind = KindNetwork
			res.Attempts = attempt + 1
			return res
		}
	}
	res.Attempts = attempts
	res.Err = fmt.Errorf("文件 %q 下载失败（共尝试 %d 次）: %w", f.Path, attempts, lastErr)
	res.Kind = classify(lastErr)
	return res
}

// candidateURLs 校验原始地址并展开镜像候选：镜像地址在前，官方地址在后。
func candidateURLs(downloads []string, m Mirrors) ([]string, error) {
	var out []string
	seen := make(map[string]bool)
	for _, raw := range downloads {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("下载地址非法: %q", raw)
		}
		for _, c := range m.Candidates(raw) {
			if seen[c] {
				continue
			}
			if err := validateHTTPURL(c); err != nil {
				return nil, err
			}
			seen[c] = true
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("没有可用的下载地址")
	}
	return out, nil
}

func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("下载地址非法: %q", raw)
	}
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
