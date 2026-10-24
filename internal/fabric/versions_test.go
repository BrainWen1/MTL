package fabric

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMinecraftVersions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			fmt.Fprint(w, `{"versions":[
				{"id":"1.21.8","type":"release"},
				{"id":"1.21.7","type":"release"},
				{"id":"24w14a","type":"snapshot"},
				{"id":"../evil","type":"release"},
				{"id":"","type":"release"}
			]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	versions, err := MinecraftVersions(context.Background(), Options{
		MojangManifestURL: srv.URL + "/manifest.json",
		HTTPClient:        srv.Client(),
		Retries:           -1,
	})
	if err != nil {
		t.Fatalf("MinecraftVersions: %v", err)
	}
	if len(versions) != 3 {
		t.Fatalf("应跳过非法条目，实际 %+v", versions)
	}
	if versions[0].ID != "1.21.8" || versions[0].Type != "release" {
		t.Fatalf("版本顺序/字段错误: %+v", versions)
	}
}

func TestMinecraftVersionsEmptyIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"versions":[]}`)
	}))
	defer srv.Close()

	if _, err := MinecraftVersions(context.Background(), Options{
		MojangManifestURL: srv.URL + "/manifest.json",
		HTTPClient:        srv.Client(),
		Retries:           -1,
	}); err == nil {
		t.Fatal("空版本清单应返回错误")
	}
}

func TestLatestLoaderVersionPrefersStable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/versions/loader/1.20.1" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `[
			{"loader":{"version":"0.16.0","stable":false}},
			{"loader":{"version":"0.15.11","stable":true}},
			{"loader":{"version":"0.15.10","stable":true}}
		]`)
	}))
	defer srv.Close()

	got, err := LatestLoaderVersion(context.Background(), "1.20.1", Options{
		MetaBaseURL: srv.URL,
		HTTPClient:  srv.Client(),
		Retries:     -1,
	})
	if err != nil {
		t.Fatalf("LatestLoaderVersion: %v", err)
	}
	if got != "0.15.11" {
		t.Fatalf("应选择最新稳定版 0.15.11，实际 %q", got)
	}
}

func TestLatestLoaderVersionFallsBackToFirst(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"loader":{"version":"0.16.0","stable":false}}]`)
	}))
	defer srv.Close()

	got, err := LatestLoaderVersion(context.Background(), "1.21.8", Options{
		MetaBaseURL: srv.URL,
		HTTPClient:  srv.Client(),
		Retries:     -1,
	})
	if err != nil {
		t.Fatalf("LatestLoaderVersion: %v", err)
	}
	if got != "0.16.0" {
		t.Fatalf("没有 stable 时应回落到第一个版本，实际 %q", got)
	}
}

func TestLatestLoaderVersionRejectsBadInput(t *testing.T) {
	if _, err := LatestLoaderVersion(context.Background(), "../1.20.1", Options{Retries: -1}); err == nil {
		t.Fatal("包含路径分隔符的版本应被拒绝")
	}
}
