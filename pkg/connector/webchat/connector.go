// Package webchat implements the gbot web chat connector: an HTTP+WS server
// that serves a React SPA, bridges the engine's QueryEvent stream over
// WebSocket to browser/WebView clients, and handles Ask (permission)
// interactions via a request/response WS protocol.
//
// The connector subscribes to the main engine's hub (the same engine the TUI
// drives). It is request-driven: inbound WS messages trigger queries; the
// connector itself owns no polling loop.
package webchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// queryEventWithAbort embeds QueryEvent and adds an "aborted" boolean for the
// query_end wire payload. The engine's terminal Error is `json:"-"` so it
// never crosses the WS boundary; this is the only channel for the frontend to
// learn the query was user-interrupted (mirrors TUI's
// errors.AsType[*engine.AbortError] check at pkg/tui/repl.go:1134). For
// non-abort events Aborted is false and omitempty drops the key, so the wire
// shape is byte-identical to marshalling QueryEvent directly.
type queryEventWithAbort struct {
	types.QueryEvent
	Aborted bool `json:"aborted,omitempty"`
}

// handlerBufSize is the outbound channel buffer, matching TUI's appCh
// (pkg/tui/handler.go: handlerBufSize = 1024). A single large buffer plus
// blocking send means messages are never dropped; a slow client briefly
// back-pressures the engine loop (acceptable for correctness).
const handlerBufSize = 1024

// WebChatConnector implements connector.Connector for the web chat. It
// subscribes to the main engine's hub, translates QueryEvents into the WS
// wire protocol (Phase 0), and drives queries from inbound WS messages.
type WebChatConnector struct {
	engine *engine.Engine
	hub    *hub.Hub

	unsubscribe func()

	// Ask → ResponseCh plumbing. When the engine dispatches EventAsk, Handle
	// stores evt.Ask under a fresh monotonic id and emits the ask outbound.
	// When the client responds, the WS handler looks up the id and writes to
	// ResponseCh. On disconnect, all pending asks are aborted.
	pendingAsks map[string]*types.AskEvent
	pendingMu   sync.Mutex
	askCounter  atomic.Int64

	// Single buffered channel for all outbound messages, mirroring TUI's
	// appCh (pkg/tui/handler.go: handlerBufSize = 1024). Blocking send —
	// the hub calls Handle synchronously, so a full channel at most briefly
	// delays the engine loop, which is acceptable for correctness (no drops).
	msgCh chan []byte

	// The single active WS connection. The connector supports one client at a
	// time; a new connection replaces the prior one.
	activeWS atomic.Pointer[websocket.Conn]

	// Testable seams (same pattern as WeChat connector.go). queryFn defaults
	// to eng.Query; tests override to record dispatches. isBusyFn defaults to
	// eng.IsBusy. messagesFn/toolsFn default to eng.Messages/eng.Tools and are
	// overridden by tests to exercise buildHistoryMessage without a real engine.
	queryFn    func(ctx context.Context, userMessage, systemPrompt string)
	isBusyFn   func() bool
	messagesFn func() []types.Message
	toolsFn    func() map[string]tool.Tool
	enqueueFn  func(item types.QueuedItem)
	abortFn    func()
}

// New builds a WebChatConnector bound to the given engine and hub. The
// connector subscribes to h immediately (same pattern as WeChat at
// pkg/connector/wechat/connector.go:127); the returned unsubscribe func is
// stored and called by Stop.
func New(eng *engine.Engine, h *hub.Hub) *WebChatConnector {
	c := &WebChatConnector{
		engine:      eng,
		hub:         h,
		pendingAsks: make(map[string]*types.AskEvent),
		msgCh:       make(chan []byte, handlerBufSize),
	}
	c.queryFn = func(ctx context.Context, userMessage, _ string) {
		eng.Query(ctx, userMessage, eng.SystemPrompt())
	}
	c.isBusyFn = func() bool {
		if c.engine == nil {
			return false
		}
		return c.engine.IsBusy()
	}
	c.messagesFn = func() []types.Message {
		if c.engine == nil {
			return nil
		}
		return c.engine.Messages()
	}
	c.toolsFn = func() map[string]tool.Tool {
		if c.engine == nil {
			return nil
		}
		return c.engine.Tools()
	}
	c.enqueueFn = func(item types.QueuedItem) {
		c.engine.EnqueueAttachment(item)
	}
	c.abortFn = func() {
		c.engine.Abort()
	}
	if h != nil {
		c.unsubscribe = h.Subscribe(c)
	}
	return c
}

// Start is a no-op: the HTTP/WS server is registered separately via
// RegisterChatWS in main.go, and the connector is request-driven.
func (c *WebChatConnector) Start(ctx context.Context) error { return nil }

