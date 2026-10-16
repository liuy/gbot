package wui

// Mutation-kill tests for batch A1 (connector.go lines 1-1700). Each test
// targets specific escaped mutants; assertions are exact-value so a single
// mutated line flips them. Grouped by the production function under test.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/filehistory"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/types"
)

// ---- shared helpers ----

// a1LogSink records slog messages. Several mutants under test (partial
// restore warn, autoRewind failure warn, per-event debug trace) have no
// observable effect besides the log line, so the tests must read the log.
type a1LogSink struct {
	mu   sync.Mutex
	msgs []string
}

func (s *a1LogSink) Enabled(context.Context, slog.Level) bool { return true }

func (s *a1LogSink) Handle(_ context.Context, r slog.Record) error {
	s.mu.Lock()
	s.msgs = append(s.msgs, r.Message)
	s.mu.Unlock()
	return nil
}

func (s *a1LogSink) WithAttrs([]slog.Attr) slog.Handler { return s }
func (s *a1LogSink) WithGroup(string) slog.Handler      { return s }

func (s *a1LogSink) count(msg string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, m := range s.msgs {
		if m == msg {
			n++
		}
	}
	return n
}

// a1SwapDefaultLogger swaps slog's default logger for the test's duration.
// Safe against the package's t.Parallel tests: sequential tests never
// interleave with parallel ones in Go's test runner.
func a1SwapDefaultLogger(t *testing.T) *a1LogSink {
	t.Helper()
	sink := &a1LogSink{}
	old := slog.Default()
	slog.SetDefault(slog.New(sink))
	t.Cleanup(func() { slog.SetDefault(old) })
	return sink
}

// a1BlockProvider parks the engine's query inside provider.Stream until ctx
// is canceled, giving engineAdapter tests a deterministic busy window that a
// plain provider-less engine cannot provide (it errors out instantly).
type a1BlockProvider struct {
	onStream func()
}

func (p *a1BlockProvider) Name() string { return "a1block" }

func (p *a1BlockProvider) Complete(context.Context, *llm.Request) (*llm.Response, error) {
	return nil, errors.New("a1: Complete unsupported")
}

func (p *a1BlockProvider) Stream(ctx context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	p.onStream()
	ch := make(chan llm.StreamEvent)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

func a1ReadRaw(t *testing.T, ws *websocket.Conn) (int, []byte) {
	t.Helper()
	if err := ws.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil { // REAL-TIME
		t.Fatalf("set read deadline: %v", err)
	}
	mt, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read ws frame: %v", err)
	}
	return mt, data
}

func a1UserMsg(text string) types.Message {
	return types.Message{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(text)}}
}

func a1AssistantMsg(text string) types.Message {
	return types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock(text)}}
}

// ---- resetQueryStats ----

func TestMutA1_ResetQueryStatsZeroesAllFields(t *testing.T) {
	qs := &queryStats{}
	qs.inputTokens.Store(7)
	qs.outputTokens.Store(8)
	qs.cacheReadInputTokens.Store(9)
	qs.cacheCreationInputTokens.Store(10)
	qs.toolCount.Store(11)
	qs.thinkingMs.Store(12)
	qs.startMs.Store(13)
	resetQueryStats(qs)
	want := int64(0)
	for name, got := range map[string]int64{
		"inputTokens":              qs.inputTokens.Load(),
		"outputTokens":             qs.outputTokens.Load(),
		"cacheReadInputTokens":     qs.cacheReadInputTokens.Load(),
		"cacheCreationInputTokens": qs.cacheCreationInputTokens.Load(),
		"toolCount":                qs.toolCount.Load(),
		"thinkingMs":               qs.thinkingMs.Load(),
		"startMs":                  qs.startMs.Load(),
	} {
		if got != want {
			t.Errorf("resetQueryStats: %s = %d, want %d", name, got, want)
		}
	}
}

// ---- accumulateStats ----

func TestMutA1_AccumulateStatsUsage(t *testing.T) {
	qs := &queryStats{}
	accumulateStats(qs, types.QueryEvent{Type: types.EventUsage, Usage: &types.UsageEvent{
		InputTokens: 10, OutputTokens: 20, CacheReadInputTokens: 30, CacheCreationInputTokens: 40,
	}})
	if got := qs.inputTokens.Load(); got != 10 {
		t.Errorf("inputTokens = %d, want 10", got)
	}
	if got := qs.outputTokens.Load(); got != 20 {
		t.Errorf("outputTokens = %d, want 20", got)
	}
	if got := qs.cacheReadInputTokens.Load(); got != 30 {
		t.Errorf("cacheReadInputTokens = %d, want 30", got)
	}
	if got := qs.cacheCreationInputTokens.Load(); got != 40 {
		t.Errorf("cacheCreationInputTokens = %d, want 40", got)
	}
	if got := qs.toolCount.Load(); got != 0 {
		t.Errorf("toolCount = %d, want 0", got)
	}

	// Usage event with nil Usage must neither panic nor accumulate.
	qsNil := &queryStats{}
	accumulateStats(qsNil, types.QueryEvent{Type: types.EventUsage})
	if got := qsNil.inputTokens.Load(); got != 0 {
		t.Errorf("usage-with-nil: inputTokens = %d, want 0", got)
	}

	// Non-usage event carrying Usage must not accumulate.
	qsOther := &queryStats{}
	accumulateStats(qsOther, types.QueryEvent{Type: types.EventTextDelta, Text: "x", Usage: &types.UsageEvent{InputTokens: 1}})
	if got := qsOther.inputTokens.Load(); got != 0 {
		t.Errorf("text_delta+usage: inputTokens = %d, want 0", got)
	}
}

func TestMutA1_AccumulateStatsQueryStart(t *testing.T) {
	// In a synctest bubble the clock is frozen while the goroutine runs, so
	// the stored startMs is comparable for equality against a captured
	// reading instead of a wall-clock bound.
	inBubble(t, func(t *testing.T) {
		// Sub-agent query_start must not set startMs.
		qsAgent := &queryStats{}
		accumulateStats(qsAgent, types.QueryEvent{Type: types.EventQueryStart, Agent: &types.AgentMeta{ParentToolUseID: "p"}})
		if got, want := qsAgent.startMs.Load(), int64(0); got != want {
			t.Errorf("agent query_start: startMs = %d, want %d", got, want)
		}

		before := time.Now().UnixMilli()
		qsTop := &queryStats{}
		accumulateStats(qsTop, types.QueryEvent{Type: types.EventQueryStart})
		if got := qsTop.startMs.Load(); got != before {
			t.Errorf("top-level query_start: startMs = %d, want the captured reading %d", got, before)
		}

		// Non-querystart top-level event must not set startMs.
		qsOther := &queryStats{}
		accumulateStats(qsOther, types.QueryEvent{Type: types.EventTextDelta, Text: "x"})
		if got, want := qsOther.startMs.Load(), int64(0); got != want {
			t.Errorf("text_delta: startMs = %d, want %d", got, want)
		}
	})
}

