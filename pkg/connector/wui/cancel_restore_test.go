package wui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"sync/atomic"

	"github.com/gorilla/websocket"

	"github.com/liuy/gbot/pkg/types"
)

// readUntilType reads WS frames until one matches wantType (or times out).
func readUntilType(t *testing.T, ws *websocket.Conn, wantType string) json.RawMessage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second) // REAL-TIME
	for time.Now().Before(deadline) {           // REAL-TIME
		data := readWSMessage(t, ws)
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &head) == nil && head.Type == wantType {
			return json.RawMessage(data)
		}
	}
	t.Fatalf("timed out waiting for frame type %q", wantType)
	return nil
}

// TestCancelQueued_RestoresAttachmentsWithResendableIDs pins the queue-cancel
// restore contract: cancel_queued must return the popped messages with their
// attachments re-staged under fresh upload ids, so the client can rebuild
// chips that re-send without re-uploading bytes.
func TestCancelQueued_RestoresAttachmentsWithResendableIDs(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	mock := c.mock()
	mock.systemPromptFn = func() string { return "" }
	// All fn fields are set BEFORE dialing: the metadata frame sent at
	// connect already reads PendingAttachments from the server goroutine.
	// Mid-test flips race the readLoop — drive busy through an atomic the
	// closure reads instead.
	var busy atomic.Bool
	busy.Store(true) // streaming: message queues
	mock.isBusyFn = func() bool { return busy.Load() }
	mock.pendingAttachmentsFn = func() []types.QueuedItem {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return append([]types.QueuedItem(nil), mock.enqueueCalls...)
	}
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "a1",
		"name": "shot.png", "mime": "image/png", "size": len(minimalPNGAttachment),
	})
	writeWSBinary(t, ws, minimalPNGAttachment)
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "a1"})
	writeWSText(t, ws, map[string]any{
		"type": "message", "text": "see this",
		"attachments": []map[string]any{
			{"id": "a1", "name": "shot.png", "mime": "image/png", "size": len(minimalPNGAttachment)},
		},
	})

	queued := readUntilType(t, ws, "queued")
	var q struct {
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal(queued, &q); err != nil || q.UUID == "" {
		t.Fatalf("queued event uuid missing: %v %q", err, queued)
	}

	writeWSText(t, ws, map[string]any{"type": "cancel_queued", "uuids": []string{q.UUID}})

	result := readUntilType(t, ws, "cancel_result")
	var cr struct {
		Removed  []string `json:"removed"`
		Restored []struct {
			Text        string `json:"text"`
			Attachments []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Mime string `json:"mime"`
				Size int    `json:"size"`
			} `json:"attachments"`
		} `json:"restored"`
	}
	if err := json.Unmarshal(result, &cr); err != nil {
		t.Fatalf("cancel_result decode: %v", err)
	}
	if len(cr.Removed) != 1 || cr.Removed[0] != q.UUID {
		t.Fatalf("removed = %v, want [%s]", cr.Removed, q.UUID)
	}
	if len(cr.Restored) != 1 || cr.Restored[0].Text != "see this" {
		t.Fatalf("restored = %+v, want one entry with text", cr.Restored)
	}
	atts := cr.Restored[0].Attachments
	if len(atts) != 1 {
		t.Fatalf("restored attachments = %+v, want 1", atts)
	}
	// Image blocks lose their filename at assembly (queuedAttachmentJSON
	// contract); the client synthesizes a display name from the mime.
	if atts[0].ID == "" || atts[0].ID == "a1" {
		t.Fatalf("restored attachment id = %q, want a fresh re-staged id", atts[0].ID)
	}
	if atts[0].Mime != "image/png" || atts[0].Size != len(minimalPNGAttachment) {
		t.Fatalf("restored attachment meta = %+v", atts[0])
	}

	// The fresh id must round-trip: resend while idle dispatches the image.
	busy.Store(false)
	writeWSText(t, ws, map[string]any{
		"type": "message", "text": "again",
		"attachments": []map[string]any{
			{"id": atts[0].ID, "name": atts[0].Name, "mime": atts[0].Mime, "size": atts[0].Size},
		},
	})
	if !waitFor(10*time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryWithContentCalls) == 1
	}) {
		t.Fatal("resend with restored id never dispatched")
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	call := mock.queryWithContentCalls[0]
	hasImage := false
	for _, cb := range call.content {
		if cb.Type == "image" {
			hasImage = true
		}
	}
	if !hasImage {
		t.Fatalf("resend content missing image block: %+v", call.content)
	}
}

