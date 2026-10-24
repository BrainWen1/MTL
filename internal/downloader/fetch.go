package downloader

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrRangeUnsupported 表示服务端未实现 HTTP Range；下载层会自动回退为完整下载，
// 不会因此让文件失败。该错误主要用于需要明确分类的调用方。
var ErrRangeUnsupported = errors.New("服务端不支持断点续传")

type fetchStats struct {
	Resumed          bool
	RangeUnsupported bool
	Bytes            int64
}

// fetchOnce 执行一次完整下载尝试：
//  1. 打开目标旁的稳定 *.tmp；
//  2. 有未完成内容时先 HEAD 探测服务端是否支持 Range；
//  3. 支持则从文件末尾继续，不支持则清空重下；
//  4. 校验哈希后关闭文件并原子改名。
//
// 网络中断/取消时保留 *.tmp 供下次续传；哈希不一致时立即删除。
func fetchOnce(ctx context.Context, client *http.Client, rawURL, target string, spec hashSpec,
	expected int64, timeout time.Duration, onBytes func(written, total int64)) (_ fetchStats, err error) {
	var stats fetchStats
	attemptCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		attemptCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	limit := expected
	if limit <= 0 || limit > maxFileBytes {
		limit = maxFileBytes
	}

	partial := target + partialSuffix
	f, err := os.OpenFile(partial, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return stats, fmt.Errorf("创建临时文件失败: %w", err)
	}
	closed := false
	closeFile := func() error {
		if closed {
			return nil
		}
		closed = true
		return f.Close()
	}
	defer func() { _ = closeFile() }()
	// 失败或取消时保留已写入内容，续传依赖它；完全没写入的临时文件没有续传价值，直接清理。
	// 该 defer 后注册、先执行，因此 Stat 时文件仍处于打开状态（Windows 关闭后无法 Stat）。
	defer func() {
		if err == nil {
			return
		}
		size := int64(-1)
		if info, statErr := f.Stat(); statErr == nil {
			size = info.Size()
		}
		_ = closeFile()
		if size == 0 {
			_ = os.Remove(partial)
		}
	}()

	info, err := f.Stat()
	if err != nil {
		return stats, fmt.Errorf("读取临时文件状态失败: %w", err)
	}
	offset := info.Size()
	if offset > limit {
		if err := f.Truncate(0); err != nil {
			return stats, fmt.Errorf("清空超限临时文件失败: %w", err)
		}
		offset = 0
	}

	// 上次已在临时文件中写满但未来得及改名：直接校验并提交。
	if offset == limit && offset > 0 {
		got, err := hashOpenFile(f, spec, 0, offset)
		if err == nil && strings.EqualFold(got, spec.sum) {
			if err := commitPartial(closeFile, partial, target); err != nil {
				return stats, err
			}
			stats.Bytes = offset
			return stats, nil
		}
		if err := f.Truncate(0); err != nil {
			return stats, fmt.Errorf("清空无效临时文件失败: %w", err)
		}
		offset = 0
	}

	totalKnown := int64(-1)
	if offset > 0 {
		rangeOK, total := probeRange(attemptCtx, client, rawURL)
		totalKnown = total
		if rangeOK && total >= 0 {
			// 服务端文件与清单声明不一致时，旧临时文件不可信。
			if expected > 0 && total != expected {
				if err := f.Truncate(0); err != nil {
					return stats, fmt.Errorf("清空过期临时文件失败: %w", err)
				}
				offset = 0
			} else if total > limit {
				return stats, fmt.Errorf("响应大小 %d 超过上限 %d", total, limit)
			} else if total < offset {
				if err := f.Truncate(0); err != nil {
					return stats, fmt.Errorf("清空超长临时文件失败: %w", err)
				}
				offset = 0
			}
		} else if !rangeOK {
			// 明确记录“服务端不支持 Range”，便于上层汇总。
			stats.RangeUnsupported = true
			if err := f.Truncate(0); err != nil {
				return stats, fmt.Errorf("清空临时文件失败: %w", err)
			}
			offset = 0
		}
	}

	resp, err := requestFile(attemptCtx, client, rawURL, offset)
	if err != nil {
		return stats, err
	}
	if offset > 0 && resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		// 服务端认为范围无效：清空后完整重下一次。
		_ = resp.Body.Close()
		stats.RangeUnsupported = true
		if err := f.Truncate(0); err != nil {
			return stats, fmt.Errorf("清空临时文件失败: %w", err)
		}
		offset = 0
		resp, err = requestFile(attemptCtx, client, rawURL, 0)
		if err != nil {
			return stats, err
		}
	}
	defer resp.Body.Close()

	if offset > 0 {
		switch resp.StatusCode {
		case http.StatusPartialContent:
			start, _, ok := parseContentRange(resp.Header.Get("Content-Range"))
			if !ok || start != offset {
				_ = f.Truncate(0)
				return stats, fmt.Errorf("服务端返回的续传范围无效: %q", resp.Header.Get("Content-Range"))
			}
			stats.Resumed = true
		case http.StatusOK:
			// 服务端忽略了 Range，返回的是完整文件：从头写入。
			stats.RangeUnsupported = true
			if err := f.Truncate(0); err != nil {
				return stats, fmt.Errorf("清空临时文件失败: %w", err)
			}
			offset = 0
		default:
			return stats, statusError(resp)
		}
	} else if resp.StatusCode != http.StatusOK {
		return stats, statusError(resp)
	}

	if resp.ContentLength > 0 && offset+resp.ContentLength > limit {
		return stats, fmt.Errorf("响应大小 %d 超过上限 %d", offset+resp.ContentLength, limit)
	}

	// 续传时先把已有前缀计入哈希，再继续写入新数据。
	h := spec.new()
	if offset > 0 {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return stats, fmt.Errorf("定位临时文件失败: %w", err)
		}
		if _, err := io.Copy(h, io.LimitReader(f, offset)); err != nil {
			return stats, fmt.Errorf("读取临时文件失败: %w", err)
		}
	} else if err := f.Truncate(0); err != nil {
		return stats, fmt.Errorf("清空临时文件失败: %w", err)
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return stats, fmt.Errorf("定位临时文件失败: %w", err)
	}

	progressTotal := expected
	if progressTotal <= 0 {
		progressTotal = totalKnown
	}
	pw := &progressWriter{
		w:      io.MultiWriter(f, h),
		offset: offset,
		total:  progressTotal,
		on:     onBytes,
	}
	n, err := io.Copy(pw, io.LimitReader(resp.Body, limit-offset+1))
	if err != nil {
		return stats, fmt.Errorf("写入临时文件失败: %w", err)
	}
	written := offset + n
	if written > limit {
		_ = f.Truncate(0)
		return stats, fmt.Errorf("文件大小超过上限 %d 字节", limit)
	}

	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, spec.sum) {
		// 哈希不对的临时文件不能参与下次续传。
		_ = closeFile()
		_ = os.Remove(partial)
		return stats, fmt.Errorf("%w（%s）: 得到 %s，期望 %s", ErrHashMismatch, spec.name(), got, spec.sum)
	}
	if err := commitPartial(closeFile, partial, target); err != nil {
		return stats, err
	}
	stats.Bytes = written
	return stats, nil
}

