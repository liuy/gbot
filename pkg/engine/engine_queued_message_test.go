package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/utils"
)

// Queued message tests: a user message sent while the engine is mid-turn
// waits in the attachment queue until the turn boundary — the assistant has
// completed and its tool results are in history — then attaches at the
// sequence tail through appendQueuedUserMessage, which persists the final
// reminder-wrapped wire form so the prompt prefix is byte-stable across
// restarts. Attaching any earlier (before an in-flight tool_use assistant)
// twisted the time order and invalidated the live decode's KV cache.

// queuedEnvelope returns the exact expected content block for a
// user-queued message with raw text, computed through the SAME legacy path
// (normalizeAttachmentForAPI) so the tests pin byte-identity between the
// persisted form and the old assembled form.
func queuedEnvelope(t *testing.T, raw string) []types.ContentBlock {
	t.Helper()
	legacy := types.Message{
		Role:        types.RoleUser,
		MessageType: types.MessageTypeAttachment,
		Content:     []types.ContentBlock{types.NewTextBlock(raw)},
		Attachment: &types.Attachment{
			Type:       types.AttachmentTypeQueued,
			Prompt:     raw,
			SourceUUID: "itst-uuid",
			Mode:       types.ItemModePrompt,
			Origin:     &types.MessageOrigin{Kind: types.OriginHuman},
		},
	}
	return normalizeAttachmentForAPI(legacy).Content
}

// blockingTool signals startCh when called and blocks until releaseCh closes.
type blockingTool struct {
	name      string
	startCh   chan struct{}
	releaseCh chan struct{}
}

func (t *blockingTool) Name() string                                { return t.name }
func (t *blockingTool) Aliases() []string                           { return nil }
func (t *blockingTool) Description(json.RawMessage) (string, error) { return "blocks", nil }
func (t *blockingTool) InputSchema() json.RawMessage                { return nil }
func (t *blockingTool) Call(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
	close(t.startCh)
	<-t.releaseCh
	return &tool.ToolResult{Data: "ok"}, nil
}
func (t *blockingTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *blockingTool) IsReadOnly(json.RawMessage) bool        { return true }
func (t *blockingTool) IsDestructive(json.RawMessage) bool     { return false }
func (t *blockingTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (t *blockingTool) IsEnabled() bool                        { return true }
func (t *blockingTool) InterruptBehavior() tool.InterruptBehavior {
	return tool.InterruptCancel
}
func (t *blockingTool) MaxResultSize() int { return 50000 }
func (t *blockingTool) Prompt() string     { return "" }
func (t *blockingTool) RenderResult(data any) string {
	if s, ok := data.(string); ok {
		return s
	}
	return ""
}

// findEnvelopeMessages returns the indices of user messages whose first text
// block starts with the queued message reminder envelope.
func findEnvelopeMessages(msgs []types.Message) []int {
	var out []int
	for i, m := range msgs {
		if m.Role != types.RoleUser || len(m.Content) == 0 {
			continue
		}
		if m.Content[0].Type == types.ContentTypeText &&
			strings.HasPrefix(m.Content[0].Text, "<system-reminder>\nThe user sent a new message while you were working:") {
			out = append(out, i)
		}
	}
	return out
}

// TestQueuedMessage_MidToolExec_TailAttachAtTurnBoundary is the core timing
// contract: EnqueueAttachment with a user-origin prompt while a query is
// active only queues the item — no history append, no persist, no
// EventAttachment while the tool runs. When the assistant completed and its
// tool results are in history, the boundary attaches the message at the tail
// (order: assistant(tool_use), tool_result, queued message), emits the echo,
// and the follow-up LLM request carries the envelope.
func TestQueuedMessage_MidToolExec_TailAttachAtTurnBoundary(t *testing.T) {
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "t1", "blocker", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "final answer"), nil)

	bt := &blockingTool{name: "blocker", startCh: make(chan struct{}), releaseCh: make(chan struct{})}
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{bt.Name(): bt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })
	released := false
	release := func() {
		if !released {
			released = true
			close(bt.releaseCh)
		}
	}
	t.Cleanup(release)

	eng.Query(context.Background(), "run the tool", "")
	select {
	case <-bt.startCh:
	case <-time.After(5 * time.Second):
		t.Fatal("tool never started")
	}

	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "wait, change of plans",
		Mode:      types.ItemModePrompt,
		UUID:      "itst-uuid",
		Priority:  types.PriorityNext,
		Origin:    &types.MessageOrigin{Kind: types.OriginHuman},
		Timestamp: time.Date(2026, 10, 5, 11, 0, 0, 0, time.UTC),
	})

	// While the tool runs the message must be NOWHERE but the queue.
	if envelopes := findEnvelopeMessages(eng.Messages()); len(envelopes) != 0 {
		t.Fatalf("history contains %d queued message messages while the tool runs, want 0 (attach must wait for the turn boundary)", len(envelopes))
	}
	if n := eng.AttachmentsLen(); n != 1 {
		t.Fatalf("attachment queue len = %d, want 1 (message waits in the queue)", n)
	}
	if evs := ec.FindEvents(types.EventAttachment); len(evs) != 0 {
		t.Fatalf("%d EventAttachment events while the tool runs, want 0", len(evs))
	}

	release()
	// The attach fires at the boundary, before the follow-up LLM call.
	if !ec.WaitForEventCount(types.EventAttachment, 1, 5*time.Second) {
		t.Fatal("no EventAttachment after the tool completed")
	}
	if res := ec.WaitForResult(); res.Error != nil {
		t.Fatalf("query error: %v", res.Error)
	}

	msgs := eng.Messages()
	envelopes := findEnvelopeMessages(msgs)
	if len(envelopes) != 1 {
		t.Fatalf("history contains %d queued message messages after the turn, want 1", len(envelopes))
	}
	assistantIdx, toolResultIdx := -1, -1
	for i, m := range msgs {
		switch m.Role {
		case types.RoleAssistant:
			for _, b := range m.Content {
				if b.Type == types.ContentTypeToolUse && b.ID == "t1" {
					assistantIdx = i
				}
			}
		case types.RoleUser:
			for _, b := range m.Content {
				if b.Type == types.ContentTypeToolResult && b.ToolUseID == "t1" {
					toolResultIdx = i
				}
			}
		}
	}
	if assistantIdx == -1 || toolResultIdx == -1 {
		t.Fatalf("assistant(tool_use)@%d tool_result@%d not found in history", assistantIdx, toolResultIdx)
	}
	envIdx := envelopes[0]
	if assistantIdx >= toolResultIdx || toolResultIdx >= envIdx {
		t.Errorf("sequence order broken: assistant(tool_use)@%d, tool_result@%d, queued message@%d; want assistant < tool_result < queued message", assistantIdx, toolResultIdx, envIdx)
	}
	got := msgs[envIdx]
	want := queuedEnvelope(t, "wait, change of plans")
	if got.Content[0].Text != want[0].Text {
		t.Errorf("attached content = %q, want byte-exact reminder envelope %q", got.Content[0].Text, want[0].Text)
	}
	if got.ID != "itst-uuid" {
		t.Errorf("message ID = %q, want item UUID %q", got.ID, "itst-uuid")
	}
	// The follow-up request must carry the envelope verbatim (it is an
	// ordinary history message — assembly sends it as-is).
	foundEnvelope := false
	for _, m := range mp.lastRequestMessages() {
		if m.Role != types.RoleUser {
			continue
		}
		for _, b := range m.Content {
			if b.Type == types.ContentTypeText && strings.Contains(b.Text, want[0].Text) {
				foundEnvelope = true
			}
		}
	}
	if !foundEnvelope {
		t.Error("follow-up LLM request does not contain the queued message envelope")
	}
}

