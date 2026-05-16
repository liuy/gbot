package engine

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// attachmentQueue unit tests
// ---------------------------------------------------------------------------

func TestAttachmentQueue_EnqueueAndDrainAll(t *testing.T) {
	var q attachmentQueue
	q.Enqueue(types.QueuedItem{Value: "a", Priority: types.PriorityLater})
	q.Enqueue(types.QueuedItem{Value: "b", Priority: types.PriorityNext})
	q.Enqueue(types.QueuedItem{Value: "c", Priority: types.PriorityNow})

	items := q.DrainAll()
	if len(items) != 3 {
		t.Fatalf("DrainAll returned %d items, want 3", len(items))
	}
	if items[0].Value != "a" {
		t.Errorf("items[0].Value = %q, want %q", items[0].Value, "a")
	}
}

func TestAttachmentQueue_DrainByPriority_Order(t *testing.T) {
	var q attachmentQueue
	q.Enqueue(types.QueuedItem{Value: "later", Priority: types.PriorityLater})
	q.Enqueue(types.QueuedItem{Value: "next", Priority: types.PriorityNext})
	q.Enqueue(types.QueuedItem{Value: "now", Priority: types.PriorityNow})

	items := q.DrainByPriority(types.PriorityNext)
	if len(items) != 2 {
		t.Fatalf("DrainByPriority(Next) returned %d items, want 2", len(items))
	}
	values := map[string]bool{}
	for _, item := range items {
		values[item.Value] = true
	}
	if !values["now"] {
		t.Error("expected 'now' in drained items")
	}
	if !values["next"] {
		t.Error("expected 'next' in drained items")
	}
	if values["later"] {
		t.Error("'later' should not be drained by DrainByPriority(Next)")
	}

	remaining := q.DrainAll()
	if len(remaining) != 1 || remaining[0].Value != "later" {
		t.Errorf("DrainAll after DrainByPriority = %v, want [later]", remaining)
	}
}

func TestAttachmentQueue_DrainEmpty(t *testing.T) {
	var q attachmentQueue
	if items := q.DrainAll(); len(items) != 0 {
		t.Errorf("DrainAll on empty = %d items, want 0", len(items))
	}
	if items := q.DrainByPriority(types.PriorityNow); len(items) != 0 {
		t.Errorf("DrainByPriority on empty = %d items, want 0", len(items))
	}
}

// ---------------------------------------------------------------------------
// createAttachmentMessages tests
// ---------------------------------------------------------------------------

func TestCreateAttachmentMessages_JobMode_FlagMeta(t *testing.T) {
	eng := &Engine{}
	items := []types.QueuedItem{
		{
			Value:  "<job-notification>done</job-notification>",
			Mode:   types.ItemModeJob,
			IsMeta: true,
			Origin: &types.MessageOrigin{Kind: types.OriginJob},
		},
	}
	msgs := eng.createAttachmentMessages(items)
	if len(msgs) != 1 {
		t.Fatalf("createAttachmentMessages returned %d messages, want 1", len(msgs))
	}
	m := msgs[0]
	if m.Role != types.RoleUser {
		t.Errorf("Role = %q, want %q", m.Role, types.RoleUser)
	}
	if m.MessageType != types.MessageTypeAttachment {
		t.Errorf("MessageType = %q, want %q", m.MessageType, types.MessageTypeAttachment)
	}
	if m.Attachment == nil {
		t.Fatal("Attachment is nil")
	}
	if m.Attachment.Mode != types.ItemModeJob {
		t.Errorf("Attachment.Mode = %q, want %q", m.Attachment.Mode, types.ItemModeJob)
	}
	if !m.HasFlag(types.FlagMeta) {
		t.Error("FlagMeta not set on job attachment message")
	}
}

func TestCreateAttachmentMessages_PromptMode_NoFlagMeta(t *testing.T) {
	eng := &Engine{}
	items := []types.QueuedItem{
		{
			Value: "user prompt",
			Mode:  types.ItemModePrompt,
		},
	}
	msgs := eng.createAttachmentMessages(items)
	if len(msgs) != 1 {
		t.Fatalf("createAttachmentMessages returned %d messages, want 1", len(msgs))
	}
	if msgs[0].HasFlag(types.FlagMeta) {
		t.Error("FlagMeta should not be set for prompt mode attachment")
	}
}

// ---------------------------------------------------------------------------
// wrapOriginText tests
// ---------------------------------------------------------------------------