func TestMutA1_AccumulateStatsToolStart(t *testing.T) {
	// ToolStart with nil ToolUse must neither panic nor count.
	qsNil := &queryStats{}
	accumulateStats(qsNil, types.QueryEvent{Type: types.EventToolStart})
	if got := qsNil.toolCount.Load(); got != 0 {
		t.Errorf("tool_start nil ToolUse: toolCount = %d, want 0", got)
	}

	qs := &queryStats{}
	accumulateStats(qs, types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t"}})
	if got := qs.toolCount.Load(); got != 1 {
		t.Errorf("tool_start: toolCount = %d, want 1", got)
	}

	// Non-toolstart event carrying ToolUse must not count.
	qsOther := &queryStats{}
	accumulateStats(qsOther, types.QueryEvent{Type: types.EventQueryStart, ToolUse: &types.ToolUseEvent{ID: "t"}})
	if got := qsOther.toolCount.Load(); got != 0 {
		t.Errorf("query_start+ToolUse: toolCount = %d, want 0", got)
	}
}

func TestMutA1_AccumulateStatsThinkingEnd(t *testing.T) {
	// ThinkingEnd with nil Thinking must neither panic nor accumulate.
	qsNil := &queryStats{}
	accumulateStats(qsNil, types.QueryEvent{Type: types.EventThinkingEnd})
	if got := qsNil.thinkingMs.Load(); got != 0 {
		t.Errorf("thinking_end nil Thinking: thinkingMs = %d, want 0", got)
	}

	qs := &queryStats{}
	accumulateStats(qs, types.QueryEvent{Type: types.EventThinkingEnd, Thinking: &types.ThinkingEvent{Duration: 1500 * time.Millisecond}})
	if got := qs.thinkingMs.Load(); got != 1500 {
		t.Errorf("thinking_end: thinkingMs = %d, want 1500", got)
	}

	// Non-thinkingend event carrying Thinking must not accumulate.
	qsOther := &queryStats{}
	accumulateStats(qsOther, types.QueryEvent{Type: types.EventThinkingStart, Thinking: &types.ThinkingEvent{Duration: time.Second}})
	if got := qsOther.thinkingMs.Load(); got != 0 {
		t.Errorf("thinking_start+Thinking: thinkingMs = %d, want 0", got)
	}
}

// ---- findBlock / targetList ----

func TestMutA1_FindBlock(t *testing.T) {
	blocks := []streamBlock{
		{Kind: "text", ID: "txt", Text: "hello"},
		{Kind: "tool", ID: "outer", Name: "Bash", Children: []streamBlock{
			{Kind: "tool", ID: "nested", Name: "Read"},
		}},
	}
	// Only tool blocks are addressable: a matching ID on a text block is nil.
	if b := findBlock(blocks, "txt"); b != nil {
		t.Errorf("findBlock(text block) = %+v, want nil", b)
	}
	if b := findBlock(blocks, "outer"); b == nil || b.ID != "outer" {
		t.Errorf("findBlock(outer) = %+v, want outer tool block", b)
	}
	// Depth-first descent must reach nested children.
	if b := findBlock(blocks, "nested"); b == nil || b.ID != "nested" {
		t.Errorf("findBlock(nested) = %+v, want nested tool block", b)
	}
	if b := findBlock(blocks, "absent"); b != nil {
		t.Errorf("findBlock(absent) = %+v, want nil", b)
	}
	// Empty-children tool block still finds itself.
	solo := []streamBlock{{Kind: "tool", ID: "solo", Children: []streamBlock{}}}
	if b := findBlock(solo, "solo"); b == nil || b.ID != "solo" {
		t.Errorf("findBlock(solo) = %+v, want solo tool block", b)
	}
}

func TestMutA1_TargetList(t *testing.T) {
	ss := &streamState{blocks: []streamBlock{{Kind: "tool", ID: "p1", Name: "Agent"}}}
	if got := targetList(ss, types.QueryEvent{Type: types.EventTextDelta}); got != &ss.blocks {
		t.Errorf("targetList(top-level) = %p, want root blocks %p", got, &ss.blocks)
	}
	// Sub-agent event without a parent tool ID belongs to the root list.
	agentNoParent := types.QueryEvent{Type: types.EventTextDelta, Agent: &types.AgentMeta{AgentType: "Explore"}}
	if got := targetList(ss, agentNoParent); got != &ss.blocks {
		t.Errorf("targetList(agent no parent) = %p, want root blocks %p", got, &ss.blocks)
	}
	parent := findBlock(ss.blocks, "p1")
	agentWithParent := types.QueryEvent{Type: types.EventTextDelta, Agent: &types.AgentMeta{ParentToolUseID: "p1"}}
	if got := targetList(ss, agentWithParent); got != &parent.Children {
		t.Errorf("targetList(agent with parent) = %p, want parent children %p", got, &parent.Children)
	}
	agentOrphan := types.QueryEvent{Type: types.EventTextDelta, Agent: &types.AgentMeta{ParentToolUseID: "zz"}}
	if got := targetList(ss, agentOrphan); got != nil {
		t.Errorf("targetList(agent orphan parent) = %p, want nil (event is dropped)", got)
	}
}

// ---- updateStreamState ----

func TestMutA1_UpdateStreamStateToolStartFields(t *testing.T) {
	// Bubble-frozen clock: StartedAt must equal the captured reading exactly.
	inBubble(t, func(t *testing.T) {
		before := time.Now().UnixMilli()
		ss := &streamState{}
		updateStreamState(ss, types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{
			ID: "tu1", Name: "Bash", Summary: "bash summary",
			IsSearch: true, IsRead: true, IsList: true, IsLsp: true, IsTask: true,
		}})
		if len(ss.blocks) != 1 {
			t.Fatalf("blocks = %d, want 1", len(ss.blocks))
		}
		b := ss.blocks[0]
		if b.Kind != "tool" || b.ID != "tu1" || b.Name != "Bash" {
			t.Errorf("identity fields = %+v", b)
		}
		if b.Summary != "bash summary" {
			t.Errorf("Summary = %q, want %q", b.Summary, "bash summary")
		}
		if !b.IsSearch || !b.IsRead || !b.IsList || !b.IsLsp || !b.IsTask {
			t.Errorf("classification flags = search:%v read:%v list:%v lsp:%v task:%v, want all true",
				b.IsSearch, b.IsRead, b.IsList, b.IsLsp, b.IsTask)
		}
		if b.IsWeb {
			t.Errorf("IsWeb = true for Bash tool, want false")
		}
		if b.State != "running" {
			t.Errorf("State = %q, want running", b.State)
		}
		if b.StartedAt != before {
			t.Errorf("StartedAt = %d, want the captured reading %d", b.StartedAt, before)
		}
		// Children must be an empty non-nil slice: the wire shape renders
		// `children: []`, and field-clearing it to nil emits `null`.
		if b.Children == nil || len(b.Children) != 0 {
			t.Errorf("Children = %#v, want empty non-nil slice", b.Children)
		}
	})
}

