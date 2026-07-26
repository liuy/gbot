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

// TestAttachmentAccumulator_HandleStart_AcceptsAnyID asserts the two-phase
// model's key invariant: handleStart does not require id pre-registration —
// the commit-time user_message.attachments is the source of truth.
func TestAttachmentAccumulator_HandleStart_AcceptsAnyID(t *testing.T) {
	acc := &attachmentAccumulator{}
	if errMsg := acc.handleStart(attachmentStartMsg{ID: "anything", Name: "x.png", Mime: "image/png", Size: 10}); errMsg != "" {
		t.Fatalf("handleStart rejected arbitrary id: %q", errMsg)
	}
	if acc.activeID != "anything" {
		t.Errorf("activeID = %q, want 'anything'", acc.activeID)
	}
}

func TestAttachmentAccumulator_HandleStart_RejectsOversize(t *testing.T) {
	acc := &attachmentAccumulator{}
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
	if errMsg := acc.handleStart(attachmentStartMsg{
		Type: "attachment_start", ID: "a1", Name: "edge.bin", Mime: "application/octet-stream", Size: maxAttachmentSize,
	}); errMsg != "" {
		t.Errorf("size==max should be accepted; got errMsg=%q", errMsg)
	}
}

func TestAttachmentAccumulator_HandleStart_RejectsNestedStart(t *testing.T) {
	acc := &attachmentAccumulator{}
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
	if acc.activeID != "a1" {
		t.Errorf("activeID = %q, want 'a1' (first preserved)", acc.activeID)
	}
}

func TestAttachmentAccumulator_HandleStart_InitializesBuffer(t *testing.T) {
	acc := &attachmentAccumulator{}
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
	acc.handleStart(attachmentStartMsg{ID: "a1", Name: "x.png", Mime: "image/png", Size: 2})
	if errMsg := acc.handleBinary([]byte{0x01, 0x02}); errMsg != "" {
		t.Fatalf("first chunk to size limit failed: %q", errMsg)
	}
	errMsg := acc.handleBinary([]byte{0x03})
	if errMsg == "" {
		t.Fatal("expected overflow error, got empty")
	}
	if !strings.Contains(errMsg, "overflow") {
		t.Errorf("errMsg = %q, want substring 'overflow'", errMsg)
	}
	// activeID cleared (no dropped concept in two-phase model — the
	// attachment simply doesn't land in saved).
	if acc.activeID != "" {
		t.Errorf("activeID = %q, want empty after overflow clear", acc.activeID)
	}
}

func TestAttachmentAccumulator_HandleEnd_RejectsSizeMismatch(t *testing.T) {
	store := newAccStore(t)
	acc := &attachmentAccumulator{}
	acc.handleStart(attachmentStartMsg{ID: "a1", Name: "x.png", Mime: "image/png", Size: 10})
	acc.handleBinary([]byte{0x01, 0x02})
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
	if acc.activeID != "" {
		t.Errorf("activeID = %q, want empty after clear", acc.activeID)
	}
}