// TestQueuedMessage_MidToolExec_MultiQueuedFIFO pins drain order: several
// messages queued mid-tool attach at the boundary in the order they were
// sent, all after the tool result.
func TestQueuedMessage_MidToolExec_MultiQueuedFIFO(t *testing.T) {
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "t1", "blocker", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "both noted"), nil)

	bt := &blockingTool{name: "blocker", startCh: make(chan struct{}), releaseCh: make(chan struct{})}
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{bt.Name(): bt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })
	released := false
	release := func() {
		if !released {
			released = true
			close(bt.releaseCh)
		}
	}
	t.Cleanup(release)

	eng.Query(context.Background(), "run the tool", "")
	select {
	case <-bt.startCh:
	case <-time.After(5 * time.Second):
		t.Fatal("tool never started")
	}
	for i, text := range []string{"first change", "second change"} {
		eng.EnqueueAttachment(types.QueuedItem{
			Value:     text,
			Mode:      types.ItemModePrompt,
			UUID:      fmt.Sprintf("fifo-%d", i),
			Priority:  types.PriorityNext,
			Origin:    &types.MessageOrigin{Kind: types.OriginHuman},
			Timestamp: time.Date(2026, 10, 5, 11, 0, i, 0, time.UTC),
		})
	}
	if n := eng.AttachmentsLen(); n != 2 {
		t.Fatalf("attachment queue len = %d, want 2", n)
	}

	release()
	if !ec.WaitForEventCount(types.EventAttachment, 2, 5*time.Second) {
		t.Fatal("did not observe 2 EventAttachment events at the boundary")
	}
	if res := ec.WaitForResult(); res.Error != nil {
		t.Fatalf("query error: %v", res.Error)
	}

	msgs := eng.Messages()
	envelopes := findEnvelopeMessages(msgs)
	if len(envelopes) != 2 {
		t.Fatalf("history contains %d queued message messages, want 2", len(envelopes))
	}
	first := msgs[envelopes[0]].Content[0].Text
	second := msgs[envelopes[1]].Content[0].Text
	if !strings.Contains(first, "first change") || !strings.Contains(second, "second change") {
		t.Errorf("FIFO order broken:\nfirst attached:  %q\nsecond attached: %q", first, second)
	}
	// Both after the tool result: the message directly before the first
	// attached queued message must carry the t1 tool_result.
	prev := msgs[envelopes[0]-1]
	hasResult := false
	for _, b := range prev.Content {
		if b.Type == types.ContentTypeToolResult && b.ToolUseID == "t1" {
			hasResult = true
		}
	}
	if !hasResult {
		t.Errorf("message directly before the first attached queued message (role=%s) does not carry the t1 tool_result", prev.Role)
	}
	// The follow-up request carries both, in order.
	reqTexts := ""
	for _, m := range mp.lastRequestMessages() {
		for _, b := range m.Content {
			if b.Type == types.ContentTypeText {
				reqTexts += b.Text + "\n"
			}
		}
	}
	firstPos := strings.Index(reqTexts, "first change")
	secondPos := strings.Index(reqTexts, "second change")
	if firstPos == -1 || secondPos == -1 {
		t.Errorf("follow-up request missing queued messages (firstPos=%d secondPos=%d)", firstPos, secondPos)
	} else if firstPos > secondPos {
		t.Errorf("follow-up request order broken: first change at %d after second change at %d", firstPos, secondPos)
	}
}