func TestMutA1_UpdateStreamStateTurnStart(t *testing.T) {
	// Top-level turn_start must not panic on the nil-Agent rename guard.
	ss := &streamState{}
	updateStreamState(ss, types.QueryEvent{Type: types.EventTurnStart})
	if !ss.queryActive {
		t.Errorf("queryActive = false after top-level turn_start, want true")
	}

	// fork agents keep the plain parent name.
	ssFork := &streamState{blocks: []streamBlock{{Kind: "tool", ID: "p1", Name: "Agent"}}}
	updateStreamState(ssFork, types.QueryEvent{Type: types.EventTurnStart, Agent: &types.AgentMeta{ParentToolUseID: "p1", AgentType: "fork"}})
	if got := ssFork.blocks[0].Name; got != "Agent" {
		t.Errorf("fork turn_start: parent Name = %q, want %q", got, "Agent")
	}

	// Real agent types annotate the parent tool name.
	ssPlan := &streamState{blocks: []streamBlock{{Kind: "tool", ID: "p1", Name: "Agent"}}}
	updateStreamState(ssPlan, types.QueryEvent{Type: types.EventTurnStart, Agent: &types.AgentMeta{ParentToolUseID: "p1", AgentType: "Planner"}})
	if got := ssPlan.blocks[0].Name; got != "Agent Planner" {
		t.Errorf("planner turn_start: parent Name = %q, want %q", got, "Agent Planner")
	}
}

func TestMutA1_UpdateStreamStateToolParamDelta(t *testing.T) {
	ss := &streamState{}
	updateStreamState(ss, types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "tu1", Name: "Bash"}})
	updateStreamState(ss, types.QueryEvent{Type: types.EventToolParamDelta, PartialInput: &types.PartialInputEvent{
		ID: "tu1", Summary: "s1", IsSearch: true, IsRead: true, IsList: true, IsLsp: true, IsTask: true,
	}})
	b := findBlock(ss.blocks, "tu1")
	if b == nil {
		t.Fatal("tool block disappeared")
	}
	if b.Summary != "s1" {
		t.Errorf("Summary = %q, want s1", b.Summary)
	}
	if !b.IsSearch || !b.IsRead || !b.IsList || !b.IsLsp || !b.IsTask {
		t.Errorf("flags = search:%v read:%v list:%v lsp:%v task:%v, want all true",
			b.IsSearch, b.IsRead, b.IsList, b.IsLsp, b.IsTask)
	}
	// Param delta for an unknown block is dropped, not panicked on.
	updateStreamState(ss, types.QueryEvent{Type: types.EventToolParamDelta, PartialInput: &types.PartialInputEvent{ID: "absent"}})
	if len(ss.blocks) != 1 {
		t.Errorf("blocks = %d, want 1", len(ss.blocks))
	}
}

func TestMutA1_UpdateStreamStateAttachmentEcho(t *testing.T) {
	mkSS := func() *streamState {
		ss := &streamState{}
		updateStreamState(ss, types.QueryEvent{Type: types.EventTurnStart})
		return ss
	}
	mkMsg := func(att *types.Attachment, texts ...string) *types.Message {
		content := make([]types.ContentBlock, 0, len(texts))
		for _, s := range texts {
			content = append(content, types.NewTextBlock(s))
		}
		return &types.Message{Role: types.RoleUser, Content: content, Attachment: att, Timestamp: time.Unix(5000, 0)}
	}

	// Prompt-mode attachment echoes the prompt text.
	ss := mkSS()
	updateStreamState(ss, types.QueryEvent{Type: types.EventAttachment, Message: mkMsg(&types.Attachment{Mode: types.ItemModePrompt, Prompt: "PROMPT"}, "ignored")})
	if len(ss.blocks) != 1 || ss.blocks[0].Kind != "user" || ss.blocks[0].Text != "PROMPT" {
		t.Fatalf("prompt echo block = %+v, want user block with text PROMPT", ss.blocks)
	}
	if got := ss.blocks[0].StartedAt; got != 5000000 {
		t.Errorf("StartedAt = %d, want 5000000", got)
	}

	// Queue-mode attachments echo content, never the prompt.
	ssQ := mkSS()
	updateStreamState(ssQ, types.QueryEvent{Type: types.EventAttachment, Message: mkMsg(&types.Attachment{Mode: types.ItemModeMessage, Prompt: "PROMPT"}, "CONTENT")})
	if len(ssQ.blocks) != 1 || ssQ.blocks[0].Text != "CONTENT" {
		t.Fatalf("queue echo block = %+v, want user block with text CONTENT", ssQ.blocks)
	}

	// Prompt-mode with empty prompt falls back to content blocks.
	ssF := mkSS()
	updateStreamState(ssF, types.QueryEvent{Type: types.EventAttachment, Message: mkMsg(&types.Attachment{Mode: types.ItemModePrompt, Prompt: ""}, "FALLBACK")})
	if len(ssF.blocks) != 1 || ssF.blocks[0].Text != "FALLBACK" {
		t.Fatalf("fallback echo block = %+v, want user block with text FALLBACK", ssF.blocks)
	}

	// No attachment: multiple text blocks concatenate in order.
	ssC := mkSS()
	updateStreamState(ssC, types.QueryEvent{Type: types.EventAttachment, Message: mkMsg(nil, "A", "B")})
	if len(ssC.blocks) != 1 || ssC.blocks[0].Text != "AB" {
		t.Fatalf("concat echo block = %+v, want user block with text AB", ssC.blocks)
	}
}

