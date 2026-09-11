package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	testingfs "testing/fstest"
)

func testAssets() fs.FS {
	return testingfs.MapFS{
		"index.html":     &testingfs.MapFile{Data: []byte("<!doctype html><div id=app></div>")},
		"assets/app.js":  &testingfs.MapFile{Data: []byte("console.log('ok')")},
		"assets/app.css": &testingfs.MapFile{Data: []byte("body{}")},
	}
}

func TestSPAFallbackAndAssetRules(t *testing.T) {
	handler := NewHandler(testAssets())
	for _, path := range []string{"/", "/index.html", "/setup", "/login", "/overview", "/about"} {
		record := httptest.NewRecorder()
		handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, path, nil))
		if record.Code != http.StatusOK || !strings.Contains(record.Body.String(), "id=app") || record.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("route %s = %d headers=%v body=%q", path, record.Code, record.Header(), record.Body.String())
		}
	}
	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if asset.Code != http.StatusOK || !strings.Contains(asset.Header().Get("Content-Type"), "javascript") || asset.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("asset = %d headers=%v", asset.Code, asset.Header())
	}
	missingAsset := httptest.NewRecorder()
	handler.ServeHTTP(missingAsset, httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil))
	if missingAsset.Code != http.StatusNotFound || strings.Contains(missingAsset.Body.String(), "id=app") {
		t.Fatalf("missing asset = %d %q", missingAsset.Code, missingAsset.Body.String())
	}
}

func TestSPADoesNotConsumeAPIOrProbePaths(t *testing.T) {
	handler := NewHandler(testAssets())
	for _, path := range []string{"/api/v2/not-found", "/api/v1/health", "/healthz", "/readyz"} {
		record := httptest.NewRecorder()
		handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, path, nil))
		if record.Code != http.StatusNotFound || !strings.Contains(record.Header().Get("Content-Type"), "application/json") || strings.Contains(record.Body.String(), "id=app") {
			t.Fatalf("protected path %s = %d headers=%v body=%q", path, record.Code, record.Header(), record.Body.String())
		}
	}
}
