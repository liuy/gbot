package engine

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/bash"
	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/utils"
)

// Job/message reminder persistence tests: drained job items are persisted as
// plain user messages in their final wire form (the same bytes the API
// assembly produces), so a restart replays the notification byte-for-byte and
// the prompt-cache prefix never diverges mid-conversation. Persist == inject
// at the drain site is the only construction that guarantees this.

// findReminderEnvelopeMessages returns the indices of user messages whose
// first text block satisfies utils.IsInjectedReminderEnvelope. findEnvelopeMessages
// (engine_queued_message_test.go) matches only the 插话 prefix and reports 0
// for job envelopes — using it here would make every assertion vacuous.
func findReminderEnvelopeMessages(msgs []types.Message) []int {
	var out []int
	for i, m := range msgs {
		if m.Role != types.RoleUser {
			continue
		}
		if utils.IsInjectedReminderEnvelope(utils.FirstTextBlockContent(m)) {
			out = append(out, i)
		}
	}
	return out
}

// loadDecodedStore loads and decodes a session's rows. Store assertions must
// go through the decoded form: the raw Content column is json.Marshal output
// with HTML escaping, so any needle containing '<' never matches it.
func loadDecodedStore(t *testing.T, store *short.Store, sessionID string) []types.Message {
	t.Helper()
	storeMsgs, err := store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	engMsgs, err := short.StoreMessagesToEngine(storeMsgs)
	if err != nil {
		t.Fatalf("StoreMessagesToEngine: %v", err)
	}
	return engMsgs
}

// bashNotificationXML builds a real producer-shaped notification so the
// persisted bytes are exercised against the actual wire form, not a hand
// approximation of it.
func bashNotificationXML() string {
	return bash.JobNotification{
		JobID:   "bg-1",
		Status:  "completed",
		Summary: `Background command "make check" completed (exit code 0)`,
	}.FormatXML()
}

// agentNotificationXML mirrors pkg/tool/agent/fork.go:282-294 output shape.
func agentNotificationXML() string {
	return "<job-notification>\n<job-id>agent-1</job-id>\n<tool-use-id>toolu_1</tool-use-id>\n" +
		"<status>completed</status>\n<agent-type>fork</agent-type>\n<duration-ms>1200</duration-ms>\n" +
		"<tokens>42</tokens>\n<tool-uses>1</tool-uses>\n<summary>Background agent &quot;refactor auth&quot; completed</summary>\n" +
		"<result>\ndone\n</result>\n</job-notification>"
}

// reminderJobItem builds the canonical test enqueue: a completed bash job
// with a real FormatXML body.
func reminderJobItem(uuid string) types.QueuedItem {
	return types.QueuedItem{
		Value:     bashNotificationXML(),
		Mode:      types.ItemModeJob,
		UUID:      uuid,
		Priority:  types.PriorityNext,
		Origin:    &types.MessageOrigin{Kind: types.OriginJob},
		Timestamp: time.Date(2026, 10, 13, 13, 43, 5, 0, time.UTC),
	}
}

