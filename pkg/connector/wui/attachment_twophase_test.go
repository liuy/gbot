package wui

import (
	"strings"
	"testing"
	"time"
)

// TestWSAttachment_UploadWithoutUserMessage_NoDispatch is a regression guard
// (passes on both old and new code). Upload alone must never dispatch — only
// the commit-time user_message triggers handleMessageInbound. New code keeps
// saved around but waits for user_message; old code never enters waiting so
// handleStart refuses and saved stays empty.
func TestWSAttachment_UploadWithoutUserMessage_NoDispatch(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)
	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "a1",
		"name": "p1.png", "mime": "image/png", "size": len(minimalPNGAttachment),
	})
	writeWSBinary(t, ws, minimalPNGAttachment)
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "a1"})

	if waitFor(200*time.Millisecond, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryCalls)+len(mock.queryWithContentCalls) != 0
	}) {
		t.Fatal("engine dispatched without user_message")
	}
}

// TestWSAttachment_DispatchOnlyAfterUserMessage is red-light 1: the new model
// dispatches only when the user_message commit frame arrives (after uploads),
// with content blocks assembled in declaration order.
func TestWSAttachment_DispatchOnlyAfterUserMessage(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)
	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	for _, id := range []string{"a1", "a2"} {
		writeWSText(t, ws, map[string]any{
			"type": "attachment_start", "id": id,
			"name": id + ".png", "mime": "image/png", "size": len(minimalPNGAttachment),
		})
		writeWSBinary(t, ws, minimalPNGAttachment)
		writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": id})
	}
	if waitFor(150*time.Millisecond, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryWithContentCalls) != 0
	}) {
		t.Fatal("dispatched before user_message")
	}

	writeWSText(t, ws, map[string]any{
		"type": "message", "text": "two images",
		"attachments": []map[string]any{
			{"id": "a1", "name": "a1.png", "mime": "image/png", "size": len(minimalPNGAttachment)},
			{"id": "a2", "name": "a2.png", "mime": "image/png", "size": len(minimalPNGAttachment)},
		},
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
	if len(call.content) != 3 {
		t.Fatalf("content blocks = %d, want 3", len(call.content))
	}
	if call.content[0].Text != "two images" {
		t.Errorf("content[0].Text = %q", call.content[0].Text)
	}
	if call.content[1].Type != "image" || call.content[2].Type != "image" {
		t.Errorf("content[1]/[2] not both image")
	}
}

// TestWSAttachment_UserMessageWithMissingAttachment_SendsError is red-light 2:
// committing a user_message whose attachment ids are not all in saved must
// produce an error frame naming the missing id and must NOT dispatch.
func TestWSAttachment_UserMessageWithMissingAttachment_SendsError(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)
	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "a1",
		"name": "p1.png", "mime": "image/png", "size": len(minimalPNGAttachment),
	})
	writeWSBinary(t, ws, minimalPNGAttachment)
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "a1"})

	writeWSText(t, ws, map[string]any{
		"type": "message", "text": "missing one",
		"attachments": []map[string]any{
			{"id": "a1", "name": "p1.png", "mime": "image/png", "size": len(minimalPNGAttachment)},
			{"id": "a2", "name": "p2.png", "mime": "image/png", "size": len(minimalPNGAttachment)},
		},
	})
	errMsg := readWSError(t, ws)
	if !strings.Contains(errMsg, "missing attachment") || !strings.Contains(errMsg, "a2") {
		t.Errorf("error = %q, want 'missing attachment' and 'a2'", errMsg)
	}
	if waitFor(200*time.Millisecond, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryCalls)+len(mock.queryWithContentCalls) != 0
	}) {
		t.Fatal("dispatched despite missing attachment")
	}
}

// TestWSAttachment_AfterMissingError_RetryAndCommitWorks is red-light 3: after
// a commit failure the accumulator must NOT reset (saved retains a1), so the
// user can upload the missing id and re-commit successfully.
func TestWSAttachment_AfterMissingError_RetryAndCommitWorks(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)
	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "a1",
		"name": "p1.png", "mime": "image/png", "size": len(minimalPNGAttachment),
	})
	writeWSBinary(t, ws, minimalPNGAttachment)
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "a1"})

	writeWSText(t, ws, map[string]any{
		"type": "message", "text": "retry me",
		"attachments": []map[string]any{
			{"id": "a1", "name": "p1.png", "mime": "image/png", "size": len(minimalPNGAttachment)},
			{"id": "a2", "name": "p2.png", "mime": "image/png", "size": len(minimalPNGAttachment)},
		},
	})
	_ = readWSError(t, ws)

	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "a2",
		"name": "p2.png", "mime": "image/png", "size": len(minimalPNGAttachment),
	})
	writeWSBinary(t, ws, minimalPNGAttachment)
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "a2"})

	writeWSText(t, ws, map[string]any{
		"type": "message", "text": "retry me",
		"attachments": []map[string]any{
			{"id": "a1", "name": "p1.png", "mime": "image/png", "size": len(minimalPNGAttachment)},
			{"id": "a2", "name": "p2.png", "mime": "image/png", "size": len(minimalPNGAttachment)},
		},
	})
	if !waitFor(time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryWithContentCalls) == 1
	}) {
		mock.mu.Lock()
		t.Fatalf("queryWithContentCalls = %d, want 1 after retry", len(mock.queryWithContentCalls))
		mock.mu.Unlock()
		return
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.queryWithContentCalls[0].content) != 3 {
		t.Fatalf("content blocks = %d, want 3 (text + 2 images)", len(mock.queryWithContentCalls[0].content))
	}
}
