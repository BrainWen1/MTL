package launch

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mtl/internal/mrpack"
)

const maxNativeBytes int64 = 1 << 30

// extractNatives 把所有 native jar 解压到独立临时目录，返回目录路径。
// 调用方负责在进程退出后 os.RemoveAll。
func extractNatives(jars []string) (string, error) {
	dir, err := os.MkdirTemp("", "mtl-natives-*")
	if err != nil {
		return "", fmt.Errorf("创建 native 临时目录失败: %w", err)
	}
	cleanup := func() {
		os.RemoveAll(dir)
	}

	var total int64
	for _, jar := range jars {
		zr, err := zip.OpenReader(jar)
		if err != nil {
			cleanup()
			return "", fmt.Errorf("打开 native jar %s 失败: %w", jar, err)
		}
		err = extractNativeJar(zr, dir, &total)
		zr.Close()
		if err != nil {
			cleanup()
			return "", fmt.Errorf("解压 native jar %s 失败: %w", jar, err)
		}
	}
	return dir, nil
}

func extractNativeJar(zr *zip.ReadCloser, dir string, total *int64) error {
	for _, f := range zr.File {
		name := f.Name
		if name == "" || f.FileInfo().IsDir() {
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("拒绝符号链接条目 %q", name)
		}
		if !f.Mode().IsRegular() {
			return fmt.Errorf("拒绝特殊文件条目 %q", name)
		}
		if *total+int64(f.UncompressedSize64) > maxNativeBytes {
			return fmt.Errorf("native 解压总量超过上限 %d 字节", maxNativeBytes)
		}

		target, err := mrpack.SafeJoin(dir, name)
		if err != nil {
			return fmt.Errorf("非法条目 %q: %w", name, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.Create(target)
		if err != nil {
			rc.Close()
			return err
		}
		n, err := io.Copy(w, io.LimitReader(rc, maxNativeBytes-*total+1))
		rc.Close()
		w.Close()
		if err != nil {
			return err
		}
		*total += n
		if *total > maxNativeBytes {
			return fmt.Errorf("native 解压总量超过上限 %d 字节", maxNativeBytes)
		}
	}
	return nil
}