// commitPartial 关闭临时文件、补齐权限并原子改名为目标文件。
// Windows 下必须先关闭句柄才能 rename。
func commitPartial(closeFile func() error, partial, target string) error {
	if err := closeFile(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Chmod(partial, 0o644); err != nil {
		return err
	}
	if err := os.Rename(partial, target); err != nil {
		return fmt.Errorf("移动到目标路径失败: %w", err)
	}
	return nil
}

// probeRange 通过 HEAD 探测服务端是否支持 Range 以及文件总大小。
// HEAD 失败或服务端未声明 Accept-Ranges 时返回 false，由调用方完整下载。
func probeRange(ctx context.Context, client *http.Client, rawURL string) (bool, int64) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return false, -1
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, -1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, -1
	}
	ok := strings.Contains(strings.ToLower(resp.Header.Get("Accept-Ranges")), "bytes")
	return ok, resp.ContentLength
}

func requestFile(ctx context.Context, client *http.Client, rawURL string, offset int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if resp.StatusCode != http.StatusPartialContent &&
			resp.StatusCode != http.StatusOK &&
			resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
			resp.Body.Close()
			return nil, statusError(resp)
		}
	} else if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, statusError(resp)
	}
	return resp, nil
}

func statusError(resp *http.Response) error {
	return fmt.Errorf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
}

// parseContentRange 解析 "bytes <start>-<end>/<total>"；total 为 "*" 时返回 -1。
func parseContentRange(v string) (start, end int64, ok bool) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(strings.ToLower(v), "bytes ") {
		return 0, 0, false
	}
	v = strings.TrimSpace(v[len("bytes "):])
	slash := strings.IndexByte(v, '/')
	if slash < 0 {
		return 0, 0, false
	}
	rangePart := v[:slash]
	dash := strings.IndexByte(rangePart, '-')
	if dash < 0 {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(rangePart[:dash]), 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false
	}
	end, err = strconv.ParseInt(strings.TrimSpace(rangePart[dash+1:]), 10, 64)
	if err != nil || end < start {
		return 0, 0, false
	}
	return start, end, true
}

func hashOpenFile(f *os.File, spec hashSpec, offset, length int64) (string, error) {
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	h := spec.new()
	if _, err := io.Copy(h, io.LimitReader(f, length)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// progressWriter 只在超过大文件阈值时按 10% / 1 秒节流上报字节进度。
type progressWriter struct {
	w        io.Writer
	offset   int64
	written  int64
	total    int64
	on       func(written, total int64)
	lastEmit time.Time
	lastPct  int
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.written += int64(n)
	if p.on == nil || p.total < bigFileBytes || p.total <= 0 {
		return n, err
	}
	written := p.offset + p.written
	if written > p.total {
		written = p.total
	}
	pct := int(written * 100 / p.total)
	now := time.Now()
	if (pct >= p.lastPct+10 || now.Sub(p.lastEmit) >= time.Second || written == p.total) && pct != p.lastPct {
		p.lastPct = pct
		p.lastEmit = now
		p.on(written, p.total)
	}
	return n, err
}