// Stop unsubscribes from the hub, aborts any pending asks, cancels any active
// query, and closes the channel so the writeLoop exits.
func (c *WebChatConnector) Stop() {
	if c.unsubscribe != nil {
		c.unsubscribe()
	}
	c.cleanupConn()
	close(c.msgCh)
}

// Send is an interface no-op: webchat has no outbound platform. Nobody calls
// Send on the webchat connector; it exists solely to satisfy the
// connector.Connector contract.
func (c *WebChatConnector) Send(userID, text string) error { return nil }

// Handle implements hub.EventHandler. Every event is marshalled to its wire
// payload and pushed onto msgCh with a blocking send (no drops). The hub
// calls Handle synchronously, so a slow client briefly back-pressures the
// engine loop, which is the desired correctness tradeoff — same model TUI
// uses with its 1024-buffer appCh.
func (c *WebChatConnector) Handle(event hub.Event) {
	switch event.Type {
	case types.EventAsk:
		c.handleAsk(event)
		return
	case types.EventError:
		c.msgCh <- buildErrorMessage(event.Error)
		return
	}

	aborted := false
	if event.Type == types.EventQueryEnd && event.Error != nil {
		if _, ok := errors.AsType[*engine.AbortError](event.Error); ok {
			aborted = true
		}
	}
	payload, err := json.Marshal(struct {
		Type  string              `json:"type"`
		Event queryEventWithAbort `json:"event"`
	}{Type: "event", Event: queryEventWithAbort{QueryEvent: event, Aborted: aborted}})
	if err != nil {
		slog.Warn("webchat: marshal event failed", "type", event.Type, "error", err)
		return
	}
	c.msgCh <- payload
}

// handleAsk stores the AskEvent under a fresh id, builds the askOutbound
// struct (NOT marshalling *types.AskEvent directly — its fields have no json
// tags), and emits it on msgCh.
func (c *WebChatConnector) handleAsk(event hub.Event) {
	if event.Ask == nil {
		return
	}
	id := strconv.FormatInt(c.askCounter.Add(1), 10)
	c.pendingMu.Lock()
	c.pendingAsks[id] = event.Ask
	c.pendingMu.Unlock()

	kind := "permission"
	if event.Ask.Kind == types.AskInput {
		kind = "input"
	}
	out := askOutbound{
		Type:       "ask",
		ID:         id,
		Kind:       kind,
		ToolName:   event.Ask.ToolName,
		Input:      event.Ask.Input,
		Message:    event.Ask.Message,
		RuleDetail: event.Ask.RuleDetail,
		Prompt:     event.Ask.Prompt,
		Masked:     event.Ask.Masked,
		AgentType:  event.Ask.AgentType,
	}
	payload, err := json.Marshal(out)
	if err != nil {
		slog.Warn("webchat: marshal ask failed", "id", id, "error", err)
		return
	}
	c.msgCh <- payload
}

