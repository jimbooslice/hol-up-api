package main

import (
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"hol-up-prototype/internal/httpapi"
	"hol-up-prototype/internal/realtime"
	"hol-up-prototype/internal/telemetry"
)

func TestWebFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"index.html":               "<!doctype html><title>Hol Up</title>",
		"assets/index-abc123.js":   "export const line = 'A';",
		"assets/index-abc123.css":  "body { color: blue; }",
		"assets/nested/index.html": "no directory index",
		"private.txt":              "not an asset",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside web root"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "assets", "escape.txt")); err != nil {
		t.Fatal(err)
	}
	api := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("UI request reached API") })
	handler := webHandler(api, dir)
	for _, tt := range []struct {
		path, body, mediaType string
		status                int
	}{
		{"/", "<!doctype html><title>Hol Up</title>", "text/html", 200},
		{"/?preview=1", "<!doctype html><title>Hol Up</title>", "text/html", 200},
		{"/assets/index-abc123.js", "export const line = 'A';", "text/javascript", 200},
		{"/assets/index-abc123.css", "body { color: blue; }", "text/css", 200},
		{"/assets/missing.js", "404 page not found\n", "text/plain", 404},
		{"/assets/", "404 page not found\n", "text/plain", 404},
		{"/assets/nested", "404 page not found\n", "text/plain", 404},
		{"/assets/nested/", "404 page not found\n", "text/plain", 404},
		{"/assets/../private.txt", "404 page not found\n", "text/plain", 404},
		{"/assets/%2e%2e/private.txt", "404 page not found\n", "text/plain", 404},
		{"/assets//index-abc123.js", "404 page not found\n", "text/plain", 404},
		{"/assets/escape.txt", "404 page not found\n", "text/plain", 404},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(method+" "+tt.path, func(t *testing.T) {
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, httptest.NewRequest(method, tt.path, nil))
				mediaType, _, err := mime.ParseMediaType(w.Header().Get("Content-Type"))
				if w.Code != tt.status || err != nil || mediaType != tt.mediaType || w.Header().Get("Location") != "" {
					t.Fatalf("response = %d %v, want %d %s without redirect", w.Code, w.Header(), tt.status, tt.mediaType)
				}
				if method == http.MethodGet && w.Body.String() != tt.body {
					t.Errorf("body = %q, want %q", w.Body.String(), tt.body)
				}
				if method == http.MethodHead && tt.status == 200 && w.Body.Len() != 0 {
					t.Errorf("HEAD file response has a body: %q", w.Body.String())
				}
			})
		}
	}
}

func TestMissingWebOutput(t *testing.T) {
	empty := t.TempDir()
	indexDirectory := t.TempDir()
	if err := os.Mkdir(filepath.Join(indexDirectory, "index.html"), 0o755); err != nil {
		t.Fatal(err)
	}
	api := httpapi.NewHandler(nil, telemetry.New(), time.Now, time.Minute, nil)
	for _, dir := range []string{filepath.Join(t.TempDir(), "absent"), empty, indexDirectory} {
		t.Run(dir, func(t *testing.T) {
			handler := webHandler(api, dir)
			for _, path := range []string{"/", "/assets/missing.js"} {
				if w := apiRequest(handler, path); w.Code != http.StatusNotFound {
					t.Errorf("%s status = %d, want 404", path, w.Code)
				}
			}
			if w := apiRequest(handler, "/healthz"); w.Code != http.StatusOK {
				t.Errorf("health status = %d, want 200", w.Code)
			}
		})
	}
}

func TestWebPreservesAPIResponses(t *testing.T) {
	base := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	for _, loaded := range []bool{false, true} {
		store := realtime.NewStore()
		if loaded {
			if err := store.Replace(realtime.Snapshot{SourceTimestamp: base, ReceivedAt: base, SourceName: "ace", Complete: true}); err != nil {
				t.Fatal(err)
			}
		}
		api := httpapi.NewHandler([]httpapi.Source{{Name: "ace", Routes: []string{"A"}, Store: store}}, telemetry.New("ace"), func() time.Time { return base }, time.Minute, nil)
		for _, dir := range []string{t.TempDir(), filepath.Join(t.TempDir(), "absent")} {
			handler := webHandler(api, dir)
			for _, path := range []string{
				"/healthz", "/readyz", "/metrics", "/openapi.yaml",
				"/api/v1/routes", "/api/v1/routes/A/geometry",
				"/api/v1/trains?route_id=A", "/api/v1/trains?route_id=A&view=positions",
				"/api/v1/trains", "/api/v1/trains?route_id=A&view=invalid",
				"/unknown", "/index.html", "/assets", "/private.txt",
				"//healthz", "/api//v1/routes", "/api/../healthz", "/api/v1/routes/", "/%68ealthz",
				"/", "/assets/index-abc123.js",
			} {
				for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodTrace, http.MethodConnect} {
					if (path == "/" || path == "/assets/index-abc123.js") && (method == http.MethodGet || method == http.MethodHead) {
						continue // These are the only requests owned by the UI.
					}
					t.Run(method+" "+path, func(t *testing.T) {
						want, got := httptest.NewRecorder(), httptest.NewRecorder()
						api.ServeHTTP(want, httptest.NewRequest(method, path, nil))
						handler.ServeHTTP(got, httptest.NewRequest(method, path, nil))
						if got.Code != want.Code || !reflect.DeepEqual(got.Header(), want.Header()) || got.Body.String() != want.Body.String() {
							t.Fatalf("wrapped = %d %v %s; original = %d %v %s", got.Code, got.Header(), got.Body.String(), want.Code, want.Header(), want.Body.String())
						}
					})
				}
			}
		}
	}
}
