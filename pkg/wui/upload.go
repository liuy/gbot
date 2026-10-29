package wui

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/tool/toolresult"
)

// Package-level vars, not consts: the cap-boundary tests inject small values
// so they do not stream hundreds of MiB per case (500 MiB × several cases
// would dominate the suite). Production code never writes these. Same seam
// idiom as sttLiveClient (stt.go). Tests mutate them via withUploadCaps and
// those tests must NOT call t.Parallel().
var (
	maxUploadImageSize = int64(500) << 20 // Anthropic Files API parity (500 MB/file)
	maxUploadDocSize   = int64(50) << 20  // parse-path protection, see handleUpload
)

const (
	uploadBodySlack     = 8 << 10 // multipart framing headroom above the file bytes
	uploadFormField     = "file"  // the multipart field name the client must use
	uploadSniffHead     = 512     // bytes read ahead to pick image vs document
	uploadMaxConcurrent = 2       // concurrent in-flight uploads before 503
)

// uploadSem caps concurrent uploads so N multi-hundred-MiB streams cannot pile
// onto the cache disk at once. Non-blocking: a full semaphore is a refusal, not
// a queue (the client's chip turns red and retry is one tap away).
var uploadSem = make(chan struct{}, uploadMaxConcurrent)

// uploadBodyBound is the transport-level bound for the whole request body
// (file bytes + multipart framing + any junk the client puts in other parts).
// Always derived from the caps, never a literal, so an injected cap moves every
// bound together.
func uploadBodyBound() int64 {
	m := maxUploadImageSize
	if maxUploadDocSize > m {
		m = maxUploadDocSize
	}
	return m + uploadBodySlack
}

// uploadCapForKind maps classifyAttachment's kind to the cap that applies.
// Unclassifiable bytes stay documents (the small cap) — the safe direction.
func uploadCapForKind(kind string) int64 {
	if kind == "image" {
		return maxUploadImageSize
	}
	return maxUploadDocSize
}

