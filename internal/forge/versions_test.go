package forge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLatestVersionUsesRecommended(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"promos":{
			"1.20.1-recommended":"47.4.28",
			"1.20.1-latest":"47.4.30",
			"1.20.1-old-version":"1"
		}}`)
	}))
	defer srv.Close()

	got, err := LatestVersion(context.Background(), "1.20.1", Options{
		PromotionsURL: srv.URL + "/promotions_slim.json",
		HTTPClient:    srv.Client(),
		Retries:       -1,
	})
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if got != "47.4.28" {
		t.Fatalf("应优先 recommended，实际 %q", got)
	}
}

func TestLatestVersionFallsBackToLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"promos":{"1.21-latest":"51.0.33"}}`)
	}))
	defer srv.Close()

	got, err := LatestVersion(context.Background(), "1.21", Options{
		PromotionsURL: srv.URL + "/promotions_slim.json",
		HTTPClient:    srv.Client(),
		Retries:       -1,
	})
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if got != "51.0.33" {
		t.Fatalf("没有 recommended 时应回落到 latest，实际 %q", got)
	}
}

func TestLatestVersionMissingIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"promos":{}}`)
	}))
	defer srv.Close()

	if _, err := LatestVersion(context.Background(), "1.21.8", Options{
		PromotionsURL: srv.URL + "/promotions_slim.json",
		HTTPClient:    srv.Client(),
		Retries:       -1,
	}); err == nil {
		t.Fatal("没有可用版本时应返回错误")
	}
}

func TestSupportsMinecraftVersion(t *testing.T) {
	for _, v := range []string{"1.20.1", "1.21.8"} {
		if !SupportsMinecraftVersion(v) {
			t.Fatalf("应支持 %s", v)
		}
	}
	for _, v := range []string{"1.19.2", "1.20", "24w14a"} {
		if SupportsMinecraftVersion(v) {
			t.Fatalf("不应支持 %s", v)
		}
	}
}
