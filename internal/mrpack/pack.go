package mrpack

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxOverrideFileBytes   uint64 = 1 << 30 // 单文件 1 GiB
	maxOverridesTotalBytes uint64 = 4 << 30 // overrides 合计 4 GiB
)

type Pack struct {
	Index *Index
	r     *zip.ReadCloser
}

func Open(path string) (*Pack, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("打开 mrpack 失败: %w", err)
	}
	idx, err := readIndex(zr)
	if err != nil {
		zr.Close()
		return nil, err
	}
	return &Pack{Index: idx, r: zr}, nil
}

func (p *Pack) Close() error {
	return p.r.Close()
}

func readIndex(zr *zip.ReadCloser) (*Index, error) {
	var entry *zip.File
	for _, f := range zr.File {
		if f.Name != IndexFileName {
			continue
		}
		if entry != nil {
			return nil, fmt.Errorf("包内存在多个 %s", IndexFileName)
		}
		entry = f
	}
	if entry == nil {
		return nil, fmt.Errorf("包根目录缺少 %s", IndexFileName)
	}
	if entry.FileInfo().IsDir() {
		return nil, fmt.Errorf("%s 不是文件", IndexFileName)
	}
	if entry.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s 不能是符号链接", IndexFileName)
	}
	if entry.UncompressedSize64 > maxIndexBytes {
		return nil, fmt.Errorf("%s 超过 %d 字节上限", IndexFileName, maxIndexBytes)
	}

	rc, err := entry.Open()
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", IndexFileName, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxIndexBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", IndexFileName, err)
	}
	if len(data) > maxIndexBytes {
		return nil, fmt.Errorf("%s 超过 %d 字节上限", IndexFileName, maxIndexBytes)
	}
	return ParseIndex(data)
}

// ExtractOverrides 先解压 overrides，再解压 client-overrides，后者覆盖前者。
func (p *Pack) ExtractOverrides(dest string) error {
	if dest == "" {
		return fmt.Errorf("目标目录为空")
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("创建目标目录失败: %w", err)
	}

	var total uint64
	for _, prefix := range []string{"overrides", "client-overrides"} {
		n, err := p.extractPrefix(dest, prefix)
		if err != nil {
			return err
		}
		total += n
		if total > maxOverridesTotalBytes {
			return fmt.Errorf("overrides 解压总量超过上限 %d 字节", maxOverridesTotalBytes)
		}
	}
	return nil
}

func (p *Pack) extractPrefix(dest, prefix string) (uint64, error) {
	seen := make(map[string]string)
	var total uint64

	for _, f := range p.r.File {
		name := f.Name
		if name == prefix+"/" {
			continue
		}
		if !strings.HasPrefix(name, prefix+"/") {
			continue
		}
		rel := name[len(prefix)+1:]
		if rel == "" {
			continue
		}

		mode := f.Mode()
		if mode&os.ModeSymlink != 0 {
			return total, fmt.Errorf("拒绝符号链接条目 %q", name)
		}

		if f.FileInfo().IsDir() || strings.HasSuffix(name, "/") {
			rel = strings.TrimSuffix(rel, "/")
			full, err := SafeJoin(dest, rel)
			if err != nil {
				return total, fmt.Errorf("非法目录 %q: %w", name, err)
			}
			if err := os.MkdirAll(full, 0o755); err != nil {
				return total, fmt.Errorf("创建目录 %q 失败: %w", rel, err)
			}
			continue
		}
		if !mode.IsRegular() {
			return total, fmt.Errorf("拒绝特殊文件条目 %q", name)
		}
		if f.UncompressedSize64 > maxOverrideFileBytes {
			return total, fmt.Errorf("文件 %q 超过单文件上限", name)
		}

		full, err := SafeJoin(dest, rel)
		if err != nil {
			return total, fmt.Errorf("非法文件 %q: %w", name, err)
		}
		key := strings.ToLower(rel)
		if prev, ok := seen[key]; ok {
			return total, fmt.Errorf("%s 内路径重复（大小写不敏感）: %q 与 %q", prefix, prev, name)
		}
		seen[key] = name

		n, err := extractFile(full, f)
		if err != nil {
			return total, fmt.Errorf("解压 %q 失败: %w", name, err)
		}
		total += n
		if total > maxOverridesTotalBytes {
			return total, fmt.Errorf("%s 解压总量超过上限 %d 字节", prefix, maxOverridesTotalBytes)
		}
	}
	return total, nil
}

// extractFile 写入同目录临时文件后 rename，避免失败时留下半截文件。
func extractFile(dst string, f *zip.File) (uint64, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, fmt.Errorf("创建父目录失败: %w", err)
	}
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".mtl-extract-*")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			os.Remove(tmpName)
		}
	}()

	n, err := io.Copy(tmp, io.LimitReader(rc, int64(maxOverrideFileBytes)+1))
	if err != nil {
		tmp.Close()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if uint64(n) > maxOverrideFileBytes {
		return 0, fmt.Errorf("解压后大小超过单文件上限")
	}

	perm := os.FileMode(0o644)
	if f.Mode().Perm()&0o111 != 0 {
		perm = 0o755
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return 0, err
	}
	committed = true
	return uint64(n), nil
}
