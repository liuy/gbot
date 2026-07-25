package wui

import (
	"bytes"
	"fmt"
	"path/filepath"

	"github.com/liuy/gbot/pkg/media"
)

// maxAttachmentSize caps a single attachment's declared size (50 MiB).
// Checked at attachment_start so a huge declared size is rejected before any
// bytes land in memory. Mirrors the prior upload.go HTTP limit.
const maxAttachmentSize = 50 << 20

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

// attachmentAccumulator tracks per-WS upload state. Owned by readLoop as a
// local variable; disconnect naturally GC's it. NOT a connector field — the
// single-client takeover model means only one readLoop is active at a time,
// but tying state to readLoop's stack frame is cleaner than relying on that
// invariant.
type attachmentAccumulator struct {
	waiting    bool
	text       string
	pendingIDs []string                     // ordered, from user_message.attachments
	metas      map[string]inboundAttachment // wire metadata (id, name, mime, size)
	saved      map[string]savedAttachment   // id → post-save classification; filled by handleEnd
	dropped    map[string]bool              // id → true when size mismatch / save failure / id mismatch
	activeID   string                       // empty between attachments
	buf        *bytes.Buffer                // nil between attachments
	activeMeta inboundAttachment            // valid only while activeID != ""
}

// startWaiting enters the waiting state. Returns false if already waiting
// (caller sends a "uploads in progress" error and drops the new message).
func (a *attachmentAccumulator) startWaiting(text string, atts []inboundAttachment) bool {
	if a.waiting {
		return false
	}
	a.waiting = true
	a.text = text
	a.pendingIDs = make([]string, len(atts))
	a.metas = make(map[string]inboundAttachment, len(atts))
	a.saved = make(map[string]savedAttachment)
	a.dropped = make(map[string]bool)
	for i, att := range atts {
		a.pendingIDs[i] = att.ID
		a.metas[att.ID] = att
	}
	a.activeID = ""
	a.buf = nil
	return true
}

// handleStart processes an attachment_start frame. Validates id ∈ pendingIDs,
// size ≤ maxAttachmentSize, and that no other attachment is mid-stream.
// Returns an error string on validation failure (caller sends WS error and
// continues — partial degradation); "" on success.
func (a *attachmentAccumulator) handleStart(msg attachmentStartMsg) string {
	if !a.waiting {
		return "uploads not in progress"
	}
	if a.activeID != "" {
		return fmt.Sprintf("attachment %s upload already in progress", a.activeID)
	}
	if _, ok := a.metas[msg.ID]; !ok {
		return fmt.Sprintf("unknown id %s", msg.ID)
	}
	if msg.Size > maxAttachmentSize {
		return fmt.Sprintf("attachment %s too large: %d bytes (max %d)", msg.ID, msg.Size, maxAttachmentSize)
	}
	a.activeID = msg.ID
	a.activeMeta = msg.inboundShape()
	a.buf = new(bytes.Buffer)
	return ""
}

// inboundShape converts an attachmentStartMsg to its inboundAttachment form
// so handleStart can populate activeMeta consistently with the user_message
// metadata. The wire fields are identical, so this is a struct copy.
func (msg attachmentStartMsg) inboundShape() inboundAttachment {
	return inboundAttachment{ID: msg.ID, Name: msg.Name, Mime: msg.Mime, Size: msg.Size}
}

// handleBinary appends a chunk to the active attachment's buffer. Returns an
// error string if no active attachment or chunk overflow; "" on success.
// On overflow the active attachment is dropped (matches handleEnd's
// size-mismatch handling — a single bad chunk poisons the file).
func (a *attachmentAccumulator) handleBinary(data []byte) string {
	if a.activeID == "" {
		return "no active attachment"
	}
	a.buf.Write(data)
	got := int64(a.buf.Len())
	if got > a.activeMeta.Size {
		id := a.activeID
		a.dropActive()
		return fmt.Sprintf("attachment %s size overflow (buffer %d > declared %d)", id, got, a.activeMeta.Size)
	}
	return ""
}

// dropActive clears the active attachment's state and marks it dropped.
// Used by handleBinary overflow and handleEnd save failures — size mismatch
// uses it too. The caller still receives an error string to send.
func (a *attachmentAccumulator) dropActive() {
	if a.activeID == "" {
		return
	}
	a.dropped[a.activeID] = true
	a.activeID = ""
	a.buf = nil
}

// handleEnd processes an attachment_end frame. Validates id == activeID
// (rejects out-of-order end-frames without dropping the active attachment —
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
		// Size mismatch drops the active attachment so the next start can
		// proceed; partial degradation, mirrors the legacy upload.go
		// philosophy of not aborting the whole batch.
		errMsg := fmt.Sprintf("attachment %s size mismatch (got %d bytes, declared %d)", id, a.buf.Len(), declared)
		a.dropActive()
		return errMsg
	}
	if store == nil {
		errMsg := fmt.Sprintf("attachment %s cannot be saved: no media cache", id)
		a.dropActive()
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
		a.dropActive()
		return errMsg
	}
	a.saved[id] = savedAttachment{path: path, mime: mime, ext: ext, kind: kind}
	a.activeID = ""
	a.buf = nil
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

// readyToDispatch reports whether all pendingIDs have either been saved or
// dropped. Called by readLoop after each handleEnd to decide whether to fire
// handleMessageInbound.
func (a *attachmentAccumulator) readyToDispatch() bool {
	if !a.waiting {
		return false
	}
	for _, id := range a.pendingIDs {
		if _, saved := a.saved[id]; saved {
			continue
		}
		if a.dropped[id] {
			continue
		}
		return false
	}
	return true
}

// buildContents constructs []inboundContent for assembleContentBlocks from
// non-dropped attachments. Reads kind/path/mime from acc.saved[id] (set by
// handleEnd) and the original filename from acc.metas[id].Name. Order
// matches acc.pendingIDs so the engine sees attachments in declaration order.
func (a *attachmentAccumulator) buildContents() []inboundContent {
	out := make([]inboundContent, 0, len(a.pendingIDs))
	for _, id := range a.pendingIDs {
		saved, ok := a.saved[id]
		if !ok {
			continue // dropped — partial degradation
		}
		var typ string
		switch saved.kind {
		case "image":
			typ = "image"
		default:
			typ = "document"
		}
		out = append(out, inboundContent{
			Type: typ,
			Source: inboundSource{
				Type: "file",
				Path: saved.path,
				Mime: saved.mime,
				Name: a.metas[id].Name,
			},
		})
	}
	return out
}

// reset clears all state (called after dispatch).
func (a *attachmentAccumulator) reset() {
	a.waiting = false
	a.text = ""
	a.pendingIDs = nil
	a.metas = nil
	a.saved = nil
	a.dropped = nil
	a.activeID = ""
	a.buf = nil
}
