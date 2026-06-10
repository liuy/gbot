package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

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
	want := "A background agent completed a job:\njob output"
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
	if !strings.Contains(text, "A background agent completed a job:") {
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

		// 4. Wait for processAttachments to complete
		// Job-mode attachments no longer emit EventAttachment.
		// processAttachments does not emit EventQueryEnd, so wait for
		// EventTurnStart from runTurns as completion signal.
		deadline := time.After(5 * time.Second)
		for {
			allEvents := collector.Events()
			for _, e := range allEvents {
				if e.Type == types.EventAttachment {
					t.Fatal("job-mode attachment should NOT emit EventAttachment")
				}
			}
			gotTurn := false
			for _, e := range allEvents {
				if e.Type == types.EventTurnStart {
					gotTurn = true
				}
			}
			if gotTurn {
				break
			}
			select {
			case <-deadline:
				t.Fatalf("timed out waiting for EventTurnStart. Total events: %d", len(allEvents))
			default:
				runtime.Gosched()
			}
		}

		// 5. Verify the attachment message was added to sub-engine messages
		msgs := sub.Messages()
		found := false
		for _, m := range msgs {
			for _, b := range m.Content {
				if strings.Contains(b.Text, "bg-1") {
					found = true
				}
			}
		}
		if !found {
			t.Error("attachment content should appear in sub-engine messages")
		}
}

// TestSubEngine_AttachmentRunsTurns verifies that processAttachments on a
// sub-engine calls runTurns when it has attachments — sub-engines process
// their own background task notifications via the agentic loop.
func TestSubEngine_AttachmentRunsTurns(t *testing.T) {
	collector := newEventCollector()

	// trackingProvider records Stream() calls so we can verify runTurns was invoked.
	prov := &trackingProvider{}

	parent := New(&Params{
		Provider:   prov,
		Model:      "test",
		Dispatcher: collector,
	})

	sub := parent.NewSubEngine(SubEngineOptions{
		SystemPrompt:    `{"role":"system","content":"sub-agent"}`,
		ParentToolUseID: "call_agent_001",
		AgentType:       "General",
	})

	// Enqueue attachment on sub-engine
	xml := `<job-notification><job-id>bg-1</job-id><status>completed</status><summary>done</summary></job-notification>`
	sub.EnqueueAttachment(types.QueuedItem{
		Value:     xml,
		Mode:      types.ItemModeJob,
		IsMeta:    true,
		Origin:    &types.MessageOrigin{Kind: types.OriginJob},
		Timestamp: time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC),
	})

	// Wait for runTurns to complete by polling for EventQueryEnd
	timeout := time.After(3 * time.Second)
waitLoop:
	for {
		for _, e := range collector.Events() {
			if e.Type == types.EventQueryEnd {
				break waitLoop
			}
		}
		select {
		case <-timeout:
			break waitLoop
		default:
			runtime.Gosched()
		}
	}

		// Job-mode attachments no longer emit EventAttachment.
		// Verify no EventAttachment was emitted but LLM turn ran.
		for _, e := range collector.Events() {
			if e.Type == types.EventAttachment {
				t.Fatal("job-mode attachment should NOT emit EventAttachment")
			}
		}

	// KEY CHECK: sub-engine should have called the LLM via runTurns
	// to process its own attachment.
	if prov.streamCalls.Load() == 0 {
		t.Fatal("sub-engine processAttachments should call LLM (runTurns) for its attachment")
	}

	// Verify agentic events were emitted
	var foundTurnStart bool
	for _, e := range collector.Events() {
		if e.Type == types.EventTurnStart {
			foundTurnStart = true
		}
	}
	if !foundTurnStart {
		t.Error("sub-engine processAttachments should emit EventTurnStart")
	}
}

// trackingProvider is a test provider that counts Stream() calls and returns a
// minimal valid stream so runTurns completes normally instead of retrying forever.
type trackingProvider struct {
	streamCalls atomic.Int32
}

func (p *trackingProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return nil, nil
}

func (p *trackingProvider) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	p.streamCalls.Add(1)
	ch := make(chan llm.StreamEvent, 6)
	go func() {
		defer close(ch)
		ch <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "test"}}
		ch <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}}
		ch <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "ok"}}
		ch <- llm.StreamEvent{Type: "content_block_stop", Index: 0}
		ch <- llm.StreamEvent{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 1}}
		ch <- llm.StreamEvent{Type: "message_stop"}
	}()
	return ch, nil
}

