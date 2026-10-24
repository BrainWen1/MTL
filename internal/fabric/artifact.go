package fabric

import (
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mtl/internal/downloader"
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

// 与 downloader 共用哈希错误哨兵，便于上层统一 errors.Is 判断。
var errHashMismatch = downloader.ErrHashMismatch

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
	timeout     time.Duration
	mirrors     downloader.Mirrors
	logMode     downloader.LogMode
	targetOS    string
	targetArch  string
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

// downloadArtifacts 复用公共 downloader：并发下载、断点续传、镜像回退、
// 临时文件校验通过后原子改名；已存在且哈希正确的文件跳过。
func downloadArtifacts(ctx context.Context, root string, arts []artifact, o options) (downloaded, skipped int, err error) {
	if len(arts) == 0 {
		return 0, 0, nil
	}
	files := make([]mrpack.File, 0, len(arts))
	display := make(map[string]string, len(arts))
	label := make(map[string]string, len(arts))
	for _, a := range arts {
		f := mrpack.File{
			Path:      a.relPath,
			Downloads: []string{a.url},
			FileSize:  a.size,
		}
		if a.kind == hashSHA512 {
			f.Hashes.SHA512 = a.sum
		} else {
			f.Hashes.SHA1 = a.sum
		}
		files = append(files, f)
		display[a.relPath] = a.display
		label[a.relPath] = a.label
	}

	verbose := o.logMode == downloader.LogVerbose
	lastPct := -10
	results, err := downloader.Download(ctx, root, files, downloader.Options{
		Concurrency: o.concurrency,
		Retries:     o.retries,
		RetryDelay:  o.retryDelay,
		Timeout:     o.timeout,
		HTTPClient:  o.httpClient,
		Mirrors:     o.mirrors,
		OnStart: func(res downloader.Result) {
			if verbose {
				o.progress.report(fmt.Sprintf("下载%s %s", label[res.File.Path], display[res.File.Path]))
			}
		},
		Progress: func(done, total int, res downloader.Result) {
			if !verbose {
				return
			}
			if res.Skipped {
				o.progress.report(fmt.Sprintf("跳过%s %s（已存在） (%d/%d)",
					label[res.File.Path], display[res.File.Path], done, total))
				return
			}
			o.progress.report(fmt.Sprintf("完成%s %s (%d/%d)",
				label[res.File.Path], display[res.File.Path], done, total))
		},
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
				o.progress.report(fmt.Sprintf("下载进度：%d/%d（%d%%，失败 %d）", done, total, pct, failed))
				return
			}
			o.progress.report(fmt.Sprintf("下载进度：%d/%d（%d%%）", done, total, pct))
		},
		FileProgress: func(path string, written, total int64) {
			if total <= 0 {
				return
			}
			o.progress.report(fmt.Sprintf("下载%s %s（%d/%d，%d%%）",
				label[path], display[path], written, total, written*100/total))
		},
	})
	if err != nil {
		return 0, 0, err
	}
	for _, res := range results {
		if res.Skipped {
			skipped++
		} else {
			downloaded++
		}
	}
	return downloaded, skipped, nil
}

// getBytes 拉取无独立校验和的元数据（大小受限），复用公共下载层的镜像回退与重试。
func (o options) getBytes(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	if err := validateDownloadURL(rawURL); err != nil {
		return nil, err
	}
	return downloader.GetBytes(ctx, rawURL, limit, downloader.RequestOptions{
		HTTPClient: o.httpClient,
		Retries:    o.retries,
		RetryDelay: o.retryDelay,
		Timeout:    o.timeout,
		Mirrors:    o.mirrors,
	})
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
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
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
