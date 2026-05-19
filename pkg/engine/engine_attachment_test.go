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

	// 1. background job completes — external goroutine calls EnqueueAttachment
	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "background job output: exit code 0",
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
			if strings.Contains(m.Attachment.Prompt, "background job output") {
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

// ---------------------------------------------------------------------------
// Multi-prompt attachment merge chain test
// ---------------------------------------------------------------------------

// TestPromptAttachment_MultiMergeChain verifies that multiple queued user
// prompts are: enqueued → drained → created as individual attachment messages
// → merged into a single user message by marshalMessages for the API call.
//
// This is the engine-side counterpart to the TUI call chain test: the TUI
// shows each queued message independently, but the API sees them as one user
// turn with multiple content blocks.
func TestPromptAttachment_MultiMergeChain(t *testing.T) {
	eng := New(&Params{})

	// 1. Simulate two user prompts queued during streaming
	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "first prompt",
		Mode:      types.ItemModePrompt,
		UUID:      "uuid-1",
		Origin:    &types.MessageOrigin{Kind: types.OriginHuman},
		Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "second prompt",
		Mode:      types.ItemModePrompt,
		UUID:      "uuid-2",
		Origin:    &types.MessageOrigin{Kind: types.OriginHuman},
		Timestamp: time.Date(2024, 1, 1, 0, 0, 1, 0, time.UTC),
	})

	// 2. Turn boundary drain — exactly what engine does after LLM response
	drained := eng.attachments.DrainByPriority(types.PriorityNext)
	if len(drained) != 2 {
		t.Fatalf("drain: got %d items, want 2", len(drained))
	}

	// 3. Create attachment messages — one per queued item
	attMsgs := eng.createAttachmentMessages(drained)
	if len(attMsgs) != 2 {
		t.Fatalf("createAttachmentMessages: got %d, want 2", len(attMsgs))
	}
	for i, m := range attMsgs {
		if m.Role != types.RoleUser {
			t.Errorf("attMsgs[%d].Role = %q, want user", i, m.Role)
		}
		if m.MessageType != types.MessageTypeAttachment {
			t.Errorf("attMsgs[%d].MessageType = %q, want attachment", i, m.MessageType)
		}
		if m.HasFlag(types.FlagMeta) {
			t.Errorf("attMsgs[%d] should not have FlagMeta for prompt mode", i)
		}
		if m.Attachment == nil {
			t.Fatalf("attMsgs[%d].Attachment is nil", i)
		}
		if m.Attachment.Mode != types.ItemModePrompt {
			t.Errorf("attMsgs[%d].Attachment.Mode = %q, want prompt", i, m.Attachment.Mode)
		}
		if m.Attachment.SourceUUID != drained[i].UUID {
			t.Errorf("attMsgs[%d].SourceUUID = %q, want %q", i, m.Attachment.SourceUUID, drained[i].UUID)
		}
	}

	// 4. Append to engine messages (includes prior user message for context)
	eng.appendMessage(types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("original question")},
	})
	eng.appendMessages(attMsgs)

	// Verify all 3 messages in engine (1 original + 2 attachments)
	engineMsgs := eng.Messages()
	if len(engineMsgs) != 3 {
		t.Fatalf("engine messages: got %d, want 3 (1 original + 2 attachments)", len(engineMsgs))
	}

	// 5. marshalMessages should merge the 2 attachments into the last user message
	// Result: 1 user message with 3 content blocks (original + first + second)
	marshaled := eng.marshalMessages()
	if len(marshaled) != 1 {
		t.Fatalf("marshalMessages: got %d messages, want 1 (all merged into original user message)", len(marshaled))
	}
	merged := marshaled[0]
	if merged.Role != types.RoleUser {
		t.Errorf("merged role = %q, want user", merged.Role)
	}
	if len(merged.Content) != 3 {
		t.Fatalf("merged content blocks = %d, want 3 (original + 2 attachments)", len(merged.Content))
	}
	// First block is the original question
	if merged.Content[0].Text != "original question" {
		t.Errorf("content[0] = %q, want 'original question'", merged.Content[0].Text)
	}
	// Subsequent blocks are the queued prompts (wrapped by normalizeAttachmentForAPI)
	if !strings.Contains(merged.Content[1].Text, "first prompt") {
		t.Errorf("content[1] missing 'first prompt': %q", merged.Content[1].Text)
	}
	if !strings.Contains(merged.Content[2].Text, "second prompt") {
		t.Errorf("content[2] missing 'second prompt': %q", merged.Content[2].Text)
	}
}