// TestJobReminder_PersistedAtTurnBoundaryAndRestartStable is the core timing
// + persistence contract for job notifications: while the tool runs the item
// is only queued (no history, no store row, no event); the turn-boundary
// drain appends the final-form envelope and a batch persist (turn boundary
// or query exit — this poll cannot tell them apart) commits it while the
// query is still running; reloading the session reproduces the live
// sequence and the assembled wire bytes byte-for-byte.
func TestJobReminder_PersistedAtTurnBoundaryAndRestartStable(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "t1", "blocker", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "acknowledged the job"), nil)

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

	item := reminderJobItem("job-bg-1")
	eng.EnqueueAttachment(item)

	// While the tool runs the notification is nowhere but the queue.
	if n := eng.AttachmentsLen(); n != 1 {
		t.Fatalf("attachment queue len = %d, want 1", n)
	}
	if idx := findReminderEnvelopeMessages(eng.Messages()); len(idx) != 0 {
		t.Fatalf("history contains %d reminder envelopes while the tool runs, want 0", len(idx))
	}
	if idx := findReminderEnvelopeMessages(loadDecodedStore(t, store, session.SessionID)); len(idx) != 0 {
		t.Fatalf("store contains %d reminder envelopes while the tool runs, want 0", len(idx))
	}
	if evs := ec.FindEvents(types.EventAttachment); len(evs) != 0 {
		t.Fatalf("%d EventAttachment events while the tool runs, want 0", len(evs))
	}

	release()
	// A batch persist commits the envelope while the query is still running.
	deadline := time.Now().Add(5 * time.Second) // REAL-TIME
	var persisted []types.Message
	for {
		persisted = loadDecodedStore(t, store, session.SessionID)
		if idx := findReminderEnvelopeMessages(persisted); len(idx) == 1 {
			break
		}
		if time.Now().After(deadline) { // REAL-TIME: poll loop bounded by deadline
			t.Fatalf("store contains %d reminder envelopes after the boundary drain, want 1 (the turn-boundary batch persist must commit it)", len(findReminderEnvelopeMessages(persisted)))
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}
	want := buildReminderContent([]types.ContentBlock{types.NewTextBlock(item.Value)}, item.Origin)
	row := persisted[findReminderEnvelopeMessages(persisted)[0]]
	if len(row.Content) != len(want) || row.Content[0].Text != want[0].Text {
		t.Errorf("persisted envelope text = %q, want byte-exact reminder envelope %q",
			utils.FirstTextBlockContent(row), want[0].Text)
	}
	// Plain user shape: role user, no attachment metadata, no flags.
	if row.Role != types.RoleUser {
		t.Errorf("persisted row role = %q, want user", row.Role)
	}
	if row.Attachment != nil {
		t.Errorf("persisted row still carries Attachment metadata: %+v", row.Attachment)
	}
	if row.HasFlag(types.FlagMeta) {
		t.Error("persisted row carries FlagMeta — wui buildHistory would drop it")
	}
	// The row keeps the ITEM's fixed timestamp (a field-clear here would
	// silently fall through to the zero-guard's time.Now()).
	if !row.Timestamp.Equal(item.Timestamp) {
		t.Errorf("persisted row Timestamp = %v, want the item timestamp %v", row.Timestamp, item.Timestamp)
	}

	if res := ec.WaitForResult(); res.Error != nil {
		t.Fatalf("query error: %v", res.Error)
	}
	// The boundary drain emits exactly one live event so TUI/WUI insert the
	// row without a refresh. The event copy carries the stub (stored row stays
	// plain), Prompt holds the persisted envelope, Mode discriminates job.
	evs := ec.FindEvents(types.EventAttachment)
	if len(evs) != 1 {
		t.Fatalf("%d EventAttachment events for a job item, want exactly 1 (live row insert)", len(evs))
	}
	if evs[0].Message == nil || evs[0].Message.Attachment == nil {
		t.Fatal("reminder EventAttachment carries no stub message")
	}
	att := evs[0].Message.Attachment
	if att.Type != types.AttachmentTypeQueued {
		t.Errorf("event stub type = %q, want queued_item", att.Type)
	}
	if att.Origin == nil || att.Origin.Kind != types.OriginJob {
		t.Errorf("event stub origin = %+v, want the item origin (job)", att.Origin)
	}
	if att.Mode != types.ItemModeJob {
		t.Errorf("event stub mode = %q, want job", att.Mode)
	}
	if att.SourceUUID != item.UUID {
		t.Errorf("event stub SourceUUID = %q, want item UUID %q", att.SourceUUID, item.UUID)
	}
	wantEnvelope := buildReminderContent([]types.ContentBlock{types.NewTextBlock(item.Value)}, item.Origin)[0].Text
	if att.Prompt != wantEnvelope {
		t.Errorf("event stub Prompt = %q, want the persisted envelope %q", att.Prompt, wantEnvelope)
	}
	if att.Prompt != utils.FirstTextBlockContent(*evs[0].Message) {
		t.Errorf("event stub Prompt %q diverges from the stored message content %q", att.Prompt, utils.FirstTextBlockContent(*evs[0].Message))
	}
	// Pure UI signal: no seq bump (a bump would buy a redundant LLM turn) and
	// no extra turn — the query ran exactly tool-call + follow-up.
	if eng.queuedMsgSeq != 0 {
		t.Errorf("queuedMsgSeq = %d after a job reminder drain, want 0 (no bump)", eng.queuedMsgSeq)
	}
	if eng.turnCount != 2 {
		t.Errorf("turnCount = %d after the query, want 2 (tool turn + follow-up, no extra)", eng.turnCount)
	}

	// The query-exit persist defer settles the store at the live size; then
	// every message must match the live history role-by-role, byte-by-byte.
	settleDeadline := time.Now().Add(5 * time.Second) // REAL-TIME
	live := eng.Messages()
	var reloaded []types.Message
	for {
		reloaded = loadDecodedStore(t, store, session.SessionID)
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

	// Pairing repair must be a no-op on the reloaded sequence.
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

	// Wire-level prefix stability: the assembled request (including the
	// injectTimestamp prefix derived from the persisted Timestamp) must be
	// byte-identical across the restart.
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

// TestJobReminder_IdleDrainPersists covers the second producer of job
// notification messages: the idle path (processAttachments → DrainAll). If
// only the turn-boundary path persisted, this store count stays 0.
func TestJobReminder_IdleDrainPersists(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "noted the completion"), nil)
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
	// The idle kick reads e.systemPrompt (not the Query parameter), so it
	// must be set through the setter or EnqueueAttachment never starts a turn.
	eng.SetSystemPrompt("sys")

	item := reminderJobItem("job-idle-1")
	eng.EnqueueAttachment(item)

	if res := ec.WaitForResult(); res.Error != nil {
		t.Fatalf("idle query error: %v", res.Error)
	}

	if n := eng.AttachmentsLen(); n != 0 {
		t.Fatalf("attachment queue len = %d after idle drain, want 0", n)
	}
	decoded := loadDecodedStore(t, store, session.SessionID)
	idx := findReminderEnvelopeMessages(decoded)
	if len(idx) != 1 {
		t.Fatalf("store contains %d reminder envelopes after idle drain, want 1", len(idx))
	}
	want := buildReminderContent([]types.ContentBlock{types.NewTextBlock(item.Value)}, item.Origin)
	if text := utils.FirstTextBlockContent(decoded[idx[0]]); text != want[0].Text {
		t.Errorf("persisted envelope = %q, want %q", text, want[0].Text)
	}
	// The idle drain emits exactly one live event (same contract as the
	// turn-boundary drain): stub message, envelope in Prompt, job mode.
	evs := ec.FindEvents(types.EventAttachment)
	if len(evs) != 1 {
		t.Fatalf("%d EventAttachment events for an idle-drained job item, want exactly 1 (live row insert)", len(evs))
	}
	if evs[0].Message == nil || evs[0].Message.Attachment == nil {
		t.Fatal("reminder EventAttachment carries no stub message")
	}
	if evs[0].Message.Attachment.Mode != types.ItemModeJob {
		t.Errorf("event stub mode = %q, want job", evs[0].Message.Attachment.Mode)
	}
	// Pure UI signal: no seq bump, and the idle query ran exactly one turn.
	if eng.queuedMsgSeq != 0 {
		t.Errorf("queuedMsgSeq = %d after an idle job reminder drain, want 0 (no bump)", eng.queuedMsgSeq)
	}
	if eng.turnCount != 1 {
		t.Errorf("turnCount = %d after the idle query, want 1 (no extra turn)", eng.turnCount)
	}
}

