package main

import (
	"io/fs"
	"net/http"
	"os"
	"strings"
)

// Dispatch only UI reads; the API owns all other paths and method errors.
func webHandler(api http.Handler, dir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) ||
			(r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/assets/")) {
			api.ServeHTTP(w, r)
			return
		}

		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if !fs.ValidPath(name) {
			http.NotFound(w, r)
			return
		}
		file, err := os.OpenInRoot(dir, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, name, info.ModTime(), file)
	})
}
