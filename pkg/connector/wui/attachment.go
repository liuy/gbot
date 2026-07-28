package wui

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/liuy/gbot/pkg/media"
)

// maxAttachmentSize caps a single attachment's declared size (50 MiB).
// Checked at attachment_start so a huge declared size is rejected before any
// bytes land in memory. Mirrors the prior upload.go HTTP limit.
const maxAttachmentSize = 50 << 20

// maxSendFileSize caps outbound file delivery via the Send tool. base64 inline
// encoding expands raw bytes ~1.33x, so 10 MiB raw ≈ 13.3 MiB on the wire —
// safe for WS frame and browser memory. A larger file surfaces an error to
// the LLM so it can tell the user instead of silently dropping the send.
const maxSendFileSize = 10 << 20

// inboundAttachment is the wire shape of one entry in user_message.attachments[].
// The path is assigned server-side after bytes land (the client only sends
// id+name+mime+size).
type inboundAttachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
}

// attachmentStartMsg is the wire shape of an attachment_start text frame.
type attachmentStartMsg struct {
	Type string `json:"type"` // "attachment_start"
	ID   string `json:"id"`
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
}

// attachmentEndMsg is the wire shape of an attachment_end text frame.
type attachmentEndMsg struct {
	Type string `json:"type"` // "attachment_end"
	ID   string `json:"id"`
}

// savedAttachment records the post-save classification for one attachment.
// Storing kind/mime/ext here means buildContents is a pure map lookup —
// assembleContentBlocks downstream does its own re-sniff, but the
// accumulator hands the readLoop a fully-resolved []inboundContent.
type savedAttachment struct {
	path string
	mime string // sniffed mime (SniffImageMime result, or MimeFromExt fallback)
	ext  string // extension preserved for documents (.pdf, .docx, ...)
	kind string // "image" or "document"
}

// attachmentAccumulator is a stateless staging area for the two-phase commit
// upload model. saved persists across user_message attempts so a failed
// commit (missing id) does not lose already-uploaded bytes; reset only runs
// after a successful dispatch. activeID/buf/activeMeta track one in-flight
// stream upload (single activeUploadID constraint — uploads are serial).
//
// Owned by readLoop as a local variable; disconnect naturally GC's it. NOT a
// connector field — the single-client takeover model means only one readLoop
// is active at a time, but tying state to readLoop's stack frame is cleaner
// than relying on that invariant.
type attachmentAccumulator struct {
	saved      map[string]savedAttachment // id → post-save classification; filled by handleEnd, cleared by reset
	activeID   string                     // empty between attachments
	buf        *bytes.Buffer              // nil between attachments
	activeMeta inboundAttachment          // valid only while activeID != ""
}

// handleStart processes an attachment_start frame. Validates size and that no
// other attachment is mid-stream. Returns an error string on validation
// failure (caller sends WS error and continues); "" on success.
//
// Two-phase model: handleStart accepts ANY id (no pre-registration) — the
// commit-time user_message.attachments is the source of truth for which ids
// are expected, and missing ids are caught by buildContents.
func (a *attachmentAccumulator) handleStart(msg attachmentStartMsg) string {
	if a.activeID != "" {
		return fmt.Sprintf("attachment %s upload already in progress", a.activeID)
	}
	if msg.Size > maxAttachmentSize {
		return fmt.Sprintf("attachment %s too large: %d bytes (max %d)", msg.ID, msg.Size, maxAttachmentSize)
	}
	a.activeID = msg.ID
	a.activeMeta = inboundAttachment{ID: msg.ID, Name: msg.Name, Mime: msg.Mime, Size: msg.Size}
	a.buf = new(bytes.Buffer)
	if a.saved == nil {
		// saved persists across uploads within one WS connection and is
		// cleared only by reset on successful commit. Lazy-init here so
		// handleEnd can assign without a separate setup call.
		a.saved = make(map[string]savedAttachment)
	}
	return ""
}

// handleBinary appends a chunk to the active attachment's buffer. Returns an
// error string if no active attachment or chunk overflow; "" on success.
// On overflow the active attachment is cleared (does NOT land in saved — the
// frontend learns this at commit time when buildContents reports it missing).
func (a *attachmentAccumulator) handleBinary(data []byte) string {
	if a.activeID == "" {
		return "no active attachment"
	}
	a.buf.Write(data)
	got := int64(a.buf.Len())
	if got > a.activeMeta.Size {
		id := a.activeID
		a.clearActive()
		return fmt.Sprintf("attachment %s size overflow (buffer %d > declared %d)", id, got, a.activeMeta.Size)
	}
	return ""
}