// TestMessageReminder_DrainEmitsLiveEvent pins the message-delivery half of
// the live-event contract: a drained message item emits exactly one
// EventAttachment whose stub Prompt is the bare <message from="…"> envelope
// (Mode message), with no seq bump and no extra turn — identical to the job
// half except the envelope shape.
func TestMessageReminder_DrainEmitsLiveEvent(t *testing.T) {
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "noted"), nil)
	ec := newEventCollector()
	eng := New(&Params{
		Provider:   mp,
		Dispatcher: ec,
		Model:      "test-model",
		Logger:     slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSystemPrompt("sys")

	item := types.QueuedItem{
		Value:     "<message from=\"main\">\nhello main\n</message>",
		Mode:      types.ItemModeMessage,
		UUID:      "msg-1",
		Priority:  types.PriorityNext,
		Origin:    &types.MessageOrigin{Kind: types.OriginMessage},
		Timestamp: time.Date(2026, 10, 13, 13, 43, 5, 0, time.UTC),
	}
	eng.EnqueueAttachment(item)
	if res := ec.WaitForResult(); res.Error != nil {
		t.Fatalf("idle query error: %v", res.Error)
	}

	evs := ec.FindEvents(types.EventAttachment)
	if len(evs) != 1 {
		t.Fatalf("%d EventAttachment events for a message item, want exactly 1 (live row insert)", len(evs))
	}
	if evs[0].Message == nil || evs[0].Message.Attachment == nil {
		t.Fatal("reminder EventAttachment carries no stub message")
	}
	att := evs[0].Message.Attachment
	if att.Mode != types.ItemModeMessage {
		t.Errorf("event stub mode = %q, want message", att.Mode)
	}
	if att.SourceUUID != item.UUID {
		t.Errorf("event stub SourceUUID = %q, want %q", att.SourceUUID, item.UUID)
	}
	if att.Prompt != item.Value {
		t.Errorf("event stub Prompt = %q, want the bare delivery envelope %q", att.Prompt, item.Value)
	}
	if sender := utils.MessageDeliverySender(att.Prompt); sender != "main" {
		t.Errorf("utils.MessageDeliverySender(%q) = %q, want main", att.Prompt, sender)
	}
	if eng.queuedMsgSeq != 0 {
		t.Errorf("queuedMsgSeq = %d after a message reminder drain, want 0 (no bump)", eng.queuedMsgSeq)
	}
	if eng.turnCount != 1 {
		t.Errorf("turnCount = %d after the idle query, want 1 (no extra turn)", eng.turnCount)
	}
}

// TestJobReminder_ZeroTimestampGuard: a zero Timestamp would marshal as a
// zero-year prefix in memory while the store stamps time.Now() — the reloaded
// wire form would diverge. appendQueuedReminderMessage must stamp it.
func TestJobReminder_ZeroTimestampGuard(t *testing.T) {
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
	item := reminderJobItem("job-zero-ts")
	item.Timestamp = time.Time{} // deliberately zero
	eng.EnqueueAttachment(item)
	close(bt.releaseCh)
	if res := ec.WaitForResult(); res.Error != nil {
		t.Fatalf("query error: %v", res.Error)
	}

	eng.mu.RLock()
	var liveMsg *types.Message
	for i := range eng.messages {
		if utils.IsInjectedReminderEnvelope(utils.FirstTextBlockContent(eng.messages[i])) {
			liveMsg = &eng.messages[i]
		}
	}
	eng.mu.RUnlock()
	if liveMsg == nil {
		t.Fatal("no reminder envelope message in live history")
	}
	if liveMsg.Timestamp.IsZero() {
		t.Fatal("reminder message Timestamp is zero — reload would render a different prefix")
	}
	decoded := loadDecodedStore(t, store, session.SessionID)
	idx := findReminderEnvelopeMessages(decoded)
	if len(idx) != 1 {
		t.Fatalf("store contains %d reminder envelopes, want 1", len(idx))
	}
	if !decoded[idx[0]].Timestamp.Equal(liveMsg.Timestamp) {
		t.Errorf("persisted row CreatedAt = %v, want the stamped in-memory timestamp %v",
			decoded[idx[0]].Timestamp, liveMsg.Timestamp)
	}

	// Wire stability: the [ts] prefix must render identically live and
	// reloaded. Wait for the query-exit persist defer to settle first, or
	// the reloaded sequence is shorter than the live one.
	settleDeadline := time.Now().Add(5 * time.Second) // REAL-TIME
	for {
		decoded = loadDecodedStore(t, store, session.SessionID)
		if len(decoded) == len(eng.Messages()) {
			break
		}
		if time.Now().After(settleDeadline) { // REAL-TIME: poll loop bounded by deadline
			t.Fatalf("store never settled: %d rows vs %d live messages", len(decoded), len(eng.Messages()))
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}
	eng2 := New(&Params{Provider: mp, Model: "test-model", Logger: slog.Default()})
	t.Cleanup(func() { eng2.Close() })
	eng2.SetMessages(decoded)
	liveWire := eng.marshalMessages()
	reloadedWire := eng2.marshalMessages()
	if len(liveWire) != len(reloadedWire) {
		t.Fatalf("wire message count: live %d, reloaded %d", len(liveWire), len(reloadedWire))
	}
	for i := range liveWire {
		if len(liveWire[i].Content) != len(reloadedWire[i].Content) {
			t.Fatalf("wire msg[%d] block count: live %d, reloaded %d", i, len(liveWire[i].Content), len(reloadedWire[i].Content))
		}
		for b := range liveWire[i].Content {
			if liveWire[i].Content[b].Text != reloadedWire[i].Content[b].Text {
				t.Errorf("wire msg[%d] block[%d] diverges across restart:\nlive:     %q\nreloaded: %q",
					i, b, liveWire[i].Content[b].Text, reloadedWire[i].Content[b].Text)
			}
		}
	}
}

// TestJobReminder_MidQueryOrderingAndPriority pins that history order is
// PRIORITY order, not enqueue order: DrainByPriority keeps FIFO only among
// the matched items, so the PriorityNext item enqueued SECOND lands first,
// and the PriorityLater item must not be adopted mid-query — it waits for the
// idle kick after the query ends (the contract documented at
// TestAbort_CancelsProcessAttachmentsAfterQueryEnds).
func TestJobReminder_MidQueryOrderingAndPriority(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "t1", "blocker", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "final answer"), nil)
	mp.addResponse(textStreamEvents("test-model", "later noted"), nil)

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
	// The idle kick (which drains the PriorityLater item) reads e.systemPrompt.
	eng.SetSystemPrompt("sys")

	eng.Query(context.Background(), "run the tool", "")
	select {
	case <-bt.startCh:
	case <-time.After(5 * time.Second):
		t.Fatal("tool never started")
	}

	later := reminderJobItem("job-later")
	later.Value = strings.Replace(bashNotificationXML(), "bg-1", "bg-later", 1)
	later.Priority = types.PriorityLater
	later.Timestamp = time.Date(2026, 10, 13, 13, 43, 1, 0, time.UTC)
	next := reminderJobItem("job-next")
	next.Value = strings.Replace(bashNotificationXML(), "bg-1", "bg-next", 1)
	next.Timestamp = time.Date(2026, 10, 13, 13, 43, 2, 0, time.UTC)
	// Enqueue order deliberately INVERTED from drain order.
	eng.EnqueueAttachment(later)
	eng.EnqueueAttachment(next)

	// When the follow-up LLM call starts, the boundary drain has already run:
	// exactly the PriorityNext item is appended, the later item stays queued.
	release := func() { close(bt.releaseCh) }
	release()
	waitForCallCount(t, mp, 2, 5*time.Second)

	msgs := eng.Messages()
	envIdx := findReminderEnvelopeMessages(msgs)
	if len(envIdx) != 1 {
		t.Fatalf("history contains %d reminder envelopes at the boundary, want exactly 1 (only PriorityNext)", len(envIdx))
	}
	toolResultIdx := -1
	for i, m := range msgs {
		if m.Role != types.RoleUser {
			continue
		}
		for _, b := range m.Content {
			if b.Type == types.ContentTypeToolResult && b.ToolUseID == "t1" {
				toolResultIdx = i
			}
		}
	}
	if toolResultIdx == -1 {
		t.Fatal("tool_result for t1 not found in history")
	}
	if envIdx[0] != toolResultIdx+1 {
		t.Errorf("PriorityNext envelope at index %d, want %d (immediately after the tool_result)", envIdx[0], toolResultIdx+1)
	}
	if text := utils.FirstTextBlockContent(msgs[envIdx[0]]); !strings.Contains(text, "bg-next") {
		t.Errorf("boundary envelope carries %q, want the bg-next item", text)
	}
	if n := eng.AttachmentsLen(); n != 1 {
		t.Fatalf("attachment queue len = %d at the boundary, want 1 (PriorityLater must not be adopted mid-query)", n)
	}

	if res := ec.WaitForResult(); res.Error != nil {
		t.Fatalf("query error: %v", res.Error)
	}
	// The idle kick drains the later item after the query's final assistant.
	if !ec.WaitForEventCount(types.EventQueryEnd, 2, 5*time.Second) {
		t.Fatal("idle kick never ran for the PriorityLater item")
	}
	msgs = eng.Messages()
	envIdx = findReminderEnvelopeMessages(msgs)
	if len(envIdx) != 2 {
		t.Fatalf("history contains %d reminder envelopes after the idle drain, want 2", len(envIdx))
	}
	finalAssistantIdx := -1
	for i, m := range msgs {
		if m.Role == types.RoleAssistant && strings.Contains(utils.FirstTextBlockContent(m), "final answer") {
			finalAssistantIdx = i
		}
	}
	if finalAssistantIdx == -1 {
		t.Fatal("query's final assistant message not found")
	}
	laterIdx := -1
	for _, i := range envIdx {
		if strings.Contains(utils.FirstTextBlockContent(msgs[i]), "bg-later") {
			laterIdx = i
		}
	}
	if laterIdx == -1 {
		t.Fatal("bg-later envelope not found in history")
	}
	if laterIdx <= finalAssistantIdx {
		t.Errorf("PriorityLater envelope at index %d, want > %d (after the query's final assistant)", laterIdx, finalAssistantIdx)
	}

	decoded := loadDecodedStore(t, store, session.SessionID)
	if got := len(findReminderEnvelopeMessages(decoded)); got != 2 {
		t.Fatalf("store contains %d decoded reminder rows, want 2", got)
	}
	for _, i := range findReminderEnvelopeMessages(decoded) {
		text := utils.FirstTextBlockContent(decoded[i])
		var raw string
		if strings.Contains(text, "bg-later") {
			raw = later.Value
		} else {
			raw = next.Value
		}
		want := buildReminderContent([]types.ContentBlock{types.NewTextBlock(raw)}, &types.MessageOrigin{Kind: types.OriginJob})
		if text != want[0].Text {
			t.Errorf("persisted envelope = %q, want %q", text, want[0].Text)
		}
	}
}