func TestAttachmentAccumulator_HandleEnd_RejectsMismatchedID(t *testing.T) {
	store := newAccStore(t)
	acc := &attachmentAccumulator{}
	acc.handleStart(attachmentStartMsg{ID: "a1", Name: "x.png", Mime: "image/png", Size: 4})
	acc.handleBinary([]byte{0x01, 0x02, 0x03, 0x04})
	// Calling handleEnd with a different id — the active attachment must
	// NOT be cleared; only size/save failures clear. The correct end-frame
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
	if acc.activeID != "a1" {
		t.Errorf("activeID = %q, want 'a1' preserved", acc.activeID)
	}
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

func TestAttachmentAccumulator_HandleEnd_NilStore_ClearsAndErrors(t *testing.T) {
	acc := &attachmentAccumulator{}
	acc.handleStart(attachmentStartMsg{ID: "a1", Name: "x.png", Mime: "image/png", Size: 4})
	acc.handleBinary([]byte{0x01, 0x02, 0x03, 0x04})
	errMsg := acc.handleEnd("a1", nil)
	if errMsg == "" {
		t.Fatal("expected 'no media cache' error, got empty")
	}
	if !strings.Contains(errMsg, "no media cache") {
		t.Errorf("errMsg = %q, want substring 'no media cache'", errMsg)
	}
	if _, ok := acc.saved["a1"]; ok {
		t.Error("saved[a1] gained an entry despite nil store")
	}
	if acc.activeID != "" {
		t.Errorf("activeID = %q, want empty after clear", acc.activeID)
	}
}

func TestAttachmentAccumulator_BuildContents_ClassifiesByMime(t *testing.T) {
	store := newAccStore(t)
	acc := &attachmentAccumulator{}
	// Save both attachments first (upload phase).
	acc.handleStart(attachmentStartMsg{ID: "img1", Name: "photo.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))})
	acc.handleBinary(minimalPNGAttachment)
	acc.handleEnd("img1", store)
	acc.handleStart(attachmentStartMsg{ID: "doc1", Name: "report.pdf", Mime: "application/pdf", Size: int64(len(pdfFixture))})
	acc.handleBinary(pdfFixture)
	acc.handleEnd("doc1", store)

	// Commit phase: pass the user_message's []inboundAttachment metadata.
	contents, missing := acc.buildContents([]inboundAttachment{
		{ID: "img1", Name: "photo.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))},
		{ID: "doc1", Name: "report.pdf", Mime: "application/pdf", Size: int64(len(pdfFixture))},
	})
	if missing != "" {
		t.Fatalf("missing = %q, want empty", missing)
	}
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
	if contents[0].Source.Path == "" || contents[1].Source.Path == "" {
		t.Error("paths should be populated from saved[id].path")
	}
}

// TestAttachmentAccumulator_BuildContents_MissingIDs_ReturnsError asserts the
// commit-time invariant: if any id is absent from saved (failed upload or
// never sent), buildContents returns the missing ids and nil contents so the
// caller can refuse dispatch.
func TestAttachmentAccumulator_BuildContents_MissingIDs_ReturnsError(t *testing.T) {
	store := newAccStore(t)
	acc := &attachmentAccumulator{}
	acc.handleStart(attachmentStartMsg{ID: "a1", Name: "x.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))})
	acc.handleBinary(minimalPNGAttachment)
	acc.handleEnd("a1", store)
	contents, missing := acc.buildContents([]inboundAttachment{
		{ID: "a1", Name: "x.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))},
		{ID: "a2", Name: "y.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))},
	})
	if contents != nil {
		t.Fatalf("contents = %v, want nil when missing", contents)
	}
	if !strings.Contains(missing, "a2") || strings.Contains(missing, "a1") {
		t.Errorf("missing = %q, want only 'a2'", missing)
	}
}

func TestAttachmentAccumulator_Reset_ClearsAllState(t *testing.T) {
	store := newAccStore(t)
	acc := &attachmentAccumulator{}
	acc.handleStart(attachmentStartMsg{ID: "a1", Name: "x.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))})
	acc.handleBinary(minimalPNGAttachment)
	acc.handleEnd("a1", store)
	acc.reset()
	if acc.saved != nil {
		t.Errorf("saved = %v, want nil after reset", acc.saved)
	}
	if acc.activeID != "" || acc.buf != nil {
		t.Errorf("activeID/buf not cleared: activeID=%q buf=%v", acc.activeID, acc.buf)
	}
}

// TestAttachmentAccumulator_HandleEnd_AfterReset_LazyInitsSavedAndSavesAgain
// exercises the full lifecycle twice: upload→commit→reset→upload→commit.
// reset sets saved back to nil; the lazy-init in handleStart must re-fire so
// the second round's handleEnd can assign without a nil-map panic, and
// buildContents must resolve the new id. Guards against moving the lazy-init
// to a one-shot constructor or weakening it so it only fires on first use.
func TestAttachmentAccumulator_HandleEnd_AfterReset_LazyInitsSavedAndSavesAgain(t *testing.T) {
	store := newAccStore(t)
	acc := &attachmentAccumulator{}

	// Round 1: full upload + commit cycle.
	if errMsg := acc.handleStart(attachmentStartMsg{ID: "r1", Name: "first.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))}); errMsg != "" {
		t.Fatalf("round 1 handleStart failed: %q", errMsg)
	}
	if errMsg := acc.handleBinary(minimalPNGAttachment); errMsg != "" {
		t.Fatalf("round 1 handleBinary failed: %q", errMsg)
	}
	if errMsg := acc.handleEnd("r1", store); errMsg != "" {
		t.Fatalf("round 1 handleEnd failed: %q", errMsg)
	}
	contents1, missing1 := acc.buildContents([]inboundAttachment{
		{ID: "r1", Name: "first.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))},
	})
	if missing1 != "" {
		t.Fatalf("round 1 buildContents missing: %q", missing1)
	}
	if len(contents1) != 1 {
		t.Fatalf("round 1 buildContents returned %d items, want 1", len(contents1))
	}

	// reset clears saved to nil — the next handleStart must lazy-init it
	// again so handleEnd can write to the map.
	acc.reset()
	if acc.saved != nil {
		t.Fatalf("saved = %v, want nil after reset (precondition)", acc.saved)
	}

	// Round 2: same flow on the reset accumulator. If the lazy-init does not
	// re-fire, handleEnd panics on nil-map assignment.
	if errMsg := acc.handleStart(attachmentStartMsg{ID: "r2", Name: "second.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))}); errMsg != "" {
		t.Fatalf("round 2 handleStart failed: %q", errMsg)
	}
	if errMsg := acc.handleBinary(minimalPNGAttachment); errMsg != "" {
		t.Fatalf("round 2 handleBinary failed: %q", errMsg)
	}
	if errMsg := acc.handleEnd("r2", store); errMsg != "" {
		t.Fatalf("round 2 handleEnd failed: %q", errMsg)
	}
	if acc.saved == nil {
		t.Fatal("saved = nil after round 2 handleEnd, want lazy-init to have re-fired")
	}
	if _, ok := acc.saved["r2"]; !ok {
		t.Error("saved[r2] missing after round 2 handleEnd")
	}
	// r1 must NOT survive reset — if it does, reset failed to clear saved
	// and the lazy-init reused stale state.
	if _, ok := acc.saved["r1"]; ok {
		t.Error("saved[r1] leaked across reset — reset must clear it")
	}

	contents2, missing2 := acc.buildContents([]inboundAttachment{
		{ID: "r2", Name: "second.png", Mime: "image/png", Size: int64(len(minimalPNGAttachment))},
	})
	if missing2 != "" {
		t.Fatalf("round 2 buildContents missing: %q", missing2)
	}
	if len(contents2) != 1 {
		t.Fatalf("round 2 buildContents returned %d items, want 1", len(contents2))
	}
	if contents2[0].Type != "image" {
		t.Errorf("contents2[0].Type = %q, want 'image'", contents2[0].Type)
	}
	if contents2[0].Source.Name != "second.png" {
		t.Errorf("contents2[0].Source.Name = %q, want 'second.png'", contents2[0].Source.Name)
	}
	if contents2[0].Source.Mime != "image/png" {
		t.Errorf("contents2[0].Source.Mime = %q, want 'image/png'", contents2[0].Source.Mime)
	}
	if contents2[0].Source.Path == "" {
		t.Error("contents2[0].Source.Path = empty, want populated from saved[r2].path")
	}
}
