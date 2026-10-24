package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"mtl/internal/mrpack"
)

// TestDownloadResumesPartialFile 模拟 Ctrl+C 后残留 *.tmp：服务端支持 Range 时
// 只请求剩余字节，并校验最终文件。
func TestDownloadResumesPartialFile(t *testing.T) {
	const body = "0123456789abcdefghijklmnopqrstuvwxyz"
	const offset = 10
	var getHits, headHits int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			atomic.AddInt32(&headHits, 1)
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			atomic.AddInt32(&getHits, 1)
			if got := r.Header.Get("Range"); got != fmt.Sprintf("bytes=%d-", offset) {
				t.Errorf("Range = %q, want bytes=%d-", got, offset)
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, len(body)-1, len(body)))
			w.Header().Set("Content-Length", strconv.Itoa(len(body)-offset))
			w.WriteHeader(http.StatusPartialContent)
			io.WriteString(w, body[offset:])
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	target := filepath.Join(dir, "mods", "resume.jar")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+".tmp", []byte(body[:offset]), 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := Download(context.Background(), dir,
		[]mrpack.File{packFile("mods/resume.jar", body, srv.URL+"/f")},
		Options{Retries: -1, RetryDelay: time.Millisecond})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !results[0].Resumed {
		t.Fatalf("应标记为断点续传: %+v", results[0])
	}
	if got := atomic.LoadInt32(&headHits); got != 1 {
		t.Fatalf("HEAD 探测次数 = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&getHits); got != 1 {
		t.Fatalf("GET 次数 = %d, want 1", got)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取目标文件: %v", err)
	}
	if string(got) != body {
		t.Fatalf("续传内容错误: %q", got)
	}
	if _, err := os.Stat(target + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("续传成功后不应残留 .tmp: %v", err)
	}
}

// TestDownloadRestartsWhenRangeUnsupported 验证服务端不支持 Range 时从头重下，
// 而不是报错或写出拼接损坏的文件。
func TestDownloadRestartsWhenRangeUnsupported(t *testing.T) {
	const body = "restart-from-scratch"
	var getHits int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			// 不声明 Accept-Ranges，模拟不支持续传的源站。
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			return
		}
		atomic.AddInt32(&getHits, 1)
		if got := r.Header.Get("Range"); got != "" {
			t.Errorf("服务端不支持 Range 时不应发送 Range，实际 %q", got)
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	target := filepath.Join(dir, "mods", "restart.jar")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+".tmp", []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := Download(context.Background(), dir,
		[]mrpack.File{packFile("mods/restart.jar", body, srv.URL+"/f")},
		Options{Retries: -1})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !results[0].RangeUnsupported || results[0].Resumed {
		t.Fatalf("结果应标记 RangeUnsupported 且未续传: %+v", results[0])
	}
	if got := atomic.LoadInt32(&getHits); got != 1 {
		t.Fatalf("GET 次数 = %d, want 1", got)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != body {
		t.Fatalf("完整重下内容错误: %v, %q", err, got)
	}
}

// TestDownloadAggregatesFailures 验证单个文件失败不会中止其余文件，
// 并通过 BatchError 汇总失败明细。
func TestDownloadAggregatesFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			io.WriteString(w, "ok-body")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	files := []mrpack.File{
		packFile("mods/ok.jar", "ok-body", srv.URL+"/ok"),
		packFile("mods/missing.jar", "missing", srv.URL+"/missing"),
	}
	results, err := Download(context.Background(), dir, files, Options{Retries: -1, RetryDelay: time.Millisecond})
	if err == nil {
		t.Fatal("存在失败文件时应返回汇总错误")
	}
	var batch *BatchError
	if !errors.As(err, &batch) {
		t.Fatalf("错误类型 = %T, want *BatchError", err)
	}
	if batch.FailedCount() != 1 || batch.Total != 2 {
		t.Fatalf("BatchError = %+v", batch)
	}
	if batch.Failed[0].Kind != KindNetwork {
		t.Fatalf("失败分类 = %q, want %q", batch.Failed[0].Kind, KindNetwork)
	}
	if results[0].Err != nil {
		t.Fatalf("成功文件不应带错误: %v", results[0].Err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "mods", "ok.jar")); err != nil || string(got) != "ok-body" {
		t.Fatalf("其他文件应继续下载: %v, %q", err, got)
	}
}