func TestWrapOriginText_JobOrigin(t *testing.T) {
	got := wrapOriginText("job output", &types.MessageOrigin{Kind: types.OriginJob})
	want := "A background agent completed a task:\njob output"
	if got != want {
		t.Errorf("wrapOriginText(job) = %q, want %q", got, want)
	}
}

func TestWrapOriginText_HumanOrigin(t *testing.T) {
	got := wrapOriginText("hello", &types.MessageOrigin{Kind: types.OriginHuman})
	if !strings.Contains(got, "hello") || !strings.Contains(got, "The user sent a new message") {
		t.Errorf("wrapOriginText(human) = %q, want user message prefix", got)
	}
}

func TestWrapOriginText_NilOrigin(t *testing.T) {
	got := wrapOriginText("raw text", nil)
	if !strings.Contains(got, "raw text") || !strings.Contains(got, "The user sent a new message") {
		t.Errorf("wrapOriginText(nil) = %q, want user message prefix (default)", got)
	}
}

func TestWrapOriginText_CoordinatorOrigin(t *testing.T) {
	got := wrapOriginText("coord msg", &types.MessageOrigin{Kind: types.OriginCoordinator})
	if !strings.Contains(got, "coord msg") || !strings.Contains(got, "The coordinator sent a message") {
		t.Errorf("wrapOriginText(coordinator) = %q, want coordinator prefix", got)
	}
}

func TestWrapOriginText_ChannelOrigin(t *testing.T) {
	got := wrapOriginText("chan msg", &types.MessageOrigin{Kind: types.OriginChannel})
	if !strings.Contains(got, "chan msg") || !strings.Contains(got, "external channel") {
		t.Errorf("wrapOriginText(channel) = %q, want channel prefix", got)
	}
}

// ---------------------------------------------------------------------------
// normalizeAttachmentForAPI tests
// ---------------------------------------------------------------------------

func TestNormalizeAttachmentForAPI_Job(t *testing.T) {
	msg := types.Message{
		Role:        types.RoleUser,
		MessageType: types.MessageTypeAttachment,
		Attachment: &types.Attachment{
			Type:   types.AttachmentTypeQueued,
			Prompt: "job result",
			Mode:   types.ItemModeJob,
			Origin: &types.MessageOrigin{Kind: types.OriginJob},
			IsMeta: true,
		},
	}
	result := normalizeAttachmentForAPI(msg)
	if result.Role != types.RoleUser {
		t.Errorf("Role = %q, want %q", result.Role, types.RoleUser)
	}
	if !result.HasFlag(types.FlagMeta) {
		t.Error("FlagMeta not set on normalized attachment")
	}
	if len(result.Content) != 1 {
		t.Fatalf("Content blocks = %d, want 1", len(result.Content))
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "<system-reminder>") {
		t.Errorf("Content not wrapped in <system-reminder>: %q", text)
	}
	if !strings.Contains(text, "A background agent completed a task:") {
		t.Errorf("Content missing job origin prefix: %q", text)
	}
}

func TestNormalizeAttachmentForAPI_InfersJobOrigin(t *testing.T) {
	msg := types.Message{
		Role:        types.RoleUser,
		MessageType: types.MessageTypeAttachment,
		Attachment: &types.Attachment{
			Type:   types.AttachmentTypeQueued,
			Prompt: "inferred job",
			Mode:   types.ItemModeJob,
		},
	}
	result := normalizeAttachmentForAPI(msg)
	if !result.HasFlag(types.FlagMeta) {
		t.Error("FlagMeta should be set when origin is inferred from job mode")
	}
}

// ---------------------------------------------------------------------------
// EnqueueAttachment priority defaults
// ---------------------------------------------------------------------------

func TestEnqueueAttachment_PriorityDefaults(t *testing.T) {
	eng := New(&Params{})

	eng.EnqueueAttachment(types.QueuedItem{Value: "prompt", Mode: types.ItemModePrompt, Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)})
	items := eng.attachments.DrainAll()
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Priority != types.PriorityNext {
		t.Errorf("prompt priority = %q, want %q", items[0].Priority, types.PriorityNext)
	}

	eng.EnqueueAttachment(types.QueuedItem{Value: "job", Mode: types.ItemModeJob, Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)})
	items = eng.attachments.DrainAll()
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Priority != types.PriorityNext {
		t.Errorf("job priority = %q, want %q", items[0].Priority, types.PriorityNext)
	}
}

// ---------------------------------------------------------------------------
// marshalMessages attachment merge
// ---------------------------------------------------------------------------