// TestJobReminder_InsertIndex_PairedVsCrashRecovered pins the insert index
// for both history shapes. Naive expectation recorded so nobody "fixes" the
// test back: the 插话 template (TestQueuedMessage_CrashRecoveredUnpairedTail_
// PersistedAndReloaded) uses messages = [assistant(unpaired), user] with
// cursor = 2, so its clamp lands at the TAIL — that third shape is already
// pinned there and is deliberately not duplicated here.
func TestJobReminder_InsertIndex_PairedVsCrashRecovered(t *testing.T) {
	t.Run("paired tail appends at the end", func(t *testing.T) {
		store := newTestStore(t)
		session, err := store.CreateSession("", "test-model")
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		eng := New(&Params{Logger: slog.Default()})
		t.Cleanup(func() { eng.Close() })
		eng.SetStore(store, "")
		eng.SetSessionID(session.SessionID)

		eng.mu.Lock()
		eng.messages = []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("q")}},
			{Role: types.RoleAssistant, Content: []types.ContentBlock{
				{Type: types.ContentTypeToolUse, ID: "tu1", Name: "Bash", Input: []byte(`{}`)},
			}},
			{Role: types.RoleUser, Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, ToolUseID: "tu1", Content: json.RawMessage(`[{"type":"text","text":"ok"}]`)},
			}},
		}
		eng.lastPersistedIdx = 1
		eng.mu.Unlock()

		item := reminderJobItem("job-shape-a")
		eng.appendQueuedReminderMessage(item)

		msgs := eng.Messages()
		if len(msgs) != 4 {
			t.Fatalf("history = %d messages, want 4", len(msgs))
		}
		if !utils.IsInjectedReminderEnvelope(utils.FirstTextBlockContent(msgs[3])) {
			t.Errorf("message at index 3 is not the reminder envelope: %q", utils.FirstTextBlockContent(msgs[3]))
		}
		decoded := loadDecodedStore(t, store, session.SessionID)
		idx := findReminderEnvelopeMessages(decoded)
		if len(idx) != 1 {
			t.Fatalf("store contains %d reminder rows, want 1 (note: only messages past the cursor persist)", len(idx))
		}
		want := buildReminderContent([]types.ContentBlock{types.NewTextBlock(item.Value)}, item.Origin)
		if text := utils.FirstTextBlockContent(decoded[idx[0]]); text != want[0].Text {
			t.Errorf("persisted envelope = %q, want %q", text, want[0].Text)
		}

		marshaled := eng.marshalMessages()
		repaired := EnsureToolResultPairing(marshaled)
		if len(repaired) != len(marshaled) {
			t.Fatalf("EnsureToolResultPairing changed the sequence: %d -> %d messages", len(marshaled), len(repaired))
		}
		for i := range marshaled {
			if len(marshaled[i].Content) != len(repaired[i].Content) {
				t.Errorf("EnsureToolResultPairing mutated msg[%d] blocks: %d -> %d", i, len(marshaled[i].Content), len(repaired[i].Content))
			}
		}
		for _, m := range repaired {
			for _, b := range m.Content {
				if b.Type == types.ContentTypeToolResult && strings.Contains(string(b.Content), syntheticToolResultPlaceholder) {
					t.Error("pairing injected a synthetic tool_result although every tool_use was paired")
				}
			}
		}
	})

	t.Run("crash-recovered unpaired tail inserts before the assistant", func(t *testing.T) {
		store := newTestStore(t)
		session, err := store.CreateSession("", "test-model")
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		eng := New(&Params{Logger: slog.Default()})
		t.Cleanup(func() { eng.Close() })
		eng.SetStore(store, "")
		eng.SetSessionID(session.SessionID)

		eng.mu.Lock()
		eng.messages = []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("q")}},
			{Role: types.RoleAssistant, Content: []types.ContentBlock{
				{Type: types.ContentTypeToolUse, ID: "tu_crash", Name: "Bash", Input: []byte(`{}`)},
			}},
		}
		eng.lastPersistedIdx = 1
		eng.mu.Unlock()

		item := reminderJobItem("job-shape-b")
		eng.appendQueuedReminderMessage(item)

		msgs := eng.Messages()
		if len(msgs) != 3 {
			t.Fatalf("history = %d messages, want 3", len(msgs))
		}
		if msgs[1].ID != item.UUID || !utils.IsInjectedReminderEnvelope(utils.FirstTextBlockContent(msgs[1])) {
			t.Errorf("message at index 1 = (ID %q, %q), want the reminder envelope (ID %q)",
				msgs[1].ID, utils.FirstTextBlockContent(msgs[1]), item.UUID)
		}
		if msgs[2].Role != types.RoleAssistant {
			t.Errorf("message at index 2 role = %q, want the unpaired assistant", msgs[2].Role)
		}
		decoded := loadDecodedStore(t, store, session.SessionID)
		idx := findReminderEnvelopeMessages(decoded)
		if len(idx) != 1 {
			t.Fatalf("store contains %d reminder rows, want 1", len(idx))
		}
		want := buildReminderContent([]types.ContentBlock{types.NewTextBlock(item.Value)}, item.Origin)
		if text := utils.FirstTextBlockContent(decoded[idx[0]]); text != want[0].Text {
			t.Errorf("persisted envelope = %q, want %q", text, want[0].Text)
		}

		// Pairing is NEVER a no-op while an unpaired tool_use survives — the
		// property to pin is what queuedInsertIndex defends: the synthetic
		// tool_result lands immediately after its assistant, and the envelope
		// keeps its index and text unchanged relative to the pre-pairing
		// assembly (comparing against the raw envelope bytes would be a
		// guaranteed false failure — injectTimestamp already prefixed [ts]).
		before := eng.marshalMessages()
		after := EnsureToolResultPairing(eng.marshalMessages())
		envIdx := -1
		for i, m := range before {
			if m.ID == item.UUID {
				envIdx = i
			}
		}
		if envIdx != 1 {
			t.Fatalf("envelope at index %d before pairing, want 1", envIdx)
		}
		if utils.FirstTextBlockContent(after[envIdx]) != utils.FirstTextBlockContent(before[envIdx]) {
			t.Errorf("pairing mutated the envelope text:\nbefore: %q\nafter:  %q",
				utils.FirstTextBlockContent(before[envIdx]), utils.FirstTextBlockContent(after[envIdx]))
		}
		assistantAfter := -1
		for i, m := range after {
			if m.Role == types.RoleAssistant {
				assistantAfter = i
			}
		}
		if assistantAfter == -1 {
			t.Fatal("unpaired assistant lost by pairing")
		}
		if assistantAfter+1 >= len(after) || after[assistantAfter+1].Role != types.RoleUser {
			t.Fatalf("synthetic tool_result message does not immediately follow the assistant (assistant@%d, next@%d of %d)",
				assistantAfter, assistantAfter+1, len(after))
		}
		foundSynthetic := false
		for _, b := range after[assistantAfter+1].Content {
			if b.Type == types.ContentTypeToolResult && b.ToolUseID == "tu_crash" &&
				strings.Contains(string(b.Content), syntheticToolResultPlaceholder) {
				foundSynthetic = true
			}
		}
		if !foundSynthetic {
			t.Error("no synthetic tool_result for tu_crash in the message after the assistant")
		}
	})
}

