package downloader

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mtl/internal/mrpack"
)

func sha512Hex(b []byte) string {
	sum := sha512.Sum512(b)
	return hex.EncodeToString(sum[:])
}

func packFile(path, body string, urls ...string) mrpack.File {
	return mrpack.File{
		Path:      path,
		Hashes:    mrpack.Hashes{SHA512: sha512Hex([]byte(body))},
		Downloads: urls,
		FileSize:  int64(len(body)),
	}
}

func serveFiles(t *testing.T, bodies map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func updateMaxInt32(dst *int32, v int32) {
	for {
		old := atomic.LoadInt32(dst)
		if v <= old || atomic.CompareAndSwapInt32(dst, old, v) {
			return
		}
	}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	var found []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), ".mtl-dl-") {
			found = append(found, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历目录: %v", err)
	}
	if len(found) > 0 {
		t.Fatalf("残留临时文件: %v", found)
	}
}

func TestDownloadVerifiesAndWrites(t *testing.T) {
	bodies := map[string]string{"/a": "alpha content", "/b": "beta content"}
	srv := serveFiles(t, bodies)
	dir := t.TempDir()
	files := []mrpack.File{
		packFile("mods/a.jar", bodies["/a"], srv.URL+"/a"),
		packFile("config/nested/b.txt", bodies["/b"], srv.URL+"/b"),
	}

	results, err := Download(context.Background(), dir, files, Options{Concurrency: 2})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("结果数 = %d", len(results))
	}
	for i, want := range []string{"alpha content", "beta content"} {
		if results[i].Skipped {
			t.Fatalf("%s 不应被跳过", files[i].Path)
		}
		got, err := os.ReadFile(results[i].Target)
		if err != nil {
			t.Fatalf("读取 %s: %v", files[i].Path, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", files[i].Path, got, want)
		}
	}
	assertNoTempFiles(t, dir)
}

func TestDownloadRejectsHashMismatch(t *testing.T) {
	srv := serveFiles(t, map[string]string{"/bad": "tampered"})
	dir := t.TempDir()
	f := packFile("mods/bad.jar", "expected", srv.URL+"/bad")

	_, err := Download(context.Background(), dir, []mrpack.File{f}, Options{Retries: -1})
	if err == nil {
		t.Fatal("哈希不符应报错")
	}
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("错误应包含 ErrHashMismatch: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "mods", "bad.jar")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("校验失败的文件不应出现在目标路径")
	}
	assertNoTempFiles(t, dir)
}

func TestDownloadRetriesThenSucceeds(t *testing.T) {
	const body = "ok"
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) < 3 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	f := packFile("mods/retry.jar", body, srv.URL+"/f")
	results, err := Download(context.Background(), dir, []mrpack.File{f}, Options{Retries: 3, RetryDelay: time.Millisecond})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("请求次数 = %d, want 3", got)
	}
	if _, err := os.ReadFile(results[0].Target); err != nil {
		t.Fatalf("文件未落盘: %v", err)
	}
}

func TestDownloadFallsBackToMirror(t *testing.T) {
	const body = "mirror"
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.URL.Path != "/good" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	f := packFile("mods/mirror.jar", body, srv.URL+"/bad", srv.URL+"/good")
	if _, err := Download(context.Background(), dir, []mrpack.File{f}, Options{Retries: 1, RetryDelay: time.Millisecond}); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("请求次数 = %d, want 2", got)
	}
}

