package webchat

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:assets
var embeddedAssets embed.FS

func assetFS() fs.FS {
	sub, err := fs.Sub(embeddedAssets, "assets")
	if err != nil {
		panic("webchat: assets subdir missing: " + err.Error())
	}
	return sub
}

// RegisterStaticRoutes mounts the SPA at mux root. Serves index.html for any
// path that doesn't match a real asset (SPA client-side routing fallback).
func RegisterStaticRoutes(mux *http.ServeMux) {
	fileServer := http.FileServer(http.FS(assetFS()))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			fileServer.ServeHTTP(w, r)
			return
		}
		if info, err := fs.Stat(assetFS(), path); err != nil || info.IsDir() {
			r2 := *r
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, &r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
