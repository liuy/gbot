package wui

// Mutation-kill tests for batch A2 (connector.go escapes reported in
// /tmp/wui-batch-a2.txt). Each test targets a mutation site that existing
// coverage missed; assertions pin the exact production behavior so the
// mutated copy fails. Tests intentionally avoid t.Parallel because some
// swap the package-wide slog default.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

// mutA2Tool is a configurable tool.Tool. desc/descErr drive Description;
// renderFn drives RenderResult.
type mutA2Tool struct {
	name    string
	desc    string
	descErr bool
	render  func(any) string
}

func (t *mutA2Tool) Name() string      { return t.name }
func (t *mutA2Tool) Aliases() []string { return nil }
func (t *mutA2Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (t *mutA2Tool) Description(json.RawMessage) (string, error) {
	if t.descErr {
		return "", errors.New("muta2 desc error")
	}
	return t.desc, nil
}
func (t *mutA2Tool) RenderResult(data any) string {
	if t.render != nil {
		return t.render(data)
	}
	return fmt.Sprintf("RENDER:%v", data)
}
func (t *mutA2Tool) Call(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
	return nil, nil
}
func (t *mutA2Tool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (t *mutA2Tool) IsReadOnly(json.RawMessage) bool           { return true }
func (t *mutA2Tool) IsDestructive(json.RawMessage) bool        { return false }
func (t *mutA2Tool) IsConcurrencySafe(json.RawMessage) bool    { return true }
func (t *mutA2Tool) IsEnabled() bool                           { return true }
func (t *mutA2Tool) InterruptBehavior() tool.InterruptBehavior { return 0 }
func (t *mutA2Tool) MaxResultSize() int                        { return 0 }
func (t *mutA2Tool) Prompt() string                            { return "" }

// mutA2FullTool additionally implements ToolWithSearchOrRead and
// ToolWithDecodeResult: DecodeResult unwraps the wire array and RenderResult
// echoes the inner text verbatim.
type mutA2FullTool struct {
	mutA2Tool
	srk     tool.SearchReadKind
	decoded func(string) string
}

func (t *mutA2FullTool) IsSearchOrRead(json.RawMessage) tool.SearchReadKind { return t.srk }
func (t *mutA2FullTool) DecodeResult(raw json.RawMessage) (any, error) {
	text, err := tool.UnmarshalSingleBlock(raw)
	if err != nil {
		return nil, err
	}
	return text, nil
}
func (t *mutA2FullTool) RenderResult(data any) string {
	s, ok := data.(string)
	if !ok {
		return "FALLBACK"
	}
	if t.decoded != nil {
		return t.decoded(s)
	}
	return s
}

// mutA2LLMProvider records the ctx of the last Complete/Stream call.
type mutA2LLMProvider struct {
	name   string
	gotCtx context.Context
}

func (p *mutA2LLMProvider) Name() string { return p.name }
func (p *mutA2LLMProvider) Complete(ctx context.Context, _ *llm.Request) (*llm.Response, error) {
	p.gotCtx = ctx
	return nil, errors.New("muta2 provider boom")
}
func (p *mutA2LLMProvider) Stream(ctx context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	p.gotCtx = ctx
	return nil, errors.New("muta2 provider boom")
}

// mutA2WarnHandler counts slog records; mirrors outqueue_test.go's pattern.
type mutA2WarnHandler struct {
	records []string
}

func (h *mutA2WarnHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *mutA2WarnHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r.Message)
	return nil
}
func (h *mutA2WarnHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *mutA2WarnHandler) WithGroup(string) slog.Handler      { return h }

func swapMutA2WarnHandler(t *testing.T) *mutA2WarnHandler {
	t.Helper()
	h := &mutA2WarnHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(old) })
	return h
}

// mutA2HistPayload mirrors buildHistory/buildPreCompactHistory output JSON.
type mutA2HistPayload struct {
	Type       string           `json:"type"`
	Messages   []historyChatMsg `json:"messages"`
	NextCursor string           `json:"nextCursor"`
	HasMore    bool             `json:"hasMore"`
}

func mutA2UserMsg(id, text string) types.Message {
	return types.Message{
		ID:        id,
		Role:      types.RoleUser,
		Timestamp: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
		Content:   []types.ContentBlock{types.NewTextBlock(text)},
	}
}

func mutA2BareConnector() *WUIConnector {
	c := &WUIConnector{
		pendingAsks: make(map[string]*types.AskEvent),
		providers:   map[string]llm.Provider{},
		done:        make(chan struct{}),
		thumbs:      newThumbCache(),
	}
	c.outQ.Store(newOutQueue(16))
	return c
}

// ---------------------------------------------------------------------------
// engineAdapter delegation (connector.go:173-221)
// ---------------------------------------------------------------------------

func TestMutA2EngineAdapterNewSession(t *testing.T) {
	// No store: the underlying engine errors; the adapter must surface it.
	noStore := engine.New(&engine.Params{EngineID: "ea-nos", Model: "m"})
	noStore.SetDispatcher(hub.NewHub())
	a := &engineAdapter{eng: noStore}
	if _, err := a.NewSession(); err == nil || !strings.Contains(err.Error(), "no store") {
		t.Fatalf("NewSession() without store: err = %v, want %q", err, "engine: no store")
	}

	// With a store: a fresh non-empty session id is returned.
	store, err := short.NewStore(t.TempDir() + "/ea.db")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	eng := engine.New(&engine.Params{EngineID: "ea-ok", Model: "m"})
	eng.SetDispatcher(hub.NewHub())
	eng.SetStore(store, "/proj")
	b := &engineAdapter{eng: eng}
	sid, err := b.NewSession()
	if err != nil {
		t.Fatalf("NewSession() with store: %v", err)
	}
	if sid == "" {
		t.Fatal("NewSession() returned empty session id")
	}
	if sid != eng.SessionID() {
		t.Errorf("NewSession() = %q, want engine SessionID() %q", sid, eng.SessionID())
	}
}

func TestMutA2EngineAdapterTitleNoStore(t *testing.T) {
	eng := engine.New(&engine.Params{EngineID: "ea-t", Model: "m"})
	eng.SetDispatcher(hub.NewHub())
	a := &engineAdapter{eng: eng}
	err := a.UpdateSessionTitle("s1", "title")
	if err == nil || !strings.Contains(err.Error(), "no store") {
		t.Fatalf("UpdateSessionTitle without store: err = %v, want %q", err, "engine: no store")
	}
}

func TestMutA2EngineAdapterTitleError(t *testing.T) {
	store, err := short.NewStore(t.TempDir() + "/ea.db")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	eng := engine.New(&engine.Params{EngineID: "ea-te", Model: "m"})
	eng.SetDispatcher(hub.NewHub())
	eng.SetStore(store, "/proj")
	// Closed store: every title update must error out through the adapter.
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	a := &engineAdapter{eng: eng}
	if err := a.UpdateSessionTitle("missing-session", "t"); err == nil {
		t.Fatal("UpdateSessionTitle on closed store: err = nil, want non-nil")
	}
}

func TestMutA2EngineAdapterGetters(t *testing.T) {
	eng := engine.New(&engine.Params{EngineID: "ea-g", Model: "m"})
	eng.SetDispatcher(hub.NewHub())
	a := &engineAdapter{eng: eng}
	a.UpdateAutoCompactConfig(engine.AutoCompactConfig{ContextWindow: 200000})
	eng.SetContextTokens(4321)
	if got := a.ContextWindow(); got != 200000 {
		t.Errorf("ContextWindow() = %d, want 200000", got)
	}
	if got := a.GetContextTokens(); got != 4321 {
		t.Errorf("GetContextTokens() = %d, want 4321", got)
	}
	bd := a.ContextBreakdown()
	if bd == nil {
		t.Fatal("ContextBreakdown() = nil, want non-nil breakdown")
	}
	if bd.ContextWindow != 200000 {
		t.Errorf("breakdown.ContextWindow = %d, want 200000", bd.ContextWindow)
	}
	if bd.TotalTokens != 4321 {
		t.Errorf("breakdown.TotalTokens = %d, want 4321", bd.TotalTokens)
	}
}

func TestMutA2EngineAdapterSetters(t *testing.T) {
	eng := engine.New(&engine.Params{EngineID: "ea-s", Model: "m0"})
	eng.SetDispatcher(hub.NewHub())
	a := &engineAdapter{eng: eng}

	fp := &mutA2LLMProvider{name: "muta2p"}
	a.SetProvider(fp)
	if a.Provider() != fp {
		t.Error("Provider() after SetProvider does not return the same provider")
	}
	a.SetModel("m2")
	if eng.Model() != "m2" {
		t.Errorf("engine Model() = %q, want m2", eng.Model())
	}
	a.SetMaxTokens(555)
	if eng.MaxTokens() != 555 {
		t.Errorf("engine MaxTokens() = %d, want 555", eng.MaxTokens())
	}
	a.SetInputModalities([]string{"text", "image"})
	if !eng.SupportsModality("image") {
		t.Error("SupportsModality(image) = false after SetInputModalities")
	}
	if err := a.SetThinking(llm.EffortHigh); err != nil {
		t.Fatalf("SetThinking(high): %v", err)
	}
	if a.Thinking() != llm.EffortHigh {
		t.Errorf("Thinking() = %q, want high", a.Thinking())
	}
	if err := a.SetThinking(""); err == nil {
		t.Error("SetThinking(\"\") = nil error, want engine's empty-effort error")
	}
	a.UpdateAutoCompactConfig(engine.AutoCompactConfig{ContextWindow: 128000})
	if a.ContextWindow() != 128000 {
		t.Errorf("ContextWindow() = %d after UpdateAutoCompactConfig, want 128000", a.ContextWindow())
	}
}

