package wui

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/types"
)

// setupInboundConnector returns a connector with a real media store containing
// one image file (PNG bytes) and one document file (text content) ready for
// attachment tests to reference by path.
func setupInboundConnector(t *testing.T) (*WUIConnector, string, string) {
	t.Helper()
	c := newTestConnector(t)
	store, err := media.NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("media.NewAt: %v", err)
	}
	t.Cleanup(store.Close)
	c.SetMediaCache(store)

	imgPath, err := store.Save(media.CategoryImage, minimalPNGAttachment, ".png")
	if err != nil {
		t.Fatalf("Save image: %v", err)
	}
	docPath, err := store.Save(media.CategoryDocument, []byte("plain text body"), ".txt")
	if err != nil {
		t.Fatalf("Save doc: %v", err)
	}
	return c, imgPath, docPath
}

func TestHandleMessageInbound_ImageContent_ConvertedToBase64Block(t *testing.T) {
	c, imgPath, _ := setupInboundConnector(t)
	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	c.handleMessageInbound("", []inboundContent{
		{Type: "image", Source: inboundSource{Type: "file", Path: imgPath, Mime: "image/png"}},
	})

	if !waitFor(time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryWithContentCalls) == 1
	}) {
		mock.mu.Lock()
		t.Fatalf("queryWithContentCalls = %d, want 1", len(mock.queryWithContentCalls))
		mock.mu.Unlock()
		return
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	call := mock.queryWithContentCalls[0]
	if len(call.content) != 1 {
		t.Fatalf("content blocks = %d, want 1 (image only)", len(call.content))
	}
	b := call.content[0]
	if b.Type != types.ContentTypeImage {
		t.Errorf("block type = %v, want image", b.Type)
	}
	if b.Source == nil {
		t.Fatal("Source nil")
	}
	if b.Source.Type != "base64" {
		t.Errorf("source type = %q, want base64", b.Source.Type)
	}
	if b.Source.Data == "" {
		t.Error("source Data is empty, want base64-encoded image bytes")
	}
	if _, err := base64.StdEncoding.DecodeString(b.Source.Data); err != nil {
		t.Errorf("source Data is not valid base64: %v", err)
	}
}

func TestHandleMessageInbound_DocumentContent_ConvertedToTextBlock(t *testing.T) {
	c, _, docPath := setupInboundConnector(t)
	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	c.handleMessageInbound("", []inboundContent{
		{Type: "document", Source: inboundSource{Type: "file", Path: docPath, Name: "foo.txt"}},
	})

	if !waitFor(time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryWithContentCalls) == 1
	}) {
		mock.mu.Lock()
		t.Fatalf("queryWithContentCalls = %d, want 1", len(mock.queryWithContentCalls))
		mock.mu.Unlock()
		return
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	call := mock.queryWithContentCalls[0]
	if len(call.content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(call.content))
	}
	b := call.content[0]
	if b.Type != types.ContentTypeText {
		t.Errorf("block type = %v, want text", b.Type)
	}
	if !strings.HasPrefix(b.Text, "[Document: foo.txt saved at ") {
		t.Errorf("text = %q, want prefix [Document: foo.txt saved at ", b.Text)
	}
	if !strings.HasSuffix(b.Text, "plain text body") {
		t.Errorf("text = %q, want suffix 'plain text body'", b.Text)
	}
}

func TestHandleMessageInbound_InvalidPath_Skipped_SilentWhenTextEmpty(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()
	mock.isBusyFn = func() bool { return false }

	// Invalid path: /etc/passwd is not under the media cache (and there's
	// no media cache at all on this connector).
	c.handleMessageInbound("", []inboundContent{
		{Type: "image", Source: inboundSource{Type: "file", Path: "/etc/passwd"}},
	})

	// Poll briefly to give the goroutine launched by handleMessageInbound
	// time to land (or, in this case, NOT land) any calls. We use waitFor
	// with a condition that polls for ANY call landing; if it returns true,
	// the negative-assertion follow-up below would catch the bug. If it
	// returns false (expected), the goroutine had its window and we move on.
	callsLanded := func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryWithContentCalls)+len(mock.queryCalls) != 0
	}
	_ = waitFor(200*time.Millisecond, callsLanded)
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.queryWithContentCalls) != 0 {
		t.Errorf("queryWithContentCalls = %d, want 0 (rejected path should not dispatch)", len(mock.queryWithContentCalls))
	}
	if len(mock.queryCalls) != 0 {
		t.Errorf("queryCalls = %d, want 0 (no text fallback path)", len(mock.queryCalls))
	}
}