// TestQueuedFinalContent_JobOrigin pins the job-channel content builder: the
// explicit OriginJob arm and the nil-origin inference (mirroring
// normalizeAttachmentForAPI), with a real agent XML body and a real bash
// FormatXML value. Cross-package drift guard: the engine-built text equals a
// literal written here, and utils.IsInjectedReminderEnvelope accepts both the
// literal and the built text.
func TestQueuedFinalContent_JobOrigin(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		origin *types.MessageOrigin
		raw    string
	}{
		{"explicit origin", &types.MessageOrigin{Kind: types.OriginJob}, agentNotificationXML()},
		{"nil origin infers job", nil, agentNotificationXML()},
		{"bash notification body", &types.MessageOrigin{Kind: types.OriginJob}, bashNotificationXML()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := types.QueuedItem{Value: tc.raw, Mode: types.ItemModeJob, UUID: "u-job", Origin: tc.origin}
			content := queuedFinalContent(item)
			if len(content) != 1 || content[0].Type != types.ContentTypeText {
				t.Fatalf("content = %d blocks, want a single text block", len(content))
			}
			literal := "<system-reminder>\nA background agent completed a job:\n" + tc.raw + "\n</system-reminder>"
			if content[0].Text != literal {
				t.Errorf("queuedFinalContent = %q, want %q", content[0].Text, literal)
			}
			if !utils.IsInjectedReminderEnvelope(literal) {
				t.Errorf("utils.IsInjectedReminderEnvelope(literal) = false — engine/utils envelope drift")
			}
			if !utils.IsInjectedReminderEnvelope(content[0].Text) {
				t.Errorf("utils.IsInjectedReminderEnvelope(built) = false — engine/utils envelope drift")
			}
		})
	}
}