// TestQueuedMessage_MidToolExec_PersistedAtBoundaryAndRestartStable drives a
// full query with a mid-tool queued message against a real store. The message
// must NOT be in the store while the tool runs; the boundary attach persists
// it immediately (before the follow-up LLM call completes), and reloading the
// session must reproduce the live sequence byte-for-byte — the restart
// cache-stability contract.
func TestQueuedMessage_MidToolExec_PersistedAtBoundaryAndRestartStable(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "t1", "blocker", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "acknowledged the queued message"), nil)

	bt := &blockingTool{name: "blocker", startCh: make(chan struct{}), releaseCh: make(chan struct{})}
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{bt.Name(): bt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")
	eng.SetSessionID(session.SessionID)
	released := false
	release := func() {
		if !released {
			released = true
			close(bt.releaseCh)
		}
	}
	t.Cleanup(release)

	eng.Query(context.Background(), "run the tool", "")
	select {
	case <-bt.startCh:
	case <-time.After(5 * time.Second):
		t.Fatal("tool never started")
	}

	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "restart-stability check",
		Mode:      types.ItemModePrompt,
		UUID:      "itst-uuid",
		Priority:  types.PriorityNext,
		Origin:    &types.MessageOrigin{Kind: types.OriginHuman},
		Timestamp: time.Date(2026, 10, 5, 11, 0, 0, 0, time.UTC),
	})

	// While the tool runs the message is only queued: nothing in history,
	// nothing persisted.
	if envelopes := findEnvelopeMessages(eng.Messages()); len(envelopes) != 0 {
		t.Fatalf("history contains %d queued message messages while the tool runs, want 0", len(envelopes))
	}
	if n := eng.AttachmentsLen(); n != 1 {
		t.Fatalf("attachment queue len = %d, want 1", n)
	}
	storeMsgs, err := store.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	midRun, err := short.StoreMessagesToEngine(storeMsgs)
	if err != nil {
		t.Fatalf("StoreMessagesToEngine: %v", err)
	}
	if idx := findEnvelopeMessages(midRun); len(idx) != 0 {
		t.Fatalf("store contains %d envelope messages while the tool runs, want 0 (nothing may persist before the turn boundary)", len(idx))
	}

	release()
	if !ec.WaitForEventCount(types.EventAttachment, 1, 5*time.Second) {
		t.Fatal("no EventAttachment after the tool completed")
	}
	// Attach-time persist: the store holds the envelope while the query is
	// still running (the follow-up LLM call has not returned yet).
	deadline := time.Now().Add(5 * time.Second) // REAL-TIME
	var persisted []types.Message
	for {
		storeMsgs, err := store.LoadMessages(session.SessionID)
		if err != nil {
			t.Fatalf("LoadMessages: %v", err)
		}
		persisted, err = short.StoreMessagesToEngine(storeMsgs)
		if err != nil {
			t.Fatalf("StoreMessagesToEngine: %v", err)
		}
		if idx := findEnvelopeMessages(persisted); len(idx) == 1 {
			break
		}
		if time.Now().After(deadline) { // REAL-TIME: poll loop bounded by deadline
			t.Fatalf("store contains %d envelope messages after the boundary attach, want 1 (attach must persist immediately)", len(findEnvelopeMessages(persisted)))
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}
	want := queuedEnvelope(t, "restart-stability check")
	if persisted[findEnvelopeMessages(persisted)[0]].Content[0].Text != want[0].Text {
		t.Errorf("persisted envelope = %q, want %q",
			persisted[findEnvelopeMessages(persisted)[0]].Content[0].Text, want[0].Text)
	}

	if res := ec.WaitForResult(); res.Error != nil {
		t.Fatalf("query error: %v", res.Error)
	}

	// The query-exit PersistNewMessages defer runs after QueryEnd; poll until
	// the store settles at the live history size before comparing.
	settleDeadline := time.Now().Add(5 * time.Second) // REAL-TIME
	live := eng.Messages()
	var reloaded []types.Message
	for {
		storeMsgs, err := store.LoadMessages(session.SessionID)
		if err != nil {
			t.Fatalf("LoadMessages after query: %v", err)
		}
		reloaded, err = short.StoreMessagesToEngine(storeMsgs)
		if err != nil {
			t.Fatalf("StoreMessagesToEngine after query: %v", err)
		}
		if len(reloaded) == len(live) {
			break
		}
		if time.Now().After(settleDeadline) { // REAL-TIME: poll loop bounded by deadline
			t.Fatalf("reloaded message count = %d, want %d (live history)", len(reloaded), len(live))
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}
	for i := range live {
		if reloaded[i].Role != live[i].Role {
			t.Errorf("msg[%d] role = %q, want %q", i, reloaded[i].Role, live[i].Role)
		}
		if len(reloaded[i].Content) != len(live[i].Content) {
			t.Fatalf("msg[%d] block count = %d, want %d", i, len(reloaded[i].Content), len(live[i].Content))
		}
		for b := range live[i].Content {
			if reloaded[i].Content[b].Type != live[i].Content[b].Type ||
				reloaded[i].Content[b].Text != live[i].Content[b].Text {
				t.Errorf("msg[%d] block[%d] diverges across restart: %q vs %q",
					i, b, reloaded[i].Content[b].Text, live[i].Content[b].Text)
			}
		}
	}

	// Pairing repair must be a no-op on the reloaded sequence: a lone
	// queued message between assistant(tool_use) and its result would otherwise
	// drop the real tool_result for a synthetic placeholder.
	eng2 := New(&Params{Provider: mp, Model: "test-model", Logger: slog.Default()})
	t.Cleanup(func() { eng2.Close() })
	eng2.SetMessages(reloaded)
	marshaled := eng2.marshalMessages()
	repaired := EnsureToolResultPairing(marshaled)
	if len(repaired) != len(marshaled) {
		t.Fatalf("EnsureToolResultPairing changed reloaded sequence: %d -> %d messages", len(marshaled), len(repaired))
	}
	for i := range marshaled {
		if len(marshaled[i].Content) != len(repaired[i].Content) {
			t.Errorf("EnsureToolResultPairing mutated msg[%d] blocks: %d -> %d", i, len(marshaled[i].Content), len(repaired[i].Content))
		}
	}

	// Prefix stability at the WIRE level: the assembled request (including
	// injectTimestamp output derived from the persisted Timestamp) must be
	// byte-identical before and after the restart, so the provider's
	// exact-prefix cache reuses across the restart.
	liveWire := eng.marshalMessages()
	for i := range liveWire {
		if len(liveWire[i].Content) != len(marshaled[i].Content) {
			t.Fatalf("wire msg[%d] block count: live %d, reloaded %d", i, len(liveWire[i].Content), len(marshaled[i].Content))
		}
		for b := range liveWire[i].Content {
			if liveWire[i].Content[b].Text != marshaled[i].Content[b].Text {
				t.Errorf("wire msg[%d] block[%d] diverges across restart:\nlive:     %q\nreloaded: %q",
					i, b, liveWire[i].Content[b].Text, marshaled[i].Content[b].Text)
			}
		}
	}
}

// streamGateProvider passes through to a wrapped provider but holds the
// FIRST Stream call open until the gate closes, so a test can inject a queued message mid-stream
// deterministically while the final response is mid-stream.
type streamGateProvider struct {
	inner llm.Provider
	gate  chan struct{}
	mu    sync.Mutex
	calls int
}

func (p *streamGateProvider) Name() string { return "streamgate" }
func (p *streamGateProvider) Complete(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	return p.inner.Complete(ctx, req)
}
func (p *streamGateProvider) Stream(ctx context.Context, req *llm.Request) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	if n != 1 {
		return p.inner.Stream(ctx, req)
	}
	ch := make(chan llm.StreamEvent, 1)
	go func() {
		defer close(ch)
		select {
		case <-p.gate:
		case <-ctx.Done():
			return
		}
		innerCh, err := p.inner.Stream(ctx, req)
		if err != nil {
			return
		}
		for ev := range innerCh {
			ch <- ev
		}
	}()
	return ch, nil
}