func TestMutA1_UpdateStreamStateToolEnd(t *testing.T) {
	ss := &streamState{}
	updateStreamState(ss, types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "tu1", Name: "Bash"}})
	// Sub-agent activity to verify the completion clear.
	ss.blocks[0].Children = []streamBlock{{Kind: "text", Text: "child work"}}
	updateStreamState(ss, types.QueryEvent{Type: types.EventToolEnd, ToolResult: &types.ToolResultEvent{
		ToolUseID: "tu1", DisplayOutput: "done-out",
		IsSearch: true, IsRead: true, IsList: true, IsLsp: true, IsTask: true,
		Duration: 5 * time.Millisecond,
	}})
	b := findBlock(ss.blocks, "tu1")
	if b == nil {
		t.Fatal("tool block disappeared")
	}
	if b.State != "done" {
		t.Errorf("State = %q, want done", b.State)
	}
	if b.DisplayOutput != "done-out" {
		t.Errorf("DisplayOutput = %q, want done-out", b.DisplayOutput)
	}
	if !b.IsSearch || !b.IsRead || !b.IsList || !b.IsLsp || !b.IsTask {
		t.Errorf("flags = search:%v read:%v list:%v lsp:%v task:%v, want all true",
			b.IsSearch, b.IsRead, b.IsList, b.IsLsp, b.IsTask)
	}
	if b.TimingNs != 5000000 {
		t.Errorf("TimingNs = %d, want 5000000", b.TimingNs)
	}
	if b.Children == nil || len(b.Children) != 0 {
		t.Errorf("Children = %#v, want cleared empty non-nil slice", b.Children)
	}

	// Error results mark the block errored.
	ssErr := &streamState{}
	updateStreamState(ssErr, types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "tu2", Name: "Bash"}})
	updateStreamState(ssErr, types.QueryEvent{Type: types.EventToolEnd, ToolResult: &types.ToolResultEvent{ToolUseID: "tu2", IsError: true}})
	if got := findBlock(ssErr.blocks, "tu2").State; got != "error" {
		t.Errorf("error result State = %q, want error", got)
	}

	// Unknown tool ID and nil ToolResult are dropped without panic.
	updateStreamState(ss, types.QueryEvent{Type: types.EventToolEnd, ToolResult: &types.ToolResultEvent{ToolUseID: "absent"}})
	updateStreamState(ss, types.QueryEvent{Type: types.EventToolEnd})
}

func TestMutA1_UpdateStreamStateToolOutputDelta(t *testing.T) {
	ss := &streamState{}
	updateStreamState(ss, types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "tu1", Name: "Bash"}})
	updateStreamState(ss, types.QueryEvent{Type: types.EventToolOutputDelta, ToolResult: &types.ToolResultEvent{ToolUseID: "tu1", DisplayOutput: "partial"}})
	if got := findBlock(ss.blocks, "tu1").DisplayOutput; got != "partial" {
		t.Errorf("DisplayOutput = %q, want partial", got)
	}
	// Unknown ID must not panic on the nil block guard.
	updateStreamState(ss, types.QueryEvent{Type: types.EventToolOutputDelta, ToolResult: &types.ToolResultEvent{ToolUseID: "absent", DisplayOutput: "x"}})
}

func TestMutA1_UpdateStreamStateThinkingDelta(t *testing.T) {
	// Sub-agent delta whose parent tool is gone: nil target list is dropped.
	ssOrphan := &streamState{}
	updateStreamState(ssOrphan, types.QueryEvent{
		Type: types.EventThinkingDelta, Thinking: &types.ThinkingEvent{Text: "T"},
		Agent: &types.AgentMeta{ParentToolUseID: "zz"},
	})
	if len(ssOrphan.blocks) != 0 {
		t.Errorf("orphan thinking delta appended %d blocks, want 0", len(ssOrphan.blocks))
	}

	// Empty block list: the last-block probe must not index [-1].
	ssEmpty := &streamState{}
	updateStreamState(ssEmpty, types.QueryEvent{Type: types.EventThinkingDelta, Thinking: &types.ThinkingEvent{Text: "T"}})
	if len(ssEmpty.blocks) != 0 {
		t.Errorf("delta on empty state appended %d blocks, want 0", len(ssEmpty.blocks))
	}

	// Deltas accumulate onto the active thinking block.
	ss := &streamState{}
	updateStreamState(ss, types.QueryEvent{Type: types.EventThinkingStart})
	updateStreamState(ss, types.QueryEvent{Type: types.EventThinkingDelta, Thinking: &types.ThinkingEvent{Text: "abc"}})
	updateStreamState(ss, types.QueryEvent{Type: types.EventThinkingDelta, Thinking: &types.ThinkingEvent{Text: "def"}})
	if len(ss.blocks) != 1 || ss.blocks[0].Text != "abcdef" {
		t.Fatalf("thinking block = %+v, want single block with text abcdef", ss.blocks)
	}

	// Delta after a non-thinking last block is dropped entirely.
	ssTool := &streamState{}
	updateStreamState(ssTool, types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t", Name: "Bash"}})
	updateStreamState(ssTool, types.QueryEvent{Type: types.EventThinkingDelta, Thinking: &types.ThinkingEvent{Text: "xyz"}})
	if len(ssTool.blocks) != 1 {
		t.Fatalf("blocks = %d, want 1 (no new block for delta after tool)", len(ssTool.blocks))
	}
	if got := ssTool.blocks[0].Text; got != "" {
		t.Errorf("tool block Text polluted = %q, want empty", got)
	}
}

func TestMutA1_UpdateStreamStateThinkingEnd(t *testing.T) {
	// Empty list: no panic, no append.
	ssEmpty := &streamState{}
	updateStreamState(ssEmpty, types.QueryEvent{Type: types.EventThinkingEnd, Thinking: &types.ThinkingEvent{Duration: time.Second}})
	if len(ssEmpty.blocks) != 0 {
		t.Errorf("thinking end on empty state appended %d blocks, want 0", len(ssEmpty.blocks))
	}

	// Active thinking block is finalized.
	ss := &streamState{}
	updateStreamState(ss, types.QueryEvent{Type: types.EventThinkingStart})
	updateStreamState(ss, types.QueryEvent{Type: types.EventThinkingEnd, Thinking: &types.ThinkingEvent{Duration: 2500 * time.Millisecond}})
	if len(ss.blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(ss.blocks))
	}
	if ss.blocks[0].Active {
		t.Errorf("Active = true after thinking end, want false")
	}
	if got := ss.blocks[0].DurationNs; got != 2500000000 {
		t.Errorf("DurationNs = %d, want 2500000000", got)
	}

	// Thinking end after a text block leaves it untouched.
	ssText := &streamState{}
	updateStreamState(ssText, types.QueryEvent{Type: types.EventTextDelta, Text: "keep"})
	updateStreamState(ssText, types.QueryEvent{Type: types.EventThinkingEnd, Thinking: &types.ThinkingEvent{Duration: time.Second}})
	if len(ssText.blocks) != 1 || ssText.blocks[0].Kind != "text" || ssText.blocks[0].Text != "keep" {
		t.Fatalf("text block after thinking end = %+v, want untouched text block", ssText.blocks)
	}
	if got := ssText.blocks[0].DurationNs; got != 0 {
		t.Errorf("text block DurationNs = %d, want 0", got)
	}

	// Nil Thinking is dropped without panic.
	updateStreamState(ss, types.QueryEvent{Type: types.EventThinkingEnd})
}