// TestQueuedFinalContent_NilOriginMatchesLegacyAssembly is the case that
// fails loudly if the nil-origin → OriginJob inference is dropped: the
// persisted text would carry the user 插话 head instead.
func TestQueuedFinalContent_NilOriginMatchesLegacyAssembly(t *testing.T) {
	t.Parallel()
	raw := agentNotificationXML()
	item := types.QueuedItem{Value: raw, Mode: types.ItemModeJob, UUID: "u-nil", Origin: nil}
	persisted := queuedFinalContent(item)

	legacy := types.Message{
		Role:        types.RoleUser,
		MessageType: types.MessageTypeAttachment,
		Content:     []types.ContentBlock{types.NewTextBlock(raw)},
		Attachment: &types.Attachment{
			Type:       types.AttachmentTypeQueued,
			Prompt:     raw,
			SourceUUID: "u-nil",
			Mode:       types.ItemModeJob,
			Origin:     nil,
		},
	}
	assembled := normalizeAttachmentForAPI(legacy)

	if len(persisted) != len(assembled.Content) {
		t.Fatalf("block count %d, want %d", len(persisted), len(assembled.Content))
	}
	for i := range persisted {
		if persisted[i].Type != assembled.Content[i].Type || persisted[i].Text != assembled.Content[i].Text {
			t.Errorf("block[%d] diverges:\npersisted: %+v\nassembled: %+v", i, persisted[i], assembled.Content[i])
		}
	}
}