// TestMirrorsPreferAndFallback 验证镜像优先，镜像失败后自动回退官方原始地址。
func TestMirrorsPreferAndFallback(t *testing.T) {
	newOrigin := func(body string, hits *int32) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(hits, 1)
			io.WriteString(w, body)
		}))
		t.Cleanup(srv.Close)
		return srv
	}

	t.Run("镜像优先", func(t *testing.T) {
		var mirrorHits, originHits int32
		mirror := newOrigin("mirror-body", &mirrorHits)
		origin := newOrigin("origin-body", &originHits)

		data, err := GetBytes(context.Background(), origin.URL+"/meta.json", 1024, RequestOptions{
			Retries: 0,
			Mirrors: Mirrors{Mod: mirror.URL},
		})
		if err != nil {
			t.Fatalf("GetBytes: %v", err)
		}
		if string(data) != "mirror-body" {
			t.Fatalf("内容 = %q, want mirror-body", data)
		}
		if mirrorHits != 1 || originHits != 0 {
			t.Fatalf("请求次数 mirror=%d origin=%d, want 1/0", mirrorHits, originHits)
		}
	})

	t.Run("镜像失败回退官方", func(t *testing.T) {
		var mirrorHits, originHits int32
		mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&mirrorHits, 1)
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		t.Cleanup(mirror.Close)
		origin := newOrigin("origin-body", &originHits)

		data, err := GetBytes(context.Background(), origin.URL+"/meta.json", 1024, RequestOptions{
			Retries:    0,
			RetryDelay: time.Millisecond,
			Mirrors:    Mirrors{Mod: mirror.URL},
		})
		if err != nil {
			t.Fatalf("GetBytes: %v", err)
		}
		if string(data) != "origin-body" {
			t.Fatalf("内容 = %q, want origin-body", data)
		}
		if mirrorHits != 1 || originHits != 1 {
			t.Fatalf("请求次数 mirror=%d origin=%d, want 1/1", mirrorHits, originHits)
		}
	})
}

func TestRewriteURLKeepsResourcePath(t *testing.T) {
	got, err := RewriteURL("https://piston-meta.mojang.com/mc/game/version_manifest_v2.json?x=1",
		"https://mirror.example/mc/")
	if err != nil {
		t.Fatalf("RewriteURL: %v", err)
	}
	const want = "https://mirror.example/mc/mc/game/version_manifest_v2.json?x=1"
	if got != want {
		t.Fatalf("RewriteURL = %q, want %q", got, want)
	}
}

func TestMirrorsHostMapping(t *testing.T) {
	m := Mirrors{Mojang: "https://m.example", Mod: "https://mods.example", Fabric: "https://f.example"}
	cases := []struct {
		raw  string
		want string
	}{
		{"https://piston-meta.mojang.com/a.json", "https://m.example/a.json"},
		{"https://resources.download.minecraft.net/ab/abcd", "https://m.example/ab/abcd"},
		{"https://libraries.minecraft.net/g/a/1/a.jar", "https://m.example/g/a/1/a.jar"},
		{"https://cdn.modrinth.com/data/x.jar", "https://mods.example/data/x.jar"},
		{"https://maven.fabricmc.net/a/b.jar", "https://f.example/a/b.jar"},
	}
	for _, c := range cases {
		got := m.Candidates(c.raw)
		if len(got) != 2 || got[0] != c.want || got[1] != c.raw {
			t.Fatalf("Candidates(%q) = %v, want [%q %q]", c.raw, got, c.want, c.raw)
		}
	}

	// 未配置镜像时只返回原始地址。
	if got := (Mirrors{}).Candidates("https://x.example/a"); len(got) != 1 || got[0] != "https://x.example/a" {
		t.Fatalf("无镜像 Candidates = %v", got)
	}
}
