package hub

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

type logRecord struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

type logCapture struct{ recs *[]logRecord }

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	*c.recs = append(*c.recs, logRecord{level: r.Level, msg: r.Message, attrs: attrs})
	return nil
}

func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(string) slog.Handler      { return c }

// captureDefault redirects the package-level slog default that logEngineEvent
// writes to, for the duration of the test.
func captureDefault(t *testing.T) *[]logRecord {
	t.Helper()
	var recs []logRecord
	prev := slog.Default()
	slog.SetDefault(slog.New(&logCapture{recs: &recs}))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &recs
}

// assertRecords pins the exact entries emitted: message, level, and every
// attribute. A dropped case falls through to "engine:unknown", a dropped nil
// guard panics, and a dropped attribute changes the map — all caught here.
func assertRecords(t *testing.T, recs []logRecord, want []logRecord) {
	t.Helper()
	if len(recs) != len(want) {
		t.Fatalf("emitted %d records, want %d\ngot:  %+v\nwant: %+v", len(recs), len(want), recs, want)
	}
	for i, w := range want {
		g := recs[i]
		if g.level != w.level || g.msg != w.msg {
			t.Errorf("record %d = (%v, %q), want (%v, %q)", i, g.level, g.msg, w.level, w.msg)
			continue
		}
		if len(g.attrs) != len(w.attrs) {
			t.Errorf("record %d attrs = %d entries %v, want %d entries %v", i, len(g.attrs), g.attrs, len(w.attrs), w.attrs)
			continue
		}
		for k, wv := range w.attrs {
			gv, ok := g.attrs[k]
			if !ok {
				t.Errorf("record %q missing attr %q", w.msg, k)
				continue
			}
			if gv != wv {
				t.Errorf("record %q attr %q = %#v, want %#v", w.msg, k, gv, wv)
			}
		}
	}
}

func info(msg string, attrs ...any) logRecord {
	m := make(map[string]any)
	for i := 0; i < len(attrs); i += 2 {
		m[attrs[i].(string)] = attrs[i+1]
	}
	return logRecord{level: slog.LevelInfo, msg: msg, attrs: m}
}