// ---- onEngineEvent ----

func TestMutA1_OnEngineEventQueryEndReset(t *testing.T) {
	c := newTestConnector(t)
	slot := c.activeSlotTest(t)

	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "x"})
	c.Handle(types.QueryEvent{Type: types.EventUsage, Usage: &types.UsageEvent{InputTokens: 10}})
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "task1", Name: "Task"}})

	slot.ssMu.Lock()
	blocks := len(slot.streamState.blocks)
	slot.ssMu.Unlock()
	if blocks != 2 {
		t.Fatalf("setup: blocks = %d, want 2 (text + tool)", blocks)
	}
	if got := slot.queryStats.inputTokens.Load(); got != 10 {
		t.Fatalf("setup: inputTokens = %d, want 10", got)
	}
	if !slot.taskToolIDs["task1"] {
		t.Fatalf("setup: task1 not tracked")
	}

	// Sub-agent query_end must NOT reset per-engine state.
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd, Agent: &types.AgentMeta{ParentToolUseID: "p"}})
	slot.ssMu.Lock()
	blocks = len(slot.streamState.blocks)
	slot.ssMu.Unlock()
	if blocks != 2 {
		t.Errorf("sub-agent query_end reset blocks to %d, want 2 preserved", blocks)
	}
	if got := slot.queryStats.inputTokens.Load(); got != 10 {
		t.Errorf("sub-agent query_end reset inputTokens to %d, want 10", got)
	}
	if !slot.taskToolIDs["task1"] {
		t.Errorf("sub-agent query_end cleared taskToolIDs, want preserved")
	}

	// Top-level query_end resets stream state, stats, and task tracking.
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd})
	slot.ssMu.Lock()
	blocks = len(slot.streamState.blocks)
	tasks := len(slot.taskToolIDs)
	slot.ssMu.Unlock()
	if blocks != 0 {
		t.Errorf("query_end left %d blocks, want 0", blocks)
	}
	if got := slot.queryStats.inputTokens.Load(); got != 0 {
		t.Errorf("query_end left inputTokens = %d, want 0", got)
	}
	if tasks != 0 {
		t.Errorf("query_end left %d taskToolIDs, want 0", tasks)
	}
}

func TestMutA1_OnEngineEventFrameNoAbortFlag(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()
	// A rewound-able message so a wrongly-triggered auto-rewind would leave
	// observable traces (rewind call + rewound flag) on the frame.
	mock.SetMessagesFn(func() []types.Message { return []types.Message{a1UserMsg("q")} })
	ws := dialAndStore(t, c)

	// A non-query_end event carrying an AbortError must not mark the frame
	// aborted nor trigger the auto-rewind path.
	c.Handle(types.QueryEvent{
		Type:  types.EventTextDelta,
		Text:  "x",
		Error: &engine.AbortError{Phase: "streaming", Err: context.Canceled},
	})

	data := readWSMessage(t, ws)
	var frame struct {
		Type  string         `json:"type"`
		Event map[string]any `json:"event"`
	}
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	if frame.Type != "event" {
		t.Fatalf("frame type = %q, want event", frame.Type)
	}
	if _, ok := frame.Event["aborted"]; ok {
		t.Errorf("frame carries aborted on plain event: %v", frame.Event["aborted"])
	}
	if _, ok := frame.Event["rewound"]; ok {
		t.Errorf("frame carries rewound on plain event: %v", frame.Event["rewound"])
	}
	if got := frame.Event["text"]; got != "x" {
		t.Errorf("frame event text = %v, want x", got)
	}
	mock.mu.RLock()
	rewinds := len(mock.rewindCalls)
	mock.mu.RUnlock()
	if rewinds != 0 {
		t.Errorf("rewindCalls = %d, want 0 — only query_end aborts may rewind", rewinds)
	}
}

func TestMutA1_OnEngineEventTaskTrackingAndDebugTrace(t *testing.T) {
	sink := a1SwapDefaultLogger(t)
	c := newTestConnector(t)
	slot := c.activeSlotTest(t)

	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "task1", Name: "Task"}})
	if !slot.taskToolIDs["task1"] {
		t.Errorf("Task tool_start not tracked")
	}

	// ToolStart with nil ToolUse must not panic in the tracker.
	c.Handle(types.QueryEvent{Type: types.EventToolStart})

	// A non-toolstart event carrying ToolUse must not be recorded as Task.
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "y", ToolUse: &types.ToolUseEvent{ID: "task2", Name: "Task"}})
	// A non-Task tool start must not be tracked either.
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "bash1", Name: "Bash"}})
	if len(slot.taskToolIDs) != 1 {
		t.Errorf("taskToolIDs = %v, want only task1", slot.taskToolIDs)
	}

	if sink.count("wui:event") == 0 {
		t.Errorf("per-event debug trace 'wui:event' missing from log")
	}
}

// ---- SendFile ----