// clearActive clears the in-flight upload state without touching saved.
// Used by handleBinary overflow and handleEnd failures — the attachment
// simply doesn't land in saved, and the frontend learns this at commit time.
func (a *attachmentAccumulator) clearActive() {
	a.activeID = ""
	a.buf = nil
}

// handleEnd processes an attachment_end frame. Validates id == activeID
// (rejects out-of-order end-frames without clearing the active attachment —
// the correct end-frame can still complete it), validates accumulated size,
// classifies mime (SniffImageMime → image vs MimeFromExt → document), saves
// via mediaCache.Save, and records the classification in acc.saved[id] for
// buildContents. Returns "" on success or an error message otherwise.
func (a *attachmentAccumulator) handleEnd(id string, store *media.Store) string {
	if a.activeID == "" {
		return "no active attachment"
	}
	if id != a.activeID {
		return fmt.Sprintf("attachment_end for inactive id %s (active %s)", id, a.activeID)
	}
	declared := a.activeMeta.Size
	if int64(a.buf.Len()) != declared {
		errMsg := fmt.Sprintf("attachment %s size mismatch (got %d bytes, declared %d)", id, a.buf.Len(), declared)
		a.clearActive()
		return errMsg
	}
	if store == nil {
		errMsg := fmt.Sprintf("attachment %s cannot be saved: no media cache", id)
		a.clearActive()
		return errMsg
	}
	data := a.buf.Bytes()
	mime, ext, kind := classifyAttachment(data, a.activeMeta.Name, a.activeMeta.Mime)
	var cat media.Category
	switch kind {
	case "image":
		cat = media.CategoryImage
	case "document":
		cat = media.CategoryDocument
	}
	path, err := store.Save(cat, data, ext)
	if err != nil {
		errMsg := fmt.Sprintf("attachment %s save failed: %v", id, err)
		a.clearActive()
		return errMsg
	}
	a.saved[id] = savedAttachment{path: path, mime: mime, ext: ext, kind: kind}
	a.clearActive()
	return ""
}

// classifyAttachment mirrors the prior upload.go classification: sniff image
// magic bytes first; fall back to filename extension and treat as a document.
// For images, ext is derived from the sniffed mime (so a .png named "x.bin"
// still saves as .png); for documents, the filename extension is preserved
// (round-tripping through mime maps loses types like .pptx/.docx).
func classifyAttachment(data []byte, name, _ string) (mime, ext, kind string) {
	if sniffed := media.SniffImageMime(data); sniffed != "" {
		return sniffed, media.ExtFromMime(sniffed), "image"
	}
	return media.MimeFromExt(filepath.Ext(name)), preserveDocExt(name), "document"
}

// preserveDocExt returns the filename extension for document attachments,
// defaulting to .bin when no extension is present. MimeFromExt→ExtFromMime
// would collapse .pptx/.docx to .bin, so the original ext is preserved.
func preserveDocExt(name string) string {
	if e := filepath.Ext(name); e != "" {
		return e
	}
	return ".bin"
}

// buildContents looks up each commit-time attachment in saved and assembles
// []inboundContent. Returns missingIDs (comma-joined) if any id is absent —
// caller sends an error and does NOT dispatch. Name comes from the commit
// metadata (att.Name); path/mime/kind come from the upload-time classification
// (saved[id]).
func (a *attachmentAccumulator) buildContents(atts []inboundAttachment) ([]inboundContent, string) {
	out := make([]inboundContent, 0, len(atts))
	var missing []string
	for _, att := range atts {
		saved, ok := a.saved[att.ID]
		if !ok {
			missing = append(missing, att.ID)
			continue
		}
		typ := "document"
		if saved.kind == "image" {
			typ = "image"
		}
		out = append(out, inboundContent{
			Type: typ,
			Source: inboundSource{
				Type: "file",
				Path: saved.path,
				Mime: saved.mime,
				Name: att.Name,
			},
		})
	}
	if len(missing) > 0 {
		return nil, strings.Join(missing, ", ")
	}
	return out, ""
}

// reset clears all state (called after a successful commit/dispatch).
func (a *attachmentAccumulator) reset() {
	a.saved = nil
	a.activeID = ""
	a.buf = nil
}
