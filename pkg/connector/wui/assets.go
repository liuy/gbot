package wui

import (
	"embed"
	"io"
	"net/http"
)

//go:embed all:assets
var embeddedAssets embed.FS

// RegisterStaticRoutes mounts the SPA at mux root. All routes serve the
// single pre-gzip compressed index.html (JS+CSS inlined by vite-plugin-singlefile).
func RegisterStaticRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		f, err := embeddedAssets.Open("assets/index.html.gz")
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		defer f.Close()
		if _, err := io.Copy(w, f); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	})
}