func TestMutA2EngineAdapterPreCompactNoStore(t *testing.T) {
	eng := engine.New(&engine.Params{EngineID: "ea-pc", Model: "m"})
	eng.SetDispatcher(hub.NewHub())
	a := &engineAdapter{eng: eng}
	page, total, hasBoundary := a.PreCompactMessages(0, 30)
	if page != nil || total != 0 || hasBoundary {
		t.Errorf("PreCompactMessages without store = (%v, %d, %v), want (nil, 0, false)", page, total, hasBoundary)
	}
}

func TestMutA2EngineAdapterManualCompactCtx(t *testing.T) {
	store, err := short.NewStore(t.TempDir() + "/ea.db")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ses, err := store.CreateSessionWithEngine("/proj", "test-model", "ea-mc")
	if err != nil {
		t.Fatalf("CreateSessionWithEngine: %v", err)
	}
	// Two large messages so the compactor's tail budget is exceeded and the
	// summarize path reaches the provider; content is irrelevant.
	for i := range 2 {
		em := types.Message{
			ID:        fmt.Sprintf("big-%d", i),
			Role:      types.RoleUser,
			Timestamp: time.Date(2026, 3, 1, 10, 0, i, 0, time.UTC),
			Content:   []types.ContentBlock{types.NewTextBlock(strings.Repeat("x", 200*1024))},
		}
		ts, err := short.EngineMessagesToStore([]types.Message{em})
		if err != nil {
			t.Fatalf("EngineMessagesToStore: %v", err)
		}
		if err := store.AppendMessage(ses.SessionID, ts[0]); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	fp := &mutA2LLMProvider{name: "muta2p"}
	eng := engine.New(&engine.Params{
		EngineID:    "ea-mc",
		Model:       "test-model",
		Provider:    fp,
		AutoCompact: engine.AutoCompactConfig{ContextWindow: 100000},
	})
	eng.SetDispatcher(hub.NewHub())
	eng.SetStore(store, "/proj")
	if _, err := eng.SwitchSession(ses.SessionID); err != nil {
		t.Fatalf("SwitchSession: %v", err)
	}
	a := &engineAdapter{eng: eng}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The summarize call fails at the provider; the failure itself proves the
	// provider was reached — the contract under test is that the caller's ctx
	// is the one forwarded.
	_, mcErr := a.ManualCompact(ctx, mutA2UserMsg("u", "/compact"), "")
	if mcErr == nil || !strings.Contains(mcErr.Error(), "summarize") {
		t.Fatalf("ManualCompact err = %v, want summarize failure from the fake provider", mcErr)
	}
	if fp.gotCtx == nil {
		t.Fatal("ManualCompact forwarded nil ctx to the provider")
	}
}

// ---------------------------------------------------------------------------
// handleAsk marshal failure (connector.go:1700)
// ---------------------------------------------------------------------------

func TestMutA2HandleAskMarshalFailure(t *testing.T) {
	c := mutA2BareConnector()
	h := swapMutA2WarnHandler(t)
	// Invalid RawMessage JSON makes json.Marshal(askOutbound) fail; nothing
	// may be queued on the WS in that case, and the failure is logged.
	evt := hub.Event{Ask: &types.AskEvent{
		Kind:       types.AskPermission,
		ToolName:   "Bash",
		Input:      json.RawMessage(`{invalid`),
		Message:    "run?",
		ResponseCh: make(chan types.AskResponse, 1),
	}}
	c.handleAsk(evt)
	if got := c.outQ.Load().len(); got != 0 {
		t.Fatalf("outQ len after failed marshal = %d, want 0", got)
	}
	if len(h.records) != 1 || h.records[0] != "wui: marshal ask failed" {
		t.Errorf("warn records = %v, want one wui: marshal ask failed", h.records)
	}
	// Control: a well-formed ask queues exactly one frame.
	evt.Ask.Input = json.RawMessage(`{"command":"ls"}`)
	c.handleAsk(evt)
	if got := c.outQ.Load().len(); got != 1 {
		t.Fatalf("outQ len after valid ask = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// preCompactMessages (connector.go:1748-1778)
// ---------------------------------------------------------------------------

func TestMutA2PreCompactLoadError(t *testing.T) {
	store := seedPreCompactStore(t, "s1", 3, false, 0)
	// NULL parent_uuid breaks row scanning only for the non-boundary rows,
	// so GetLastBoundary still succeeds while LoadMessagesBeforeSeq fails.
	if _, err := store.DB().Exec("UPDATE messages SET parent_uuid = NULL WHERE uuid = 'pre-1'"); err != nil {
		t.Fatalf("corrupt row: %v", err)
	}
	page, total, hasBoundary := preCompactMessages(store, "s1", 0, 10)
	if page != nil || total != 0 || hasBoundary {
		t.Errorf("preCompactMessages on scan error = (%v, %d, %v), want (nil, 0, false)", page, total, hasBoundary)
	}
}

func mutA2RawRow(t *testing.T, store *short.Store, sid, uuid, typ, subtype, content, metadata string) {
	t.Helper()
	row := &short.TranscriptMessage{
		UUID:      uuid,
		Type:      typ,
		Subtype:   subtype,
		Content:   content,
		Metadata:  metadata,
		CreatedAt: time.Date(2026, 3, 2, 8, 0, 0, 0, time.UTC),
	}
	if err := store.AppendMessage(sid, row); err != nil {
		t.Fatalf("AppendMessage %s: %v", uuid, err)
	}
}

func mutA2Boundary(t *testing.T, store *short.Store, sid, lastUUID string) {
	t.Helper()
	b := short.CreateCompactBoundaryMessage("auto", 1000, lastUUID)
	if err := store.AppendMessage(sid, b); err != nil {
		t.Fatalf("AppendMessage boundary: %v", err)
	}
}

func mutA2StoreSession(t *testing.T) (*short.Store, string) {
	t.Helper()
	store, err := short.NewStore(t.TempDir() + "/muta2.db")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sid := "muta2-s"
	if _, err := store.DB().Exec("INSERT INTO sessions (session_id, project_dir) VALUES (?, '')", sid); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	return store, sid
}

func mutA2UUIDs(page []*short.TranscriptMessage) []string {
	out := make([]string, len(page))
	for i, m := range page {
		out[i] = m.UUID
	}
	return out
}

func TestMutA2PreCompactAttachmentDropped(t *testing.T) {
	store, sid := mutA2StoreSession(t)
	mutA2RawRow(t, store, sid, "u0", "user", "", `[{"type":"text","text":"a"}]`, "")
	mutA2RawRow(t, store, sid, "att", "attachment", "", `[{"type":"text","text":"skip"}]`, "")
	mutA2RawRow(t, store, sid, "u1", "user", "", `[{"type":"text","text":"b"}]`, "")
	mutA2Boundary(t, store, sid, "u1")

	page, total, hasBoundary := preCompactMessages(store, sid, 0, 10)
	if !hasBoundary || total != 2 {
		t.Fatalf("total = %d, hasBoundary = %v; want 2, true (attachment excluded)", total, hasBoundary)
	}
	if got := mutA2UUIDs(page); len(got) != 2 || got[0] != "u0" || got[1] != "u1" {
		t.Errorf("page = %v, want [u0 u1]", got)
	}
}

func TestMutA2PreCompactSystemInfoDropped(t *testing.T) {
	store, sid := mutA2StoreSession(t)
	mutA2RawRow(t, store, sid, "sys0", "system", "informational", `[{"type":"text","text":"sys"}]`, "")
	mutA2RawRow(t, store, sid, "u0", "user", "", `[{"type":"text","text":"keep"}]`, "")
	mutA2Boundary(t, store, sid, "u0")

	page, total, hasBoundary := preCompactMessages(store, sid, 0, 10)
	if !hasBoundary || total != 1 {
		t.Fatalf("total = %d, hasBoundary = %v; want 1, true (system informational excluded)", total, hasBoundary)
	}
	if got := mutA2UUIDs(page); len(got) != 1 || got[0] != "u0" {
		t.Errorf("page = %v, want [u0] (rows after the informational row survive)", got)
	}
}

func TestMutA2PreCompactMetaFlagDropped(t *testing.T) {
	store, sid := mutA2StoreSession(t)
	metaMsg := types.Message{
		ID:      "meta-u",
		Role:    types.RoleUser,
		Flags:   types.FlagMeta,
		Content: []types.ContentBlock{types.NewTextBlock("hidden")},
	}
	ts, err := short.EngineMessagesToStore([]types.Message{metaMsg})
	if err != nil {
		t.Fatalf("EngineMessagesToStore: %v", err)
	}
	if err := store.AppendMessage(sid, ts[0]); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	mutA2RawRow(t, store, sid, "u0", "user", "", `[{"type":"text","text":"keep"}]`, "")
	// Plain metadata without any flag: hasFilteredFlag must keep this row.
	mutA2RawRow(t, store, sid, "u1", "user", "", `[{"type":"text","text":"plain-meta"}]`, `{}`)
	mutA2Boundary(t, store, sid, "u1")

	page, total, hasBoundary := preCompactMessages(store, sid, 0, 10)
	if !hasBoundary || total != 2 {
		t.Fatalf("total = %d, hasBoundary = %v; want 2, true (FlagMeta row excluded, plain-metadata row kept)", total, hasBoundary)
	}
	if got := mutA2UUIDs(page); len(got) != 2 || got[0] != "u0" || got[1] != "u1" {
		t.Errorf("page = %v, want [u0 u1]", got)
	}
}

func TestMutA2PreCompactDeliveredEqualsTotal(t *testing.T) {
	store := seedPreCompactStore(t, "s1", 3, false, 0)
	page, total, hasBoundary := preCompactMessages(store, "s1", 3, 10)
	if !hasBoundary || total != 3 {
		t.Fatalf("total = %d, hasBoundary = %v; want 3, true", total, hasBoundary)
	}
	if page != nil {
		t.Errorf("page = %v, want nil sentinel when delivered == total", mutA2UUIDs(page))
	}
}

// ---------------------------------------------------------------------------
// buildHistoryChatMsg (connector.go:1804-1917)
// ---------------------------------------------------------------------------

func TestMutA2HistoryChatMsgBasics(t *testing.T) {
	c := mutA2BareConnector()
	ts := time.Date(2026, 3, 4, 9, 30, 0, 0, time.UTC)
	m := types.Message{
		ID:        "m1",
		Role:      types.RoleUser,
		Timestamp: ts,
		Content: []types.ContentBlock{
			types.NewTextBlock("a"),
			types.NewTextBlock("b"),
		},
	}
	hm := c.buildHistoryChatMsg(m, nil, nil, nil)
	if hm.ID != "m1" || hm.Role != "user" {
		t.Errorf("id/role = %q/%q, want m1/user", hm.ID, hm.Role)
	}
	if hm.StartedAt != ts.UnixMilli() {
		t.Errorf("startedAt = %d, want %d", hm.StartedAt, ts.UnixMilli())
	}
	if hm.Status != "done" {
		t.Errorf("status = %q, want done", hm.Status)
	}
	if hm.Text != "ab" {
		t.Errorf("text = %q, want concatenated ab", hm.Text)
	}
	if len(hm.Blocks) != 2 {
		t.Fatalf("blocks len = %d, want 2", len(hm.Blocks))
	}
	for i, want := range []string{"a", "b"} {
		if hm.Blocks[i].Kind != "text" || hm.Blocks[i].Text != want {
			t.Errorf("blocks[%d] = %s/%q, want text/%q", i, hm.Blocks[i].Kind, hm.Blocks[i].Text, want)
		}
	}
}

func TestMutA2HistoryChatMsgAPIError(t *testing.T) {
	c := mutA2BareConnector()
	doc := types.ContentBlock{Type: types.ContentTypeDocument, Name: "d.md", Mime: "text/markdown", Size: 12}
	m := types.Message{
		ID:      "e1",
		Role:    types.RoleAssistant,
		Flags:   types.FlagAPIError,
		Content: []types.ContentBlock{types.NewTextBlock("boom1"), types.NewTextBlock("boom2"), doc},
	}
	hm := c.buildHistoryChatMsg(m, nil, nil, nil)
	if hm.Error != "boom1boom2" {
		t.Errorf("error = %q, want concatenated boom1boom2", hm.Error)
	}
	if hm.Text != "" {
		t.Errorf("text = %q, want empty (flagged text never lands in Text)", hm.Text)
	}
	if len(hm.Blocks) != 1 || hm.Blocks[0].Kind != "document" {
		t.Errorf("blocks = %+v, want only the document block after the flagged texts", hm.Blocks)
	}
}

func TestMutA2HistoryChatMsgWhitespaceText(t *testing.T) {
	c := mutA2BareConnector()
	m := types.Message{
		ID:   "w1",
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("  \n\t"),
			types.NewTextBlock("real"),
		},
	}
	hm := c.buildHistoryChatMsg(m, nil, nil, nil)
	if hm.Text != "  \n\treal" {
		t.Errorf("text = %q, want legacy whitespace preserved", hm.Text)
	}
	if len(hm.Blocks) != 1 || hm.Blocks[0].Text != "real" {
		t.Errorf("blocks = %+v, want exactly one block (whitespace-only text consumes no slot)", hm.Blocks)
	}
}

func TestMutA2HistoryChatMsgImagePlaceholder(t *testing.T) {
	c := mutA2BareConnector()
	m := types.Message{
		ID:   "i1",
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeImage, Source: nil},
			{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "file", Path: "/tmp/dir/foo.png"}},
			{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "file"}},
			types.NewTextBlock("hi"),
		},
	}
	hm := c.buildHistoryChatMsg(m, nil, nil, nil)
	wantText := "[image][image: foo.png][image]hi"
	if hm.Text != wantText {
		t.Errorf("text = %q, want %q", hm.Text, wantText)
	}
	wantBlocks := []struct{ kind, text string }{
		{"text", "[image]"},
		{"text", "[image: foo.png]"},
		{"text", "[image]"},
		{"text", "hi"},
	}
	if len(hm.Blocks) != len(wantBlocks) {
		t.Fatalf("blocks len = %d, want %d (%+v)", len(hm.Blocks), len(wantBlocks), hm.Blocks)
	}
	for i, w := range wantBlocks {
		if hm.Blocks[i].Kind != w.kind || hm.Blocks[i].Text != w.text {
			t.Errorf("blocks[%d] = %s/%q, want %s/%q", i, hm.Blocks[i].Kind, hm.Blocks[i].Text, w.kind, w.text)
		}
	}

	// Degraded image followed by a document: both blocks survive.
	m2 := types.Message{
		ID:   "i2",
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeImage, Source: nil},
			{Type: types.ContentTypeDocument, Name: "n", Mime: "text/plain", Size: 1},
		},
	}
	hm2 := c.buildHistoryChatMsg(m2, nil, nil, nil)
	if len(hm2.Blocks) != 2 || hm2.Blocks[0].Kind != "text" || hm2.Blocks[1].Kind != "document" {
		t.Errorf("blocks = %+v, want [text placeholder, document]", hm2.Blocks)
	}
}

