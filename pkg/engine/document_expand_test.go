package engine

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/types"
)

// seedDocumentFixture prepares HOME/.gbot/cache with a stored document and
// its parse-cache entry, returning a document block referencing the file.
// The seeded cache mirrors "parsed on an earlier turn" so the expansion under
// test is deterministic (no real parse).
func seedDocumentFixture(t *testing.T, name, key, markdown string) types.ContentBlock {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".gbot", "cache")
	for _, dir := range []string{"documents", string(media.CategoryParse)} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	docPath := filepath.Join(root, "documents", key+".txt")
	if err := os.WriteFile(docPath, []byte("unparsed bytes"), 0o644); err != nil {
		t.Fatalf("write stored doc: %v", err)
	}
	cachePath := filepath.Join(root, string(media.CategoryParse), key+".md")
	if err := os.WriteFile(cachePath, []byte(markdown), 0o644); err != nil {
		t.Fatalf("write parse cache: %v", err)
	}
	return types.NewDocumentBlock(name, docPath, "text/plain", int64(len("unparsed bytes")), 0)
}

// TestPrepareAPIMessages_ExpandsDocumentBlocks drives the LLM-context
// choke point with a user message carrying a document reference block: the
// API view must carry header+markdown TEXT, no document block may leak to the
// provider, and the engine's stored history must still hold the reference.
func TestPrepareAPIMessages_ExpandsDocumentBlocks(t *testing.T) {
	doc := seedDocumentFixture(t, "notes.txt", "0123456789abcdef", "# parsed\n\nbody text")

	eng := New(&Params{Provider: &testProvider{}, Model: "test"})
	t.Cleanup(func() { eng.Close() })
	eng.SetSessionID("document-expand-test")
	eng.appendMessage(types.NewUserMessage([]types.ContentBlock{
		types.NewTextBlock("summarize this"),
		doc,
	}))

	api := eng.prepareAPIMessages(context.Background())
	if len(api) != 1 {
		t.Fatalf("api messages = %d, want 1", len(api))
	}
	if len(api[0].Content) != 2 {
		t.Fatalf("content blocks = %d, want 2", len(api[0].Content))
	}
	if api[0].Content[1].Type != types.ContentTypeText {
		t.Fatalf("content[1].Type = %q, want text", api[0].Content[1].Type)
	}
	if want := "[Document: notes.txt]\n# parsed\n\nbody text"; api[0].Content[1].Text != want {
		t.Errorf("expanded text = %q, want %q", api[0].Content[1].Text, want)
	}

	stored := eng.Messages()
	if len(stored) != 1 || stored[0].Content[1].Type != types.ContentTypeDocument {
		t.Errorf("engine history mutated: %+v, want the document reference preserved", stored)
	}
}

// TestPrepareAPIMessages_DocumentParseFailure_HeaderOnly covers the
// unparseable document: expansion degrades to a header-only text block
// (matching the pre-document-block fallback semantics) instead of failing
// the whole LLM call.
func TestPrepareAPIMessages_DocumentParseFailure_HeaderOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".gbot", "cache")
	if err := os.MkdirAll(filepath.Join(root, "documents"), 0o755); err != nil {
		t.Fatalf("mkdir documents: %v", err)
	}
	// Null bytes: fileread refuses the file, and with no prior parse-cache
	// entry the expansion must fall back to the header.
	docPath := filepath.Join(root, "documents", "fedcba9876543210.bin")
	if err := os.WriteFile(docPath, []byte{0x00, 0x01, 0x00}, 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}

	eng := New(&Params{Provider: &testProvider{}, Model: "test"})
	t.Cleanup(func() { eng.Close() })
	eng.SetSessionID("document-fail-test")
	eng.appendMessage(types.NewUserMessage([]types.ContentBlock{
		types.NewDocumentBlock("weird.bin", docPath, "application/octet-stream", 3, 0),
	}))

	api := eng.prepareAPIMessages(context.Background())
	// injectTimestamp prepends the wall-clock block for user messages whose
	// first block is not text — so the API view is [ts, header-only text].
	if len(api) != 1 || len(api[0].Content) != 2 {
		t.Fatalf("api = %+v, want one message with [timestamp, header] blocks", api)
	}
	if api[0].Content[1].Type != types.ContentTypeText {
		t.Fatalf("content[1].Type = %q, want text", api[0].Content[1].Type)
	}
	if want := "[Document: weird.bin]"; api[0].Content[1].Text != want {
		t.Errorf("fallback text = %q, want %q", api[0].Content[1].Text, want)
	}
}