func TestHandleMessageInbound_InvalidPath_WithText_StillSendsText(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	// Invalid image + valid text. Per the plan, dispatch uses len(content)
	// (not len(blocks)): since len(content) > 0, QueryWithContent is used
	// even when all paths are rejected. The text still reaches the engine
	// as the sole ContentBlock.
	c.handleMessageInbound("hi", []inboundContent{
		{Type: "image", Source: inboundSource{Type: "file", Path: "/etc/passwd"}},
	})

	if !waitFor(time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryWithContentCalls) == 1
	}) {
		mock.mu.Lock()
		t.Fatalf("queryWithContentCalls = %d, want 1", len(mock.queryWithContentCalls))
		mock.mu.Unlock()
		return
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	call := mock.queryWithContentCalls[0]
	if len(call.content) != 1 {
		t.Fatalf("content blocks = %d, want 1 (text only after rejection)", len(call.content))
	}
	b := call.content[0]
	if b.Type != types.ContentTypeText || b.Text != "hi" {
		t.Errorf("content[0] = %+v, want text 'hi'", b)
	}
	// No raw Query call should have landed.
	if len(mock.queryCalls) != 0 {
		t.Errorf("queryCalls = %d, want 0 (QueryWithContent handles text fallback)", len(mock.queryCalls))
	}
}

func TestHandleMessageInbound_TextAndImage_BothSent(t *testing.T) {
	c, imgPath, _ := setupInboundConnector(t)
	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	c.handleMessageInbound("see this", []inboundContent{
		{Type: "image", Source: inboundSource{Type: "file", Path: imgPath, Mime: "image/png"}},
	})

	if !waitFor(time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryWithContentCalls) == 1
	}) {
		mock.mu.Lock()
		t.Fatalf("queryWithContentCalls = %d, want 1", len(mock.queryWithContentCalls))
		mock.mu.Unlock()
		return
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	call := mock.queryWithContentCalls[0]
	if len(call.content) != 2 {
		t.Fatalf("content blocks = %d, want 2 (text + image)", len(call.content))
	}
	if call.content[0].Type != types.ContentTypeText || call.content[0].Text != "see this" {
		t.Errorf("content[0] = %+v, want text 'see this'", call.content[0])
	}
	if call.content[1].Type != types.ContentTypeImage {
		t.Errorf("content[1].Type = %v, want image", call.content[1].Type)
	}
}

func TestHandleMessageInbound_EngineBusy_EnqueuesWithContent(t *testing.T) {
	c, imgPath, _ := setupInboundConnector(t)
	mock := c.mock()
	mock.isBusyFn = func() bool { return true }

	c.handleMessageInbound("hi", []inboundContent{
		{Type: "image", Source: inboundSource{Type: "file", Path: imgPath, Mime: "image/png"}},
	})

	if !waitFor(time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.enqueueCalls) == 1
	}) {
		mock.mu.Lock()
		t.Fatalf("enqueueCalls = %d, want 1", len(mock.enqueueCalls))
		mock.mu.Unlock()
		return
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	item := mock.enqueueCalls[0]
	if item.Value != "hi" {
		t.Errorf("Value = %q, want 'hi'", item.Value)
	}
	if len(item.Content) != 2 {
		t.Fatalf("item.Content blocks = %d, want 2 (text + image)", len(item.Content))
	}
	if item.Content[1].Type != types.ContentTypeImage {
		t.Errorf("item.Content[1].Type = %v, want image", item.Content[1].Type)
	}
}