func TestMutA2HistoryChatMsgThinking(t *testing.T) {
	c := mutA2BareConnector()
	blank := types.ContentBlock{Type: types.ContentTypeThinking, Thinking: "   "}
	deep := types.ContentBlock{Type: types.ContentTypeThinking, Thinking: "deep thought", ThinkingDurationNs: 77}
	hm := c.buildHistoryChatMsg(types.Message{ID: "t1", Role: types.RoleAssistant, Content: []types.ContentBlock{blank, deep}}, nil, nil, nil)
	if len(hm.Thinking) != 1 {
		t.Fatalf("thinking entries = %d, want 1 (whitespace-only skipped)", len(hm.Thinking))
	}
	if hm.Thinking[0].Text != "deep thought" || hm.Thinking[0].DurationNs != 77 {
		t.Errorf("thinking[0] = %+v, want {deep thought 77}", hm.Thinking[0])
	}
	if len(hm.Blocks) != 1 || hm.Blocks[0].Kind != "thinking" || hm.Blocks[0].Thinking == nil || hm.Blocks[0].Thinking.DurationNs != 77 {
		t.Errorf("blocks = %+v, want one thinking block with duration 77", hm.Blocks)
	}
}

func TestMutA2HistoryChatMsgToolUse(t *testing.T) {
	c := mutA2BareConnector()
	tools := map[string]tool.Tool{
		"X": &mutA2FullTool{
			mutA2Tool: mutA2Tool{name: "X", desc: "SUM-X"},
			srk:       tool.SearchReadKind{IsSearch: true, IsRead: true, IsList: true, IsLsp: true, IsTask: true},
		},
	}
	wire := json.RawMessage(`[{"type":"text","text":"WIRE"}]`)
	toolResults := map[string]types.ContentBlock{
		"tu1": {Type: types.ContentTypeToolResult, ToolUseID: "tu1", Content: wire, IsError: true, ToolDurationNs: 99},
	}
	doneMsg := types.Message{
		ID:   "tu-done",
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "tu1", Name: "X", Input: json.RawMessage(`{}`)},
		},
	}
	hm := c.buildHistoryChatMsg(doneMsg, tools, toolResults, nil)
	if len(hm.Tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(hm.Tools))
	}
	e := hm.Tools[0]
	if e.ID != "tu1" || e.Name != "X" {
		t.Errorf("entry id/name = %q/%q, want tu1/X", e.ID, e.Name)
	}
	if e.Summary != "SUM-X" {
		t.Errorf("summary = %q, want SUM-X", e.Summary)
	}
	if !e.IsSearch || !e.IsRead || !e.IsList || !e.IsLsp || !e.IsTask {
		t.Errorf("srk flags = %+v, want all true", e)
	}
	if !e.IsError {
		t.Error("IsError = false, want true from the tool_result block")
	}
	if e.IsRunning {
		t.Error("IsRunning = true, want false (result present)")
	}
	if e.DisplayOutput != "WIRE" {
		t.Errorf("displayOutput = %q, want WIRE", e.DisplayOutput)
	}
	if e.DurationNs != 99 {
		t.Errorf("durationNs = %d, want 99", e.DurationNs)
	}
	if len(hm.Blocks) != 1 || hm.Blocks[0].Kind != "tool" || hm.Blocks[0].Tool == nil || hm.Blocks[0].Tool.ID != "tu1" {
		t.Errorf("blocks = %+v, want one tool block for tu1", hm.Blocks)
	}

	// No result yet: the entry must flip to running.
	runningMsg := types.Message{
		ID:   "tu-run",
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "tu2", Name: "X", Input: json.RawMessage(`{}`)},
		},
	}
	hm2 := c.buildHistoryChatMsg(runningMsg, tools, toolResults, nil)
	if len(hm2.Tools) != 1 || !hm2.Tools[0].IsRunning {
		t.Errorf("running entry = %+v, want IsRunning true", hm2.Tools)
	}
}