// buildHistoryMessage returns a JSON "history" message containing a PAGINATED
// slice of the engine's conversation, or nil if there are no messages at all.
//
// cursor is an opaque token encoding how many messages from the END have
// already been delivered in prior pages ("0"/"" → deliver the most recent
// page). limit defaults to 10 when <= 0. The payload carries nextCursor and
// hasMore so the frontend can request further pages via history_request.
//
// The full history is still computed (needed to resolve tool_result
// cross-references); only the serialized payload shrinks. Tool summaries and
// outputs are rendered via the tool's own Description/RenderResult — the same
// path as TUI's engineMessagesToViews — so history looks identical to streaming.
func (c *WebChatConnector) buildHistoryMessage(cursor string, limit int) []byte {
	if c.messagesFn == nil {
		return nil
	}
	msgs := c.messagesFn()
	if len(msgs) == 0 {
		return nil
	}
	var tools map[string]tool.Tool
	if c.toolsFn != nil {
		tools = c.toolsFn()
	}

	// First pass: collect all tool_results keyed by tool_use_id (same as TUI).
	toolResults := make(map[string]types.ContentBlock)
	for _, m := range msgs {
		for _, cb := range m.Content {
			if cb.Type == types.ContentTypeToolResult && cb.ToolUseID != "" {
				toolResults[cb.ToolUseID] = cb
			}
		}
	}

	var out []historyChatMsg
	for _, m := range msgs {
		if m.Role == types.RoleSystem {
			continue
		}
		if m.HasFlag(types.FlagMeta) || m.HasFlag(types.FlagCompactSummary) {
			continue
		}
		hm := historyChatMsg{
			ID:        m.ID,
			Role:      string(m.Role),
			StartedAt: m.Timestamp.UnixMilli(),
			Status:    "done",
		}
		for _, cb := range m.Content {
			switch cb.Type {
			case types.ContentTypeText:
				hm.Text += cb.Text
				// Match TUI's engineMessagesToViews: skip whitespace-only text
				// blocks in the ordered array so they don't consume an
				// eventIndex slot in the frontend. Legacy hm.Text concatenates
				// all text including whitespace (unchanged behavior).
				if strings.TrimSpace(cb.Text) != "" {
					hm.Blocks = append(hm.Blocks, historyBlock{Kind: "text", Text: cb.Text})
				}
			case types.ContentTypeThinking:
				if strings.TrimSpace(cb.Thinking) != "" {
					thinkingEntry := historyThinkingEntry{Text: cb.Thinking}
					hm.Thinking = append(hm.Thinking, thinkingEntry)
					hm.Blocks = append(hm.Blocks, historyBlock{Kind: "thinking", Thinking: &thinkingEntry})
				}
			case types.ContentTypeToolUse:
				entry := historyToolEntry{
					ID:      cb.ID,
					Name:    formatToolDisplayName(cb.Name),
					Summary: computeToolSummary(cb.Name, cb.Input, tools),
				}
				if result, ok := toolResults[cb.ID]; ok {
					entry.IsError = result.IsError
					entry.DisplayOutput, entry.DurationNs = renderToolOutput(cb.Name, result.Content, tools)
				}
				hm.Tools = append(hm.Tools, entry)
				hm.Blocks = append(hm.Blocks, historyBlock{Kind: "tool", Tool: &entry})
			}
		}
		if m.Usage != nil {
			hm.Usage = historyUsage{
				InputTokens:   m.Usage.InputTokens,
				OutputTokens:  m.Usage.OutputTokens,
				CacheRead:     m.Usage.CacheReadInputTokens,
				CacheCreation: m.Usage.CacheCreationInputTokens,
			}
		}
		out = append(out, hm)
	}
	if len(out) == 0 {
		return nil
	}

	// Pagination: cursor is the count of messages from the END already
	// delivered in prior pages. Cursor ""/0 → deliver the most recent page.
	// The slice is taken from the END so a growing conversation (new messages
	// appended at the back) never shifts already-delivered page offsets.
	total := len(out)
	if limit <= 0 {
		limit = 10
	}
	offset := 0
	if cursor != "" {
		if n, err := strconv.Atoi(cursor); err == nil && n > 0 {
			offset = n
		}
	}
	end := max(
		// exclusive upper bound
		total-offset, 0)
	start := max(end-limit, 0)
	page := out[start:end]
	hasMore := start > 0
	nextCursor := ""
	if hasMore {
		nextCursor = strconv.Itoa(offset + (end - start))
	}

	payload, _ := json.Marshal(struct {
		Type       string           `json:"type"`
		Messages   []historyChatMsg `json:"messages"`
		NextCursor string           `json:"nextCursor"`
		HasMore    bool             `json:"hasMore"`
	}{Type: "history", Messages: page, NextCursor: nextCursor, HasMore: hasMore})
	return payload
}

// formatToolDisplayName mirrors TUI's logic for MCP tool names.
func formatToolDisplayName(name string) string {
	if !strings.HasPrefix(name, "mcp__") {
		return name
	}
	parts := strings.SplitN(name, "__", 3)
	if len(parts) < 3 {
		return name
	}
	return parts[1] + " - " + parts[2] + " (MCP)"
}

// computeToolSummary calls the tool's Description — same as TUI.
func computeToolSummary(name string, input json.RawMessage, tools map[string]tool.Tool) string {
	t, ok := tools[name]
	if !ok {
		return ""
	}
	desc, err := t.Description(input)
	if err != nil {
		return ""
	}
	return desc
}