// TestParseDocument_FallbackOnParseFailure exercises the parseDocument
// fallback path: a path that exists on disk (no path validator in the new
// WS upload flow) but is not a document fileread can convert. The function
// MUST return a header-only text block, never an error.
func TestParseDocument_FallbackOnParseFailure(t *testing.T) {
	c := newTestConnector(t)
	store, err := media.NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("media.NewAt: %v", err)
	}
	t.Cleanup(store.Close)
	c.SetMediaCache(store)

	// Save a file under documents/ with .bin extension — fileread cannot
	// parse arbitrary binary, so it returns empty/error.
	garbagePath, err := store.Save(media.CategoryDocument, []byte{0x00, 0x01, 0x02, 0x03}, ".bin")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	block := c.parseDocument(context.Background(), garbagePath, "weird.bin")
	if block.Type != types.ContentTypeText {
		t.Errorf("block type = %v, want text", block.Type)
	}
	if !strings.HasPrefix(block.Text, "[Document: weird.bin saved at ") {
		t.Errorf("text = %q, want prefix '[Document: weird.bin saved at '", block.Text)
	}
	// Fallback path emits ONLY the header line (no trailing newline + body).
	if strings.Contains(block.Text, "\n") {
		t.Errorf("fallback text contains newline — should be header-only, got: %q", block.Text)
	}
	// The path must be present so the LLM can locate the file if needed.
	if !strings.HasSuffix(block.Text, garbagePath+"]") {
		t.Errorf("text = %q, want suffix %q]", block.Text, garbagePath+"]")
	}
}

// TestParseDocument_InlineText verifies a real text file is parsed and the
// content is appended after the [Document: ...] header line.
func TestParseDocument_InlineText(t *testing.T) {
	c := newTestConnector(t)
	store, err := media.NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("media.NewAt: %v", err)
	}
	t.Cleanup(store.Close)
	c.SetMediaCache(store)

	const body = "the quick brown fox jumps over the lazy dog"
	path, err := store.Save(media.CategoryDocument, []byte(body), ".txt")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	block := c.parseDocument(context.Background(), path, "story.txt")
	if block.Type != types.ContentTypeText {
		t.Errorf("block type = %v, want text", block.Type)
	}
	prefix := "[Document: story.txt saved at " + path + "]"
	if !strings.HasPrefix(block.Text, prefix) {
		t.Errorf("text = %q, want prefix %q", block.Text, prefix)
	}
	// Content follows the header line, separated by newline.
	rest := strings.TrimPrefix(block.Text, prefix+"\n")
	if rest != body {
		t.Errorf("body = %q, want %q", rest, body)
	}
}

// TestAssembleContentBlocks_UnknownTypeSkipped verifies that content items
// with an unrecognized type field are silently skipped (defense against
// future protocol additions arriving before the backend is updated).
func TestAssembleContentBlocks_UnknownTypeSkipped(t *testing.T) {
	c, imgPath, _ := setupInboundConnector(t)
	blocks := c.assembleContentBlocks(context.Background(), "hello", []inboundContent{
		{Type: "image", Source: inboundSource{Type: "file", Path: imgPath, Mime: "image/png"}},
		{Type: "video", Source: inboundSource{Type: "file", Path: "/x/y.mp4"}},
	})
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2 (text + valid image, video skipped)", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeText || blocks[0].Text != "hello" {
		t.Errorf("blocks[0] = %+v, want text 'hello'", blocks[0])
	}
	if blocks[1].Type != types.ContentTypeImage {
		t.Errorf("blocks[1].Type = %v, want image", blocks[1].Type)
	}
}

// TestAssembleContentBlocks_NilMediaCacheSkipsAll verifies the handler
// degrades cleanly when mediaCache is unset (test-only state, but defensive).
func TestAssembleContentBlocks_NilMediaCacheSkipsAll(t *testing.T) {
	c := newTestConnector(t) // no SetMediaCache call
	blocks := c.assembleContentBlocks(context.Background(), "", []inboundContent{
		{Type: "image", Source: inboundSource{Type: "file", Path: "/x/y.png"}},
	})
	if len(blocks) != 0 {
		t.Errorf("blocks = %d, want 0 (no media cache → all skipped)", len(blocks))
	}
}