// TestCancelQueued_EmptyUUIDsPopsAll pins the id-race escape hatch: when the
// client cancels before the server-assigned uuids arrive, an empty uuid list
// pops every pending prompt item so nothing leaks into the next turn.
func TestCancelQueued_EmptyUUIDsPopsAll(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	mock := c.mock()
	mock.systemPromptFn = func() string { return "" }
	mock.isBusyFn = func() bool { return true }
	mock.pendingAttachmentsFn = func() []types.QueuedItem {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return append([]types.QueuedItem(nil), mock.enqueueCalls...)
	}
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	writeWSText(t, ws, map[string]any{"type": "message", "text": "orphan risk"})
	queued := readUntilType(t, ws, "queued")
	var q struct {
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal(queued, &q); err != nil || q.UUID == "" {
		t.Fatalf("queued event: %v %q", err, queued)
	}

	writeWSText(t, ws, map[string]any{"type": "cancel_queued", "uuids": []string{}})
	result := readUntilType(t, ws, "cancel_result")
	var cr struct {
		Removed  []string `json:"removed"`
		Restored []struct {
			Text string `json:"text"`
		} `json:"restored"`
	}
	if err := json.Unmarshal(result, &cr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(cr.Removed) != 1 || len(cr.Restored) != 1 || cr.Restored[0].Text != "orphan risk" {
		t.Fatalf("pop-all result = %+v", cr)
	}
}

// TestCancelEpoch_MidParseCancelRestoresInsteadOfEnqueue pins the epoch
// guard: when the user cancels while a document is still parsing, the
// assemble goroutine must NOT enqueue (the client already cleared its
// bubbles) — it emits a late cancel_result carrying the restored payload.
// The assembleGate hook holds the goroutine mid-parse so the test can bump
// the epoch inside the real capture→compare window.
func TestCancelEpoch_MidParseCancelRestoresInsteadOfEnqueue(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	mock := c.mock()
	mock.systemPromptFn = func() string { return "" }
	mock.isBusyFn = func() bool { return true }
	mock.pendingAttachmentsFn = func() []types.QueuedItem { return nil }

	entered := make(chan struct{})
	release := make(chan struct{})
	prevGate := assembleGate
	assembleGate = func() {
		close(entered)
		<-release
	}
	t.Cleanup(func() { assembleGate = prevGate })

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	writeWSText(t, ws, map[string]any{"type": "message", "text": "mid parse"})
	<-entered

	// The user cancels while the parse goroutine is parked: epoch bumps.
	slot := c.activeSlot()
	if slot == nil {
		t.Fatal("no active slot")
	}
	slot.cancelEpoch.Add(1)
	close(release)

	// The goroutine must take the late-restore branch: a cancel_result
	// frame with the text, and nothing enters the engine queue.
	result := readUntilType(t, ws, "cancel_result")
	var cr struct {
		Restored []struct {
			Text string `json:"text"`
		} `json:"restored"`
	}
	if err := json.Unmarshal(result, &cr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(cr.Restored) != 1 || cr.Restored[0].Text != "mid parse" {
		t.Fatalf("restored = %+v, want [mid parse]", cr.Restored)
	}
	// The queue never received the item (also verified via metadata frame:
	// pendingAttachmentsFn returns nil — any enqueue would surface as a
	// 'queued' frame; drain checked nothing but cancel_result arrived).
	mock.mu.Lock()
	enqueued := len(mock.enqueueCalls)
	mock.mu.Unlock()
	if enqueued != 0 {
		t.Fatalf("enqueueCalls = %d, want 0 (late path must not enqueue)", enqueued)
	}
}

// TestResolveContents_BindsRestoredIDAcrossConnections pins the reconnect
// survival: a fresh accumulator (new WS connection) resolves an attachment
// id remembered by a PREVIOUS connection through the connector-level map.
func TestResolveContents_BindsRestoredIDAcrossConnections(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	defer srv.Close()
	c.rememberUpload("restored-1", savedAttachment{
		path: "/cache/doc.pdf", mime: "application/pdf", kind: "document",
	})
	acc := &attachmentAccumulator{}
	contents, missing := c.resolveContents(acc, []inboundAttachment{
		{ID: "restored-1", Name: "doc.pdf", Mime: "application/pdf", Size: 10},
	})
	if missing != "" {
		t.Fatalf("missing = %q, want resolved via connector map", missing)
	}
	if len(contents) != 1 || contents[0].Source.Path != "/cache/doc.pdf" {
		t.Fatalf("contents = %+v", contents)
	}
	// Second resolve on the SAME accumulator now hits acc.saved directly.
	_, missing2 := c.resolveContents(acc, []inboundAttachment{
		{ID: "restored-1", Name: "doc.pdf", Mime: "application/pdf", Size: 10},
	})
	if missing2 != "" {
		t.Fatalf("second resolve missing = %q", missing2)
	}
}