// TestPrepareAPIMessages_AttachmentDocument_BusyPath proves the busy-path
// chain end to end: a queued attachment carrying [text, document] blocks is
// normalized by marshalMessages (system-reminder text + verbatim document
// reference) and then expanded by prepareAPIMessages — the document content
// reaches the provider even though it arrived mid-stream.
func TestPrepareAPIMessages_AttachmentDocument_BusyPath(t *testing.T) {
	doc := seedDocumentFixture(t, "notes.txt", "0123456789abcdef", "parsed body")

	eng := New(&Params{Provider: &testProvider{}, Model: "test"})
	t.Cleanup(func() { eng.Close() })
	eng.SetSessionID("document-busy-test")
	attMsgs := eng.createAttachmentMessages([]types.QueuedItem{{
		Value:     "while you were working",
		Content:   []types.ContentBlock{types.NewTextBlock("while you were working"), doc},
		Mode:      types.ItemModePrompt,
		UUID:      "att-1",
		Priority:  types.PriorityNext,
		Origin:    &types.MessageOrigin{Kind: types.OriginHuman},
		Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}})
	eng.appendMessages(attMsgs)

	api := eng.prepareAPIMessages(context.Background())
	if len(api) != 1 {
		t.Fatalf("api messages = %d, want 1 (attachment stands alone)", len(api))
	}
	content := api[0].Content
	if len(content) != 2 {
		t.Fatalf("content blocks = %d, want 2 (reminder + expanded document)", len(content))
	}
	if content[0].Type != types.ContentTypeText ||
		!strings.Contains(content[0].Text, "<system-reminder>") ||
		!strings.Contains(content[0].Text, "while you were working") {
		t.Errorf("content[0] = %+v, want system-reminder wrapping the prompt", content[0])
	}
	if content[1].Type != types.ContentTypeText || content[1].Text != "[Document: notes.txt]\nparsed body" {
		t.Errorf("content[1] = %+v, want expanded document text", content[1])
	}
	for _, b := range content {
		if b.Type == types.ContentTypeDocument {
			t.Errorf("document reference block leaked to provider: %+v", b)
		}
	}
}

// TestQueryWithContent_DocumentExpandedInProviderRequest runs the FULL live
// chain: QueryWithContent (async goroutine) → runTurns → callLLM → provider.
// The request the provider receives must carry the expanded text block and
// zero document blocks — this catches a regression at any hop between the
// public entry point and the LLM boundary. The engine's own stored history
// keeps the reference block (asserted via the query_start event).
func TestQueryWithContent_DocumentExpandedInProviderRequest(t *testing.T) {
	doc := seedDocumentFixture(t, "notes.txt", "0123456789abcdef", "e2e markdown body")

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "reply"), nil)
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		Model:      "test-model",
		Logger:     slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSessionID("document-e2e-test")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	eng.QueryWithContent(ctx, []types.ContentBlock{
		types.NewTextBlock("summarize this"),
		doc,
	}, "")

	result := ec.WaitForResult()
	if result.Error != nil {
		t.Fatalf("query error: %v", result.Error)
	}

	var sawExpanded, sawDocument bool
	for _, msg := range mp.lastRequestMessages() {
		for _, b := range msg.Content {
			if b.Type == types.ContentTypeDocument {
				sawDocument = true
			}
			if b.Type == types.ContentTypeText && strings.Contains(b.Text, "[Document: notes.txt]\ne2e markdown body") {
				sawExpanded = true
			}
		}
	}
	if !sawExpanded {
		t.Error("provider request missing the expanded document text block")
	}
	if sawDocument {
		t.Error("document reference block leaked into the provider request")
	}

	starts := ec.FindEvents(types.EventQueryStart)
	if len(starts) != 1 || starts[0].Message == nil {
		t.Fatalf("EventQueryStart count = %d (want 1 with Message)", len(starts))
	}
	if starts[0].Message.Content[1].Type != types.ContentTypeDocument {
		t.Errorf("stored message block[1].Type = %q, want document reference preserved in history",
			starts[0].Message.Content[1].Type)
	}
}

// TestNormalizeAttachmentForAPI_TextOnly_Unchanged pins the Value-only
// attachment (no explicit Content blocks — the createAttachmentMessages
// fallback [text(Value)]): output must match the historical string-prompt
// form, a single system-reminder text block.
func TestNormalizeAttachmentForAPI_TextOnly_Unchanged(t *testing.T) {
	msg := types.Message{
		ID:          "att-2",
		Role:        types.RoleUser,
		MessageType: types.MessageTypeAttachment,
		Content:     []types.ContentBlock{types.NewTextBlock("plain queued")},
		Attachment: &types.Attachment{
			Type:       types.AttachmentTypeQueued,
			Prompt:     "plain queued",
			SourceUUID: "att-2",
			Mode:       types.ItemModePrompt,
			Origin:     &types.MessageOrigin{Kind: types.OriginHuman},
		},
	}
	got := normalizeAttachmentForAPI(msg)
	if len(got.Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(got.Content))
	}
	if got.Content[0].Type != types.ContentTypeText {
		t.Fatalf("content[0].Type = %q, want text", got.Content[0].Type)
	}
	if !strings.HasPrefix(got.Content[0].Text, "<system-reminder>\n") ||
		!strings.HasSuffix(got.Content[0].Text, "\n</system-reminder>") ||
		!strings.Contains(got.Content[0].Text, "plain queued") {
		t.Errorf("content[0].Text = %q, want system-reminder wrapping the prompt", got.Content[0].Text)
	}
	if got.ID != "att-2" {
		t.Errorf("ID = %q, want att-2", got.ID)
	}
}