// TestQueuedMessage_DuringStreaming_AnsweredInFollowUpTurn covers the
// queued message arriving while the FINAL response is streaming: it stays in
// the queue mid-stream (the in-flight answer is not interrupted), attaches at
// the terminal boundary, and the seq watermark forces one more turn so the
// model addresses it.
func TestQueuedMessage_DuringStreaming_AnsweredInFollowUpTurn(t *testing.T) {
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "final answer"), nil)
	mp.addResponse(textStreamEvents("test-model", "acknowledged the queued message"), nil)

	gated := &streamGateProvider{inner: mp, gate: make(chan struct{})}
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   gated,
		Dispatcher: ec,
		Model:      "test-model",
		Logger:     slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	eng.Query(context.Background(), "hello", "")
	// Wait until the first stream is in flight (held open by the gate).
	deadline := time.Now().Add(5 * time.Second) // REAL-TIME
	for {
		gated.mu.Lock()
		calls := gated.calls
		gated.mu.Unlock()
		if calls == 1 {
			break
		}
		if time.Now().After(deadline) { // REAL-TIME: poll loop bounded by deadline
			t.Fatal("first LLM stream never started")
		}
		time.Sleep(5 * time.Millisecond) // REAL-TIME
	}
	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "actually one more thing",
		Mode:      types.ItemModePrompt,
		UUID:      "itst-uuid",
		Priority:  types.PriorityNext,
		Origin:    &types.MessageOrigin{Kind: types.OriginHuman},
		Timestamp: time.Date(2026, 10, 5, 11, 0, 0, 0, time.UTC),
	})
	// Mid-stream the message is only queued — attaching would interrupt the
	// live decode and invalidate its KV prefix.
	if msgs := eng.Messages(); len(findEnvelopeMessages(msgs)) != 0 {
		t.Fatalf("queued message appended mid-stream: %d envelope messages, want 0", len(findEnvelopeMessages(msgs)))
	}
	if n := eng.AttachmentsLen(); n != 1 {
		t.Fatalf("attachment queue len = %d, want 1 mid-stream", n)
	}
	close(gated.gate)

	if res := ec.WaitForResult(); res.Error != nil {
		t.Fatalf("query error: %v", res.Error)
	}

	if got := mp.callCount(); got != 2 {
		t.Errorf("LLM call count = %d, want 2 (terminal path must re-run for the unseen queued message)", got)
	}
	msgs := eng.Messages()
	envelopes := findEnvelopeMessages(msgs)
	if len(envelopes) != 1 {
		t.Fatalf("history contains %d queued message messages, want 1", len(envelopes))
	}
	// An assistant message must exist AFTER the queued message (the follow-up turn).
	hasAssistantAfter := false
	for i := envelopes[0] + 1; i < len(msgs); i++ {
		if msgs[i].Role == types.RoleAssistant {
			hasAssistantAfter = true
		}
	}
	if !hasAssistantAfter {
		t.Error("no assistant message after the queued message — follow-up turn did not run")
	}
}