// TestAssembleContentBlocks_NonImageBytesSkipped verifies that a path whose
// bytes are not a recognized image format is silently skipped (SniffImageMime
// returns "" on the document's text bytes). The legacy path validator is
// gone in the WS upload flow — the sniff is now the only gate.
func TestAssembleContentBlocks_NonImageBytesSkipped(t *testing.T) {
	c, _, docPath := setupInboundConnector(t)
	blocks := c.assembleContentBlocks(context.Background(), "", []inboundContent{
		// Claiming a document path is an image — must be skipped because
		// SniffImageMime sees the document's plain-text bytes and returns "".
		{Type: "image", Source: inboundSource{Type: "file", Path: docPath}},
	})
	if len(blocks) != 0 {
		t.Errorf("blocks = %d, want 0 (non-image bytes should be skipped)", len(blocks))
	}
}

// TestHandleMessageInbound_AllRejectedSendsError verifies that when ALL
// content items are rejected AND text is empty, a WS error frame is sent
// (rather than silently dispatching nothing).
func TestHandleMessageInbound_AllRejectedSendsError(t *testing.T) {
	// Build a connector WITHOUT starting wsWriter so the test can drain
	// wsCh directly. Otherwise wsWriter would consume and discard frames
	// (no active WS connection).
	mock := &mockEngine{
		isBusyFn: func() bool { return false },
	}
	const engineID = "main"
	c := &WUIConnector{
		slots:       make(map[string]*engineSlot),
		pendingAsks: make(map[string]*types.AskEvent),
		wsCh:        make(chan wsMsg, 16),
		done:        make(chan struct{}),
		testMock:    mock,
	}
	activeID := engineID
	c.active.Store(&activeID)
	slot := &engineSlot{
		engineID:    engineID,
		engine:      mock,
		taskToolIDs: make(map[string]bool),
	}
	slot.active.Store(true)
	c.slots[engineID] = slot
	// No media cache → all attachments rejected.

	c.handleMessageInbound("", []inboundContent{
		{Type: "image", Source: inboundSource{Type: "file", Path: "/x/y.png"}},
	})

	select {
	case got := <-c.wsCh:
		if !strings.Contains(string(got.data), `"type":"error"`) {
			t.Errorf("payload = %q, want substring '\"type\":\"error\"'", string(got.data))
		}
		if !strings.Contains(string(got.data), "attachments rejected") {
			t.Errorf("payload = %q, want substring 'attachments rejected'", string(got.data))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no WS frame received within 2s")
	}
}

// TestHandleMessageInbound_DocumentUsesPathBasenameWhenNameEmpty ensures
// the document block falls back to filepath.Base(path) when Name is empty.
func TestHandleMessageInbound_DocumentUsesPathBasenameWhenNameEmpty(t *testing.T) {
	c, _, docPath := setupInboundConnector(t)
	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	c.handleMessageInbound("", []inboundContent{
		{Type: "document", Source: inboundSource{Type: "file", Path: docPath}}, // no Name
	})

	if !waitFor(time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryWithContentCalls) == 1
	}) {
		mock.mu.Lock()
		t.Fatalf("queryWithContentCalls = %d, want 1", len(mock.queryWithContentCalls))
		mock.mu.Unlock()
		return
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	b := mock.queryWithContentCalls[0].content[0]
	want := "[Document: " + filepath.Base(docPath) + " saved at " + docPath + "]"
	if !strings.HasPrefix(b.Text, want) {
		t.Errorf("text = %q, want prefix %q", b.Text, want)
	}
}

// TestHandleMessageInbound_ImageMimeResniff ensures that when the client
// omits the mime field, the backend re-sniffs from the file bytes rather
// than emitting an image block with empty MediaType.
func TestHandleMessageInbound_ImageMimeResniff(t *testing.T) {
	c, imgPath, _ := setupInboundConnector(t)
	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	c.handleMessageInbound("", []inboundContent{
		{Type: "image", Source: inboundSource{Type: "file", Path: imgPath}}, // no Mime
	})

	if !waitFor(time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryWithContentCalls) == 1
	}) {
		mock.mu.Lock()
		t.Fatalf("queryWithContentCalls = %d, want 1", len(mock.queryWithContentCalls))
		mock.mu.Unlock()
		return
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	b := mock.queryWithContentCalls[0].content[0]
	if b.Source == nil {
		t.Fatal("Source nil")
	}
	if b.Source.MediaType != "image/png" {
		t.Errorf("MediaType = %q, want image/png (re-sniffed from bytes)", b.Source.MediaType)
	}
}
