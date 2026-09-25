package wui

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
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

// Prebundled noVNC (esbuild, TLA-safe ESM, software-decode patched).
// vite's emptyOutDir is disabled so a rebuild cannot wipe it; regenerated
// via `npm run build:novnc`, and `npm run check:novnc` (wired into
// `make web-check`) proves the committed bytes are current. Note that
// disabling emptyOutDir means any other stale file in assets/ survives a
// build too — assets/ must stay limited to tracked artifacts.
//
//go:embed assets/novnc.esm.js
var novncESM []byte

// @google/model-viewer dist bundle for the artifact glTF viewer (its
// built-in studio lighting and camera controls replace any hand-built
// rig). Same embed/gzip lifecycle as the other prebundles.
//
//go:embed assets/model-viewer.esm.js
var modelViewerESM []byte

// wuiAssetHash fingerprints the UNCOMPRESSED page: gzip headers carry
// MTIME, so hashing the compressed bytes would flap every build even when
// content is unchanged. Sent on every WS connection (metadata's connect
// payload); after a daemon upgrade (tableflip/REUSEPORT) the reconnecting
// client compares it against its boot snapshot and hard-reloads to pick up
// the new bundle. The embed is immutable for the process lifetime, so
// OnceValue keeps concurrent connections race-free without re-hashing.
var wuiAssetHash = sync.OnceValue(func() string {
	sum := sha256.Sum256(indexHTML)
	return hex.EncodeToString(sum[:])
})

// gzipIndex compresses the embedded page exactly once; bytes.Buffer writes
// cannot fail, but the error path is kept so a future source change cannot
// silently serve an empty body.
var gzipIndex = sync.OnceValues(func() ([]byte, error) {
	return gzipBytes(indexHTML)
})

// gzipNovnc mirrors gzipIndex: the prebundle is ~185 KB and, with
// Cache-Control: no-store, is re-fetched on every console open — including
// across the paired-device proxy.
var gzipNovnc = sync.OnceValues(func() ([]byte, error) {
	return gzipBytes(novncESM)
})

var gzipModelViewer = sync.OnceValues(func() ([]byte, error) {
	return gzipBytes(modelViewerESM)
})

func gzipBytes(src []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(src); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RegisterStaticRoutes mounts the SPA at mux root. Every path except the
// exact prebundle paths (noVNC, model-viewer) serves the single-file
// index.html, gzip-compressed.
func RegisterStaticRoutes(mux *http.ServeMux) {
	// Exact-path pattern wins over the "/" SPA catch-all below. "GET" also
	// matches HEAD (Go 1.22 method patterns), so probes get the headers.
	mux.HandleFunc("GET /assets/novnc.esm.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Content-Encoding", "gzip")

		body, err := gzipNovnc()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if _, err := w.Write(body); err != nil {
			slog.Warn("wui: novnc.esm.js write failed", "error", err)
		}
	})
	mux.HandleFunc("GET /assets/model-viewer.esm.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Content-Encoding", "gzip")

		body, err := gzipModelViewer()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if _, err := w.Write(body); err != nil {
			slog.Warn("wui: model-viewer.esm.js write failed", "error", err)
		}
	})
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