func TestMutA2HistoryChatMsgUsage(t *testing.T) {
	c := mutA2BareConnector()
	m := types.Message{
		ID:    "us1",
		Role:  types.RoleAssistant,
		Usage: &types.Usage{InputTokens: 11, OutputTokens: 22, CacheReadInputTokens: 33, CacheCreationInputTokens: 44},
	}
	hm := c.buildHistoryChatMsg(m, nil, nil, nil)
	want := historyUsage{InputTokens: 11, OutputTokens: 22, CacheRead: 33, CacheCreation: 44}
	if hm.Usage != want {
		t.Errorf("usage = %+v, want %+v", hm.Usage, want)
	}
	// Usage absent: stays zero.
	hm2 := c.buildHistoryChatMsg(mutA2UserMsg("us0", "x"), nil, nil, nil)
	if hm2.Usage != (historyUsage{}) {
		t.Errorf("usage without Usage block = %+v, want zero", hm2.Usage)
	}
}

// ---------------------------------------------------------------------------
// historyImageDataURL (connector.go:1925-1972)
// ---------------------------------------------------------------------------

func mutA2PNG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	var buf strings.Builder
	if err := png.Encode(&mutBufWriter{&buf}, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return []byte(buf.String())
}

type mutBufWriter struct{ b *strings.Builder }

func (w *mutBufWriter) Write(p []byte) (int, error) { return w.b.Write(p) }

func TestMutA2ImageDataURLNilSource(t *testing.T) {
	c := mutA2BareConnector()
	url, ok := c.historyImageDataURL(types.ContentBlock{Type: types.ContentTypeImage})
	if ok || url != "" {
		t.Errorf("nil source = (%q, %v), want (\"\", false) without panic", url, ok)
	}
	// Unknown source type and undecodable base64 both degrade to (\"\", false).
	weird := types.ContentBlock{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "carrier-pigeon"}}
	if url, ok := c.historyImageDataURL(weird); ok || url != "" {
		t.Errorf("unknown source type = (%q, %v), want (\"\", false)", url, ok)
	}
	badB64 := types.ContentBlock{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "base64", Data: "!!!not-base64!!!"}}
	if url, ok := c.historyImageDataURL(badB64); ok || url != "" {
		t.Errorf("bad base64 = (%q, %v), want (\"\", false)", url, ok)
	}
	// Missing file also degrades cleanly.
	missing := types.ContentBlock{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "file", Path: "/nonexistent/muta2.png"}}
	if url, ok := c.historyImageDataURL(missing); ok || url != "" {
		t.Errorf("missing file = (%q, %v), want (\"\", false)", url, ok)
	}
	// Valid base64 source round-trips and stays stable across two reads
	// (second read may come from the cache — the value must not change).
	b64 := base64.StdEncoding.EncodeToString(mutA2PNG(t, 300, 200, color.RGBA{R: 200, A: 255}))
	cb := types.ContentBlock{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "base64", Data: b64}}
	url1, ok := c.historyImageDataURL(cb)
	if !ok || !strings.HasPrefix(url1, "data:image/png;base64,") {
		t.Fatalf("base64 read = (%.40s, %v), want passthrough data URL", url1, ok)
	}
	url2, ok := c.historyImageDataURL(cb)
	if !ok || url2 != url1 {
		t.Errorf("second base64 read = (%.40s, %v), want identical (%.40s, true)", url2, ok, url1)
	}
}

func TestMutA2ImageDataURLFileCache(t *testing.T) {
	c := mutA2BareConnector()
	path := filepath.Join(t.TempDir(), "img.png")
	if err := os.WriteFile(path, mutA2PNG(t, 800, 600, color.RGBA{R: 255, A: 255}), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	mtime := fi.ModTime()

	cb := types.ContentBlock{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "file", Path: path}}
	url1, ok := c.historyImageDataURL(cb)
	if !ok || !strings.HasPrefix(url1, "data:") {
		t.Fatalf("first read = (%q, %v), want data URL", url1, ok)
	}

	// Same path, same mtime, different bytes: the cache must still hit.
	if err := os.WriteFile(path, mutA2PNG(t, 800, 600, color.RGBA{B: 255, A: 255}), 0o644); err != nil {
		t.Fatalf("WriteFile 2: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	url2, ok := c.historyImageDataURL(cb)
	if !ok {
		t.Fatal("second read ok = false, want true")
	}
	if url2 != url1 {
		t.Errorf("cache key must include mtime: url2 = %.60s..., want identical to url1", url2)
	}

	// A bumped mtime invalidates the entry.
	later := mtime.Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatalf("Chtimes 2: %v", err)
	}
	url3, ok := c.historyImageDataURL(cb)
	if !ok {
		t.Fatal("third read ok = false, want true")
	}
	if url3 == url1 {
		t.Error("url after mtime bump = url1; changed content must produce a new thumbnail")
	}
}

func TestMutA2ImageDataURLThumbSize(t *testing.T) {
	c := mutA2BareConnector()
	path := filepath.Join(t.TempDir(), "big.png")
	if err := os.WriteFile(path, mutA2PNG(t, 1026, 1026, color.RGBA{G: 255, A: 255}), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	url, ok := c.historyImageDataURL(types.ContentBlock{
		Type:   types.ContentTypeImage,
		Source: &types.ImageSource{Type: "file", Path: path},
	})
	if !ok {
		t.Fatal("ok = false, want true")
	}
	const prefix = "data:image/jpeg;base64,"
	if !strings.HasPrefix(url, prefix) {
		t.Fatalf("url prefix = %.40s, want %q", url, prefix)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, prefix))
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	img, err := jpeg.Decode(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("jpeg decode: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != 512 || b.Dy() != 512 {
		t.Errorf("thumbnail bounds = %dx%d, want exactly 512x512", b.Dx(), b.Dy())
	}
}

// ---------------------------------------------------------------------------
// isCompactBoundaryMarker (connector.go:1985-1992)
// ---------------------------------------------------------------------------

func TestMutA2IsCompactBoundaryMarker(t *testing.T) {
	marker := types.Message{Role: types.RoleSystem, Content: []types.ContentBlock{
		types.NewTextBlock(`{"subtype":"compact_boundary"}`),
	}}
	if !isCompactBoundaryMarker(marker) {
		t.Error("marker text block not detected")
	}
	if isCompactBoundaryMarker(mutA2UserMsg("p", "plain text")) {
		t.Error("plain text message misclassified as boundary marker")
	}
	thinkingOnly := types.Message{Role: types.RoleSystem, Content: []types.ContentBlock{
		{Type: types.ContentTypeThinking, Thinking: `"compact_boundary"`},
	}}
	if isCompactBoundaryMarker(thinkingOnly) {
		t.Error("marker string inside a thinking block must not count")
	}
	// The type gate is on cb.Type: a non-text block whose Text FIELD carries
	// the marker must not be misread as a boundary marker.
	toolWithMarkerText := types.Message{Role: types.RoleSystem, Content: []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "t", Name: "X", Text: `"compact_boundary"`},
	}}
	if isCompactBoundaryMarker(toolWithMarkerText) {
		t.Error("marker string in a tool_use block's Text field must not count")
	}
}