// TestQueuedFinalContent_JobCommand pins the bash-producer channel: the
// job_command origin must land on the "A background command completed:" head.
func TestQueuedFinalContent_JobCommand(t *testing.T) {
	t.Parallel()
	raw := bashNotificationXML()
	item := types.QueuedItem{
		Value:  raw,
		Mode:   types.ItemModeJob,
		UUID:   "u-cmd",
		Origin: &types.MessageOrigin{Kind: types.OriginJobCommand},
	}
	content := queuedFinalContent(item)
	if len(content) != 1 {
		t.Fatalf("content = %d blocks, want 1", len(content))
	}
	literal := "<system-reminder>\nA background command completed:\n" + raw + "\n</system-reminder>"
	if content[0].Text != literal {
		t.Errorf("queuedFinalContent = %q, want %q", content[0].Text, literal)
	}
	if !utils.IsInjectedReminderEnvelope(literal) || !utils.IsInjectedReminderEnvelope(content[0].Text) {
		t.Error("utils.IsInjectedReminderEnvelope rejected the job_command envelope — engine/utils drift")
	}
	if utils.IsMessageDeliveryEnvelope(content[0].Text) {
		t.Error("the two sniffs overlap: job_command envelope classified as a delivery")
	}
}

// TestQueuedFinalContent_Message pins the Message-tool channel: the delivery
// rides its own envelope verbatim — no reminder head, no system-reminder.
func TestQueuedFinalContent_Message(t *testing.T) {
	t.Parallel()
	value := "<message from=\"main\">\nhello main\n</message>"
	item := types.QueuedItem{
		Value:  value,
		Mode:   types.ItemModeMessage,
		UUID:   "u-msg",
		Origin: &types.MessageOrigin{Kind: types.OriginMessage},
	}
	content := queuedFinalContent(item)
	if len(content) != 1 {
		t.Fatalf("content = %d blocks, want 1", len(content))
	}
	if content[0].Text != item.Value {
		t.Errorf("queuedFinalContent = %q, want the item Value verbatim", content[0].Text)
	}
	if strings.Contains(content[0].Text, "<system-reminder>") {
		t.Error("message channel must never be reminder-wrapped")
	}
	if !utils.IsMessageDeliveryEnvelope(content[0].Text) {
		t.Errorf("utils.IsMessageDeliveryEnvelope rejected the delivery envelope — engine/utils drift")
	}
	if utils.IsInjectedReminderEnvelope(content[0].Text) {
		t.Error("the two sniffs overlap: delivery envelope classified as a job reminder")
	}
}