// ---------------------------------------------------------------------------
// End-to-end: sub-engine attachment → taggedDispatcher → AgentMeta
// ---------------------------------------------------------------------------

// TestSubEngine_AttachmentEmit_WithAgentMeta verifies the full chain:
// sub-engine EnqueueAttachment → processAttachments → taggedDispatcher.Dispatch
// → emitted EventAttachment carries AgentMeta from the sub-engine.
//
// This is the engine-side E2E test. The TUI-side counterpart validates
// that the EventAttachment with AgentMeta is correctly converted to
// attachmentMsg{Agent: ...} by the TUI handler.
func TestSubEngine_AttachmentEmit_WithAgentMeta(t *testing.T) {
	// 1. Parent engine with event collector
	collector := newEventCollector()
	parent := New(&Params{
		Provider:    &testProvider{},
		Model:       "test",
		Dispatcher:  collector,
	})

	// 2. Create sub-engine with SystemPrompt and ParentToolUseID (triggers taggedDispatcher)
	sub := parent.NewSubEngine(SubEngineOptions{
		SystemPrompt:    `{"role":"system","content":"sub-agent"}`,
		ParentToolUseID: "call_agent_123",
		AgentType:       "General",
	})

	// Verify sub-engine has a dispatcher (taggedDispatcher)
	if sub.dispatcher == nil {
		t.Fatal("sub-engine should have a taggedDispatcher wrapping parent's dispatcher")
	}

	// 3. Enqueue attachment on sub-engine (simulates background job completion)
	xml := `<job-notification><job-id>bg-1</job-id><status>completed</status><summary>test done</summary></job-notification>`
	sub.EnqueueAttachment(types.QueuedItem{
		Value:     xml,
		Mode:      types.ItemModeJob,
		IsMeta:    true,
		Origin:    &types.MessageOrigin{Kind: types.OriginJob},
		Timestamp: time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC),
	})

	// 4. Wait for processAttachments to emit events
	// processAttachments runs async in a goroutine, and it also calls runTurns
	// which would need a provider response. The test provider returns empty by default,
	// so runTurns should terminate quickly.
	var attachEvents []types.QueryEvent
	deadline := time.After(5 * time.Second)
	for len(attachEvents) == 0 {
		select {
		case <-deadline:
			allEvents := collector.Events()
			t.Fatalf("timed out waiting for EventAttachment from sub-engine. Total events: %d", len(allEvents))
		default:
		}
		for _, e := range collector.Events() {
			if e.Type == types.EventAttachment {
				attachEvents = append(attachEvents, e)
			}
		}
	}

	// 5. Verify the EventAttachment carries AgentMeta
	evt := attachEvents[0]
	if evt.Agent == nil {
		t.Fatal("EventAttachment from sub-engine should carry AgentMeta (injected by taggedDispatcher)")
	}
	if evt.Agent.ParentToolUseID != "call_agent_123" {
		t.Errorf("Agent.ParentToolUseID = %q, want %q", evt.Agent.ParentToolUseID, "call_agent_123")
	}
	if evt.Agent.AgentType != "General" {
		t.Errorf("Agent.AgentType = %q, want %q", evt.Agent.AgentType, "General")
	}

	// 6. Verify the attachment message content
	if evt.Message == nil || evt.Message.Attachment == nil {
		t.Fatal("EventAttachment should have Message.Attachment")
	}
	if !strings.Contains(evt.Message.Attachment.Prompt, "bg-1") {
		t.Errorf("Attachment.Prompt = %q, should contain job-id bg-1", evt.Message.Attachment.Prompt)
	}
}

func TestPriorityOrder_UnknownPriority(t *testing.T) {
	// Unknown priority should sort same as PriorityNext (value 1)
	q := &attachmentQueue{}
	q.Enqueue(types.QueuedItem{Value: "unknown", Priority: types.QueuePriority("unknown")})
	q.Enqueue(types.QueuedItem{Value: "next", Priority: types.PriorityNext})
	q.Enqueue(types.QueuedItem{Value: "later", Priority: types.PriorityLater})

	items := q.DrainByPriority(types.PriorityNext)
	if len(items) != 2 {
		t.Fatalf("expected 2 items (unknown + next), got %d", len(items))
	}
	// Both unknown and next should have same priority order
	if items[0].Value != "next" && items[1].Value != "unknown" && items[0].Value != "unknown" && items[1].Value != "next" {
		t.Errorf("unexpected order: %q, %q", items[0].Value, items[1].Value)
	}

	// Later should remain
	items2 := q.DrainAll()
	if len(items2) != 1 || items2[0].Value != "later" {
		t.Errorf("expected later to remain, got %v", items2)
	}
}