func TestMutA1_SendFileChunkedFrames(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)

	dir := t.TempDir()
	name := "blob.png"
	path := filepath.Join(dir, name)
	payload := bytes.Repeat([]byte{0xAB}, 300000) // 256 KiB + 37856 remainder
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.SendFile(context.Background(), path, ""); err != nil {
		t.Fatalf("SendFile: %v", err)
	}

	var start struct {
		Type string `json:"type"`
		Name string `json:"name"`
		Mime string `json:"mime"`
		Size int64  `json:"size"`
	}
	sData := readWSMessage(t, ws)
	if err := json.Unmarshal(sData, &start); err != nil {
		t.Fatalf("unmarshal file_start: %v", err)
	}
	if start.Type != "file_start" || start.Name != name || start.Size != 300000 {
		t.Errorf("file_start = %+v", start)
	}
	if start.Mime != media.MimeFromExt(".png") {
		t.Errorf("file_start mime = %q, want %q", start.Mime, media.MimeFromExt(".png"))
	}

	mt1, chunk1 := a1ReadRaw(t, ws)
	mt2, chunk2 := a1ReadRaw(t, ws)
	if mt1 != websocket.BinaryMessage || mt2 != websocket.BinaryMessage {
		t.Errorf("chunk opcodes = %d,%d, want binary", mt1, mt2)
	}
	// Exact 256 KiB first chunk pins fileChunkSize's both constants.
	if len(chunk1) != 262144 {
		t.Errorf("first chunk = %d bytes, want 262144", len(chunk1))
	}
	if len(chunk2) != 37856 {
		t.Errorf("second chunk = %d bytes, want 37856", len(chunk2))
	}
	if !bytes.Equal(append(chunk1, chunk2...), payload) {
		t.Errorf("chunk payload does not reconstruct the file")
	}

	var end struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	eData := readWSMessage(t, ws)
	if err := json.Unmarshal(eData, &end); err != nil {
		t.Fatalf("unmarshal file_end: %v", err)
	}
	if end.Type != "file_end" || end.Name != name {
		t.Errorf("file_end = %+v", end)
	}
}

func TestMutA1_SendFileStatError(t *testing.T) {
	c := newTestConnector(t)
	path := filepath.Join(t.TempDir(), "gone.bin")
	err := c.SendFile(context.Background(), path, "")
	if err == nil {
		t.Fatal("SendFile on missing file: err = nil, want stat error")
	}
	want := fmt.Sprintf("wui: send file: stat %s: no such file or directory", path)
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("errors.Is(fs.ErrNotExist) = false, err = %v", err)
	}
}

func TestMutA1_SendFileOpenError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod-based open failure is meaningless when running as root")
	}
	c := newTestConnector(t)
	path := filepath.Join(t.TempDir(), "noperm.bin")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	err := c.SendFile(context.Background(), path, "")
	if err == nil {
		t.Fatal("SendFile on unreadable file: err = nil, want open error")
	}
	want := fmt.Sprintf("wui: read file: open %s: permission denied", path)
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("errors.Is(fs.ErrPermission) = false, err = %v", err)
	}
}

func TestMutA1_SendFileReadError(t *testing.T) {
	c := newTestConnector(t)
	dir := t.TempDir()
	err := c.SendFile(context.Background(), dir, "")
	if err == nil {
		t.Fatal("SendFile on a directory: err = nil, want read error")
	}
	want := fmt.Sprintf("wui: read file: read %s: is a directory", dir)
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, syscall.EISDIR) {
		t.Errorf("errors.Is(syscall.EISDIR) = false, err = %v", err)
	}
}

// ---- shouldAutoRewindFor / autoRewindOnAbortFor ----