type uploadResponse struct {
	ID   string `json:"id"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
	Kind string `json:"kind"` // "image" | "document"
}

// RegisterUploadRoute mounts POST /api/upload on mux. One multipart request
// per file; the response carries the content-addressed cache id the commit
// frame later references.
func RegisterUploadRoute(mux *http.ServeMux, c *WUIConnector) {
	mux.HandleFunc("POST /api/upload", c.handleUpload)
}

// uploadStreamErrStatus maps a streaming error to the HTTP answer. The two 413
// texts are deliberately distinct so each arm has its own falsifier: the
// per-kind cap says "exceeds <cap> limit", the transport bound says
// "upload body exceeds <bound>" with no suffix.
func uploadStreamErrStatus(err error) (status int, msg string) {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return http.StatusRequestEntityTooLarge, "upload body exceeds " + toolresult.FormatFileSize(int(uploadBodyBound()))
	}
	// Store-side failures (temp create/close/write, rename) — the bytes were
	// fine, the cache write was not. os.Rename failures surface as
	// *os.LinkError; the temp-path operations as *fs.PathError.
	var le *os.LinkError
	var pe *os.PathError
	if errors.As(err, &le) ||
		(errors.As(err, &pe) && (pe.Op == "open" || pe.Op == "close" || pe.Op == "write")) {
		slog.Error("wui:upload save failed", "error", err)
		return http.StatusInternalServerError, "save failed: " + err.Error()
	}
	return http.StatusBadRequest, "read failed: " + err.Error()
}

// handleUpload streams one multipart file part into the media cache and
// answers with its content-addressed id. Order of operations matters: the
// semaphore is acquired before any parsing so a burst is refused immediately,
// and the declared-size fast reject happens before the body is read at all.
// The document cap (50 MiB) is deliberately small: a committed document is
// parsed at send time (media.ParseDocument) and later expanded through
// fileread, whose 256 KB pre-check is skipped for uncapped output — a huge
// text document would become a full-string LLM bomb. Images never enter the
// text pipeline, hence the 500 MiB Anthropic Files API parity cap.
func (c *WUIConnector) handleUpload(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	name := ""
	reject := func(status int, reason string, byteCount int64) {
		slog.Warn("wui:upload rejected", "status", status, "reason", reason, "bytes", byteCount, "name", name)
		errorJSON(w, status, reason)
	}

	// 1. Acquire before any parsing: a full semaphore is a refusal, not a queue.
	select {
	case uploadSem <- struct{}{}:
		defer func() { <-uploadSem }()
	default:
		reject(http.StatusServiceUnavailable, "upload busy", 0)
		return
	}

	// Log before parsing so every request that starts an upload (past the
	// busy gate) leaves a start line even when rejected at a later step;
	// the raw header preserves the exact value unparsable types sent.
	slog.Info("wui:upload start", "content_type", r.Header.Get("Content-Type"), "declared", r.ContentLength)
	// 2. Store.
	store := c.mediaCache
	if store == nil {
		reject(http.StatusServiceUnavailable, "upload disabled: no media cache", 0)
		return
	}

	// 3. Content type.
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" {
		reject(http.StatusUnsupportedMediaType, "expected multipart/form-data", 0)
		return
	}

	// 4. Declared-size fast reject: the largest cap because the kind is not
	// known yet. The body is never read on this path.
	if r.ContentLength > uploadBodyBound() {
		reject(http.StatusRequestEntityTooLarge,
			"upload exceeds "+toolresult.FormatFileSize(int(maxUploadImageSize))+" limit", 0)
		return
	}

	// 5. Transport bound for the whole body (framing + junk parts included).
	r.Body = http.MaxBytesReader(w, r.Body, uploadBodyBound())

	// 6. Streaming multipart — never ParseMultipartForm: no MaxMemory tuning,
	// no untracked temp files, and the file part is streamed, not buffered.
	mr, err := r.MultipartReader()
	if err != nil {
		reject(http.StatusBadRequest, "malformed multipart: "+err.Error(), 0)
		return
	}

	// 7. Find the first part with the right field name AND a filename; plain
	// fields are skipped (NextPart drains them, which is what makes the
	// MaxBytesError arm reachable while draining oversized junk).
	var part *multipart.Part
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			reject(http.StatusBadRequest, "missing file field", 0)
			return
		}
		if err != nil {
			status, msg := uploadStreamErrStatus(err)
			reject(status, msg, 0)
			return
		}
		if p.FormName() == uploadFormField && p.FileName() != "" {
			part = p
			break
		}
	}

	// 8. Browsers send a basename; Base is defence in depth, nothing else
	// trusts it.
	name = filepath.Base(part.FileName())

	// 9. Kind first, cap second: the sniff head (512 bytes) is the handler's
	// whole in-memory footprint for the file. Files shorter than the head are
	// normal (ReadFull's EOF here is not an error).
	head := make([]byte, uploadSniffHead)
	n, err := io.ReadFull(part, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		status, msg := uploadStreamErrStatus(err)
		reject(status, msg, 0)
		return
	}
	head = head[:n]
	sniffMime, ext, kind := classifyAttachment(head, name, "")
	kindCap := uploadCapForKind(kind) // not named `cap`: shadows the builtin
	cat := media.CategoryDocument
	if kind == "image" {
		cat = media.CategoryImage
	}
	ext = sanitizeUploadExt(ext)

	// 10. Stream the part through the sniffed head into the cache — the part
	// is never buffered in the handler.
	path, size, err := store.SaveReader(cat, ext,
		io.MultiReader(bytes.NewReader(head), part), kindCap)
	if err != nil {
		if errors.Is(err, media.ErrTooLarge) {
			reject(http.StatusRequestEntityTooLarge,
				"upload exceeds "+toolresult.FormatFileSize(int(kindCap))+" limit", size)
			return
		}
		status, msg := uploadStreamErrStatus(err)
		reject(status, msg, size)
		return
	}

	// 11. size is the server's own count, never the client's declared number.
	id := attachmentID(cat, path)
	slog.Info("wui:upload ok", "id", id, "name", name, "bytes", size, "kind", kind,
		"dur", time.Since(start).Round(time.Millisecond).String())
	writeJSON(w, http.StatusOK, uploadResponse{ID: id, Mime: sniffMime, Size: size, Kind: kind})
}