// TestLogEngineEvent_ExactRecords covers every switch branch of the debug log
// with the exact entry it must produce, including the truncation boundaries
// (60 runes for text/thinking previews, 80 bytes for tool param deltas) and
// the nil-payload branches that must emit nothing at all.
func TestLogEngineEvent_ExactRecords(t *testing.T) {
	recs := captureDefault(t)

	r60 := strings.Repeat("a", 60)
	r61 := strings.Repeat("a", 61)
	b80 := strings.Repeat("b", 80)
	b81 := strings.Repeat("b", 81)

	tests := []struct {
		name  string
		event Event
		want  []logRecord
	}{
		{
			name:  "text_start",
			event: Event{Type: types.EventTextStart},
			want:  []logRecord{info("engine:text_start")},
		},
		{
			name:  "text_delta_short",
			event: Event{Type: types.EventTextDelta, Text: "hello"},
			want:  []logRecord{info("engine:text_delta", "text", "hello")},
		},
		{
			// Exactly at the 60-rune budget: no ellipsis.
			name:  "text_delta_exactly_60",
			event: Event{Type: types.EventTextDelta, Text: r60},
			want:  []logRecord{info("engine:text_delta", "text", r60)},
		},
		{
			name:  "text_delta_61_truncates",
			event: Event{Type: types.EventTextDelta, Text: r61},
			want:  []logRecord{info("engine:text_delta", "text", r60+"...")},
		},
		{
			name:  "text_end",
			event: Event{Type: types.EventTextEnd},
			want:  []logRecord{info("engine:text_end")},
		},
		{
			name:  "tool_start_nil_emits_nothing",
			event: Event{Type: types.EventToolStart},
			want:  nil,
		},
		{
			name:  "tool_start",
			event: Event{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "tu1", Name: "Bash", Summary: "ls"}},
			want:  []logRecord{info("engine:tool_start", "id", "tu1", "name", "Bash", "summary", "ls")},
		},
		{
			name:  "tool_param_delta_nil_emits_nothing",
			event: Event{Type: types.EventToolParamDelta},
			want:  nil,
		},
		{
			name:  "tool_param_delta_short",
			event: Event{Type: types.EventToolParamDelta, PartialInput: &types.PartialInputEvent{ID: "tu1", Delta: "short", Summary: "s"}},
			want:  []logRecord{info("engine:tool_param_delta", "id", "tu1", "delta", "short", "summary", "s")},
		},
		{
			// The 80-byte budget is byte-based, not rune-based.
			name:  "tool_param_delta_exactly_80",
			event: Event{Type: types.EventToolParamDelta, PartialInput: &types.PartialInputEvent{ID: "tu1", Delta: b80, Summary: "s"}},
			want:  []logRecord{info("engine:tool_param_delta", "id", "tu1", "delta", b80, "summary", "s")},
		},
		{
			name:  "tool_param_delta_81_truncates",
			event: Event{Type: types.EventToolParamDelta, PartialInput: &types.PartialInputEvent{ID: "tu1", Delta: b81, Summary: "s"}},
			want:  []logRecord{info("engine:tool_param_delta", "id", "tu1", "delta", b80+"...", "summary", "s")},
		},
		{
			name:  "tool_run_nil_emits_nothing",
			event: Event{Type: types.EventToolRun},
			want:  nil,
		},
		{
			name:  "tool_run",
			event: Event{Type: types.EventToolRun, ToolUse: &types.ToolUseEvent{ID: "tu1", Name: "Read"}},
			want:  []logRecord{info("engine:tool_run", "id", "tu1", "name", "Read")},
		},
		{
			name:  "tool_output_delta_nil_emits_nothing",
			event: Event{Type: types.EventToolOutputDelta},
			want:  nil,
		},
		{
			name:  "tool_output_delta_three_lines",
			event: Event{Type: types.EventToolOutputDelta, ToolResult: &types.ToolResultEvent{ToolUseID: "tu1", DisplayOutput: "line1\nline2\nline3"}},
			want:  []logRecord{info("engine:tool_output_delta", "id", "tu1", "lines", int64(3))},
		},
		{
			name:  "tool_output_delta_empty_is_one_line",
			event: Event{Type: types.EventToolOutputDelta, ToolResult: &types.ToolResultEvent{ToolUseID: "tu1", DisplayOutput: ""}},
			want:  []logRecord{info("engine:tool_output_delta", "id", "tu1", "lines", int64(1))},
		},
		{
			name:  "tool_output_delta_trailing_newline_counts_extra",
			event: Event{Type: types.EventToolOutputDelta, ToolResult: &types.ToolResultEvent{ToolUseID: "tu1", DisplayOutput: "a\n"}},
			want:  []logRecord{info("engine:tool_output_delta", "id", "tu1", "lines", int64(2))},
		},
		{
			name:  "tool_end_nil_emits_nothing",
			event: Event{Type: types.EventToolEnd},
			want:  nil,
		},
		{
			name:  "tool_end",
			event: Event{Type: types.EventToolEnd, ToolResult: &types.ToolResultEvent{ToolUseID: "tu1", DisplayOutput: "done"}},
			want:  []logRecord{info("engine:tool_end", "id", "tu1", "isError", false, "outputLen", int64(4))},
		},
		{
			name:  "tool_end_error",
			event: Event{Type: types.EventToolEnd, ToolResult: &types.ToolResultEvent{ToolUseID: "tu1", DisplayOutput: "e", IsError: true}},
			want:  []logRecord{info("engine:tool_end", "id", "tu1", "isError", true, "outputLen", int64(1))},
		},
		{
			name:  "usage_nil_emits_nothing",
			event: Event{Type: types.EventUsage},
			want:  nil,
		},
		{
			name: "usage",
			event: Event{Type: types.EventUsage, Usage: &types.UsageEvent{
				InputTokens: 100, OutputTokens: 50, CacheReadInputTokens: 10,
				CacheCreationInputTokens: 20, StopReason: "end_turn",
			}},
			want: []logRecord{info("engine:usage",
				"input", int64(100), "output", int64(50), "cache_read", int64(10),
				"cache_creation", int64(20), "stop_reason", "end_turn")},
		},
		{
			name:  "turn_start",
			event: Event{Type: types.EventTurnStart},
			want:  []logRecord{info("engine:turn_start")},
		},
		{
			name:  "turn_end",
			event: Event{Type: types.EventTurnEnd},
			want:  []logRecord{info("engine:turn_end")},
		},
		{
			name:  "query_end",
			event: Event{Type: types.EventQueryEnd},
			want:  []logRecord{info("engine:query_end")},
		},
		{
			name:  "attachment",
			event: Event{Type: types.EventAttachment},
			want:  []logRecord{info("engine:attachment")},
		},
		{
			name:  "query_start_nil_emits_nothing",
			event: Event{Type: types.EventQueryStart},
			want:  nil,
		},
		{
			name: "query_start",
			event: Event{
				Type:    types.EventQueryStart,
				Model:   "claude-sonnet",
				Message: &types.Message{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi"), types.NewTextBlock("there")}},
			},
			want: []logRecord{info("engine:query_start", "role", "user", "blocks", int64(2), "model", "claude-sonnet")},
		},
		{
			name:  "thinking_start",
			event: Event{Type: types.EventThinkingStart},
			want:  []logRecord{info("engine:thinking_start")},
		},
		{
			name:  "thinking_delta_nil_emits_nothing",
			event: Event{Type: types.EventThinkingDelta},
			want:  nil,
		},
		{
			name:  "thinking_delta_truncates_at_60",
			event: Event{Type: types.EventThinkingDelta, Thinking: &types.ThinkingEvent{Text: r61}},
			want:  []logRecord{info("engine:thinking_delta", "text", r60+"...")},
		},
		{
			name:  "thinking_end_nil_emits_nothing",
			event: Event{Type: types.EventThinkingEnd},
			want:  nil,
		},
		{
			name:  "thinking_end",
			event: Event{Type: types.EventThinkingEnd, Thinking: &types.ThinkingEvent{Duration: 2 * time.Second}},
			want:  []logRecord{info("engine:thinking_end", "duration", 2*time.Second)},
		},
		{
			name:  "retry_attempt_nil_emits_nothing",
			event: Event{Type: types.EventRetryAttempt},
			want:  nil,
		},
		{
			name: "retry_attempt",
			event: Event{Type: types.EventRetryAttempt, RetryAttempt: &types.RetryAttemptEvent{
				Attempt: 2, MaxRetries: 5, RetryInMs: 1500, ErrorType: types.RetryErrorStreamInterrupted,
			}},
			want: []logRecord{info("engine:retry_attempt",
				"attempt", int64(2), "maxRetries", int64(5), "retryInMs", int64(1500),
				// slog keeps the named string type in the value.
				"errorType", types.RetryErrorStreamInterrupted)},
		},
		{
			name:  "ask_nil_emits_nothing",
			event: Event{Type: types.EventAsk},
			want:  nil,
		},
		{
			name:  "ask",
			event: Event{Type: types.EventAsk, Ask: &types.AskEvent{ToolName: "Bash"}},
			want:  []logRecord{info("engine:permission_ask", "tool", "Bash")},
		},
		{
			name:  "connector_user_message_nil_emits_nothing",
			event: Event{Type: types.EventConnectorUserMessage},
			want:  nil,
		},
		{
			name:  "connector_user_message",
			event: Event{Type: types.EventConnectorUserMessage, Message: &types.Message{Role: types.RoleUser}},
			want:  []logRecord{info("engine:connector_user_message")},
		},
		{
			name:  "job_started_nil_emits_nothing",
			event: Event{Type: types.EventJobStarted},
			want:  nil,
		},
		{
			name:  "job_started",
			event: Event{Type: types.EventJobStarted, Job: &types.JobEvent{ID: "bg-1", Status: "running"}},
			want:  []logRecord{info("engine:job_started", "id", "bg-1", "status", "running")},
		},
		{
			name:  "job_status",
			event: Event{Type: types.EventJobStatus, Job: &types.JobEvent{ID: "bg-2", Status: "running"}},
			want:  []logRecord{info("engine:job_status", "id", "bg-2", "status", "running")},
		},
		{
			name:  "job_terminated",
			event: Event{Type: types.EventJobTerminated, Job: &types.JobEvent{ID: "agent-1", Status: "killed"}},
			want:  []logRecord{info("engine:job_terminated", "id", "agent-1", "status", "killed")},
		},
		{
			name:  "unknown_type",
			event: Event{Type: "custom_event_type"},
			want:  []logRecord{info("engine:unknown", "type", types.QueryEventType("custom_event_type"))},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			*recs = nil
			logEngineEvent(tc.event)
			assertRecords(t, *recs, tc.want)
		})
	}
}

// Dispatch must log before routing: the debug log is the only record of event
// ordering for a session.
func TestDispatch_LogsThenRoutes(t *testing.T) {
	recs := captureDefault(t)
	h := NewHub()
	m := &mockHandler{}
	h.Subscribe(m)

	h.Dispatch(Event{Type: types.EventTextDelta, Text: "routed"})

	if len(m.Events()) != 1 {
		t.Fatalf("handler received %d events, want 1", len(m.Events()))
	}
	assertRecords(t, *recs, []logRecord{info("engine:text_delta", "text", "routed")})
}
