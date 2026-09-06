package wui

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"log/slog"
	"net/http"
	"sync"
)

// The plain index.html is embedded (JS+CSS already inlined by
// vite-plugin-singlefile). The .gz build artifact stays on disk for
// deployers who precompress, but is not embedded: the narrow plaintext embed
// keeps fresh clones servable — go build alone yields a working binary.
// Locale lives in localStorage and is resolved client-side, so the body is
// identical for every request: compress once, serve forever.
//
//go:embed assets/index.html
var indexHTML []byte

// gzipIndex compresses the embedded page exactly once; bytes.Buffer writes
// cannot fail, but the error path is kept so a future source change cannot
// silently serve an empty body.
var gzipIndex = sync.OnceValues(func() ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(indexHTML); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
})

// RegisterStaticRoutes mounts the SPA at mux root. Every path serves the
// single-file index.html, gzip-compressed.
func RegisterStaticRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		body, err := gzipIndex()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if _, err := w.Write(body); err != nil {
			// The 200+gzip headers are already committed, so http.Error would
			// be silently dropped — the client is gone; only the log remains.
			slog.Warn("wui: static index write failed", "error", err)
		}
	})
}