func TestMarshalMessages_AttachmentMerge(t *testing.T) {
	eng := New(&Params{})
	eng.appendMessage(types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("hello")},
	})
	eng.appendMessage(types.Message{
		Role:        types.RoleUser,
		MessageType: types.MessageTypeAttachment,
		Attachment: &types.Attachment{
			Type:   types.AttachmentTypeQueued,
			Prompt: "job done",
			Mode:   types.ItemModeJob,
			Origin: &types.MessageOrigin{Kind: types.OriginJob},
			IsMeta: true,
		},
	})

	marshaled := eng.marshalMessages()
	if len(marshaled) != 1 {
		t.Fatalf("marshalMessages returned %d messages, want 1 (merged)", len(marshaled))
	}
	if marshaled[0].Role != types.RoleUser {
		t.Errorf("Role = %q, want %q", marshaled[0].Role, types.RoleUser)
	}
	if len(marshaled[0].Content) != 2 {
		t.Fatalf("Content blocks = %d, want 2", len(marshaled[0].Content))
	}
}

// ---------------------------------------------------------------------------
// Full chain: job attachment drained at turn boundary
// ---------------------------------------------------------------------------

// TestJobAttachment_FullChain_TurnBoundaryDrain verifies the complete path:
// EnqueueAttachment(job) → turn boundary drain(DrainByPriority(PriorityNext))
// → createAttachmentMessages → appendMessages → message visible in conversation.
//
// Regression: job mode must default to PriorityNext so the attachment is drained
// at turn boundary. If it defaults to PriorityLater, DrainByPriority(PriorityNext)
// skips it and the LLM never sees the notification.
func TestJobAttachment_FullChain_TurnBoundaryDrain(t *testing.T) {
	eng := New(&Params{})

	// 1. Background task completes — external goroutine calls EnqueueAttachment
	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "background task output: exit code 0",
		Mode:      types.ItemModeJob,
		IsMeta:    true,
		Origin:    &types.MessageOrigin{Kind: types.OriginJob},
		Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	})

	// 2. Engine turn boundary drain — exactly what engine.go does after tool results
	drainedItems := eng.attachments.DrainByPriority(types.PriorityNext)
	if len(drainedItems) != 1 {
		t.Fatalf("turn boundary drain: got %d items, want 1 — job attachment must use PriorityNext to be drained at turn boundary", len(drainedItems))
	}
	if drainedItems[0].Mode != types.ItemModeJob {
		t.Errorf("drained item mode = %q, want %q", drainedItems[0].Mode, types.ItemModeJob)
	}

	// 3. Engine creates attachment messages from drained items
	attachmentMsgs := eng.createAttachmentMessages(drainedItems)
	if len(attachmentMsgs) != 1 {
		t.Fatalf("createAttachmentMessages: got %d messages, want 1", len(attachmentMsgs))
	}
	msg := attachmentMsgs[0]
	if msg.Role != types.RoleUser {
		t.Errorf("Role = %q, want %q", msg.Role, types.RoleUser)
	}
	if msg.MessageType != types.MessageTypeAttachment {
		t.Errorf("MessageType = %q, want %q", msg.MessageType, types.MessageTypeAttachment)
	}
	if !msg.HasFlag(types.FlagMeta) {
		t.Error("FlagMeta not set on job attachment message")
	}

	// 4. Engine appends attachment message to conversation
	eng.appendMessages(attachmentMsgs)

	// 5. Verify the attachment is visible in the engine's message list
	msgs := eng.Messages()
	found := false
	for _, m := range msgs {
		if m.MessageType == types.MessageTypeAttachment && m.Attachment != nil && m.Attachment.Mode == types.ItemModeJob {
			if strings.Contains(m.Attachment.Prompt, "background task output") {
				found = true
				break
			}
		}
	}
	if !found {
		var sb strings.Builder
		for _, m := range msgs {
			fmt.Fprintf(&sb, "  role=%s type=%s\n", m.Role, m.MessageType)
		}
		t.Fatalf("job attachment message not found in engine messages after turn boundary drain.\nMessages:\n%s", sb.String())
	}
}

func TestMarshalMessages_AttachmentStandalone(t *testing.T) {
	eng := New(&Params{})
	eng.appendMessage(types.Message{
		Role:        types.RoleUser,
		MessageType: types.MessageTypeAttachment,
		Attachment: &types.Attachment{
			Type:   types.AttachmentTypeQueued,
			Prompt: "solo",
			Mode:   types.ItemModeJob,
		},
	})

	marshaled := eng.marshalMessages()
	if len(marshaled) != 1 {
		t.Fatalf("marshalMessages returned %d messages, want 1", len(marshaled))
	}
}