func TestDownloadReportsExhaustedRetries(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	f := packFile("mods/fail.jar", "x", srv.URL+"/f")
	_, err := Download(context.Background(), dir, []mrpack.File{f}, Options{Retries: 2, RetryDelay: time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "共尝试 3 次") {
		t.Fatalf("错误信息应包含尝试次数: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("请求次数 = %d, want 3", got)
	}
	assertNoTempFiles(t, dir)
}

func TestDownloadSkipsValidExistingFile(t *testing.T) {
	const body = "cached"
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	target := filepath.Join(dir, "mods", "cached.jar")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := Download(context.Background(), dir, []mrpack.File{packFile("mods/cached.jar", body, srv.URL+"/f")}, Options{})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !results[0].Skipped {
		t.Fatal("哈希正确的已存在文件应被跳过")
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("不应发起请求，实际 %d 次", got)
	}
}

func TestDownloadReplacesCorruptExistingFile(t *testing.T) {
	const body = "fresh"
	srv := serveFiles(t, map[string]string{"/f": body})
	dir := t.TempDir()
	target := filepath.Join(dir, "mods", "a.jar")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := Download(context.Background(), dir, []mrpack.File{packFile("mods/a.jar", body, srv.URL+"/f")}, Options{})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if results[0].Skipped {
		t.Fatal("哈希不符的已存在文件不应被跳过")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("文件内容 = %q, want %q", got, body)
	}
	assertNoTempFiles(t, dir)
}

func TestDownloadRejectsUnsafePath(t *testing.T) {
	srv := serveFiles(t, map[string]string{"/f": "x"})
	dir := t.TempDir()
	for _, p := range []string{"../escape.jar", "mods/../../escape.jar", "/abs.jar", "C:/escape.jar", `mods\evil.jar`} {
		t.Run(p, func(t *testing.T) {
			_, err := Download(context.Background(), dir, []mrpack.File{packFile(p, "x", srv.URL+"/f")}, Options{})
			if err == nil {
				t.Fatal("应当拒绝该路径")
			}
			if _, statErr := os.Stat(filepath.Join(filepath.Dir(dir), "escape.jar")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatal("文件逃出了实例目录")
			}
		})
	}
}

func TestDownloadConcurrencyLimit(t *testing.T) {
	var inFlight, maxInFlight int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		updateMaxInt32(&maxInFlight, atomic.AddInt32(&inFlight, 1))
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		io.WriteString(w, "chunk")
	}))
	t.Cleanup(srv.Close)

	const n = 6
	files := make([]mrpack.File, n)
	for i := range files {
		files[i] = packFile("mods/f"+string(rune('0'+i))+".jar", "chunk", srv.URL+"/f"+string(rune('0'+i)))
	}
	results, err := Download(context.Background(), t.TempDir(), files, Options{Concurrency: 2, RetryDelay: time.Millisecond})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if len(results) != n {
		t.Fatalf("结果数 = %d, want %d", len(results), n)
	}
	if got := atomic.LoadInt32(&maxInFlight); got > 2 {
		t.Fatalf("并发峰值 = %d, 超过限制 2", got)
	}
}

func TestDownloadReportsProgress(t *testing.T) {
	bodies := map[string]string{"/a": "A", "/b": "B", "/c": "C"}
	srv := serveFiles(t, bodies)
	dir := t.TempDir()
	files := []mrpack.File{
		packFile("mods/a.jar", bodies["/a"], srv.URL+"/a"),
		packFile("mods/b.jar", bodies["/b"], srv.URL+"/b"),
		packFile("mods/c.jar", bodies["/c"], srv.URL+"/c"),
	}

	var (
		mu        sync.Mutex
		paths     []string
		dones     []int
		active    int32
		maxActive int32
	)
	_, err := Download(context.Background(), dir, files, Options{
		Concurrency: 3,
		Progress: func(done, total int, res Result) {
			updateMaxInt32(&maxActive, atomic.AddInt32(&active, 1))
			time.Sleep(time.Millisecond) // 若未串行化，回调会重叠
			mu.Lock()
			paths = append(paths, res.File.Path)
			dones = append(dones, done)
			mu.Unlock()
			atomic.AddInt32(&active, -1)
			if total != len(files) {
				t.Errorf("total = %d, want %d", total, len(files))
			}
		},
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got := atomic.LoadInt32(&maxActive); got != 1 {
		t.Fatalf("回调并发度 = %d, 应为 1", got)
	}
	if len(paths) != len(files) {
		t.Fatalf("回调次数 = %d, want %d", len(paths), len(files))
	}
	for i, d := range dones {
		if d != i+1 {
			t.Fatalf("dones[%d] = %d, want %d", i, d, i+1)
		}
	}
	seen := make(map[string]bool)
	for _, p := range paths {
		if seen[p] {
			t.Fatalf("%s 被重复上报", p)
		}
		seen[p] = true
	}
}

func TestDownloadCanceled(t *testing.T) {
	srv := serveFiles(t, map[string]string{"/f": "x"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Download(ctx, t.TempDir(), []mrpack.File{packFile("mods/a.jar", "x", srv.URL+"/f")}, Options{Retries: -1})
	if err == nil {
		t.Fatal("已取消的 ctx 应返回错误")
	}
}

func TestDownloadEmptyList(t *testing.T) {
	results, err := Download(context.Background(), t.TempDir(), nil, Options{})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("结果数 = %d", len(results))
	}
}