// ---------------------------------------------------------------------------
// buildHistory (connector.go:1994-2103)
// ---------------------------------------------------------------------------

func mutA2Slot(mock *mockEngine) *engineSlot {
	return &engineSlot{engineID: "muta2", engine: mock}
}

func mutA2ParseHistory(t *testing.T, payload []byte) mutA2HistPayload {
	t.Helper()
	var out mutA2HistPayload
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("unmarshal history payload: %v (raw: %.200s)", err, payload)
	}
	return out
}

func TestMutA2BuildHistoryNilSlot(t *testing.T) {
	c := mutA2BareConnector()
	if got := c.buildHistory(nil, "", 30); got != nil {
		t.Errorf("buildHistory(nil) = %.40s, want nil", got)
	}
}

func TestMutA2BuildHistoryQueryStart(t *testing.T) {
	c := mutA2BareConnector()
	msgs := []types.Message{mutA2UserMsg("m0", "q"), mutA2UserMsg("m1", "a"), mutA2UserMsg("m2", "q2")}
	mock := &mockEngine{}
	mock.SetMessagesFn(func() []types.Message { return msgs })

	// Boundary: query starts at index 0 — exactly one message survives.
	mock.SetQueryStartMsgIdxFn(func() int { return 0 })
	p := mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if len(p.Messages) != 1 || p.Messages[0].Text != "q" {
		t.Errorf("messages after truncation = %+v, want only the query at idx 0", p.Messages)
	}

	// Boundary: idx == len(msgs) must not panic and must keep all messages.
	mock.SetQueryStartMsgIdxFn(func() int { return len(msgs) })
	p = mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if len(p.Messages) != 3 {
		t.Errorf("messages with idx == len = %d, want 3 (no truncation)", len(p.Messages))
	}
}

func TestMutA2BuildHistoryEmptyAfterFilter(t *testing.T) {
	c := mutA2BareConnector()
	mock := &mockEngine{}
	sys := types.Message{
		ID:      "sys",
		Role:    types.RoleSystem,
		Content: []types.ContentBlock{types.NewTextBlock("informational")},
	}
	mock.SetMessagesFn(func() []types.Message { return []types.Message{sys} })
	if got := c.buildHistory(mutA2Slot(mock), "", 30); got != nil {
		t.Errorf("buildHistory(all filtered) = %.60s, want nil payload", got)
	}
	// No messages at all: nil payload, not an empty history frame.
	mock.SetMessagesFn(func() []types.Message { return nil })
	if got := c.buildHistory(mutA2Slot(mock), "", 30); got != nil {
		t.Errorf("buildHistory(no messages) = %.60s, want nil payload", got)
	}
}

func TestMutA2BuildHistoryFilters(t *testing.T) {
	c := mutA2BareConnector()
	mock := &mockEngine{}
	sys := types.Message{ID: "sys", Role: types.RoleSystem, Content: []types.ContentBlock{types.NewTextBlock("sysinfo")}}
	meta := mutA2UserMsg("meta", "hidden")
	meta.Flags = types.FlagMeta
	ok := mutA2UserMsg("ok", "visible")
	// Visible message last: a break (instead of continue) at either filter
	// would drop it and collapse the payload to nil.
	mock.SetMessagesFn(func() []types.Message { return []types.Message{sys, meta, ok} })
	p := mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if len(p.Messages) != 1 || p.Messages[0].ID != "ok" || p.Messages[0].Text != "visible" {
		t.Errorf("messages = %+v, want only the unflagged user message", p.Messages)
	}
}

func TestMutA2BuildHistoryBoundaryDivider(t *testing.T) {
	c := mutA2BareConnector()
	mock := &mockEngine{}
	ts := time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)
	divider := types.Message{
		ID:        "bnd",
		Role:      types.RoleSystem,
		Timestamp: ts,
		Flags:     types.FlagCompactSummary,
		Content:   []types.ContentBlock{types.NewTextBlock(`{"subtype":"compact_boundary"}`)},
	}
	summary := types.Message{
		ID:      "sum",
		Role:    types.RoleSystem,
		Flags:   types.FlagCompactSummary,
		Content: []types.ContentBlock{types.NewTextBlock("human readable summary")},
	}
	mock.SetMessagesFn(func() []types.Message {
		return []types.Message{mutA2UserMsg("u0", "before"), divider, summary, mutA2UserMsg("u1", "after")}
	})
	p := mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if len(p.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (user, divider, user; summary skipped)", len(p.Messages))
	}
	d := p.Messages[1]
	if !d.CompactBoundary {
		t.Errorf("messages[1] = %+v, want compactBoundary divider", d)
	}
	if d.ID != "bnd" || d.Role != "system" || d.StartedAt != ts.UnixMilli() {
		t.Errorf("divider = id %q role %q startedAt %d; want bnd/system/%d", d.ID, d.Role, d.StartedAt, ts.UnixMilli())
	}
	if p.Messages[0].ID != "u0" || p.Messages[2].ID != "u1" {
		t.Errorf("message ids = %q,%q; want u0,u1", p.Messages[0].ID, p.Messages[2].ID)
	}
}

func mutA2NMessages(n int) []types.Message {
	msgs := make([]types.Message, n)
	for i := range n {
		msgs[i] = mutA2UserMsg(fmt.Sprintf("m%02d", i), fmt.Sprintf("t%02d", i))
	}
	return msgs
}

func TestMutA2BuildHistoryDefaultLimit(t *testing.T) {
	c := mutA2BareConnector()
	mock := &mockEngine{}
	msgs := mutA2NMessages(35)
	mock.SetMessagesFn(func() []types.Message { return msgs })
	p := mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "", 0))
	if len(p.Messages) != 30 {
		t.Fatalf("page len = %d, want 30 (default limit applies when limit <= 0)", len(p.Messages))
	}
	if p.Messages[0].Text != "t05" || p.Messages[29].Text != "t34" {
		t.Errorf("page bounds = %q..%q, want t05..t34", p.Messages[0].Text, p.Messages[29].Text)
	}
	if !p.HasMore || p.NextCursor != "30" {
		t.Errorf("hasMore/nextCursor = %v/%q, want true/30", p.HasMore, p.NextCursor)
	}
}

func TestMutA2BuildHistoryCursorPaging(t *testing.T) {
	c := mutA2BareConnector()
	mock := &mockEngine{}
	msgs := mutA2NMessages(35)
	mock.SetMessagesFn(func() []types.Message { return msgs })

	p := mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "5", 10))
	if len(p.Messages) != 10 {
		t.Fatalf("page len = %d, want 10", len(p.Messages))
	}
	if p.Messages[0].Text != "t20" || p.Messages[9].Text != "t29" {
		t.Errorf("page bounds = %q..%q, want t20..t29", p.Messages[0].Text, p.Messages[9].Text)
	}
	if p.NextCursor != "15" {
		t.Errorf("nextCursor = %q, want 15 (5 delivered + 10 in page)", p.NextCursor)
	}

	// Cursor "1": offset must advance by exactly one.
	p = mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "1", 0))
	if p.NextCursor != "31" {
		t.Errorf("nextCursor for cursor 1 = %q, want 31", p.NextCursor)
	}

	// Negative cursor: treated as offset 0, never a negative slice bound.
	p = mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "-3", 0))
	if len(p.Messages) != 30 || p.Messages[0].Text != "t05" {
		t.Errorf("negative cursor page = %d messages starting %q, want 30 starting t05", len(p.Messages), p.Messages[0].Text)
	}

	// Overflow cursor: Atoi errors, offset stays 0.
	p = mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "92233720368547758080", 0))
	if len(p.Messages) != 30 {
		t.Errorf("overflow cursor page len = %d, want 30", len(p.Messages))
	}
}

func TestMutA2BuildHistoryPreCompactProbe(t *testing.T) {
	c := mutA2BareConnector()
	mock := &mockEngine{}
	mock.SetMessagesFn(func() []types.Message { return mutA2NMessages(3) })
	var probeDelivered, probeLimit int
	mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
		probeDelivered, probeLimit = delivered, limit
		return nil, 0, true
	}
	p := mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if probeDelivered != 0 || probeLimit != 0 {
		t.Errorf("probe args = (%d, %d), want (0, 0): the probe must not load messages", probeDelivered, probeLimit)
	}
	if !p.HasMore || p.NextCursor != "precompact:0" {
		t.Errorf("hasMore/nextCursor = %v/%q, want true/precompact:0 when a boundary exists", p.HasMore, p.NextCursor)
	}
}

