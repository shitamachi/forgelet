package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestStaticConsoleRouting(t *testing.T) {
	files := fstest.MapFS{"index.html": {Data: []byte("<html>forgelet</html>")}, "assets/main-abcd.js": {Data: []byte("export const console = true;")}, "assets/.private": {Data: []byte("private")}}
	for _, tc := range []struct {
		method, path string
		status       int
		cache, body  string
	}{
		{"GET", "/", 200, "no-store", "<html>forgelet</html>"},
		{"GET", "/runs/run-id?job=job-id", 200, "no-store", "<html>forgelet</html>"},
		{"HEAD", "/deployments/id", 200, "no-store", ""},
		{"GET", "/assets/main-abcd.js", 200, "public, max-age=31536000, immutable", "export const console = true;"},
		{"GET", "/assets/missing.js", 404, "", "404 page not found\n"},
		{"GET", "/assets/.private", 404, "", "404 page not found\n"},
		{"GET", "/assets/../index.html", 404, "", "404 page not found\n"},
		{"GET", "/assets/", 404, "", "404 page not found\n"},
		{"GET", "/config.yaml", 404, "", "404 page not found\n"},
		{"GET", "/api/unknown", 404, "", "404 page not found\n"},
		{"GET", "/internal/unknown", 404, "", "404 page not found\n"},
		{"GET", "/webhooks/unknown", 404, "", "404 page not found\n"},
		{"POST", "/runs/id", 404, "", "404 page not found\n"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler(files).ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil))
			if w.Code != tc.status || w.Body.String() != tc.body || w.Header().Get("Cache-Control") != tc.cache {
				t.Fatalf("response: %d %q %v", w.Code, w.Body.String(), w.Header())
			}
			if tc.status == http.StatusOK && (!strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") || w.Header().Get("X-Content-Type-Options") != "nosniff") {
				t.Fatal("missing content isolation headers")
			}
		})
	}
}

func TestMissingBuildIsExplicit(t *testing.T) {
	w := httptest.NewRecorder()
	handler(fstest.MapFS{}).ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "GET", "/", nil))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "make web-build") {
		t.Fatalf("missing build: %d %s", w.Code, w.Body.String())
	}
}