// renderToolOutput renders persisted tool_result content via the tool's
// RenderResult — same logic as TUI's renderToolOutput in app.go.
func renderToolOutput(toolName string, raw json.RawMessage, tools map[string]tool.Tool) (string, int64) {
	if len(raw) == 0 {
		return "", 0
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &blocks) == nil {
			var parts []string
			for _, b := range blocks {
				if b.Type == "text" && b.Text != "" {
					parts = append(parts, b.Text)
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, "\n"), 0
			}
		}
		return string(raw), 0
	}

	rest := s
	elapsed := int64(0)
	if strings.HasPrefix(rest, "[Tool spent ") {
		if idx := strings.Index(rest, "]"); idx >= 0 {
			inner := strings.TrimPrefix(rest[:idx+1], "[Tool spent ")
			inner = strings.TrimSuffix(inner, "s]")
			if sec, err := strconv.ParseFloat(inner, 64); err == nil {
				elapsed = int64(sec * float64(time.Second))
			}
			rest = rest[idx+1:]
		}
	}
	if rest == "" {
		return "", elapsed
	}

	if strings.HasPrefix(rest, "<persisted-output>") {
		if data := readPersistedFile(rest); data != nil {
			if t, ok := tools[toolName]; ok {
				if rendered := t.RenderResult(data); rendered != "" {
					return rendered, elapsed
				}
			}
		}
		return extractPersistedPreview(rest), elapsed
	}

	if t, ok := tools[toolName]; ok {
		return t.RenderResult(json.RawMessage(rest)), elapsed
	}

	var obj struct {
		Output string `json:"output"`
	}
	if json.Unmarshal([]byte(rest), &obj) == nil && obj.Output != "" {
		return obj.Output, elapsed
	}
	return rest, elapsed
}

func readPersistedFile(s string) json.RawMessage {
	_, after, ok := strings.Cut(s, "Full output saved to: ")
	if !ok {
		return nil
	}
	pathEnd := strings.IndexByte(after, '\n')
	if pathEnd < 0 {
		pathEnd = len(after)
	}
	filePath := strings.TrimSpace(after[:pathEnd])
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	return json.RawMessage(data)
}

func extractPersistedPreview(s string) string {
	_, after, ok := strings.Cut(s, "Preview (")
	if !ok {
		return "<output saved to file>"
	}
	_, after0, ok0 := strings.Cut(after, "):\n")
	if !ok0 {
		return "<output saved to file>"
	}
	preview := after0
	lines := strings.SplitN(preview, "\n", 6)
	if len(lines) > 5 {
		lines = lines[:5]
		lines = append(lines, "...")
	}
	result := strings.Join(lines, "\n")
	if result == "" {
		return "<output saved to file>"
	}
	return result
}

// historyChatMsg is the wire shape for a single history message.
type historyChatMsg struct {
	ID        string                 `json:"id"`
	Role      string                 `json:"role"`
	Text      string                 `json:"text"`
	Thinking  []historyThinkingEntry `json:"thinking"`
	Tools     []historyToolEntry     `json:"tools"`
	Blocks    []historyBlock         `json:"blocks,omitempty"`
	Usage     historyUsage           `json:"usage"`
	Error     string                 `json:"error"`
	Status    string                 `json:"status"`
	StartedAt int64                  `json:"startedAt"`
}

// historyBlock is one entry in the ordered Blocks array. It mirrors a single
// content block from the engine message's Content[], preserving original event
// order so the frontend can interleave text/thinking/tool correctly. The legacy
// Text/Thinking/Tools fields concatenate same-type blocks and lose ordering;
// Blocks is authoritative when present.
type historyBlock struct {
	Kind     string                `json:"kind"`               // "text" | "thinking" | "tool"
	Text     string                `json:"text,omitempty"`     // kind == "text"
	Thinking *historyThinkingEntry `json:"thinking,omitempty"` // kind == "thinking"
	Tool     *historyToolEntry     `json:"tool,omitempty"`     // kind == "tool"
}

type historyThinkingEntry struct {
	Text       string `json:"text"`
	DurationNs int64  `json:"durationNs,omitempty"`
}

type historyToolEntry struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Summary       string `json:"summary,omitempty"`
	DisplayOutput string `json:"displayOutput,omitempty"`
	IsError       bool   `json:"isError,omitempty"`
	DurationNs    int64  `json:"durationNs,omitempty"`
}

type historyUsage struct {
	InputTokens   int `json:"inputTokens"`
	OutputTokens  int `json:"outputTokens"`
	CacheRead     int `json:"cacheRead"`
	CacheCreation int `json:"cacheCreation"`
}

// buildErrorMessage formats an EventError payload.
func buildErrorMessage(err error) []byte {
	msg := "unknown error"
	if err != nil {
		msg = fmt.Sprintf("%v", err)
	}
	out, _ := json.Marshal(struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}{Type: "error", Message: msg})
	return out
}

// askOutbound is the wire shape for an Ask event. It exists because
// *types.AskEvent fields have no json tags — marshalling it directly would
// emit PascalCase keys. The React client expects snake_case (Phase 0).
type askOutbound struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	ToolName   string          `json:"tool_name"`
	Input      json.RawMessage `json:"input,omitempty"`
	Message    string          `json:"message,omitempty"`
	RuleDetail string          `json:"rule_detail,omitempty"`
	Prompt     string          `json:"prompt,omitempty"`
	Masked     bool            `json:"masked,omitempty"`
	AgentType  string          `json:"agent_type,omitempty"`
}
