package wechat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// newStatConnector builds a connector with activeUserID set and a capturing
// sendToUserFn. No engine needed — Handle is pure stat logic.
func newStatConnector() (*WeChatConnector, *[]string) {
	var sent []string
	c := &WeChatConnector{
		activeUserID: "user1",
		sendToUserFn: func(_ context.Context, _, text string) error {
			sent = append(sent, text)
			return nil
		},
	}
	return c, &sent
}

// ---------------------------------------------------------------------------
// buildStatHeader
// ---------------------------------------------------------------------------

func TestBuildStatHeader_AllZero(t *testing.T) {
	c := &WeChatConnector{}
	if got := c.buildStatHeader(); got != "" {
		t.Fatalf("all-zero stats = %q, want empty", got)
	}
}

func TestBuildStatHeader_OnlyThinking(t *testing.T) {
	c := &WeChatConnector{thinkingSecs: 5}
	if got := c.buildStatHeader(); got != "思考 5秒" {
		t.Fatalf("only thinking = %q, want %q", got, "思考 5秒")
	}
}

func TestBuildStatHeader_AllCategories(t *testing.T) {
	c := &WeChatConnector{
		thinkingSecs: 5,
		searchCount:  2,
		fileCount:    3,
		cmdCount:     1,
		agentCount:   1,
	}
	want := "思考 5秒 · 搜索 2次 · 文件 3次 · 命令 1次 · 代理 1次"
	if got := c.buildStatHeader(); got != want {
		t.Fatalf("all categories = %q, want %q", got, want)
	}
}

func TestBuildStatHeader_Ordering(t *testing.T) {
	// Order must always be: 思考, 搜索, 文件, 命令, 代理 regardless of how
	// counters were incremented.
	c := &WeChatConnector{
		agentCount:   1, // set out of order
		cmdCount:     1,
		fileCount:    1,
		searchCount:  1,
		thinkingSecs: 1,
	}
	got := c.buildStatHeader()
	want := "思考 1秒 · 搜索 1次 · 文件 1次 · 命令 1次 · 代理 1次"
	if got != want {
		t.Fatalf("ordering = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Handle — stat state machine
// ---------------------------------------------------------------------------

func TestHandle_TextEnd_BuildsHeaderAndSends(t *testing.T) {
	c, sent := newStatConnector()
	c.Handle(types.QueryEvent{Type: types.EventThinkingEnd, Thinking: &types.ThinkingEvent{Duration: 5 * time.Second}})
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Web"}})
	c.Handle(types.QueryEvent{Type: types.EventToolEnd, ToolResult: &types.ToolResultEvent{ToolUseID: "t1"}})
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t2", Name: "Read"}})
	c.Handle(types.QueryEvent{Type: types.EventToolEnd, ToolResult: &types.ToolResultEvent{ToolUseID: "t2"}})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "reply"})
	c.Handle(types.QueryEvent{Type: types.EventTextEnd})

	if len(*sent) != 1 {
		t.Fatalf("sent count = %d, want 1", len(*sent))
	}
	want := "思考 5秒 · 搜索 1次 · 文件 1次\n\nreply"
	if (*sent)[0] != want {
		t.Fatalf("sent = %q, want %q", (*sent)[0], want)
	}
}

func TestHandle_TextEnd_NoStats_NoHeader(t *testing.T) {
	c, sent := newStatConnector()
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "just text"})
	c.Handle(types.QueryEvent{Type: types.EventTextEnd})

	if len(*sent) != 1 {
		t.Fatalf("sent count = %d, want 1", len(*sent))
	}
	if (*sent)[0] != "just text" {
		t.Fatalf("sent = %q, want %q (no header)", (*sent)[0], "just text")
	}
}

func TestHandle_QueryEnd_TrailingTools_SendsStatOnly(t *testing.T) {
	c, sent := newStatConnector()
	// A web search after the last TextEnd → trailing stat summary.
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Web"}})
	c.Handle(types.QueryEvent{Type: types.EventToolEnd, ToolResult: &types.ToolResultEvent{ToolUseID: "t1"}})
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd})

	if len(*sent) != 1 {
		t.Fatalf("sent count = %d, want 1 (stat-only summary)", len(*sent))
	}
	if (*sent)[0] != "搜索 1次" {
		t.Fatalf("stat-only = %q, want %q", (*sent)[0], "搜索 1次")
	}
}

func TestHandle_QueryEnd_NoStats_NoSend(t *testing.T) {
	c, sent := newStatConnector()
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd})

	if len(*sent) != 0 {
		t.Fatalf("sent count = %d, want 0 (no trailing stats)", len(*sent))
	}
}

func TestHandle_QueryEnd_ClosesQueryDone(t *testing.T) {
	c, _ := newStatConnector()
	ch := make(chan struct{})
	c.queryDone = ch
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd})

	// A closed channel yields the zero value with ok==false.
	_, ok := <-ch
	if ok {
		t.Fatal("queryDone should be closed after EventQueryEnd")
	}
	if c.queryDone != nil {
		t.Fatal("queryDone field should be nil after EventQueryEnd closes it")
	}
}