func TestMutA2BuildHistoryToolResultKey(t *testing.T) {
	c := mutA2BareConnector()
	// A text block carrying a tool_use_id must NOT satisfy a pending tool_use.
	m0 := types.Message{
		ID:   "k0",
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "hi", ToolUseID: "tuX"},
		},
	}
	m1 := types.Message{
		ID:   "k1",
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "tuX", Name: "X", Input: json.RawMessage(`{}`)},
		},
	}
	mock := &mockEngine{}
	mock.SetMessagesFn(func() []types.Message { return []types.Message{m0, m1} })
	p := mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if len(p.Messages) != 2 || len(p.Messages[1].Tools) != 1 {
		t.Fatalf("payload shape unexpected: %+v", p.Messages)
	}
	if !p.Messages[1].Tools[0].IsRunning {
		t.Errorf("tool entry = %+v, want IsRunning (text blocks never count as results)", p.Messages[1].Tools[0])
	}

	// A tool_result with an empty id cannot satisfy a tool_use with an empty id.
	m2 := types.Message{
		ID:   "k2",
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "", Content: json.RawMessage(`[{"type":"text","text":"r"}]`)},
		},
	}
	m3 := types.Message{
		ID:   "k3",
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "", Name: "X", Input: json.RawMessage(`{}`)},
		},
	}
	mock.SetMessagesFn(func() []types.Message { return []types.Message{m2, m3} })
	p = mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if len(p.Messages) != 2 || len(p.Messages[1].Tools) != 1 || !p.Messages[1].Tools[0].IsRunning {
		t.Errorf("empty-id pair = %+v, want IsRunning (empty-keyed results are not collected)", p.Messages)
	}
}

func TestMutA2BuildHistoryRichResult(t *testing.T) {
	c := mutA2BareConnector()
	tools := map[string]tool.Tool{
		"X": &mutA2FullTool{mutA2Tool: mutA2Tool{name: "X"}},
	}
	m0 := types.Message{
		ID:             "r0",
		Role:           types.RoleUser,
		ToolResultData: map[string]any{"tu1": "RICH"},
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "tu1", Content: json.RawMessage(`[{"type":"text","text":"WIRE"}]`)},
		},
	}
	m1 := types.Message{
		ID:   "r1",
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "tu1", Name: "X", Input: json.RawMessage(`{}`)},
		},
	}
	mock := &mockEngine{}
	mock.toolsFn = func() map[string]tool.Tool { return tools }
	mock.SetMessagesFn(func() []types.Message { return []types.Message{m0, m1} })
	p := mutA2ParseHistory(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if len(p.Messages) != 2 || len(p.Messages[1].Tools) != 1 {
		t.Fatalf("payload shape unexpected: %+v", p.Messages)
	}
	// WrapRichToolResult re-marshals the rich value, so the decode path sees
	// the JSON string "RICH" (quotes included) — not the wire text.
	if got := p.Messages[1].Tools[0].DisplayOutput; got != `"RICH"` {
		t.Errorf("displayOutput = %q, want rich-rendered %q", got, `"RICH"`)
	}
}

// ---------------------------------------------------------------------------
// buildPreCompactHistory (connector.go:2110-2185)
// ---------------------------------------------------------------------------

func TestMutA2PreCompactHistNilSlot(t *testing.T) {
	c := mutA2BareConnector()
	if got := c.buildPreCompactHistory(nil, "precompact:0", 30); got != nil {
		t.Errorf("buildPreCompactHistory(nil) = %.40s, want nil", got)
	}
}

func TestMutA2PreCompactHistLimitArgs(t *testing.T) {
	c := mutA2BareConnector()
	cases := []struct {
		cursor        string
		limit         int
		wantDelivered int
		wantLimit     int
	}{
		{"precompact:0", 0, 0, 30},
		{"precompact:0", 1, 0, 1},
		{"precompact:1", 0, 1, 30},
		{"precompact:3", 0, 3, 30},
		{"precompact:-3", 0, 0, 30},
		{"precompact:92233720368547758080", 0, 0, 30},
	}
	for _, tc := range cases {
		var gotDelivered, gotLimit int
		mock := &mockEngine{}
		mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
			gotDelivered, gotLimit = delivered, limit
			return nil, 0, false
		}
		c.buildPreCompactHistory(mutA2Slot(mock), tc.cursor, tc.limit)
		if gotDelivered != tc.wantDelivered || gotLimit != tc.wantLimit {
			t.Errorf("cursor %q limit %d: args = (%d, %d), want (%d, %d)",
				tc.cursor, tc.limit, gotDelivered, gotLimit, tc.wantDelivered, tc.wantLimit)
		}
	}
}

func TestMutA2PreCompactHistCursorWarn(t *testing.T) {
	c := mutA2BareConnector()
	h := swapMutA2WarnHandler(t)

	run := func(cursor string) mutA2HistPayload {
		mock := &mockEngine{}
		mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
			return nil, 0, false
		}
		return mutA2ParseHistory(t, c.buildPreCompactHistory(mutA2Slot(mock), cursor, 30))
	}

	h.records = nil
	p := run("precompact:abc")
	if len(h.records) != 1 || h.records[0] != "wui: malformed precompact cursor, treating as 0" {
		t.Errorf("warn records for malformed cursor = %v, want one wui: malformed precompact cursor record", h.records)
	}
	if p.Type != "history" {
		t.Errorf("type = %q, want history", p.Type)
	}

	h.records = nil
	run("precompact:0")
	if len(h.records) != 0 {
		t.Errorf("warn records for numeric cursor 0 = %v, want none", h.records)
	}

	h.records = nil
	run("precompact:-3")
	if len(h.records) != 0 {
		t.Errorf("warn records for negative cursor = %v, want none (Atoi succeeds)", h.records)
	}

	h.records = nil
	run("precompact:92233720368547758080")
	if len(h.records) != 1 {
		t.Errorf("warn records for overflow cursor = %v, want one", h.records)
	}
}

func TestMutA2PreCompactHistVanished(t *testing.T) {
	c := mutA2BareConnector()
	mock := &mockEngine{}
	mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
		return nil, 0, false
	}
	payload := c.buildPreCompactHistory(mutA2Slot(mock), "precompact:5", 30)
	if payload == nil {
		t.Fatal("vanished-boundary payload = nil, want an empty-page payload so the client stops paging")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(raw["type"]) != `"history"` {
		t.Errorf("type = %s, want \"history\"", raw["type"])
	}
	if string(raw["messages"]) != "[]" {
		t.Errorf("messages = %s, want [] (non-null empty array)", raw["messages"])
	}
	if string(raw["nextCursor"]) != `""` || string(raw["hasMore"]) != "false" {
		t.Errorf("nextCursor/hasMore = %s/%s, want \"\"/false", raw["nextCursor"], raw["hasMore"])
	}
}

func TestMutA2PreCompactHistRendering(t *testing.T) {
	c := mutA2BareConnector()
	tools := map[string]tool.Tool{
		"X": &mutA2FullTool{mutA2Tool: mutA2Tool{name: "X", desc: "SUM-X"}},
	}
	bndTS := time.Date(2026, 3, 6, 7, 0, 0, 0, time.UTC)
	rows := []*short.TranscriptMessage{
		{UUID: "r0", Type: "user", Content: `[{"type":"text","text":"hello"}]`, CreatedAt: bndTS},
		{UUID: "bnd", Type: "system", Subtype: "compact_boundary", Content: `[]`, CreatedAt: bndTS},
		// User row with boundary subtype must render as a chat message.
		{UUID: "r2", Type: "user", Subtype: "compact_boundary",
			Content: `[{"type":"text","text":"ub","tool_use_id":"tuX"}]`, CreatedAt: bndTS},
		// System informational row must render, not become a divider.
		{UUID: "r3", Type: "system", Subtype: "informational",
			Content: `[{"type":"text","text":"sysinfo"}]`, CreatedAt: bndTS},
		{UUID: "r4", Type: "assistant",
			Content: `[{"type":"tool_use","id":"tu1","name":"X","input":{}}]`, CreatedAt: bndTS},
		{UUID: "r5", Type: "user",
			Content:  `[{"type":"tool_result","tool_use_id":"tu1","content":[{"type":"text","text":"WIRE"}],"is_error":true,"tool_duration_ns":99}]`,
			Metadata: `{"toolUseResult":{"tu1":"RICH"}}`, CreatedAt: bndTS},
		{UUID: "r6", Type: "assistant",
			Content: `[{"type":"tool_use","id":"tuX","name":"X","input":{}}]`, CreatedAt: bndTS},
		// Empty-id pair: a tool_result with no tool_use_id can never satisfy
		// the empty-id tool_use below it.
		{UUID: "r7", Type: "user",
			Content: `[{"type":"tool_result","tool_use_id":"","content":[{"type":"text","text":"anon"}]}]`, CreatedAt: bndTS},
		{UUID: "r8", Type: "assistant",
			Content: `[{"type":"tool_use","id":"","name":"X","input":{}}]`, CreatedAt: bndTS},
	}
	mock := &mockEngine{}
	mock.toolsFn = func() map[string]tool.Tool { return tools }
	mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
		return rows, 12, true
	}
	p := mutA2ParseHistory(t, c.buildPreCompactHistory(mutA2Slot(mock), "precompact:0", 30))
	if len(p.Messages) != 9 {
		t.Fatalf("messages = %d, want 9 (%+v)", len(p.Messages), p.Messages)
	}
	d := p.Messages[1]
	if !d.CompactBoundary || d.ID != "bnd" || d.Role != "system" || d.StartedAt != bndTS.UnixMilli() {
		t.Errorf("divider = %+v, want {bnd system %d compactBoundary}", d, bndTS.UnixMilli())
	}
	if p.Messages[2].CompactBoundary || p.Messages[2].Text != "ub" {
		t.Errorf("user+boundary-subtype row = %+v, want a normal rendered message", p.Messages[2])
	}
	if p.Messages[3].CompactBoundary || p.Messages[3].Text != "sysinfo" {
		t.Errorf("system informational row = %+v, want a normal rendered message", p.Messages[3])
	}
	done := p.Messages[4].Tools[0]
	if len(p.Messages[4].Tools) != 1 {
		t.Fatalf("tool entries on r4 = %d, want 1", len(p.Messages[4].Tools))
	}
	if done.IsRunning || !done.IsError || done.DurationNs != 99 || done.DisplayOutput != `"RICH"` {
		t.Errorf("resolved tool entry = %+v, want running=false isError=true duration=99 rich output", done)
	}
	if len(p.Messages[6].Tools) != 1 || !p.Messages[6].Tools[0].IsRunning {
		t.Errorf("unresolved tool entry = %+v, want IsRunning (text blocks are not results)", p.Messages[6].Tools)
	}
	if len(p.Messages[8].Tools) != 1 || !p.Messages[8].Tools[0].IsRunning {
		t.Errorf("empty-id tool entry = %+v, want IsRunning (empty-keyed results are not collected)", p.Messages[8].Tools)
	}
	if !p.HasMore || p.NextCursor != "precompact:9" {
		t.Errorf("hasMore/nextCursor = %v/%q, want true/precompact:9", p.HasMore, p.NextCursor)
	}
}