// TestQueuedMessage_IdleEnqueueKeepsQueuePath pins that idle-time prompt items
// (whatever the origin) still take the legacy queue path — only mid-turn
// user queued messages persist immediately.
func TestQueuedMessage_IdleEnqueueKeepsQueuePath(t *testing.T) {
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })

	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "idle prompt",
		Mode:      types.ItemModePrompt,
		UUID:      "u-1",
		Origin:    &types.MessageOrigin{Kind: types.OriginHuman},
		Timestamp: time.Date(2026, 10, 5, 11, 0, 0, 0, time.UTC),
	})
	if got := eng.AttachmentsLen(); got != 1 {
		t.Errorf("idle enqueue: queue len = %d, want 1 (queue path)", got)
	}
	if msgs := eng.Messages(); len(msgs) != 0 {
		t.Errorf("idle enqueue: history = %d messages, want 0", len(msgs))
	}
}

// TestQueuedMessage_OtherOriginsStillQueuedMidTurn pins the scope: only the
// wrapOriginText default branch (user queued message) changes. Job, coordinator
// and channel origins keep the ephemeral attachment queue mid-turn.
func TestQueuedMessage_OtherOriginsStillQueuedMidTurn(t *testing.T) {
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "t1", "blocker", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "final"), nil)

	bt := &blockingTool{name: "blocker", startCh: make(chan struct{}), releaseCh: make(chan struct{})}
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{bt.Name(): bt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })

	eng.Query(context.Background(), "run the tool", "")
	select {
	case <-bt.startCh:
	case <-time.After(5 * time.Second):
		t.Fatal("tool never started")
	}

	origins := []*types.MessageOrigin{
		{Kind: types.OriginJob},
		{Kind: types.OriginCoordinator},
		{Kind: types.OriginChannel},
	}
	for i, o := range origins {
		eng.EnqueueAttachment(types.QueuedItem{
			Value:     "not a user queued message",
			Mode:      types.ItemModeJob,
			UUID:      "job-" + string(rune('a'+i)),
			Priority:  types.PriorityNext,
			Origin:    o,
			Timestamp: time.Date(2026, 10, 5, 11, 0, i, 0, time.UTC),
		})
	}
	if got := eng.AttachmentsLen(); got != len(origins) {
		t.Errorf("queue len = %d, want %d (non-human origins keep queueing)", got, len(origins))
	}
	if len(findEnvelopeMessages(eng.Messages())) != 0 {
		t.Error("non-human origins must not append history messages")
	}

	close(bt.releaseCh)
	ec.WaitForResult()
}

