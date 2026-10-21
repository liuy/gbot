package wui

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
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

// The glTF decoder runtimes (draco mesh compression, KTX2/Basis texture
// transcoding) are vendored from three's examples/jsm/libs so the fork's
// default decoder locations point at this origin and a draco/KTX2 model
// never fetches gstatic. Exactly the files DRACOLoader/KTX2Loader request
// from a decoder path under WebAssembly; the pure-JS draco_decoder.js
// fallback is not vendored because every target running the wasm meshopt
// decoder also runs WebAssembly.
//
//go:embed assets/draco assets/basis
var decoderAssets embed.FS

// wuiAssetHash fingerprints the UNCOMPRESSED assets: gzip headers carry
// MTIME, so hashing the compressed bytes would flap every build even when
// content is unchanged. All three served assets participate — the ES module
// bundles live for the page's lifetime, so a bundle-only change (e.g.
// model-viewer) with an unchanged index.html must still flip the hash, or
// reconnecting clients never pick it up. Sent on every WS connection
// (metadata's connect payload); after a daemon upgrade (tableflip/REUSEPORT)
// the reconnecting client compares it against its boot snapshot and
// hard-reloads to pick up the new bundle. The embeds are immutable for the
// process lifetime, so OnceValue keeps concurrent connections race-free
// without re-hashing.
var wuiAssetHash = sync.OnceValue(func() string {
	h := sha256.New()
	h.Write(indexHTML)
	h.Write(novncESM)
	h.Write(modelViewerESM)
	return hex.EncodeToString(h.Sum(nil))
})

// gzipIndex compresses the embedded page exactly once; bytes.Buffer writes
// cannot fail, but the error path is kept so a future source change cannot
// silently serve an empty body.
var gzipIndex = sync.OnceValues(func() ([]byte, error) {
	return gzipBytes(indexHTML)
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

// embeddedGzips memoizes each embedded asset's compressed representation,
// keyed by the embed path the routes pass. The embeds are immutable for the
// process lifetime, so entries never invalidate and the map is bounded by
// the embed set (a few MB compressed) — unlike artifactGzipCache, which
// needs an LRU because disk artifacts come and go and a GLB alone is ~80 MB.
// It is a pointer so tests can swap in a cold map and count compressions
// from a known state (same discipline as resetArtifactGzipCache).
var embeddedGzips = &sync.Map{}

// embeddedGzipCompressions counts actual gzip passes over embedded assets
// (one per asset per process); the once-only behavior is asserted in tests.
var embeddedGzipCompressions atomic.Int64

// embeddedGzip returns the cached gzip representation of body, stored at
// most once per key. LoadOrStore keeps one canonical value even when two
// cold-start requests race: the loser's bytes are dropped in favor
// of the stored (byte-identical) winner.
func embeddedGzip(key string, body []byte) ([]byte, error) {
	if v, ok := embeddedGzips.Load(key); ok {
		return v.([]byte), nil
	}
	gz, err := gzipBytes(body)
	if err != nil {
		return nil, err
	}
	embeddedGzipCompressions.Add(1)
	actual, _ := embeddedGzips.LoadOrStore(key, gz)
	return actual.([]byte), nil
}

// serveEmbeddedAsset serves one process-immutable embedded asset with gzip
// negotiation. The ETag is derived from the UNCOMPRESSED bytes (gzip headers
// carry MTIME, so hashing compressed bytes would flap every build even for
// unchanged content): the identity representation quotes the bare hash, the
// gzip one prefixes it with "gz-" — the same per-encoding convention as
// serveArtifactGzipped, so a cached identity tag can never 304 a gzip body
// and vice versa. Vary rides on every response (200 and 304 alike) so a
// shared cache cannot hand gzip bytes to a client that never asked for them.
// The exact Content-Length keeps fetch progress working; HEAD gets the same
// headers with no body.
func serveEmbeddedAsset(w http.ResponseWriter, r *http.Request, name string, body []byte, contentType, cacheControl string) {
	sum := sha256.Sum256(body)
	tag := hex.EncodeToString(sum[:8])
	w.Header().Set("Vary", "Accept-Encoding")
	w.Header().Set("Cache-Control", cacheControl)

	gz := body
	etag := `"` + tag + `"`
	gzipOK := acceptsGzip(r)
	if gzipOK {
		etag = `"gz-` + tag + `"`
	}
	w.Header().Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if gzipOK {
		var err error
		gz, err = embeddedGzip(name, body)
		if err != nil {
			w.Header().Del("ETag")
			slog.Error("wui: embedded asset gzip", "asset", name, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		// Compressed bytes cannot be sniffed; callers without an explicit
		// type (none today — decoders and bundles all pin theirs) get the
		// same fallback the artifact gzip path uses.
		if contentType == "" {
			contentType = "application/octet-stream"
		}
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(gz)))
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(gz); err != nil {
		slog.Warn("wui: embedded asset write failed", "asset", name, "error", err)
	}
}

// RegisterStaticRoutes mounts the SPA at mux root. Every path except the
// exact prebundle paths (noVNC, model-viewer) and the vendored decoder
// directories serves the single-file index.html, gzip-compressed.
func RegisterStaticRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /assets/draco/{name}", serveDecoderAsset("assets/draco"))
	mux.HandleFunc("GET /assets/basis/{name}", serveDecoderAsset("assets/basis"))

	// Exact-path pattern wins over the "/" SPA catch-all below. "GET" also
	// matches HEAD (Go 1.22 method patterns), so probes get the headers.
	mux.HandleFunc("GET /assets/novnc.esm.js", func(w http.ResponseWriter, r *http.Request) {
		serveEmbeddedAsset(w, r, "assets/novnc.esm.js", novncESM,
			"text/javascript; charset=utf-8", "no-cache, no-store, must-revalidate")
	})
	mux.HandleFunc("GET /assets/model-viewer.esm.js", func(w http.ResponseWriter, r *http.Request) {
		serveEmbeddedAsset(w, r, "assets/model-viewer.esm.js", modelViewerESM,
			"text/javascript; charset=utf-8", "no-cache, no-store, must-revalidate")
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

// serveDecoderAsset serves one vendored decoder file. The mux pattern
// restricts {name} to a single path segment and embed.FS reads only succeed
// for real embedded files, so traversal has nothing to reach. wasm gets a
// real application/wasm type instead of a sniffed octet-stream; serving,
// gzip negotiation and revalidation are serveEmbeddedAsset's job.
func serveDecoderAsset(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		body, err := decoderAssets.ReadFile(dir + "/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		contentType := ""
		switch filepath.Ext(name) {
		case ".wasm":
			contentType = "application/wasm"
		case ".js":
			contentType = "text/javascript; charset=utf-8"
		}
		serveEmbeddedAsset(w, r, dir+"/"+name, body, contentType, "no-cache")
	}
}
