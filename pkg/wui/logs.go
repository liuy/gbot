package wui

import (
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// RegisterLogRoutes mounts the daemon-log HTTP surface:
//
//   - GET /api/logs?tail=N — the last N lines of the daemon's gbot.log as
//     text/plain (default 300, capped at 2000)
//
// A missing or unreadable log is a normal state (fresh projectspace, rotated
// file, transient fs hiccup) and serves 200 with an empty body — the settings
// panel just shows nothing rather than surfacing an error.
func RegisterLogRoutes(mux *http.ServeMux, logPath string) {
	mux.HandleFunc("GET /api/logs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(tailFile(logPath, parseTail(r))))
	})
}

const (
	logTailDefault = 300
	logTailCap     = 2000
	// readWindow caps how far back a single tail reads. gbot.log rotates at
	// 20MB, so a fixed window keeps the response bounded no matter how far
	// back the client asks.
	logTailWindow = 256 << 10
)

// parseTail reads the ?tail=N query param: missing, malformed, or non-positive
// values fall back to the default; anything above the cap is clamped.
func parseTail(r *http.Request) int {
	n := logTailDefault
	if v := r.URL.Query().Get("tail"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}
	if n > logTailCap {
		n = logTailCap
	}
	return n
}

// tailFile returns the last n lines of path joined with "\n". It reads at most
// the final logTailWindow bytes: open + Stat + Seek keeps the cost flat
// regardless of file size. When the window starts mid-file the first chunk is
// a partial line and is dropped (only complete lines are ever returned). Any
// open/stat/read failure yields "" — callers treat empty as "nothing to show".
func tailFile(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return ""
	}
	size := st.Size()
	if size == 0 {
		return ""
	}

	offset := size - logTailWindow
	if offset < 0 {
		offset = 0
	}
	buf := make([]byte, size-offset)
	if _, err := f.ReadAt(buf, offset); err != nil && err != io.EOF {
		return ""
	}

	lines := strings.Split(string(buf), "\n")
	if offset > 0 && len(lines) > 0 {
		lines = lines[1:] // partial line at the window's head
	}
	// Drop the empty tail element from a trailing newline (and any stray
	// trailing empties).
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