// TestQueuedMessage_PinsByteEqualityWithLegacyAssembledForm pins cross-version
// prefix stability: the persisted queued message content must equal, byte for
// byte, what the legacy assembly (normalizeAttachmentForAPI) produced for the
// same queued item. If either side drifts, every pre-existing cache prefix
// built by the other version diverges at the queued message.
func TestQueuedMessage_PinsByteEqualityWithLegacyAssembledForm(t *testing.T) {
	img := types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/png", Data: "iVBORw0KGgo="})
	cases := []struct {
		name string
		item types.QueuedItem
	}{
		{
			name: "value only",
			item: types.QueuedItem{Value: "plain text", Mode: types.ItemModePrompt, UUID: "u1", Origin: &types.MessageOrigin{Kind: types.OriginHuman}},
		},
		{
			name: "nil origin hits default branch",
			item: types.QueuedItem{Value: "nil origin", Mode: types.ItemModePrompt, UUID: "u2"},
		},
		{
			name: "text plus image",
			item: types.QueuedItem{
				Value:   "with image",
				Mode:    types.ItemModePrompt,
				UUID:    "u3",
				Origin:  &types.MessageOrigin{Kind: types.OriginHuman},
				Content: []types.ContentBlock{types.NewTextBlock("with image"), img},
			},
		},
		{
			name: "multi text blocks join with newline",
			item: types.QueuedItem{
				Mode:   types.ItemModePrompt,
				UUID:   "u4",
				Origin: &types.MessageOrigin{Kind: types.OriginHuman},
				Content: []types.ContentBlock{
					types.NewTextBlock("line one"),
					types.NewTextBlock("line two"),
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := tc.item.Content
			if len(content) == 0 {
				content = []types.ContentBlock{types.NewTextBlock(tc.item.Value)}
			}
			persisted := buildReminderContent(content, tc.item.Origin)

			legacy := types.Message{
				Role:        types.RoleUser,
				MessageType: types.MessageTypeAttachment,
				Content:     content,
				Attachment: &types.Attachment{
					Type:       types.AttachmentTypeQueued,
					Prompt:     tc.item.Value,
					SourceUUID: tc.item.UUID,
					Mode:       types.ItemModePrompt,
					Origin:     tc.item.Origin,
				},
			}
			assembled := normalizeAttachmentForAPI(legacy)

			if len(persisted) != len(assembled.Content) {
				t.Fatalf("block count %d, want %d", len(persisted), len(assembled.Content))
			}
			for i := range persisted {
				p, a := persisted[i], assembled.Content[i]
				if p.Type != a.Type || p.Text != a.Text || p.ID != a.ID || p.Name != a.Name {
					t.Errorf("block[%d] diverges:\npersisted: %+v\nassembled: %+v", i, p, a)
				}
				if (p.Source == nil) != (a.Source == nil) {
					t.Errorf("block[%d] Source nil-ness diverges", i)
				} else if p.Source != nil && *p.Source != *a.Source {
					t.Errorf("block[%d] Source diverges:\npersisted: %+v\nassembled: %+v", i, p.Source, a.Source)
				}
			}
			// The envelope must unwrap back to the original user text so the
			// UI render helpers (utils.UnwrapQueuedReminder) and this
			// builder can never drift apart.
			original, ok := utils.UnwrapQueuedReminder(persisted[0].Text)
			if !ok {
				t.Fatalf("envelope not recognized by utils unwrapper: %q", persisted[0].Text)
			}
			// Mirrors buildReminderContent's text join; falls back to Value
			// for items that carry no text blocks.
			var parts []string
			for _, b := range tc.item.Content {
				if b.Type == types.ContentTypeText {
					parts = append(parts, b.Text)
				}
			}
			wantText := tc.item.Value
			if len(parts) != 0 {
				wantText = strings.Join(parts, "\n")
			}
			if original != wantText {
				t.Errorf("unwrapped = %q, want %q", original, wantText)
			}
		})
	}
}

// TestIsUserQueuedMessageOrigin pins the branch predicate to wrapOriginText's
// default branch: everything except job/coordinator/channel (nil included).
func TestIsUserQueuedMessageOrigin(t *testing.T) {
	cases := []struct {
		origin *types.MessageOrigin
		want   bool
	}{
		{nil, true},
		{&types.MessageOrigin{Kind: types.OriginHuman}, true},
		{&types.MessageOrigin{Kind: types.OriginJob}, false},
		{&types.MessageOrigin{Kind: types.OriginCoordinator}, false},
		{&types.MessageOrigin{Kind: types.OriginChannel}, false},
	}
	for _, tc := range cases {
		if got := isUserQueuedOrigin(tc.origin); got != tc.want {
			t.Errorf("isUserQueuedOrigin(%v) = %v, want %v", tc.origin, got, tc.want)
		}
	}
}

// TestQueuedMessageInsertIndex covers the pairing-safe placement rules.
func TestQueuedMessageInsertIndex(t *testing.T) {
	user := types.Message{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("q")}}
	assistantText := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("a")}}
	toolUseAssistant := types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "t1", Name: "x", Input: json.RawMessage(`{}`)},
	}}
	toolResult := types.Message{Role: types.RoleUser, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolResult, ToolUseID: "t1", Content: json.RawMessage(`[{"type":"text","text":"ok"}]`)},
	}}

	if got := queuedInsertIndex([]types.Message{user}); got != 1 {
		t.Errorf("append case: index = %d, want 1", got)
	}
	if got := queuedInsertIndex([]types.Message{user, assistantText}); got != 2 {
		t.Errorf("no pending tool: index = %d, want 2 (append)", got)
	}
	if got := queuedInsertIndex([]types.Message{user, toolUseAssistant}); got != 1 {
		t.Errorf("pending tool: index = %d, want 1 (before the tool_use assistant)", got)
	}
	if got := queuedInsertIndex([]types.Message{user, toolUseAssistant, toolResult, assistantText}); got != 4 {
		t.Errorf("paired tool: index = %d, want 4 (append)", got)
	}
	// Two queued messages during one tool exec: the second must not land before
	// the first (scan finds the SAME unpaired assistant, so insert after the
	// earlier queued message means the returned index equals the assistant,
	// which sits behind the first queued message).
	first := types.Message{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(
		"<system-reminder>\nThe user sent a new message while you were working:\nfirst" +
			"\n\nIMPORTANT: After completing your current task, you MUST address the user's message above. Do not ignore it.\n</system-reminder>")}}
	if got := queuedInsertIndex([]types.Message{user, first, toolUseAssistant}); got != 2 {
		t.Errorf("second queued message: index = %d, want 2 (before the unpaired assistant, after the first queued message)", got)
	}
}

