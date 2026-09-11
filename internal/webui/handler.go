// Package webui serves the committed Vite build without requiring Node at
// runtime. The API and health paths are deliberately excluded from SPA
// fallback so a missing API route cannot be mistaken for index.html.
package webui

import (
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"strings"
)

// The committed dist directory is produced by `npm run build` in web/.
// Keeping it in the repository makes ordinary pure-Go builds deterministic.
//
//go:embed dist
var embedded embed.FS

var browserRoutes = map[string]struct{}{
	"/":             {},
	"/setup":        {},
	"/login":        {},
	"/overview":     {},
	"/observations": {},
	"/about":        {},
}

func Handler() http.Handler {
	assets, err := fs.Sub(embedded, "dist")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "dashboard unavailable", http.StatusServiceUnavailable)
		})
	}
	return NewHandler(assets)
}

// NewHandler is exported for deterministic handler tests and keeps the
// serving rules independent from embed.FS details.
func NewHandler(assets fs.FS) http.Handler {
	files := http.FS(assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if isAPIOrProbePath(r.URL.Path) {
			writeJSONNotFound(w)
			return
		}
		if isBrowserRoute(r.URL.Path) || r.URL.Path == "/index.html" {
			serveIndex(w, r, files)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" || !fs.ValidPath(name) {
			http.NotFound(w, r)
			return
		}
		info, err := fs.Stat(assets, name)
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.FileServer(files).ServeHTTP(w, r)
	})
}

func isBrowserRoute(path string) bool {
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		path = "/"
	}
	_, ok := browserRoutes[path]
	return ok
}

func isAPIOrProbePath(path string) bool {
	return path == "/healthz" || path == "/readyz" || path == "/api" || strings.HasPrefix(path, "/api/")
}

func serveIndex(w http.ResponseWriter, r *http.Request, files http.FileSystem) {
	w.Header().Set("Cache-Control", "no-store")
	file, err := files.Open("/index.html")
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusServiceUnavailable)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, file)
}

func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
}

func writeJSONNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":      map[string]string{"code": "not_found", "message": "resource not found"},
		"request_id": "webui-not-found",
	})
}