// runJobReminderLiveFlow drives the full live flow once — real Query with a
// blocking tool, a real bash notification enqueued mid-tool, drained and
// persisted at the turn boundary — and returns the settled store so restart
// tests (T12/T13) share the same fixture shape.
func runJobReminderLiveFlow(t *testing.T) (*short.Store, *short.Session, *mockProvider) {
	t.Helper()
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "t1", "blocker", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "acknowledged the job"), nil)

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

	eng.Query(context.Background(), "run the tool", "")
	select {
	case <-bt.startCh:
	case <-time.After(5 * time.Second):
		t.Fatal("tool never started")
	}
	eng.EnqueueAttachment(reminderJobItem("job-restart-1"))
	close(bt.releaseCh)
	if res := ec.WaitForResult(); res.Error != nil {
		t.Fatalf("query error: %v", res.Error)
	}
	// Let the query-exit persist defer settle before the caller snapshots.
	settleDeadline := time.Now().Add(5 * time.Second) // REAL-TIME
	for {
		decoded := loadDecodedStore(t, store, session.SessionID)
		if len(decoded) == len(eng.Messages()) {
			return store, session, mp
		}
		if time.Now().After(settleDeadline) { // REAL-TIME: poll loop bounded by deadline
			t.Fatalf("store never settled: %d rows vs %d live messages", len(decoded), len(eng.Messages()))
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}
}

// TestJobReminder_NoReInjectionAfterRestart makes the anti-re-send contract
// explicit. Job notifications are one-shot events with no ShouldFire re-fire
// logic (unlike TaskReminderProvider, which scans persisted messages to
// decide). Their idempotence rests on two invariants: (a) every drain
// primitive (DrainAll/DrainByPriority/DrainIf) removes what it returns, so a
// drained item cannot be drained again; (b) the persisted row is a plain
// user message with no Attachment, so no drain path can re-read it — the
// queue is the only source, and it is empty after restart (a fresh Queue
// zero value). Any re-drain of a persisted row (or a queue snapshot replayed
// into the new engine) produces a second reminder row and fails this test.
func TestJobReminder_NoReInjectionAfterRestart(t *testing.T) {
	store, session, mp := runJobReminderLiveFlow(t)

	before := loadDecodedStore(t, store, session.SessionID)
	if got := len(findReminderEnvelopeMessages(before)); got != 1 {
		t.Fatalf("fixture holds %d reminder rows after the live flow, want 1", got)
	}

	eng2 := New(&Params{Provider: mp, Model: "test-model", Logger: slog.Default()})
	t.Cleanup(func() { eng2.Close() })
	eng2.SetStore(store, "")
	if _, err := eng2.SwitchSession(session.SessionID); err != nil {
		t.Fatalf("SwitchSession: %v", err)
	}
	if got := eng2.AttachmentsLen(); got != 0 {
		t.Fatalf("restarted engine queue len = %d, want 0 (the queue never survives a restart)", got)
	}

	mp.addResponse(textStreamEvents("test-model", "fresh turn answer"), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if res := eng2.QuerySync(ctx, "next question", ""); res.Error != nil {
		t.Fatalf("post-restart query: %v", res.Error)
	}

	after := loadDecodedStore(t, store, session.SessionID)
	if got := len(findReminderEnvelopeMessages(after)); got != 1 {
		t.Errorf("store holds %d reminder rows after the restart query, want still 1 (no re-injection)", got)
	}
	if got := len(findReminderEnvelopeMessages(eng2.Messages())); got != 1 {
		t.Errorf("restarted history holds %d reminder messages, want 1 (no re-injection)", got)
	}
	if len(after) != len(before)+2 {
		t.Errorf("store grew by %d rows across the restart query, want 2 (the new user + assistant only)", len(after)-len(before))
	}
	// Raw-column equivalent of the single-envelope count: the only legal
	// needle on the raw column is the bare keyword (no angle brackets).
	storeMsgs, err := store.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	rawHits := 0
	for _, m := range storeMsgs {
		if strings.Contains(m.Content, "job-notification") {
			rawHits++
		}
	}
	if rawHits != 1 {
		t.Errorf("raw rows containing 'job-notification' = %d, want 1", rawHits)
	}
}

// TestJobReminder_NotRewindSelectable_AfterRestart exercises the C1 rewind
// guard through a reloaded row rather than a hand-built one: the persisted
// job reminder must not be a rewind target, or a rewind would splice history
// at an injected notification.
func TestJobReminder_NotRewindSelectable_AfterRestart(t *testing.T) {
	store, session, _ := runJobReminderLiveFlow(t)

	reloaded := loadDecodedStore(t, store, session.SessionID)
	jobIdx := findReminderEnvelopeMessages(reloaded)
	if len(jobIdx) != 1 {
		t.Fatalf("fixture holds %d reminder rows, want 1", len(jobIdx))
	}
	if utils.IsSelectableUserMessage(reloaded[jobIdx[0]]) {
		t.Error("reloaded job reminder is rewind-selectable, want false (C1 guard)")
	}
	if got := utils.LastSelectableUserMessageIndex(reloaded); got == jobIdx[0] {
		t.Errorf("LastSelectableUserMessageIndex picked the job reminder row (%d), want a real user message", got)
	}
}