// TestQueuedMessage_CrashRecoveredUnpairedTail_PersistedAndReloaded pins the
// persist-cursor clamp. The state is REACHABLE: a hard kill between an
// assistant(tool_use) append and its turn-boundary persist leaves an unpaired
// tail in the DB, and a restart (SwitchSession → LoadChainMessages, which does
// not filter it) reloads it with cursor = len. The next query's queued message
// then computes an insert index below the cursor, and the uncommitted slice
// would silently drop it. The state is injected directly here only because a
// real process kill cannot be simulated in-process.
func TestQueuedMessage_CrashRecoveredUnpairedTail_PersistedAndReloaded(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	mp := &mockProvider{}
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		Model:      "test-model",
		Logger:     slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")
	eng.SetSessionID(session.SessionID)
	// Direct state injection: history ends (past the cursor) with the
	// crash's unpaired assistant; the live turn added a user message.
	eng.mu.Lock()
	eng.messages = []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "tu_crash", Name: "Bash", Input: []byte(`{}`)},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("next task")}},
	}
	eng.lastPersistedIdx = 2
	eng.mu.Unlock()
	eng.queryActive.Store(1)
	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "queued message after crash recovery",
		Mode:      types.ItemModePrompt,
		UUID:      "itst-crash",
		Priority:  types.PriorityNext,
		Origin:    &types.MessageOrigin{Kind: types.OriginHuman},
		Timestamp: time.Date(2026, 10, 5, 11, 0, 0, 0, time.UTC),
	})
	// Mid-turn the message is only queued: nothing in history, nothing persisted.
	if n := eng.AttachmentsLen(); n != 1 {
		t.Fatalf("attachment queue len = %d, want 1 (mid-turn enqueue must not attach)", n)
	}
	storeMsgs, err := store.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	midRun, err := short.StoreMessagesToEngine(storeMsgs)
	if err != nil {
		t.Fatalf("StoreMessagesToEngine: %v", err)
	}
	if idx := findEnvelopeMessages(midRun); len(idx) != 0 {
		t.Fatalf("store contains %d envelope messages while the message is queued, want 0", len(idx))
	}

	// The turn boundary attaches at max(queuedInsertIndex, cursor) = 2 —
	// below-cursor indices would drop the message from the uncommitted slice.
	eng.drainQueuedUserAttachments()
	storeMsgs, err = store.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages after attach: %v", err)
	}
	persisted, err := short.StoreMessagesToEngine(storeMsgs)
	if err != nil {
		t.Fatalf("StoreMessagesToEngine: %v", err)
	}
	idx := findEnvelopeMessages(persisted)
	if got := len(idx); got != 1 {
		t.Fatalf("store contains %d envelope messages after the boundary attach on crash-recovered history, want 1 (cursor clamp must keep it in the uncommitted slice)", got)
	}
	if persisted[idx[0]].ID != "itst-crash" {
		t.Errorf("persisted envelope ID = %q, want item UUID %q", persisted[idx[0]].ID, "itst-crash")
	}

	// Restart chain: a fresh engine recovers the session through the public
	// path and sees the queued message as an ordinary history message.
	eng2 := New(&Params{Provider: mp, Model: "test-model", Logger: slog.Default()})
	t.Cleanup(func() { eng2.Close() })
	eng2.SetStore(store, "")
	loaded, err := eng2.SwitchSession(session.SessionID)
	if err != nil {
		t.Fatalf("SwitchSession: %v", err)
	}
	reloadedIdx := findEnvelopeMessages(loaded)
	if len(reloadedIdx) != 1 {
		t.Fatalf("reloaded history contains %d envelope messages, want 1", len(reloadedIdx))
	}
	if last := len(loaded) - 1; reloadedIdx[0] != last {
		t.Errorf("reloaded envelope at index %d, want the sequence tail (%d)", reloadedIdx[0], last)
	}
}

// TestQueuedMessage_ZeroTimestampGuard: a zero Timestamp would marshal as a
// zero-year prefix in memory while the store stamps time.Now() — diverging on
// reload. appendQueuedUserMessage must stamp it.
func TestQueuedMessage_ZeroTimestampGuard(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "t1", "blocker", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "done"), nil)
	bt := &blockingTool{name: "blocker", startCh: make(chan struct{}), releaseCh: make(chan struct{})}
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{bt.Name(): bt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")
	eng.SetSessionID(session.SessionID)
	eng.Query(context.Background(), "run", "")
	select {
	case <-bt.startCh:
	case <-time.After(5 * time.Second):
		t.Fatal("tool never started")
	}
	eng.EnqueueAttachment(types.QueuedItem{
		Value:    "no timestamp set",
		Mode:     types.ItemModePrompt,
		UUID:     "itst-zero-ts",
		Priority: types.PriorityNext,
		Origin:   &types.MessageOrigin{Kind: types.OriginHuman},
		// Timestamp deliberately zero.
	})
	close(bt.releaseCh)
	if !ec.WaitForEventCount(types.EventAttachment, 1, 5*time.Second) {
		t.Fatal("no EventAttachment at the turn boundary")
	}
	if res := ec.WaitForResult(); res.Error != nil {
		t.Fatalf("query error: %v", res.Error)
	}
	eng.mu.RLock()
	var stamped int
	for _, m := range eng.messages {
		if m.Role == types.RoleUser && len(m.Content) > 0 &&
			strings.Contains(m.Content[0].Text, "no timestamp set") {
			if m.Timestamp.IsZero() {
				t.Error("queued message message Timestamp is zero — reload would render a different prefix")
			}
			stamped++
		}
	}
	eng.mu.RUnlock()
	if stamped != 1 {
		t.Fatalf("found %d queued message messages in memory, want 1", stamped)
	}
}

