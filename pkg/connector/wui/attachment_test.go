package wui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/media"
)

// minimalPNGAttachment is the same real, decodable 1x1 PNG bytes used by
// the legacy upload tests. Copied here because upload_test.go is deleted in
// Step 5, which would otherwise remove the package-level definition.
var minimalPNGAttachment = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
	0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
	0xDE,
	0x00, 0x00, 0x00, 0x10, 0x49, 0x44, 0x41, 0x54,
	0x78, 0x9C, 0x62, 0xFA, 0xCF, 0xC0, 0x00, 0x08,
	0x00, 0x00, 0xFF, 0xFF, 0x03, 0x09, 0x01, 0x02,
	0x58, 0xB6, 0xD5, 0x50,
	0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44,
	0xAE, 0x42, 0x60, 0x82,
}

// pdfFixture is a minimal-but-realistic PDF body — enough that fileread's
// fallback path emits a [Document: name saved at path] header followed by
// the raw bytes when parsed (mirrors TestRegisterUpload_ValidDocument).
var pdfFixture = []byte("%PDF-1.4 fake pdf content")

// newAccStore builds a real media.Store rooted at a per-test temp dir.
func newAccStore(t *testing.T) *media.Store {
	t.Helper()
	store, err := media.NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("media.NewAt: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

func TestAttachmentAccumulator_StartWaiting_RejectsSecondStart(t *testing.T) {
	acc := &attachmentAccumulator{}
	atts := []inboundAttachment{{ID: "a1", Name: "x.png", Mime: "image/png", Size: 1}}
	if !acc.startWaiting("text", atts) {
		t.Fatal("first startWaiting returned false, want true")
	}
	if acc.startWaiting("text2", atts) {
		t.Error("second startWaiting returned true; must reject while waiting")
	}
	// Original text + pendingIDs preserved.
	if acc.text != "text" {
		t.Errorf("text = %q, want original 'text'", acc.text)
	}
}

func TestAttachmentAccumulator_StartWaiting_PopulatesState(t *testing.T) {
	acc := &attachmentAccumulator{}
	atts := []inboundAttachment{
		{ID: "a1", Name: "x.png", Mime: "image/png", Size: 1},
		{ID: "a2", Name: "y.pdf", Mime: "application/pdf", Size: 2},
	}
	if !acc.startWaiting("hello", atts) {
		t.Fatal("startWaiting returned false")
	}
	if !acc.waiting {
		t.Error("waiting = false, want true")
	}
	if acc.text != "hello" {
		t.Errorf("text = %q, want 'hello'", acc.text)
	}
	if len(acc.pendingIDs) != 2 || acc.pendingIDs[0] != "a1" || acc.pendingIDs[1] != "a2" {
		t.Errorf("pendingIDs = %v, want [a1 a2]", acc.pendingIDs)
	}
	if got := acc.metas["a1"]; got.Name != "x.png" || got.Size != 1 {
		t.Errorf("metas[a1] = %+v, want name=x.png size=1", got)
	}
	if len(acc.saved) != 0 || len(acc.dropped) != 0 {
		t.Errorf("saved/dropped should be empty, got saved=%d dropped=%d", len(acc.saved), len(acc.dropped))
	}
	if acc.activeID != "" {
		t.Errorf("activeID = %q, want empty between attachments", acc.activeID)
	}
}

func TestAttachmentAccumulator_HandleStart_RejectsUnknownID(t *testing.T) {
	acc := &attachmentAccumulator{}
	acc.startWaiting("text", []inboundAttachment{{ID: "a1", Name: "x.png", Mime: "image/png", Size: 1}})
	errMsg := acc.handleStart(attachmentStartMsg{
		Type: "attachment_start", ID: "bogus", Name: "x.png", Mime: "image/png", Size: 1,
	})
	if errMsg == "" {
		t.Fatal("expected error for unknown id, got empty string")
	}
	if !strings.Contains(errMsg, "unknown id") {
		t.Errorf("errMsg = %q, want substring 'unknown id'", errMsg)
	}
	if acc.activeID != "" {
		t.Errorf("activeID = %q after rejection; must stay empty", acc.activeID)
	}
}

func TestAttachmentAccumulator_HandleStart_RejectsOversize(t *testing.T) {
	acc := &attachmentAccumulator{}
	acc.startWaiting("text", []inboundAttachment{{ID: "a1", Name: "huge.bin", Mime: "application/octet-stream", Size: maxAttachmentSize + 1}})
	errMsg := acc.handleStart(attachmentStartMsg{
		Type: "attachment_start", ID: "a1", Name: "huge.bin", Mime: "application/octet-stream", Size: maxAttachmentSize + 1,
	})
	if errMsg == "" {
		t.Fatal("expected error for oversize, got empty")
	}
	if !strings.Contains(errMsg, "too large") {
		t.Errorf("errMsg = %q, want substring 'too large'", errMsg)
	}
}

func TestAttachmentAccumulator_HandleStart_RejectsAtMaxBoundary(t *testing.T) {
	acc := &attachmentAccumulator{}
	acc.startWaiting("text", []inboundAttachment{{ID: "a1", Name: "edge.bin", Mime: "application/octet-stream", Size: maxAttachmentSize}})
	if errMsg := acc.handleStart(attachmentStartMsg{
		Type: "attachment_start", ID: "a1", Name: "edge.bin", Mime: "application/octet-stream", Size: maxAttachmentSize,
	}); errMsg != "" {
		t.Errorf("size==max should be accepted; got errMsg=%q", errMsg)
	}
}

func TestAttachmentAccumulator_HandleStart_RejectsNestedStart(t *testing.T) {
	acc := &attachmentAccumulator{}
	acc.startWaiting("text", []inboundAttachment{
		{ID: "a1", Name: "x.png", Mime: "image/png", Size: 1},
		{ID: "a2", Name: "y.png", Mime: "image/png", Size: 1},
	})
	if errMsg := acc.handleStart(attachmentStartMsg{
		Type: "attachment_start", ID: "a1", Name: "x.png", Mime: "image/png", Size: 1,
	}); errMsg != "" {
		t.Fatalf("first handleStart failed: %q", errMsg)
	}
	errMsg := acc.handleStart(attachmentStartMsg{
		Type: "attachment_start", ID: "a2", Name: "y.png", Mime: "image/png", Size: 1,
	})
	if errMsg == "" {
		t.Fatal("expected nested-start error, got empty")
	}
	if !strings.Contains(errMsg, "in progress") {
		t.Errorf("errMsg = %q, want substring 'in progress'", errMsg)
	}
	// First attachment stays active.
	if acc.activeID != "a1" {
		t.Errorf("activeID = %q, want 'a1' (first preserved)", acc.activeID)
	}
}

func TestAttachmentAccumulator_HandleStart_InitializesBuffer(t *testing.T) {
	acc := &attachmentAccumulator{}
	acc.startWaiting("text", []inboundAttachment{{ID: "a1", Name: "x.png", Mime: "image/png", Size: 10}})
	if errMsg := acc.handleStart(attachmentStartMsg{
		Type: "attachment_start", ID: "a1", Name: "x.png", Mime: "image/png", Size: 10,
	}); errMsg != "" {
		t.Fatalf("handleStart failed: %q", errMsg)
	}
	if acc.activeID != "a1" {
		t.Errorf("activeID = %q, want 'a1'", acc.activeID)
	}
	if acc.buf == nil {
		t.Error("buf = nil, want initialized")
	}
	if acc.activeMeta.Name != "x.png" {
		t.Errorf("activeMeta.Name = %q, want 'x.png'", acc.activeMeta.Name)
	}
}

func TestAttachmentAccumulator_HandleBinary_RejectsWhenNoActive(t *testing.T) {
	acc := &attachmentAccumulator{}
	errMsg := acc.handleBinary([]byte{0x01})
	if errMsg == "" {
		t.Fatal("expected error when no active attachment, got empty")
	}
	if !strings.Contains(errMsg, "no active") {
		t.Errorf("errMsg = %q, want substring 'no active'", errMsg)
	}
}

func TestAttachmentAccumulator_HandleBinary_AppendsToActiveBuffer(t *testing.T) {
	acc := &attachmentAccumulator{}
	acc.startWaiting("text", []inboundAttachment{{ID: "a1", Name: "x.png", Mime: "image/png", Size: 4}})
	acc.handleStart(attachmentStartMsg{ID: "a1", Name: "x.png", Mime: "image/png", Size: 4})
	if errMsg := acc.handleBinary([]byte{0x01, 0x02}); errMsg != "" {
		t.Fatalf("first handleBinary failed: %q", errMsg)
	}
	if errMsg := acc.handleBinary([]byte{0x03, 0x04}); errMsg != "" {
		t.Fatalf("second handleBinary failed: %q", errMsg)
	}
	if got := acc.buf.Len(); got != 4 {
		t.Errorf("buf.Len() = %d, want 4", got)
	}
	if !bytes.Equal(acc.buf.Bytes(), []byte{0x01, 0x02, 0x03, 0x04}) {
		t.Errorf("buf bytes = %v, want [1 2 3 4]", acc.buf.Bytes())
	}
}

func TestAttachmentAccumulator_HandleBinary_RejectsOverflow(t *testing.T) {
	acc := &attachmentAccumulator{}
	acc.startWaiting("text", []inboundAttachment{{ID: "a1", Name: "x.png", Mime: "image/png", Size: 2}})
	acc.handleStart(attachmentStartMsg{ID: "a1", Name: "x.png", Mime: "image/png", Size: 2})
	if errMsg := acc.handleBinary([]byte{0x01, 0x02}); errMsg != "" {
		t.Fatalf("first chunk to size limit failed: %q", errMsg)
	}
	errMsg := acc.handleBinary([]byte{0x03}) // exceeds declared size=2
	if errMsg == "" {
		t.Fatal("expected overflow error, got empty")
	}
	if !strings.Contains(errMsg, "overflow") {
		t.Errorf("errMsg = %q, want substring 'overflow'", errMsg)
	}
}

func TestAttachmentAccumulator_HandleEnd_RejectsSizeMismatch(t *testing.T) {
	store := newAccStore(t)
	acc := &attachmentAccumulator{}
	acc.startWaiting("text", []inboundAttachment{{ID: "a1", Name: "x.png", Mime: "image/png", Size: 10}})
	acc.handleStart(attachmentStartMsg{ID: "a1", Name: "x.png", Mime: "image/png", Size: 10})
	acc.handleBinary([]byte{0x01, 0x02}) // only 2 bytes; declared 10
	errMsg := acc.handleEnd("a1", store)
	if errMsg == "" {
		t.Fatal("expected size mismatch error, got empty")
	}
	if !strings.Contains(errMsg, "size mismatch") {
		t.Errorf("errMsg = %q, want substring 'size mismatch'", errMsg)
	}
	if _, ok := acc.saved["a1"]; ok {
		t.Error("saved[a1] gained an entry on mismatch")
	}
	if !acc.dropped["a1"] {
		t.Error("dropped[a1] = false, want true after size mismatch")
	}
	// activeID cleared so the next attachment_start can proceed.
	if acc.activeID != "" {
		t.Errorf("activeID = %q, want empty after end", acc.activeID)
	}
}

func TestAttachmentAccumulator_HandleEnd_RejectsMismatchedID(t *testing.T) {
	store := newAccStore(t)
	acc := &attachmentAccumulator{}
	acc.startWaiting("text", []inboundAttachment{{ID: "a1", Name: "x.png", Mime: "image/png", Size: 4}})
	acc.handleStart(attachmentStartMsg{ID: "a1", Name: "x.png", Mime: "image/png", Size: 4})
	acc.handleBinary([]byte{0x01, 0x02, 0x03, 0x04})
	// Calling handleEnd with a different id — the active attachment must
	// NOT be auto-dropped; only size/save errors drop. The correct end-frame
	// can still complete it.
	errMsg := acc.handleEnd("other", store)
	if errMsg == "" {
		t.Fatal("expected inactive-id error, got empty")
	}
	if !strings.Contains(errMsg, "inactive id") {
		t.Errorf("errMsg = %q, want substring 'inactive id'", errMsg)
	}
	if _, ok := acc.saved["a1"]; ok {
		t.Error("saved[a1] gained an entry on id mismatch")
	}
	if acc.dropped["a1"] {
		t.Error("dropped[a1] = true after id mismatch; must stay false (active NOT auto-dropped)")
	}
	if acc.activeID != "a1" {
		t.Errorf("activeID = %q, want 'a1' preserved", acc.activeID)
	}
	// Correct end-frame still completes the attachment.
	if errMsg := acc.handleEnd("a1", store); errMsg != "" {
		t.Fatalf("correct handleEnd after id mismatch failed: %q", errMsg)
	}
	if _, ok := acc.saved["a1"]; !ok {
		t.Error("saved[a1] still missing after correct end-frame")
	}
}

func TestAttachmentAccumulator_HandleEnd_SavesImage(t *testing.T) {
	store := newAccStore(t)
	acc := &attachmentAccumulator{}
	acc.startWaiting("text", []inboundAttachment{{ID: "a1", Name: "photo.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))}})
	acc.handleStart(attachmentStartMsg{ID: "a1", Name: "photo.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))})
	acc.handleBinary(minimalPNGAttachment)
	if errMsg := acc.handleEnd("a1", store); errMsg != "" {
		t.Fatalf("handleEnd failed: %q", errMsg)
	}
	saved, ok := acc.saved["a1"]
	if !ok {
		t.Fatal("saved[a1] missing after successful end")
	}
	imagesDir := filepath.Join(store.RootDir, string(media.CategoryImage))
	if !strings.HasPrefix(saved.path, imagesDir+string(os.PathSeparator)) {
		t.Errorf("path = %q, want prefix %s/", saved.path, imagesDir)
	}
	if saved.ext != ".png" {
		t.Errorf("ext = %q, want .png", saved.ext)
	}
	if saved.mime != "image/png" {
		t.Errorf("mime = %q, want image/png", saved.mime)
	}
	if saved.kind != "image" {
		t.Errorf("kind = %q, want 'image'", saved.kind)
	}
	if _, err := os.Stat(saved.path); err != nil {
		t.Errorf("file not on disk at %s: %v", saved.path, err)
	}
	if acc.activeID != "" {
		t.Errorf("activeID = %q, want empty after end", acc.activeID)
	}
	if acc.buf != nil {
		t.Error("buf should be nil after end (between attachments)")
	}
}

func TestAttachmentAccumulator_HandleEnd_SavesDocument(t *testing.T) {
	store := newAccStore(t)
	acc := &attachmentAccumulator{}
	acc.startWaiting("text", []inboundAttachment{{ID: "d1", Name: "report.pdf", Mime: "application/pdf", Size: int64(len(pdfFixture))}})
	acc.handleStart(attachmentStartMsg{ID: "d1", Name: "report.pdf", Mime: "application/pdf", Size: int64(len(pdfFixture))})
	acc.handleBinary(pdfFixture)
	if errMsg := acc.handleEnd("d1", store); errMsg != "" {
		t.Fatalf("handleEnd failed: %q", errMsg)
	}
	saved, ok := acc.saved["d1"]
	if !ok {
		t.Fatal("saved[d1] missing")
	}
	docsDir := filepath.Join(store.RootDir, string(media.CategoryDocument))
	if !strings.HasPrefix(saved.path, docsDir+string(os.PathSeparator)) {
		t.Errorf("path = %q, want prefix %s/", saved.path, docsDir)
	}
	if saved.ext != ".pdf" {
		t.Errorf("ext = %q, want .pdf (filename extension preserved)", saved.ext)
	}
	if saved.kind != "document" {
		t.Errorf("kind = %q, want 'document'", saved.kind)
	}
}

func TestAttachmentAccumulator_HandleEnd_NilStore_DropsAndErrors(t *testing.T) {
	acc := &attachmentAccumulator{}
	acc.startWaiting("text", []inboundAttachment{{ID: "a1", Name: "x.png", Mime: "image/png", Size: 4}})
	acc.handleStart(attachmentStartMsg{ID: "a1", Name: "x.png", Mime: "image/png", Size: 4})
	acc.handleBinary([]byte{0x01, 0x02, 0x03, 0x04})
	errMsg := acc.handleEnd("a1", nil)
	if errMsg == "" {
		t.Fatal("expected 'no media cache' error, got empty")
	}
	if !strings.Contains(errMsg, "no media cache") {
		t.Errorf("errMsg = %q, want substring 'no media cache'", errMsg)
	}
	if !acc.dropped["a1"] {
		t.Error("dropped[a1] = false, want true after nil-store failure")
	}
	if acc.activeID != "" {
		t.Errorf("activeID = %q, want empty after drop", acc.activeID)
	}
}

func TestAttachmentAccumulator_ReadyToDispatch_FalseWhenPartial(t *testing.T) {
	store := newAccStore(t)
	acc := &attachmentAccumulator{}
	acc.startWaiting("text", []inboundAttachment{
		{ID: "a1", Name: "x.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))},
		{ID: "a2", Name: "y.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))},
	})
	acc.handleStart(attachmentStartMsg{ID: "a1", Name: "x.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))})
	acc.handleBinary(minimalPNGAttachment)
	acc.handleEnd("a1", store)
	if acc.readyToDispatch() {
		t.Error("readyToDispatch = true after only 1 of 2; want false")
	}
}

func TestAttachmentAccumulator_ReadyToDispatch_TrueWhenAllDoneOrDropped(t *testing.T) {
	store := newAccStore(t)
	acc := &attachmentAccumulator{}
	acc.startWaiting("text", []inboundAttachment{
		{ID: "a1", Name: "x.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))},
		{ID: "a2", Name: "y.png", Mime: "image/png", Size: 10},
	})
	// a1 saved, a2 dropped (size mismatch).
	acc.handleStart(attachmentStartMsg{ID: "a1", Name: "x.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))})
	acc.handleBinary(minimalPNGAttachment)
	acc.handleEnd("a1", store)
	acc.handleStart(attachmentStartMsg{ID: "a2", Name: "y.png", Mime: "image/png", Size: 10})
	acc.handleBinary([]byte{0x01})
	acc.handleEnd("a2", store) // size mismatch → drop
	if !acc.readyToDispatch() {
		t.Error("readyToDispatch = false; want true when all saved-or-dropped")
	}
}

func TestAttachmentAccumulator_BuildContents_ClassifiesByMime(t *testing.T) {
	store := newAccStore(t)
	acc := &attachmentAccumulator{}
	acc.startWaiting("see this", []inboundAttachment{
		{ID: "img1", Name: "photo.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))},
		{ID: "doc1", Name: "report.pdf", Mime: "application/pdf", Size: int64(len(pdfFixture))},
	})
	// Save both in declaration order.
	acc.handleStart(attachmentStartMsg{ID: "img1", Name: "photo.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))})
	acc.handleBinary(minimalPNGAttachment)
	acc.handleEnd("img1", store)
	acc.handleStart(attachmentStartMsg{ID: "doc1", Name: "report.pdf", Mime: "application/pdf", Size: int64(len(pdfFixture))})
	acc.handleBinary(pdfFixture)
	acc.handleEnd("doc1", store)

	contents := acc.buildContents()
	if len(contents) != 2 {
		t.Fatalf("buildContents returned %d items, want 2", len(contents))
	}
	if contents[0].Type != "image" {
		t.Errorf("contents[0].Type = %q, want 'image'", contents[0].Type)
	}
	if contents[0].Source.Mime != "image/png" {
		t.Errorf("contents[0].Source.Mime = %q, want image/png", contents[0].Source.Mime)
	}
	if contents[1].Type != "document" {
		t.Errorf("contents[1].Type = %q, want 'document'", contents[1].Type)
	}
	if contents[1].Source.Name != "report.pdf" {
		t.Errorf("contents[1].Source.Name = %q, want 'report.pdf'", contents[1].Source.Name)
	}
	// Order matches pendingIDs order regardless of arrival order.
	if contents[0].Source.Path == "" || contents[1].Source.Path == "" {
		t.Error("paths should be populated from saved[id].path")
	}
}

func TestAttachmentAccumulator_BuildContents_DropsFailedIDs(t *testing.T) {
	store := newAccStore(t)
	acc := &attachmentAccumulator{}
	acc.startWaiting("text", []inboundAttachment{
		{ID: "a1", Name: "x.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))},
		{ID: "a2", Name: "broken.png", Mime: "image/png", Size: 10},
	})
	acc.handleStart(attachmentStartMsg{ID: "a1", Name: "x.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))})
	acc.handleBinary(minimalPNGAttachment)
	acc.handleEnd("a1", store)
	acc.handleStart(attachmentStartMsg{ID: "a2", Name: "broken.png", Mime: "image/png", Size: 10})
	acc.handleBinary([]byte{0x01})
	acc.handleEnd("a2", store) // size mismatch → drop

	contents := acc.buildContents()
	if len(contents) != 1 {
		t.Fatalf("buildContents returned %d, want 1 (a2 dropped)", len(contents))
	}
	if contents[0].Source.Path != acc.saved["a1"].path {
		t.Errorf("contents[0] is not the saved a1 path")
	}
}

func TestAttachmentAccumulator_Reset_ClearsAllState(t *testing.T) {
	store := newAccStore(t)
	acc := &attachmentAccumulator{}
	acc.startWaiting("text", []inboundAttachment{{ID: "a1", Name: "x.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))}})
	acc.handleStart(attachmentStartMsg{ID: "a1", Name: "x.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))})
	acc.handleBinary(minimalPNGAttachment)
	acc.handleEnd("a1", store)
	acc.reset()
	if acc.waiting {
		t.Error("waiting = true after reset, want false")
	}
	if acc.text != "" {
		t.Errorf("text = %q, want empty", acc.text)
	}
	if len(acc.pendingIDs) != 0 {
		t.Errorf("pendingIDs = %v, want empty", acc.pendingIDs)
	}
	if acc.metas != nil || acc.saved != nil || acc.dropped != nil {
		t.Errorf("maps not nil after reset: metas=%v saved=%v dropped=%v", acc.metas, acc.saved, acc.dropped)
	}
	if acc.activeID != "" || acc.buf != nil {
		t.Errorf("activeID/buf not cleared: activeID=%q buf=%v", acc.activeID, acc.buf)
	}
}