func TestHandle_ResetCountersAfterTextEnd(t *testing.T) {
	c, sent := newStatConnector()
	// First segment: 5s thinking + a reply.
	c.Handle(types.QueryEvent{Type: types.EventThinkingEnd, Thinking: &types.ThinkingEvent{Duration: 5 * time.Second}})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "first"})
	c.Handle(types.QueryEvent{Type: types.EventTextEnd})
	// Second segment: only a file op, NO thinking. The header must reflect
	// only post-first-TextEnd activity (counters reset on TextEnd).
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Read"}})
	c.Handle(types.QueryEvent{Type: types.EventToolEnd, ToolResult: &types.ToolResultEvent{ToolUseID: "t1"}})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "second"})
	c.Handle(types.QueryEvent{Type: types.EventTextEnd})

	if len(*sent) != 2 {
		t.Fatalf("sent count = %d, want 2", len(*sent))
	}
	// First reply: 5s thinking, no tools.
	if (*sent)[0] != "思考 5秒\n\nfirst" {
		t.Fatalf("first sent = %q, want %q", (*sent)[0], "思考 5秒\n\nfirst")
	}
	// Second reply: 1 file op, no thinking (reset).
	if (*sent)[1] != "文件 1次\n\nsecond" {
		t.Fatalf("second sent = %q, want %q", (*sent)[1], "文件 1次\n\nsecond")
	}
}

// Regression: the engine delivers ALL query failures (API timeout, abort,
// panic) as EventQueryEnd{Error: err}, never as EventError. Handle must notify
// the active user from the EventQueryEnd case — otherwise failures vanish
// silently. The error message goes out via sendToUserFn directly (not through
// sendWeChatReply, which would split/format it).
func TestHandle_QueryEndWithError_SendsErrorToUser(t *testing.T) {
	var sent string
	c := &WeChatConnector{
		activeUserID: "user1",
		sendToUserFn: func(_ context.Context, _, text string) error {
			sent = text
			return nil
		},
	}
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd, Error: errors.New("API timeout")})

	if !strings.Contains(sent, "Error") || !strings.Contains(sent, "timeout") {
		t.Errorf("expected error message sent, got %q", sent)
	}
}

func TestHandle_ToolNameClassification(t *testing.T) {
	tests := []struct {
		name       string
		toolName   string
		wantHeader string
	}{
		{"Web is search", "Web", "搜索 1次"},
		{"Read is file", "Read", "文件 1次"},
		{"Grep is file", "Grep", "文件 1次"},
		{"Glob is file", "Glob", "文件 1次"},
		{"Edit is file", "Edit", "文件 1次"},
		{"Write is file", "Write", "文件 1次"},
		{"Lsp is file", "Lsp", "文件 1次"},
		{"Bash is cmd", "Bash", "命令 1次"},
		{"Agent is agent", "Agent", "代理 1次"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, sent := newStatConnector()
			c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t1", Name: tt.toolName}})
			c.Handle(types.QueryEvent{Type: types.EventToolEnd, ToolResult: &types.ToolResultEvent{ToolUseID: "t1"}})
			c.Handle(types.QueryEvent{Type: types.EventQueryEnd})

			// QueryEnd sends a stat-only summary when there are trailing stats.
			if len(*sent) != 1 {
				t.Fatalf("sent count = %d, want 1", len(*sent))
			}
			if (*sent)[0] != tt.wantHeader {
				t.Fatalf("classification for %q = %q, want %q", tt.toolName, (*sent)[0], tt.wantHeader)
			}
		})
	}
}

func TestHandle_NoActiveUser_DoesNotSend(t *testing.T) {
	// Without activeUserID, TextEnd must not send even with stats.
	c, sent := newStatConnector()
	c.activeUserID = ""
	c.Handle(types.QueryEvent{Type: types.EventThinkingEnd, Thinking: &types.ThinkingEvent{Duration: 5 * time.Second}})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "reply"})
	c.Handle(types.QueryEvent{Type: types.EventTextEnd})

	if len(*sent) != 0 {
		t.Fatalf("sent count = %d, want 0 (no active user)", len(*sent))
	}
}

func TestHandle_ConnectorUserMessage_NoOp(t *testing.T) {
	c, sent := newStatConnector()
	// This event is connector-emitted; Handle must ignore it (no send).
	c.Handle(types.QueryEvent{
		Type:    types.EventConnectorUserMessage,
		Message: &types.Message{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	})

	if len(*sent) != 0 {
		t.Fatalf("sent count = %d, want 0 (connector user message is a no-op in Handle)", len(*sent))
	}
}

// ---------------------------------------------------------------------------
// sendWeChatReply: empty formatted text does not send
// ---------------------------------------------------------------------------

func TestSendWeChatReply_EmptyText_DoesNotSend(t *testing.T) {
	sent := false
	c := &WeChatConnector{
		sendToUserFn: func(_ context.Context, _, _ string) error {
			sent = true
			return nil
		},
	}
	c.sendWeChatReply(context.Background(), "user1", "")

	if sent {
		t.Fatal("empty text should not trigger a send")
	}
}

func TestSendWeChatReply_SendsFormattedText(t *testing.T) {
	var sentText string
	c := &WeChatConnector{
		sendToUserFn: func(_ context.Context, _, text string) error {
			sentText = text
			return nil
		},
	}
	c.sendWeChatReply(context.Background(), "user1", "hello world")

	if sentText != "hello world" {
		t.Fatalf("sent = %q, want %q", sentText, "hello world")
	}
}

// ---------------------------------------------------------------------------
// concurrency: Handle is called serially from the engine dispatch goroutine
// ---------------------------------------------------------------------------

func TestHandle_SerialCalls_NoDataRace(t *testing.T) {
	c, _ := newStatConnector()
	// Run Handle concurrently with -race to detect data races. The contract
	// is that Handle is called serially (one query at a time), but the
	// counters are plain fields — this test just confirms no panic under
	// sequential heavy use.
	for i := range 100 {
		toolID := fmt.Sprintf("t%d", i)
		c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: toolID, Name: "Read"}})
		c.Handle(types.QueryEvent{Type: types.EventToolEnd, ToolResult: &types.ToolResultEvent{ToolUseID: toolID}})
	}
	if c.fileCount != 100 {
		t.Fatalf("fileCount = %d, want 100", c.fileCount)
	}
}