// TestQueuedMessage_EventStubShape pins the UI contract of the emitted
// EventAttachment: no echo while the tool runs, then at the boundary the
// event carries the original text in Prompt, prompt mode, envelope in content.
func TestQueuedMessage_EventStubShape(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "t1", "blocker", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "done"), nil)
	bt := &blockingTool{name: "blocker", startCh: make(chan struct{}), releaseCh: make(chan struct{})}
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{bt.Name(): bt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")
	eng.SetSessionID(session.SessionID)
	eng.Query(context.Background(), "run", "")
	select {
	case <-bt.startCh:
	case <-time.After(5 * time.Second):
		t.Fatal("tool never started")
	}
	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "stub shape check",
		Mode:      types.ItemModePrompt,
		UUID:      "itst-stub",
		Priority:  types.PriorityNext,
		Origin:    &types.MessageOrigin{Kind: types.OriginHuman},
		Timestamp: time.Date(2026, 10, 5, 11, 0, 0, 0, time.UTC),
	})
	// No echo before the boundary: the message has not attached yet.
	if evs := ec.FindEvents(types.EventAttachment); len(evs) != 0 {
		t.Fatalf("%d EventAttachment events while the tool runs, want 0", len(evs))
	}
	close(bt.releaseCh)
	if !ec.WaitForEventCount(types.EventAttachment, 1, 5*time.Second) {
		t.Fatal("no EventAttachment at the turn boundary")
	}
	att := findQueuedMessageAttachment(ec)
	if att == nil {
		t.Fatal("no EventAttachment emitted for queued message")
	}
	if att.Prompt != "stub shape check" {
		t.Errorf("Attachment.Prompt = %q, want the original text", att.Prompt)
	}
	if att.Mode != types.ItemModePrompt {
		t.Errorf("Attachment.Mode = %v, want ItemModePrompt", att.Mode)
	}
	if res := ec.WaitForResult(); res.Error != nil {
		t.Fatalf("query error: %v", res.Error)
	}
}

// findQueuedMessageAttachment returns the Attachment of the latest
// EventAttachment whose message content carries the queued message envelope.
func findQueuedMessageAttachment(ec *eventCollector) *types.Attachment {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	for _, ev := range slices.Backward(ec.events) {

		if ev.Type == types.EventAttachment && ev.Message != nil && ev.Message.Attachment != nil {
			return ev.Message.Attachment
		}
	}
	return nil
}

// TestQueuedMessage_AbortLeavesQueueForIdleRun pins the pre-c7359230 abort
// behavior on top of turn-boundary attach: aborting mid-tool must leave the
// queued message in the queue so endQuery's idle path runs it as a fresh
// turn. Attaching at the abort point instead would strand the message in
// history with no turn left to answer it (the 12:53 regression).
func TestQueuedMessage_AbortLeavesQueueForIdleRun(t *testing.T) {
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "t1", "blocker", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "idle answer"), nil)
	bt := &blockingTool{name: "blocker", startCh: make(chan struct{}), releaseCh: make(chan struct{})}
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{bt.Name(): bt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })
	released := false
	release := func() {
		if !released {
			released = true
			close(bt.releaseCh)
		}
	}
	t.Cleanup(release)
	// Query's systemPrompt parameter does not populate e.systemPrompt; the
	// idle path's startProcessAttachmentsIfIdle returns early without it.
	eng.SetSystemPrompt("sys")
	eng.Query(context.Background(), "run the tool", "sys")
	select {
	case <-bt.startCh:
	case <-time.After(5 * time.Second):
		t.Fatal("tool never started")
	}
	eng.EnqueueAttachment(types.QueuedItem{
		Value:     "queued while aborting",
		Mode:      types.ItemModePrompt,
		UUID:      "abort-uuid",
		Priority:  types.PriorityNext,
		Origin:    &types.MessageOrigin{Kind: types.OriginHuman},
		Timestamp: time.Date(2026, 10, 6, 12, 53, 40, 0, time.UTC),
	})
	eng.Abort()
	release()
	res := ec.WaitForResult()
	if res.Error == nil {
		t.Fatal("expected the aborted query to return an error")
	}
	// The message must NOT be attached by the aborted query: no envelope in
	// history, the item still queued, no attachment event from this query.
	if envelopes := findEnvelopeMessages(eng.Messages()); len(envelopes) != 0 {
		t.Fatalf("aborted query attached %d queued messages, want 0 (must stay queued for the idle run)", len(envelopes))
	}
	// The idle path (endQuery -> startProcessAttachmentsIfIdle) picks the
	// queued message up and runs a fresh turn that answers it.
	if !ec.WaitForEventCount(types.EventAttachment, 1, 5*time.Second) {
		t.Fatal("idle path never drained the queued message")
	}
	if !ec.WaitForEventCount(types.EventQueryEnd, 2, 5*time.Second) {
		t.Fatal("idle path never ran a second query")
	}
}