// TestForkAgent_EventsForwarded verifies that a fork sub-engine dispatches
// events to the parent's dispatcher. This is needed so synchronous fork agents
// show their progress (tool calls, text output) in the parent TUI.
func TestForkAgent_EventsForwarded(t *testing.T) {
	collector := newEventCollector()
	prov := &trackingProvider{}

	parent := New(&Params{
		Provider:   prov,
		Model:      "test",
		Dispatcher: collector,
	})
	parent.SetSystemPrompt(`{"role":"system","content":"parent"}`)

	// Create a fork sub-engine.
	sub := parent.NewSubEngine(SubEngineOptions{
		SystemPrompt:    `{"role":"system","content":"fork-agent"}`,
		ParentToolUseID: "call_fork_001",
		AgentType:       "fork",
	})

	// Fork sub-engine SHOULD have a dispatcher so events reach the parent TUI.
	if sub.dispatcher == nil {
		t.Fatal("fork sub-engine should have a dispatcher for event forwarding")
	}

	// Run the fork sub-engine query — it generates text events internally.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := sub.QuerySync(ctx, "do work", `{"role":"system","content":"fork-agent"}`)
	if result.Error != nil {
		t.Fatalf("fork query failed: %v", result.Error)
	}

	// Verify events from the fork sub-engine reached the parent.
	events := collector.Events()
	if count := len(events); count == 0 {
		t.Fatal("expected events from fork sub-engine to reach parent, got 0")
	}

	// Verify events have AgentMeta with the correct ParentToolUseID.
	for _, e := range events {
		if e.Agent == nil || e.Agent.ParentToolUseID != "call_fork_001" {
			t.Errorf("event type=%s missing AgentMeta or wrong ParentToolUseID", e.Type)
		}
	}
}

// ---------------------------------------------------------------------------
// Timestamp injection integration tests
// ---------------------------------------------------------------------------

// TestTimestampInjection_NormalUserMessage verifies the full chain:
// engine message with Timestamp → marshalMessages → injectTimestamp
// prefixes [HH:MM:SS] to the user message's first text block.
func TestTimestampInjection_NormalUserMessage(t *testing.T) {
	ts := time.Date(2026, 6, 7, 14, 23, 5, 0, time.UTC)
	eng := New(&Params{})
	eng.appendMessage(types.Message{
		Role:       types.RoleUser,
		Content:    []types.ContentBlock{types.NewTextBlock("hello")},
		Timestamp:  ts,
	})
	eng.appendMessage(types.Message{
		Role:    types.RoleAssistant,
		Content: []types.ContentBlock{types.NewTextBlock("hi there")},
	})

	got := eng.marshalMessages()
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}

	// User message should have [14:23:05] prefix
	if got[0].Role != types.RoleUser {
		t.Errorf("msg[0] role = %q, want user", got[0].Role)
	}
	want := "[2026-06-07 14:23:05 UTC] hello"
	if got[0].Content[0].Text != want {
		t.Errorf("user text = %q, want %q", got[0].Content[0].Text, want)
	}

	// Assistant message should be unchanged
	if got[1].Content[0].Text != "hi there" {
		t.Errorf("assistant text = %q, want %q", got[1].Content[0].Text, "hi there")
	}
}

// TestTimestampInjection_FlagMetaSkipped verifies that FlagMeta messages
// (system-generated) do NOT get timestamp injection.
func TestTimestampInjection_FlagMetaSkipped(t *testing.T) {
	ts := time.Date(2026, 6, 7, 14, 23, 5, 0, time.UTC)
	eng := New(&Params{})
	eng.appendMessage(types.Message{
		Role:      types.RoleUser,
		Flags:     types.FlagMeta,
		Content:   []types.ContentBlock{types.NewTextBlock("system injection")},
		Timestamp: ts,
	})

	got := eng.marshalMessages()
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
	// FlagMeta message should NOT have timestamp prefix
	if strings.HasPrefix(got[0].Content[0].Text, "[") {
		t.Errorf("FlagMeta message should not have timestamp prefix, got %q", got[0].Content[0].Text)
	}
	if got[0].Content[0].Text != "system injection" {
		t.Errorf("FlagMeta text = %q, want %q", got[0].Content[0].Text, "system injection")
	}
}