func TestMutA1_ShouldAutoRewindFor(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()

	cases := []struct {
		name    string
		pending []types.QueuedItem
		msgs    []types.Message
		want    bool
	}{
		{"queued non-meta prompt blocks rewind", []types.QueuedItem{{Mode: types.ItemModePrompt}}, []types.Message{a1UserMsg("q")}, false},
		{"queued message-mode item does not block", []types.QueuedItem{{Mode: types.ItemModeMessage}}, nil, true},
		{"meta prompt does not block", []types.QueuedItem{{Mode: types.ItemModePrompt, IsMeta: true}}, nil, true},
		{"no pending, no messages", nil, nil, true},
		// No selectable user message exists (assistant-only transcript):
		// the lastUserIdx < 0 branch itself must answer true, not fall
		// through to the synthetic scan over the whole transcript.
		{"assistant-only transcript", nil, []types.Message{a1AssistantMsg("hi")}, true},
		{"assistant answer after prompt", nil, []types.Message{a1UserMsg("q"), a1AssistantMsg("a")}, false},
		{"bare prompt", nil, []types.Message{a1UserMsg("q")}, true},
	}
	for _, tc := range cases {
		pending := tc.pending
		mock.pendingAttachmentsFn = func() []types.QueuedItem { return pending }
		msgs := tc.msgs
		mock.SetMessagesFn(func() []types.Message { return msgs })
		if got := c.shouldAutoRewindFor(mock); got != tc.want {
			t.Errorf("%s: shouldAutoRewindFor = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestMutA1_AutoRewindOnAbortFor(t *testing.T) {
	sink := a1SwapDefaultLogger(t)
	c := newTestConnector(t)
	mock := c.mock()
	mock.SetMessagesFn(func() []types.Message { return []types.Message{a1UserMsg("q")} })

	// Rewind failure is reported as (false, "") with a warn trace.
	mock.rewindToFn = func(int) error { return errors.New("boom") }
	if rewound, text := c.autoRewindOnAbortFor(mock); rewound || text != "" {
		t.Errorf("failed rewind = (%v, %q), want (false, \"\")", rewound, text)
	}
	if got := sink.count("wui: autoRewind failed"); got != 1 {
		t.Errorf("warn count = %d, want 1", got)
	}

	// Success reports the rewound prompt text.
	mock.rewindToFn = nil
	rewound, text := c.autoRewindOnAbortFor(mock)
	if !rewound || text != "q" {
		t.Errorf("successful rewind = (%v, %q), want (true, \"q\")", rewound, text)
	}
	mock.mu.RLock()
	calls := append([]int(nil), mock.rewindCalls...)
	mock.mu.RUnlock()
	if len(calls) != 2 || calls[0] != 0 || calls[1] != 0 {
		t.Errorf("rewindCalls = %v, want [0 0]", calls)
	}
	if got := sink.count("wui: autoRewind failed"); got != 1 {
		t.Errorf("warn count after success = %d, want still 1", got)
	}
}

// ---- input history ----

func TestMutA1_InputHistoryPath(t *testing.T) {
	// Connector with no bound engine: empty path, no nil-interface panic.
	bare := &WUIConnector{}
	if got := bare.inputHistoryPath(); got != "" {
		t.Errorf("bare connector inputHistoryPath = %q, want empty", got)
	}

	c := newTestConnector(t)
	mock := c.mock()
	mock.projectDirFn = func() string { return "" }
	mock.engineIDFn = func() string { return "e1" }
	if got := c.inputHistoryPathFor(mock); got != "" {
		t.Errorf("empty projectDir: path = %q, want empty", got)
	}
	mock.projectDirFn = func() string { return "/proj" }
	mock.engineIDFn = func() string { return "" }
	if got := c.inputHistoryPathFor(mock); got != "" {
		t.Errorf("empty engineID: path = %q, want empty", got)
	}
	mock.engineIDFn = func() string { return "e1" }
	want := filepath.Join("/proj", "history", "e1.jsonl")
	if got := c.inputHistoryPathFor(mock); got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestMutA1_LoadInputHistoryFromFile(t *testing.T) {
	if got := loadInputHistoryFromFile(""); got != nil {
		t.Errorf("empty path = %v, want nil", got)
	}
	if got := loadInputHistoryFromFile(filepath.Join(t.TempDir(), "missing.jsonl")); got != nil {
		t.Errorf("missing file = %v, want nil", got)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "h.jsonl")
	// The duplicate-key line partially decodes ("kept" lands in Display
	// before the type error) yet must be skipped as malformed.
	lines := []string{
		`{"display":"a","timestamp":1}`,
		`{"display":"kept","display":123}`,
		`{"display":""}`,
		`{"display":"b","timestamp":2}`,
		`{"display":"b","timestamp":3}`,
		`{"display":"c","timestamp":4}`,
		`not-json`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadInputHistoryFromFile(path)
	if want := []string{"a", "b", "c"}; !slices.Equal(got, want) {
		t.Errorf("load = %v, want %v", got, want)
	}

	// Cap keeps the newest 1000 entries.
	var capLines []string
	for i := 1; i <= 1003; i++ {
		capLines = append(capLines, fmt.Sprintf(`{"display":"line%04d"}`, i))
	}
	capPath := filepath.Join(dir, "cap.jsonl")
	if err := os.WriteFile(capPath, []byte(strings.Join(capLines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	capped := loadInputHistoryFromFile(capPath)
	if len(capped) != 1000 {
		t.Fatalf("capped len = %d, want 1000", len(capped))
	}
	if capped[0] != "line0004" || capped[999] != "line1003" {
		t.Errorf("cap window = [%s, %s], want [line0004, line1003]", capped[0], capped[999])
	}
}

func TestMutA1_AppendInputHistory(t *testing.T) {
	inBubble(t, func(t *testing.T) {
		c := newTestConnector(t)
		defer c.Stop()
		mock := c.mock()
		before := time.Now().UnixMilli()
		c.appendInputHistory("cmd one")

		path := filepath.Join(mock.projectDir, "history", "main.jsonl")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("history file missing: %v", err)
		}
		if n := strings.Count(string(raw), "\n"); n != 1 {
			t.Errorf("file has %d lines, want 1", n)
		}
		var entry inputHistoryEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatalf("unmarshal history line: %v", err)
		}
		if entry.Display != "cmd one" {
			t.Errorf("display = %q, want %q", entry.Display, "cmd one")
		}
		// The bubble clock never advances mid-goroutine, so the stored
		// timestamp is exactly the captured reading.
		if entry.Timestamp != before {
			t.Errorf("timestamp = %d, want the captured reading %d", entry.Timestamp, before)
		}

		// Directory and file permissions are part of the on-disk contract.
		dirInfo, err := os.Stat(filepath.Join(mock.projectDir, "history"))
		if err != nil {
			t.Fatal(err)
		}
		if got := dirInfo.Mode().Perm(); got != 0o700 {
			t.Errorf("history dir mode = %o, want 700", got)
		}
		fileInfo, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fileInfo.Mode().Perm(); got != 0o600 {
			t.Errorf("history file mode = %o, want 600", got)
		}

		// Round trip through the loader.
		c.appendInputHistory("cmd two")
		if got := c.loadInputHistory(); !slices.Equal(got, []string{"cmd one", "cmd two"}) {
			t.Errorf("loadInputHistory = %v, want [cmd one cmd two]", got)
		}
	})
}

// ---- handleAsk ----

func TestMutA1_HandleAskOutboundFrame(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)

	c.handleAsk(types.QueryEvent{Type: types.EventAsk, Ask: &types.AskEvent{
		Kind:       types.AskPermission,
		ToolName:   "Bash",
		Input:      json.RawMessage(`{"command":"ls"}`),
		Message:    "allow?",
		RuleDetail: "Bash(ls)",
		Prompt:     "pr",
		Masked:     true,
		AgentType:  "Critic",
	}})
	var frame struct {
		Type         string          `json:"type"`
		ID           string          `json:"id"`
		Kind         string          `json:"kind"`
		ToolName     string          `json:"tool_name"`
		Input        json.RawMessage `json:"input"`
		Message      string          `json:"message"`
		RuleDetail   string          `json:"rule_detail"`
		Prompt       string          `json:"prompt"`
		Masked       bool            `json:"masked"`
		AgentType    string          `json:"agent_type"`
		DeadlineUnix int64           `json:"deadline_unix"`
	}
	if err := json.Unmarshal(readWSMessage(t, ws), &frame); err != nil {
		t.Fatalf("unmarshal ask frame: %v", err)
	}
	if frame.Type != "ask" || frame.ID != "1" || frame.Kind != "permission" {
		t.Errorf("ask header = %+v", frame)
	}
	if frame.ToolName != "Bash" || string(frame.Input) != `{"command":"ls"}` || frame.Message != "allow?" {
		t.Errorf("ask body = %+v", frame)
	}
	if frame.RuleDetail != "Bash(ls)" || frame.Prompt != "pr" || !frame.Masked || frame.AgentType != "Critic" {
		t.Errorf("ask extras = rule:%q prompt:%q masked:%v agent:%q",
			frame.RuleDetail, frame.Prompt, frame.Masked, frame.AgentType)
	}
	if frame.DeadlineUnix != 0 {
		t.Errorf("permission ask carries deadline_unix = %d, want 0", frame.DeadlineUnix)
	}
	c.pendingMu.Lock()
	stored := c.pendingAsks["1"] != nil
	c.pendingMu.Unlock()
	if !stored {
		t.Errorf("ask id 1 not stored in pendingAsks")
	}

	// Input asks count up from the shared counter and carry the deadline.
	deadline := time.Unix(1760000000, 0)
	c.handleAsk(types.QueryEvent{Type: types.EventAsk, Ask: &types.AskEvent{
		Kind:     types.AskInput,
		Prompt:   "pw",
		Deadline: deadline,
	}})
	var frame2 struct {
		ID           string `json:"id"`
		Kind         string `json:"kind"`
		DeadlineUnix int64  `json:"deadline_unix"`
	}
	if err := json.Unmarshal(readWSMessage(t, ws), &frame2); err != nil {
		t.Fatalf("unmarshal ask frame 2: %v", err)
	}
	if frame2.ID != "2" || frame2.Kind != "input" {
		t.Errorf("second ask = %+v, want id 2 kind input", frame2)
	}
	if frame2.DeadlineUnix != 1760000000 {
		t.Errorf("deadline_unix = %d, want 1760000000", frame2.DeadlineUnix)
	}
}

// ---- engineAdapter ----

func TestMutA1_EngineAdapterRewindToErrorPropagates(t *testing.T) {
	eng := engine.New(&engine.Params{EngineID: "main", Model: "m"})
	eng.SetDispatcher(hub.NewHub())
	t.Cleanup(eng.Close)
	a := &engineAdapter{eng: eng}

	err := a.RewindTo(5)
	if err == nil {
		t.Fatal("RewindTo(5) on empty engine: err = nil, want out-of-range error")
	}
	if err.Error() != "rewind index 5 out of range [0, 0]" {
		t.Errorf("err = %q, want rewind index 5 out of range [0, 0]", err.Error())
	}
}

func TestMutA1_EngineAdapterRewindToPartialRestoreWarn(t *testing.T) {
	sink := a1SwapDefaultLogger(t)
	eng := engine.New(&engine.Params{EngineID: "main"})
	eng.SetDispatcher(hub.NewHub())
	t.Cleanup(eng.Close)

	backupDir := t.TempDir()
	tracker := filehistory.NewTracker(backupDir)
	eng.SetFileHistory(tracker)
	tmpFile := filepath.Join(t.TempDir(), "test.go")
	if err := os.WriteFile(tmpFile, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tracker.TrackEdit(tmpFile); err != nil {
		t.Fatalf("TrackEdit: %v", err)
	}
	if err := tracker.MakeSnapshot("snap-0"); err != nil {
		t.Fatalf("MakeSnapshot: %v", err)
	}
	if err := os.WriteFile(tmpFile, []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Destroy the backups so the rewind truncates but cannot restore.
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one backup file")
	}
	for _, ent := range entries {
		if err := os.Remove(filepath.Join(backupDir, ent.Name())); err != nil {
			t.Fatal(err)
		}
	}
	eng.SetMessages([]types.Message{
		{ID: "snap-0", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("resp1")}},
	})

	a := &engineAdapter{eng: eng}
	if err := a.RewindTo(0); err != nil {
		t.Fatalf("RewindTo partial restore: %v — partial restore must not fail the caller", err)
	}
	if got := sink.count("wui:rewind:partial_restore"); got != 1 {
		t.Errorf("partial-restore warn count = %d, want 1", got)
	}

	// A clean rewind logs nothing extra.
	eng2 := engine.New(&engine.Params{EngineID: "e2"})
	eng2.SetDispatcher(hub.NewHub())
	t.Cleanup(eng2.Close)
	eng2.SetMessages([]types.Message{a1UserMsg("q"), a1AssistantMsg("a")})
	a2 := &engineAdapter{eng: eng2}
	if err := a2.RewindTo(1); err != nil {
		t.Fatalf("clean RewindTo: %v", err)
	}
	if got := sink.count("wui:rewind:partial_restore"); got != 1 {
		t.Errorf("clean rewind logged partial_restore (count %d), want count still 1", got)
	}
}

func TestMutA1_EngineAdapterDelegation(t *testing.T) {
	eng := engine.New(&engine.Params{EngineID: "main", Model: "test-model"})
	eng.SetDispatcher(hub.NewHub())
	t.Cleanup(eng.Close)

	streamCalls := make(chan struct{}, 8)
	prov := &a1BlockProvider{onStream: func() { streamCalls <- struct{}{} }}
	a := &engineAdapter{eng: eng}
	a.SetProvider(prov)

	ctx := context.Background()

	// Query must reach the engine (busy) and the provider (Stream call).
	a.Query(ctx, "hi", "")
	if !waitFor(5*time.Second, func() bool { return a.IsBusy() }) {
		t.Fatal("engine never busy after Query — delegation dropped")
	}
	select {
	case <-streamCalls:
	case <-time.After(5 * time.Second):
		t.Fatal("provider.Stream never called — Query delegation dropped")
	}

	// Abort must reach the engine and release the query.
	a.Abort()
	if !waitFor(5*time.Second, func() bool { return !a.IsBusy() }) {
		t.Fatal("engine still busy after Abort — delegation dropped")
	}

	// QueryWithContent must pass a live ctx through (a nil ctx panics in
	// context.WithCancel before anything else happens).
	a.QueryWithContent(ctx, []types.ContentBlock{types.NewTextBlock("hi")}, "")
	if !waitFor(5*time.Second, func() bool { return a.IsBusy() }) {
		t.Fatal("engine never busy after QueryWithContent — delegation dropped")
	}
	select {
	case <-streamCalls:
	case <-time.After(5 * time.Second):
		t.Fatal("provider.Stream never called — QueryWithContent delegation dropped")
	}
	a.Abort()
	if !waitFor(5*time.Second, func() bool { return !a.IsBusy() }) {
		t.Fatal("engine still busy after Abort — delegation dropped")
	}

	// Attachment queue round trip.
	a.EnqueueAttachment(types.QueuedItem{Value: "v1", UUID: "u1", Mode: types.ItemModePrompt})
	pend := a.PendingAttachments()
	if len(pend) != 1 || pend[0].UUID != "u1" {
		t.Errorf("PendingAttachments = %+v, want the enqueued u1 item", pend)
	}

	// System prompt round trip.
	eng.SetSystemPrompt("sp-a1")
	if got := a.SystemPrompt(); got != "sp-a1" {
		t.Errorf("SystemPrompt = %q, want sp-a1", got)
	}

	// SwitchSession surfaces the engine error instead of swallowing it.
	err := a.SwitchSession("nope")
	if err == nil {
		t.Fatal("SwitchSession on store-less engine: err = nil, want error")
	}
	if err.Error() != "switch session: no store" {
		t.Errorf("err = %q, want switch session: no store", err.Error())
	}
}