// ---------------------------------------------------------------------------
// formatToolDisplayName / historyAgentToolName (connector.go:2188-2216)
// ---------------------------------------------------------------------------

func TestMutA2FormatToolDisplayName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Grep", "Grep"},
		{"a__b__c", "a__b__c"},
		{"mcp__srv__tool", "srv - tool (MCP)"},
		{"mcp__a__b__c", "a - b__c (MCP)"},
		{"mcp__lonely", "mcp__lonely"},
	}
	for _, tc := range cases {
		if got := formatToolDisplayName(tc.in); got != tc.want {
			t.Errorf("formatToolDisplayName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMutA2HistoryAgentToolName(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		// Non-Agent tools never grow a subagent suffix.
		{"Grep", `{"subagent_type":"Explore"}`, "Grep"},
		{"Agent", `{}`, "Agent"},
		{"Agent", `{"subagent_type":"fork"}`, "Agent"},
		{"Agent", `{"subagent_type":"Explore"}`, "Agent Explore"},
		// Unmarshal error (bool field fed a string) must win over the
		// partially decoded subagent_type.
		{"Agent", `{"subagent_type":"x","fork":"notabool"}`, "Agent"},
	}
	for _, tc := range cases {
		got := historyAgentToolName(tc.name, json.RawMessage(tc.input))
		if got != tc.want {
			t.Errorf("historyAgentToolName(%q, %s) = %q, want %q", tc.name, tc.input, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// computeToolSummary / renderToolOutput / renderViaTool / persisted helpers
// (connector.go:2219-2347)
// ---------------------------------------------------------------------------

func TestMutA2ComputeToolSummary(t *testing.T) {
	if got := computeToolSummary("NoSuchTool", json.RawMessage(`{}`), nil); got != "" {
		t.Errorf("summary for unknown tool = %q, want empty", got)
	}
	tools := map[string]tool.Tool{"Grep": &mutA2Tool{name: "Grep", desc: "SUM-G"}}
	if got := computeToolSummary("Grep", json.RawMessage(`{}`), tools); got != "SUM-G" {
		t.Errorf("summary = %q, want SUM-G", got)
	}
	errTools := map[string]tool.Tool{"E": &mutA2Tool{name: "E", descErr: true}}
	if got := computeToolSummary("E", json.RawMessage(`{}`), errTools); got != "" {
		t.Errorf("summary on Description error = %q, want empty", got)
	}
}

func TestMutA2RenderToolOutputEmptyRaw(t *testing.T) {
	if got := renderToolOutput("X", nil, nil); got != "" {
		t.Errorf("renderToolOutput(nil) = %q, want empty", got)
	}
	// A tool that renders everything must still not see empty input: the
	// empty-raw guard fires before any render path.
	tools := map[string]tool.Tool{"X": &mutA2Tool{name: "X", render: func(any) string { return "R" }}}
	if got := renderToolOutput("X", nil, tools); got != "" {
		t.Errorf("renderToolOutput(nil, with tool) = %q, want empty", got)
	}
	// A 1-byte raw payload is past the empty guard and falls back to raw text.
	if got := renderToolOutput("X", json.RawMessage("x"), nil); got != "x" {
		t.Errorf("renderToolOutput(1 byte) = %q, want x", got)
	}
}

func TestMutA2RenderToolOutputTextExtraction(t *testing.T) {
	cases := []struct {
		raw  string
		want string
		note string
	}{
		{`[{"type":"text","text":"hello"},{"type":"text","text":"world"}]`, "hello\nworld", "joined text blocks"},
		{`[{"type":"image","text":"leak"}]`, `[{"type":"image","text":"leak"}]`, "non-text block is not extracted"},
		{`[{"type":"text","text":""}]`, `[{"type":"text","text":""}]`, "empty-text block is not extracted"},
		// Unmarshal type error mid-array: element 0 decodes but the error
		// must force the raw fallback.
		{`[{"type":"text","text":"a"},{"type":123}]`, `[{"type":"text","text":"a"},{"type":123}]`, "type error forces raw fallback"},
		{`[{"type":"image"}]`, `[{"type":"image"}]`, "no text at all"},
	}
	for _, tc := range cases {
		if got := renderToolOutput("X", json.RawMessage(tc.raw), nil); got != tc.want {
			t.Errorf("renderToolOutput(%s): got %q, want %q (%s)", tc.note, got, tc.want, tc.note)
		}
	}
}

func TestMutA2RenderViaToolStringForm(t *testing.T) {
	tools := map[string]tool.Tool{
		"X": &mutA2Tool{name: "X", render: func(data any) string { return "R:" + data.(string) }},
	}
	raw := json.RawMessage(`[{"type":"text","text":"payload"}]`)
	got, ok := renderViaTool("X", raw, tools)
	if !ok {
		t.Fatal("renderViaTool(string-form tool) ok = false, want true")
	}
	if got != "R:"+string(raw) {
		t.Errorf("renderViaTool = %q, want R:%s", got, raw)
	}
}

func TestMutA2ReadPersistedFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(filePath, []byte("FILECONTENT"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if got := readPersistedFile("no marker here"); got != nil {
		t.Errorf("readPersistedFile(no marker) = %s, want nil", got)
	}
	got := readPersistedFile("Full output saved to: " + filePath + "\nPreview (first 5 lines):\nx")
	if string(got) != "FILECONTENT" {
		t.Errorf("readPersistedFile = %q, want FILECONTENT", got)
	}
	// Path on the line AFTER the marker: an empty first line means no path,
	// even though a path follows on the next line.
	if got := readPersistedFile("Full output saved to: \n" + filePath + "\n"); got != nil {
		t.Errorf("readPersistedFile(newline-first) = %q, want nil (empty path segment)", got)
	}
}

func TestMutA2ExtractPersistedPreview(t *testing.T) {
	// Exactly 5 lines: no ellipsis is appended.
	five := "Preview (first 5 lines):\nl1\nl2\nl3\nl4\nl5"
	if got := extractPersistedPreview(five); got != "l1\nl2\nl3\nl4\nl5" {
		t.Errorf("preview(5 lines) = %q, want l1..l5 with no ... suffix", got)
	}
	// 6 lines: first 5 plus ellipsis.
	six := "Preview (first 5 lines):\nl1\nl2\nl3\nl4\nl5\nl6"
	if got := extractPersistedPreview(six); got != "l1\nl2\nl3\nl4\nl5\n..." {
		t.Errorf("preview(6 lines) = %q, want l1..l5 + ...", got)
	}
}

// ---------------------------------------------------------------------------
// agentTypeLog / parentIDLog (connector.go:2439-2451)
// ---------------------------------------------------------------------------

func TestMutA2AgentMetaLogs(t *testing.T) {
	if got := agentTypeLog(nil); got != "" {
		t.Errorf("agentTypeLog(nil) = %q, want empty", got)
	}
	if got := agentTypeLog(&types.AgentMeta{AgentType: "Explore"}); got != "Explore" {
		t.Errorf("agentTypeLog = %q, want Explore", got)
	}
	if got := parentIDLog(nil); got != "" {
		t.Errorf("parentIDLog(nil) = %q, want empty", got)
	}
	if got := parentIDLog(&types.AgentMeta{ParentToolUseID: "tu9"}); got != "tu9" {
		t.Errorf("parentIDLog = %q, want tu9", got)
	}
}

// ---------------------------------------------------------------------------
// buildTaskList (connector.go:2475-2537)
// ---------------------------------------------------------------------------

func TestMutA2BuildTaskListNilSlot(t *testing.T) {
	c := mutA2BareConnector()
	if got := c.buildTaskList(nil); got != nil {
		t.Errorf("buildTaskList(nil) = %.40s, want nil", got)
	}
}

func TestMutA2BuildTaskListListError(t *testing.T) {
	c := mutA2BareConnector()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// A file where the task dir should be: ReadDir fails with ENOTDIR.
	mock := &mockEngine{}
	mock.taskListFn = func() *task.List { return task.NewList(blocker) }
	if got := c.buildTaskList(mutA2Slot(mock)); got != nil {
		t.Errorf("buildTaskList(ListTasks error) = %.40s, want nil", got)
	}
}

func TestMutA2BuildTaskListFilters(t *testing.T) {
	c := mutA2BareConnector()
	tl := task.NewList(t.TempDir())
	if err := tl.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	id1, err := tl.CreateTask("done-task", "d", "", nil)
	if err != nil {
		t.Fatalf("CreateTask 1: %v", err)
	}
	completed := task.StatusCompleted
	if _, _, err := tl.UpdateTask(id1, task.TaskUpdates{Status: &completed}); err != nil {
		t.Fatalf("UpdateTask 1: %v", err)
	}
	id2, err := tl.CreateTask("visible", "d", "working hard", map[string]any{"note": "plain"})
	if err != nil {
		t.Fatalf("CreateTask 2: %v", err)
	}
	owner := "agent-2"
	if _, _, err := tl.UpdateTask(id2, task.TaskUpdates{Owner: &owner}); err != nil {
		t.Fatalf("UpdateTask 2: %v", err)
	}
	id3, err := tl.CreateTask("internal", "d", "", map[string]any{"_internal": true})
	if err != nil {
		t.Fatalf("CreateTask 3: %v", err)
	}
	id4, err := tl.CreateTask("blocker-task", "d", "", nil)
	if err != nil {
		t.Fatalf("CreateTask 4: %v", err)
	}
	if !tl.BlockTask(id4, id2) {
		t.Fatal("BlockTask(4, 2) = false")
	}

	mock := &mockEngine{}
	mock.taskListFn = func() *task.List { return tl }
	payload := c.buildTaskList(mutA2Slot(mock))
	if payload == nil {
		t.Fatal("payload = nil, want task_list frame")
	}
	var frame taskListOutbound
	if err := json.Unmarshal(payload, &frame); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if frame.Type != "task_list" {
		t.Errorf("type = %q, want task_list", frame.Type)
	}
	if len(frame.Tasks) != 3 {
		t.Fatalf("tasks = %d (%+v), want 3 (internal excluded, metadata-without-key kept)", len(frame.Tasks), frame.Tasks)
	}
	var t2 *taskListWireItem
	for i := range frame.Tasks {
		if frame.Tasks[i].Subject == "visible" {
			t2 = &frame.Tasks[i]
		}
	}
	if t2 == nil {
		t.Fatal("visible task missing from wire payload")
	}
	if t2.Owner != owner {
		t.Errorf("owner = %q, want %q", t2.Owner, owner)
	}
	if t2.ActiveForm != "working hard" {
		t.Errorf("activeForm = %q, want working hard", t2.ActiveForm)
	}
	if len(t2.BlockedBy) != 1 || t2.BlockedBy[0] != "blocker-task" {
		t.Errorf("blockedBy = %v, want [blocker-task] (pending blocker, resolved to subject)", t2.BlockedBy)
	}
	// A pending task must suppress the completed-cleanup: all four tasks
	// remain on disk after the payload is built.
	after, err := tl.ListTasks()
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(after) != 4 {
		t.Errorf("tasks on disk after buildTaskList = %d, want 4 (cleanup must not run)", len(after))
	}
	// id3's internal task survives on disk yet must stay absent from the
	// wire payload: resolve its subject through the ID, then scan the frame.
	var internalSubject string
	for _, tk := range after {
		if tk.ID == id3 {
			internalSubject = tk.Subject
		}
	}
	if internalSubject != "internal" {
		t.Fatalf("task %s on disk has subject %q, want internal", id3, internalSubject)
	}
	for i := range frame.Tasks {
		if frame.Tasks[i].Subject == internalSubject {
			t.Errorf("internal task leaked into the wire payload: %+v", frame.Tasks[i])
		}
	}
}

// ---------------------------------------------------------------------------
// buildConfig (connector.go:2560-2585)
// ---------------------------------------------------------------------------

func TestMutA2BuildConfig(t *testing.T) {
	var provCfg config.Provider
	raw := `{"name":"zhipu","url":"https://z.example","keys":["k"],"models":{"glm-4.6":{},"glm-4.5":{}}}`
	if err := json.Unmarshal([]byte(raw), &provCfg); err != nil {
		t.Fatalf("unmarshal provider: %v", err)
	}
	c := mutA2BareConnector()
	c.providerConfigs = map[string]*config.Provider{"zhipu": &provCfg}
	fp := &mutA2LLMProvider{name: "zhipu"}
	c.providers = map[string]llm.Provider{"zhipu": fp}

	mock := &mockEngine{}
	mock.providerFn = func() llm.Provider { return fp }
	mock.modelFn = func() string { return "glm-4.6" }
	mock.thinkingFn = func() llm.Effort { return llm.EffortHigh }

	payload := c.buildConfig(mutA2Slot(mock))
	if payload == nil {
		t.Fatal("payload = nil")
	}
	var frame struct {
		Type   string `json:"type"`
		Models []struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"models"`
		Current struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"current"`
		Thinking string `json:"thinking"`
	}
	if err := json.Unmarshal(payload, &frame); err != nil {
		t.Fatalf("unmarshal config: %v (%.200s)", err, payload)
	}
	if frame.Type != "config" {
		t.Errorf("type = %q, want config", frame.Type)
	}
	if len(frame.Models) != 2 {
		t.Fatalf("models = %+v, want 2 entries", frame.Models)
	}
	if frame.Models[0].Provider != "zhipu" || frame.Models[0].Model != "glm-4.6" {
		t.Errorf("models[0] = %+v, want zhipu/glm-4.6 (config order)", frame.Models[0])
	}
	if frame.Models[1].Provider != "zhipu" || frame.Models[1].Model != "glm-4.5" {
		t.Errorf("models[1] = %+v, want zhipu/glm-4.5", frame.Models[1])
	}
	if frame.Current.Provider != "zhipu" || frame.Current.Model != "glm-4.6" {
		t.Errorf("current = %+v, want zhipu/glm-4.6", frame.Current)
	}
	if frame.Thinking != "high" {
		t.Errorf("thinking = %q, want high", frame.Thinking)
	}

	// No provider configs at all: models must still marshal as [], not null,
	// so the client's array handling does not break on a fresh install.
	empty := mutA2BareConnector()
	empty.providerConfigs = map[string]*config.Provider{}
	mock2 := &mockEngine{}
	mock2.modelFn = func() string { return "m" }
	payload2 := empty.buildConfig(mutA2Slot(mock2))
	var rawCfg map[string]json.RawMessage
	if err := json.Unmarshal(payload2, &rawCfg); err != nil {
		t.Fatalf("unmarshal empty config: %v", err)
	}
	if string(rawCfg["models"]) != "[]" {
		t.Errorf("models with no providers = %s, want []", rawCfg["models"])
	}
}

// ---------------------------------------------------------------------------
// buildQuota (connector.go:2590-2620)
// ---------------------------------------------------------------------------

func TestMutA2BuildQuotaNoWS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/monitor/usage/quota/limit" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"success":true,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":40,"nextResetTime":4102444800000}]},"msg":""}`))
	}))
	t.Cleanup(srv.Close)

	var provCfg config.Provider
	raw := fmt.Sprintf(`{"name":"zhipu","url":%q,"keys":["k"]}`, srv.URL)
	if err := json.Unmarshal([]byte(raw), &provCfg); err != nil {
		t.Fatalf("unmarshal provider: %v", err)
	}
	c := mutA2BareConnector()
	c.providerConfigs = map[string]*config.Provider{"zhipu": &provCfg}

	// No WS connected: even a fetchable quota must not enqueue a frame.
	c.buildQuota()
	if got, want := c.outQ.Load().len(), 0; got != want {
		t.Errorf("outQ len after buildQuota without WS = %d, want %d", got, want)
	}
}
