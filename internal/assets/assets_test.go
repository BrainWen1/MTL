package assets

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testSHA1(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestObjectRelPath(t *testing.T) {
	hash := strings.Repeat("ab", 20)
	if got, want := objectRelPath(hash), "objects/ab/"+hash; got != want {
		t.Fatalf("objectRelPath = %q, want %q", got, want)
	}
}

func TestBuildAssetFiles(t *testing.T) {
	hashA := strings.Repeat("ab", 20)
	hashB := strings.Repeat("cd", 20)
	idx := assetIndex{Objects: map[string]assetObject{
		"minecraft/sounds/a.ogg": {Hash: hashA, Size: 123},
		"alias.ogg":              {Hash: strings.ToUpper(hashA), Size: 123},
		"other.txt":              {Hash: hashB, Size: 7},
	}}

	files, pathToKey, err := buildAssetFiles(idx, "https://resources.download.minecraft.net")
	if err != nil {
		t.Fatalf("buildAssetFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("同一 hash 应去重，files = %d", len(files))
	}

	byPath := make(map[string]int)
	for i, f := range files {
		byPath[f.Path] = i
	}
	rel := "objects/ab/" + hashA
	idxA, ok := byPath[rel]
	if !ok {
		t.Fatalf("缺少路径 %q: %v", rel, byPath)
	}
	f := files[idxA]
	if f.Hashes.SHA1 != hashA {
		t.Fatalf("SHA1 = %q", f.Hashes.SHA1)
	}
	if f.FileSize != 123 {
		t.Fatalf("Size = %d", f.FileSize)
	}
	wantURL := "https://resources.download.minecraft.net/ab/" + hashA
	if len(f.Downloads) != 1 || f.Downloads[0] != wantURL {
		t.Fatalf("Downloads = %v, want %s", f.Downloads, wantURL)
	}
	if got := pathToKey[rel]; got != "alias.ogg" {
		t.Fatalf("去重后应保留排序靠前的资源名，得到 %q", got)
	}

	relB := "objects/cd/" + hashB
	if _, ok := byPath[relB]; !ok {
		t.Fatalf("缺少路径 %q: %v", relB, byPath)
	}
}

func TestBuildAssetFilesRejectsBadHash(t *testing.T) {
	for _, h := range []string{"", "xyz", strings.Repeat("a", 39), strings.Repeat("a", 42)} {
		_, _, err := buildAssetFiles(assetIndex{Objects: map[string]assetObject{
			"bad": {Hash: h},
		}}, defaultObjectsBase)
		if err == nil {
			t.Fatalf("hash %q 应被拒绝", h)
		}
	}
}

func TestDefaultDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		appData := filepath.Join(t.TempDir(), "AppData", "Roaming")
		t.Setenv("APPDATA", appData)
		got, err := DefaultDir()
		if err != nil {
			t.Fatalf("DefaultDir: %v", err)
		}
		want := filepath.Join(appData, "mtl", "cache", "assets")
		if got != want {
			t.Fatalf("DefaultDir = %q, want %q", got, want)
		}
		return
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir: %v", err)
	}
	want := filepath.Join(home, ".cache", "mtl", "assets")
	if got != want {
		t.Fatalf("DefaultDir = %q, want %q", got, want)
	}
}

func TestEnsureDownloadsIndexAndObjects(t *testing.T) {
	const bodyA, bodyB = "aaa", "bbb"
	hashA, hashB := testSHA1(bodyA), testSHA1(bodyB)
	indexJSON := fmt.Sprintf(`{"objects":{"a.ogg":{"hash":%q,"size":3},"b.txt":{"hash":%q,"size":3}}}`, hashA, hashB)
	indexSHA := testSHA1(indexJSON)

	var indexHits, hitsA, hitsB int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index/5.json":
			atomic.AddInt32(&indexHits, 1)
			w.Write([]byte(indexJSON))
		case "/objects/" + hashA[:2] + "/" + hashA:
			atomic.AddInt32(&hitsA, 1)
			w.Write([]byte(bodyA))
		case "/objects/" + hashB[:2] + "/" + hashB:
			atomic.AddInt32(&hitsB, 1)
			w.Write([]byte(bodyB))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	var mu sync.Mutex
	var logs []string
	opts := Options{
		Dir:            dir,
		ObjectsBaseURL: srv.URL + "/objects/",
		Retries:        -1,
		RetryDelay:     time.Millisecond,
		Progress: func(msg string) {
			mu.Lock()
			logs = append(logs, msg)
			mu.Unlock()
		},
	}
	spec := IndexSpec{ID: "5", URL: srv.URL + "/index/5.json", SHA1: indexSHA, Size: int64(len(indexJSON))}

	if err := Ensure(context.Background(), spec, opts); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for hash, body := range map[string]string{hashA: bodyA, hashB: bodyB} {
		p := filepath.Join(dir, "objects", hash[:2], hash)
		got, err := os.ReadFile(p)
		if err != nil || string(got) != body {
			t.Fatalf("资源 %s 内容错误: %v, %q", hash, err, got)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "indexes", "5.json")); err != nil {
		t.Fatalf("assetIndex 未保存: %v", err)
	}

	joined := strings.Join(logs, "\n")
	for _, want := range []string{
		"正在拉取资源索引：5",
		"开始下载资源：a.ogg",
		"开始下载资源：b.txt",
		"资源下载完成，校验通过",
		"全部assets资源就绪",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("日志缺少 %q:\n%s", want, joined)
		}
	}

	// 二次执行应全部跳过且不再发起请求。
	if err := Ensure(context.Background(), spec, opts); err != nil {
		t.Fatalf("二次 Ensure: %v", err)
	}
	mu.Lock()
	joined = strings.Join(logs, "\n")
	mu.Unlock()
	if strings.Count(joined, "已存在，跳过：") < 2 {
		t.Fatalf("二次执行缺少跳过日志:\n%s", joined)
	}
	if indexHits != 1 || hitsA != 1 || hitsB != 1 {
		t.Fatalf("请求次数 index=%d a=%d b=%d, want 1/1/1", indexHits, hitsA, hitsB)
	}
}

func TestEnsureRejectsInvalidSpecBeforeNetwork(t *testing.T) {
	cases := []IndexSpec{
		{},
		{ID: "../escape", URL: "https://example.com/i.json", SHA1: strings.Repeat("a", 40)},
		{ID: "5", URL: "file:///etc/passwd", SHA1: strings.Repeat("a", 40)},
		{ID: "5", URL: "https://example.com/i.json", SHA1: "bad"},
	}
	for _, spec := range cases {
		if err := Ensure(context.Background(), spec, Options{Dir: t.TempDir()}); err == nil {
			t.Fatalf("spec %+v 应被拒绝", spec)
		}
	}
}