// TestTimestampInjection_ZeroTimestampSkipped verifies that messages with
// zero Timestamp (e.g. legacy messages before timestamp feature) are not modified.
func TestTimestampInjection_ZeroTimestampSkipped(t *testing.T) {
	eng := New(&Params{})
	eng.appendMessage(types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("no timestamp")},
		// Timestamp is zero value
	})

	got := eng.marshalMessages()
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
	if got[0].Content[0].Text != "no timestamp" {
		t.Errorf("zero-timestamp text = %q, want %q", got[0].Content[0].Text, "no timestamp")
	}
}

// TestToolUseContext_ReceivesConversationHistory verifies that when the engine
// executes a tool during streaming, the ToolUseContext.Messages contains the
// full conversation history — even when the tool goroutine starts before the
// stream ends (and thus before the post-stream SetMessages call).
//
// Scenario: AddTool starts a goroutine that calls buildToolCtx before
// the LLM stream finishes. SetMessages only runs AFTER the stream ends.
// The tool goroutine sees empty messages → fork agents get no history.
//
// This test uses a gated provider: it sends tool_use events, then blocks
// the stream before message_stop. The tool goroutine runs while the stream
// is blocked, deterministically reproducing the empty-messages condition.
//
// Without the early SetMessages call at executor creation time, this test
// fails because the goroutine sees no conversation history.
func TestToolUseContext_ReceivesConversationHistory(t *testing.T) {
	var capturedMessages []types.Message
	var capturedMu sync.Mutex
	toolCalled := make(chan struct{})

	probe := &mockTool{
		name:    "HistoryProbe",
		enabled: true,
		callFn: func(_ context.Context, _ json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			capturedMu.Lock()
			capturedMessages = tctx.Messages
			capturedMu.Unlock()
			close(toolCalled)
			return &tool.ToolResult{Data: "ok"}, nil
		},
	}

	gate := make(chan struct{})
	prov := &gatedProvider{
		toolName: "HistoryProbe",
		gate:     gate,
	}

	eng := New(&Params{
		Provider: prov,
		Model:    "test",
		Tools:    []tool.Tool{probe},
	})

	eng.appendMessage(types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("圣斗士星矢")},
	})
	eng.appendMessage(types.Message{
		Role:    types.RoleAssistant,
		Content: []types.ContentBlock{types.NewTextBlock("好看的动画")},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Run Query in background — stream blocks at gate after sending tool_use
	queryDone := make(chan struct{})
	go func() {
		defer close(queryDone)
		eng.Query(ctx, "test", "")
	}()

	// Wait for the tool to be called (goroutine ran during blocked stream)
	select {
	case <-toolCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for tool to be called")
	}

	// Check captured messages BEFORE closing the gate (before SetMessages)
	capturedMu.Lock()
	msgs := capturedMessages
	capturedMu.Unlock()

	// Let the stream finish so Query can complete
	close(gate)
	<-queryDone

	if len(msgs) == 0 {
		t.Fatal("tool received empty Messages — SetMessages not called before goroutine start")
	}

	found := false
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == types.ContentTypeText && strings.Contains(b.Text, "圣斗士星矢") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("tool Messages does not contain history: got %d messages, none contain '圣斗士星矢'", len(msgs))
	}
}

// gatedProvider sends tool_use events immediately, then blocks on gate
// before sending message_stop. This simulates LLM network delay after
// tool_use but before stream end, reproducing the deterministic bug where
// the tool goroutine runs before SetMessages.
type gatedProvider struct {
	toolName string
	gate     chan struct{}
}

func (p *gatedProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return nil, nil
}

func (p *gatedProvider) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, 10)
	go func() {
		defer close(ch)
		ch <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "test"}}
		ch <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: "tu_1", Name: p.toolName}}
		ch <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{}`}}
		ch <- llm.StreamEvent{Type: "content_block_stop", Index: 0}
		// Block here — simulates LLM network delay. Tool goroutine runs
		// during this pause, before SetMessages is called.
		<-p.gate
		ch <- llm.StreamEvent{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 10}}
		ch <- llm.StreamEvent{Type: "message_stop"}
	}()
	return ch, nil
}

