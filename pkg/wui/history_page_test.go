package wui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// histPage mirrors the unified history page frame buildHistory emits: the
// committed window in Messages and the frozen in-flight window in Stream,
// plus the anchor cursor / rev / busy / seq / nonce envelope fields.
type histPage struct {
	Type       string           `json:"type"`
	Messages   []historyChatMsg `json:"messages"`
	Stream     []streamBlock    `json:"stream"`
	NextCursor string           `json:"nextCursor"`
	HasMore    bool             `json:"hasMore"`
	Rev        string           `json:"rev"`
	Busy       bool             `json:"busy"`
	Seq        uint64           `json:"seq"`
	Nonce      uint64           `json:"nonce"`
}

func parseHistPage(t *testing.T, payload []byte) histPage {
	t.Helper()
	var p histPage
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatalf("unmarshal history page: %v (raw: %.200s)", err, payload)
	}
	return p
}

// parseAnchor extracts (C, S, G) from an "a:C:S:G" anchor cursor.
func parseAnchor(t *testing.T, cursor string) (int, int, int) {
	t.Helper()
	var c, s, g int
	if _, err := fmt.Sscanf(cursor, "a:%d:%d:%d", &c, &s, &g); err != nil {
		t.Fatalf("parse anchor cursor %q: %v", cursor, err)
	}
	return c, s, g
}

// anchorG returns the generation component of an "a:C:S:G" cursor as a
// string, for interpolating into follow-up cursor inputs (the generation is
// seeded from a process-level counter and cannot be predicted).
func anchorG(t *testing.T, cursor string) string {
	t.Helper()
	_, _, g := parseAnchor(t, cursor)
	return fmt.Sprintf("%d", g)
}

// countingTool counts RenderResult/Description invocations so tests can
// assert exactly how many messages a page build rendered.
type countingTool struct {
	mutA2Tool
	renders atomic.Int64
	descs   atomic.Int64
}

func (t *countingTool) RenderResult(any) string {
	t.renders.Add(1)
	return "COUNTED-RENDER"
}

func (t *countingTool) Description(json.RawMessage) (string, error) {
	t.descs.Add(1)
	return "COUNTED-DESC", nil
}

func pageStreamIDs(p histPage) []string {
	ids := make([]string, len(p.Stream))
	for i, b := range p.Stream {
		ids[i] = b.ID
	}
	return ids
}

// V1: only the page window is rendered — a full-render-then-slice
// implementation pays RenderResult/Description on every message.
func TestBuildHistory_RendersOnlyPageWindow(t *testing.T) {
	c := mutA2BareConnector()
	ct := &countingTool{mutA2Tool: mutA2Tool{name: "X"}}
	mock := &mockEngine{}
	msgs := make([]types.Message, 80)
	for i := range msgs {
		msgs[i] = types.Message{
			ID:        fmt.Sprintf("msg-%d", i),
			Role:      types.RoleAssistant,
			Timestamp: time.Unix(int64(1000+i), 0),
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolUse, ID: fmt.Sprintf("tu-%d", i), Name: "X", Input: json.RawMessage(`{}`)},
				{Type: types.ContentTypeToolResult, ToolUseID: fmt.Sprintf("tu-%d", i), Content: json.RawMessage(`[{"type":"text","text":"r"}]`)},
			},
		}
	}
	mock.SetMessagesFn(func() []types.Message { return msgs })
	mock.toolsFn = func() map[string]tool.Tool { return map[string]tool.Tool{"X": ct} }

	p := parseHistPage(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if len(p.Messages) != 30 {
		t.Fatalf("messages = %d, want 30", len(p.Messages))
	}
	if p.Messages[0].ID != "msg-50" {
		t.Errorf("first message ID = %q, want msg-50 (window [50,80))", p.Messages[0].ID)
	}
	if got := ct.renders.Load(); got != 30 {
		t.Errorf("RenderResult calls = %d, want 30 (window only, not all 80)", got)
	}
	if got := ct.descs.Load(); got != 30 {
		t.Errorf("Description calls = %d, want 30 (window only, not all 80)", got)
	}
	if !regexp.MustCompile(`^a:50:0:\d+$`).MatchString(p.NextCursor) {
		t.Errorf("nextCursor = %q, want a:50:0:<gen>", p.NextCursor)
	}
}

// V2: anchor cursors page into the in-flight stream region itself — the
// merged view is committed units followed by top-level stream blocks.
func TestBuildHistory_AnchorPagesIntoStreamRegion(t *testing.T) {
	c := newTestConnector(t)
	for i := range 45 {
		c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{
			ID: fmt.Sprintf("t%d", i), Name: "X",
		}})
	}

	p1 := parseHistPage(t, c.buildHistory(c.activeSlot(), "", 30))
	if len(p1.Stream) != 30 {
		t.Fatalf("page 1 stream = %d blocks, want 30", len(p1.Stream))
	}
	if ids := pageStreamIDs(p1); ids[0] != "t15" || ids[29] != "t44" {
		t.Errorf("page 1 stream IDs = %v..%v, want t15..t44", ids[0], ids[29])
	}
	if len(p1.Messages) != 0 {
		t.Errorf("page 1 messages = %d, want 0 (no committed units)", len(p1.Messages))
	}
	if !p1.HasMore {
		t.Error("page 1 hasMore = false, want true (15 blocks remain)")
	}
	if !regexp.MustCompile(`^a:0:15:\d+$`).MatchString(p1.NextCursor) {
		t.Fatalf("page 1 nextCursor = %q, want a:0:15:<gen>", p1.NextCursor)
	}
	g := anchorG(t, p1.NextCursor)

	p2 := parseHistPage(t, c.buildHistory(c.activeSlot(), "a:0:15:"+g, 30))
	if len(p2.Stream) != 15 {
		t.Fatalf("page 2 stream = %d blocks, want 15", len(p2.Stream))
	}
	if ids := pageStreamIDs(p2); ids[0] != "t0" || ids[14] != "t14" {
		t.Errorf("page 2 stream IDs = %v..%v, want t0..t14", ids[0], ids[14])
	}
	if p2.HasMore {
		t.Error("page 2 hasMore = true, want false")
	}
	if len(p2.Messages) != 0 {
		t.Errorf("page 2 messages = %d, want 0", len(p2.Messages))
	}
}

// V3: a tail page may mix committed units and in-flight blocks; the anchor
// window boundary cuts between them.
func TestBuildHistory_MixedPageBoundary(t *testing.T) {
	c := newTestConnector(t)
	c.mock().SetMessagesFn(func() []types.Message {
		return []types.Message{
			{ID: "m0", Role: types.RoleUser, Timestamp: time.Unix(1000, 0), Content: []types.ContentBlock{types.NewTextBlock("m0")}},
			{ID: "m1", Role: types.RoleUser, Timestamp: time.Unix(1001, 0), Content: []types.ContentBlock{types.NewTextBlock("m1")}},
			{ID: "m2", Role: types.RoleUser, Timestamp: time.Unix(1002, 0), Content: []types.ContentBlock{types.NewTextBlock("m2")}},
		}
	})
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "f0", Name: "X"}})
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "f1", Name: "X"}})

	p := parseHistPage(t, c.buildHistory(c.activeSlot(), "", 30))
	if len(p.Messages) != 3 || p.Messages[2].ID != "m2" {
		t.Fatalf("full-page messages = %+v, want 3 ending at m2", p.Messages)
	}
	if ids := pageStreamIDs(p); len(ids) != 2 || ids[0] != "f0" || ids[1] != "f1" {
		t.Fatalf("full-page stream IDs = %v, want [f0 f1]", ids)
	}
	if p.HasMore {
		t.Error("full page hasMore = true, want false")
	}
	if p.Rev != "3:m2" {
		t.Errorf("full page rev = %q, want 3:m2", p.Rev)
	}

	p = parseHistPage(t, c.buildHistory(c.activeSlot(), "", 3))
	if len(p.Messages) != 1 || p.Messages[0].ID != "m2" {
		t.Fatalf("limit-3 messages = %+v, want only m2", p.Messages)
	}
	if len(p.Stream) != 2 {
		t.Fatalf("limit-3 stream = %d blocks, want 2", len(p.Stream))
	}
	if !p.HasMore {
		t.Error("limit-3 page hasMore = false, want true")
	}
	if !regexp.MustCompile(`^a:2:0:\d+$`).MatchString(p.NextCursor) {
		t.Fatalf("limit-3 nextCursor = %q, want a:2:0:<gen>", p.NextCursor)
	}
	g := anchorG(t, p.NextCursor)

	p = parseHistPage(t, c.buildHistory(c.activeSlot(), "a:2:0:"+g, 30))
	if len(p.Messages) != 2 || p.Messages[0].ID != "m0" || p.Messages[1].ID != "m1" {
		t.Fatalf("anchor page messages = %+v, want [m0 m1]", p.Messages)
	}
	if len(p.Stream) != 0 {
		t.Errorf("anchor page stream = %v, want none (committed-only window)", pageStreamIDs(p))
	}
	if p.HasMore {
		t.Error("anchor page hasMore = true, want false")
	}
}

// V4: a cursor anchored in a generation that query_end killed must resolve
// to the committed continuation (S zeroed), never slice the fresh region.
func TestBuildHistory_AnchorStaleGeneration(t *testing.T) {
	c := newTestConnector(t)
	for i := range 5 {
		c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{
			ID: fmt.Sprintf("t%d", i), Name: "X",
		}})
	}
	// Learn the old region's generation from a bounded page (nextCursor
	// carries it whenever hasMore is true).
	p0 := parseHistPage(t, c.buildHistory(c.activeSlot(), "", 3))
	if len(p0.Stream) != 3 || !p0.HasMore {
		t.Fatalf("seed page stream = %d blocks hasMore=%v, want 3/true", len(p0.Stream), p0.HasMore)
	}
	gOld := anchorG(t, p0.NextCursor)

	// query_end kills the region under a fresh generation; one committed row lands.
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd})
	c.mock().SetMessagesFn(func() []types.Message {
		return []types.Message{{ID: "m0", Role: types.RoleUser, Timestamp: time.Unix(1000, 0),
			Content: []types.ContentBlock{types.NewTextBlock("m0")}}}
	})

	p := parseHistPage(t, c.buildHistory(c.activeSlot(), "a:1:5:"+gOld, 30))
	if len(p.Messages) != 1 || p.Messages[0].ID != "m0" {
		t.Errorf("stale anchor (C=1) messages = %+v, want the single committed row", p.Messages)
	}
	if len(p.Stream) != 0 {
		t.Errorf("stale anchor (C=1) stream = %v, want none (S must zero on gen change)", pageStreamIDs(p))
	}
	if p.HasMore {
		t.Error("stale anchor (C=1) hasMore = true, want false")
	}

	p = parseHistPage(t, c.buildHistory(c.activeSlot(), "a:0:5:"+gOld, 30))
	if len(p.Messages) != 0 || len(p.Stream) != 0 || p.HasMore {
		t.Errorf("stale anchor (C=0) page = %d msgs %d blocks hasMore=%v, want empty terminal page",
			len(p.Messages), len(p.Stream), p.HasMore)
	}

	// Same-generation regrowth: a new query refills the region to 5 blocks;
	// the stale anchor still must not deliver them.
	for i := range 5 {
		c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{
			ID: fmt.Sprintf("n%d", i), Name: "X",
		}})
	}
	p = parseHistPage(t, c.buildHistory(c.activeSlot(), "a:1:5:"+gOld, 30))
	if len(p.Messages) != 1 || len(p.Stream) != 0 {
		t.Errorf("regrown-region anchor page = %d msgs %d blocks, want 1/0 (new region must not be sliced)",
			len(p.Messages), len(p.Stream))
	}
}

// V5: an anchor whose committed front outruns the live count (rewind
// truncation) resolves to the terminal empty page.
func TestBuildHistory_StaleAnchorRewindTerminalPage(t *testing.T) {
	c := mutA2BareConnector()
	mock := &mockEngine{}
	mock.SetMessagesFn(func() []types.Message { return mutA2NMessages(5) })

	p := parseHistPage(t, c.buildHistory(mutA2Slot(mock), "a:9:0:0", 30))
	if len(p.Messages) != 0 {
		t.Errorf("messages = %d, want 0 (terminal empty page)", len(p.Messages))
	}
	if p.HasMore {
		t.Error("hasMore = true, want false")
	}
	if p.NextCursor != "" {
		t.Errorf("nextCursor = %q, want \"\"", p.NextCursor)
	}
}

// V7: sessionRev is the committed-view revision — deterministic on an
// unchanged view, changed by an append, truncated at the query boundary when
// busy, and "0:" when empty.
func TestSessionRev_Views(t *testing.T) {
	mock := &mockEngine{}
	msgs := mutA2NMessages(6)
	mock.SetMessagesFn(func() []types.Message { return msgs })
	if got, want := sessionRev(mock), "6:m05"; got != want {
		t.Errorf("sessionRev = %q, want %q", got, want)
	}
	if a, b := sessionRev(mock), sessionRev(mock); a != b {
		t.Errorf("sessionRev not deterministic: %q vs %q", a, b)
	}
	grown := append(append([]types.Message{}, msgs...), mutA2UserMsg("m06", "t06"))
	mock.SetMessagesFn(func() []types.Message { return grown })
	if got, want := sessionRev(mock), "7:m06"; got != want {
		t.Errorf("sessionRev after append = %q, want %q", got, want)
	}
	mock.SetQueryStartMsgIdxFn(func() int { return 5 })
	if got, want := sessionRev(mock), "6:m05"; got != want {
		t.Errorf("sessionRev busy (idx 5) = %q, want %q (truncated at idx+1)", got, want)
	}
	mock.SetMessagesFn(func() []types.Message { return nil })
	mock.SetQueryStartMsgIdxFn(func() int { return -1 })
	if got, want := sessionRev(mock), "0:"; got != want {
		t.Errorf("sessionRev empty = %q, want %q", got, want)
	}
}

// V8: an empty merged view still produces a page frame (rev "0:"), and the
// pre-compact probe extends pagination when a boundary exists.
func TestBuildHistory_EmptySessionPage0Frame(t *testing.T) {
	c := mutA2BareConnector()
	mock := &mockEngine{}
	mock.SetMessagesFn(func() []types.Message { return nil })

	p := parseHistPage(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if len(p.Messages) != 0 {
		t.Errorf("messages = %d, want 0", len(p.Messages))
	}
	if p.Rev != "0:" {
		t.Errorf("rev = %q, want \"0:\"", p.Rev)
	}
	if p.HasMore || p.NextCursor != "" {
		t.Errorf("hasMore/nextCursor = %v/%q, want false/\"\"", p.HasMore, p.NextCursor)
	}

	mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
		if delivered != 0 || limit != 0 {
			t.Errorf("probe args = (%d, %d), want (0, 0)", delivered, limit)
		}
		return nil, 0, true
	}
	p = parseHistPage(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if !p.HasMore || p.NextCursor != "precompact:0" {
		t.Errorf("boundary probe hasMore/nextCursor = %v/%q, want true/precompact:0", p.HasMore, p.NextCursor)
	}
}

// V10: tool_use inside the page window pairs with a tool_result that lives
// beyond it — the result scan covers the committed tail past the window.
func TestBuildHistory_CrossPageToolResultPairing(t *testing.T) {
	c := mutA2BareConnector()
	ct := &countingTool{mutA2Tool: mutA2Tool{name: "X"}}
	mock := &mockEngine{}
	msgs := make([]types.Message, 46)
	msgs[0] = types.Message{
		ID:        "use",
		Role:      types.RoleAssistant,
		Timestamp: time.Unix(1000, 0),
		Content:   []types.ContentBlock{{Type: types.ContentTypeToolUse, ID: "tu0", Name: "X", Input: json.RawMessage(`{}`)}},
	}
	for i := 1; i < 45; i++ {
		msgs[i] = types.Message{
			ID:        fmt.Sprintf("f%d", i),
			Role:      types.RoleUser,
			Timestamp: time.Unix(int64(1000+i), 0),
			Content:   []types.ContentBlock{types.NewTextBlock(fmt.Sprintf("filler-%d", i))},
		}
	}
	msgs[45] = types.Message{
		ID:        "res",
		Role:      types.RoleUser,
		Timestamp: time.Unix(1045, 0),
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "tu0", Content: json.RawMessage(`[{"type":"text","text":"r"}]`)},
		},
	}
	mock.SetMessagesFn(func() []types.Message { return msgs })
	mock.toolsFn = func() map[string]tool.Tool { return map[string]tool.Tool{"X": ct} }

	p := parseHistPage(t, c.buildHistory(mutA2Slot(mock), "a:30:0:0", 30))
	var use *historyChatMsg
	for i := range p.Messages {
		if p.Messages[i].ID == "use" {
			use = &p.Messages[i]
			break
		}
	}
	if use == nil {
		t.Fatalf("tool_use message missing from window [0,30): %d messages", len(p.Messages))
	}
	if len(use.Tools) != 1 {
		t.Fatalf("tool entries = %d, want 1", len(use.Tools))
	}
	if use.Tools[0].IsRunning {
		t.Error("tool entry IsRunning = true, want false (result pairs across the window edge)")
	}
	if use.Tools[0].DisplayOutput != "COUNTED-RENDER" {
		t.Errorf("displayOutput = %q, want COUNTED-RENDER (fake tool render)", use.Tools[0].DisplayOutput)
	}
}

// V11: metadata carries rev/busy instead of embedded history/snapshot; the
// page request returns the stream window the snapshot used to carry.
func TestSendMetadata_StripCarryRevBusy(t *testing.T) {
	hermeticWUI(t, func(t *testing.T, c *WUIConnector, hs *chatServer) {
		msgs := make([]types.Message, 40)
		for i := range msgs {
			msgs[i] = mutA2UserMsg(fmt.Sprintf("msg-%d", i), fmt.Sprintf("t%d", i))
		}
		c.mock().SetMessagesFn(func() []types.Message { return msgs })

		ws1 := hs.dialWS(t)
		meta := readMetadata(t, ws1)
		hs.awaitTakeover(t)
		if meta.History != nil {
			t.Errorf("metadata history = %.60s, want nil (stripped)", meta.History)
		}
		if meta.Snapshot != nil {
			t.Errorf("metadata snapshot = %.60s, want nil (stripped)", meta.Snapshot)
		}
		if meta.Rev == nil || *meta.Rev != "40:msg-39" {
			t.Errorf("metadata rev = %v, want 40:msg-39", meta.Rev)
		}
		if meta.Busy != nil {
			t.Errorf("metadata busy = %v, want nil (idle: omitempty false)", *meta.Busy)
		}

		// Busy takeover: idx 35 truncates the rev view; one delta lands in
		// the stream region before the second client takes over.
		c.mock().SetQueryStartMsgIdxFn(func() int { return 35 })
		c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "x"})
		ws2 := hs.dialWS(t)
		meta2 := readMetadata(t, ws2)
		hs.awaitTakeover(t)
		if meta2.History != nil || meta2.Snapshot != nil {
			t.Errorf("busy metadata history/snapshot = %.40s / %.40s, want nil/nil", meta2.History, meta2.Snapshot)
		}
		if meta2.Rev == nil || *meta2.Rev != "36:msg-35" {
			t.Errorf("busy metadata rev = %v, want 36:msg-35 (truncated at idx+1)", meta2.Rev)
		}
		if meta2.Busy == nil || !*meta2.Busy {
			t.Errorf("busy metadata busy = %v, want true", meta2.Busy)
		}

		page := parseHistPage(t, requestHistory(t, ws2, "", 30, 1))
		if len(page.Stream) != 1 || page.Stream[0].Kind != "text" || page.Stream[0].Text != "x" {
			t.Errorf("page stream = %+v, want one text block 'x' (in-flight on the page)", page.Stream)
		}
		if len(page.Messages) != 29 || page.Messages[0].ID != "msg-7" {
			t.Errorf("page messages = %d starting %q, want 29 starting msg-7 (window [7,36) + 1 stream block = 30 units)",
				len(page.Messages), page.Messages[0].ID)
		}
		if !page.Busy {
			t.Error("page busy = false, want true (frozen mid-query)")
		}
	})
}

// V12: end-to-end takeover page — the frozen page absorbs all events numbered
// below its seq, echoes the request nonce, and live events after it number
// above it.
func TestServeHistoryPage_TakeoverIntegration(t *testing.T) {
	hermeticWUI(t, func(t *testing.T, c *WUIConnector, hs *chatServer) {
		_ = hs.dialDrained(t)
		c.mock().SetMessagesFn(func() []types.Message {
			return []types.Message{
				{ID: "m0", Role: types.RoleUser, Timestamp: time.Unix(1000, 0), Content: []types.ContentBlock{types.NewTextBlock("m0")}},
				{ID: "m1", Role: types.RoleUser, Timestamp: time.Unix(1001, 0), Content: []types.ContentBlock{types.NewTextBlock("m1")}},
			}
		})
		c.mock().SetQueryStartMsgIdxFn(func() int { return 1 })

		ws2 := hs.dialWS(t)
		meta := readMetadata(t, ws2)
		hs.awaitTakeover(t)
		if meta.History != nil || meta.Snapshot != nil {
			t.Fatalf("metadata history/snapshot = %.40s / %.40s, want nil/nil", meta.History, meta.Snapshot)
		}
		if meta.Rev == nil || *meta.Rev != "2:m1" {
			t.Errorf("metadata rev = %v, want 2:m1", meta.Rev)
		}
		if meta.Busy == nil || !*meta.Busy {
			t.Errorf("metadata busy = %v, want true", meta.Busy)
		}

		// Two live deltas, then a page request: both must be absorbed.
		c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "a1"})
		c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "a2"})
		seq1 := extractEventSeq(t, readWSMessage(t, ws2))
		seq2 := extractEventSeq(t, readWSMessage(t, ws2))
		if seq1 >= seq2 {
			t.Fatalf("live event seqs not increasing: %d then %d", seq1, seq2)
		}

		pageRaw := requestHistory(t, ws2, "", 30, 5)
		page := parseHistPage(t, pageRaw)
		if page.Nonce != 5 {
			t.Errorf("page nonce = %d, want 5 (echo of the request nonce)", page.Nonce)
		}
		if page.Seq <= seq2 {
			t.Errorf("page seq = %d, want > %d (page froze after both deltas)", page.Seq, seq2)
		}
		if len(page.Stream) != 1 || page.Stream[0].Text != "a1a2" {
			t.Errorf("page stream = %+v, want one text block 'a1a2'", page.Stream)
		}
		if len(page.Messages) != 2 || page.Messages[0].ID != "m0" || page.Messages[1].ID != "m1" {
			t.Errorf("page messages = %+v, want [m0 m1]", page.Messages)
		}

		// A post-freeze delta must number above the page and stay out of it.
		c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "a3"})
		seq3 := extractEventSeq(t, readWSMessage(t, ws2))
		if seq3 <= page.Seq {
			t.Errorf("post-page event seq = %d, want > page seq %d", seq3, page.Seq)
		}
	})
}

// anchorPageEnv builds a bare slot with 5 committed rows (units m00..m04)
// for the cursor-parse matrix.
func anchorPageEnv() (*WUIConnector, *engineSlot, *mockEngine) {
	c := mutA2BareConnector()
	mock := &mockEngine{}
	mock.SetMessagesFn(func() []types.Message { return mutA2NMessages(5) })
	return c, mutA2Slot(mock), mock
}

// Cursor-parse matrix: every input class resolves to exactly one of tail /
// anchor window / terminal empty page, malformed shapes warn, and terminal
// pages skip the pre-compact probe (the probe would flip hasMore on).
func TestBuildHistory_AnchorParseMatrix(t *testing.T) {
	c, slot, mock := anchorPageEnv()
	mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
		return nil, 0, true // boundary exists: probe results are observable
	}
	terminalPage := func(cursor string) {
		t.Helper()
		p := parseHistPage(t, c.buildHistory(slot, cursor, 30))
		if p.Type != "history" {
			t.Fatalf("cursor %q: frame type = %q, want history", cursor, p.Type)
		}
		if len(p.Messages) != 0 || p.HasMore || p.NextCursor != "" {
			t.Errorf("cursor %q: page = %d msgs hasMore=%v nextCursor=%q, want terminal empty page",
				cursor, len(p.Messages), p.HasMore, p.NextCursor)
		}
	}

	// Malformed anchors, junk cursors, and bare numeric cursors (the removed
	// legacy alias): terminal + warn.
	warns := swapMutA2WarnHandler(t)
	malformedCursors := []string{
		"a:1:2", "a:1:2:3:4", "a:x:1:1", "a:1:y:1", "a:1:1:z", "junk", "a::0:0", "5",
	}
	for _, cursor := range malformedCursors {
		terminalPage(cursor)
	}
	if got := strings.Count(strings.Join(warns.records, "\n"), "malformed"); got != len(malformedCursors) {
		t.Errorf("malformed-cursor warns = %d, want one per cursor (%d): %q",
			got, len(malformedCursors), strings.Join(warns.records, "\n"))
	}

	// Anchor bounds: C and S past the live edges (including negatives) end
	// paging without probing the pre-compact region.
	for _, cursor := range []string{"a:9:0:0", "a:-1:0:0", "a:0:-1:0", "a:0:9:0"} {
		terminalPage(cursor)
	}

	// An anchor at the head of both regions is a legal (empty) window, NOT
	// terminal — so the pre-compact probe still extends paging.
	p := parseHistPage(t, c.buildHistory(slot, "a:0:0:0", 30))
	if len(p.Messages) != 0 || !p.HasMore || p.NextCursor != "precompact:0" {
		t.Errorf("cursor \"a:0:0:0\": page = %d msgs hasMore=%v nextCursor=%q, want empty window with precompact probe",
			len(p.Messages), p.HasMore, p.NextCursor)
	}
}

// The nonce is echoed on every page flavor and omitted when the request
// carried none; seq is stamped on pre-compact pages too.
func TestBuildHistory_NonceEchoAndSeqStamps(t *testing.T) {
	c := mutA2BareConnector()
	mock := &mockEngine{}
	mock.SetMessagesFn(func() []types.Message { return mutA2NMessages(3) })

	p := parseHistPage(t, c.buildHistory(mutA2Slot(mock), "", 30, 42))
	if p.Nonce != 42 {
		t.Errorf("page nonce = %d, want 42 (echo)", p.Nonce)
	}
	if p.Seq == 0 {
		t.Error("page seq = 0, want a stamped monotonic value >= 1")
	}

	// No nonce in the request → the field must stay absent from the wire.
	raw := c.buildHistory(mutA2Slot(mock), "", 30)
	if bytes.Contains(raw, []byte(`"nonce"`)) {
		t.Errorf("nonce present without a request nonce: %.120s", raw)
	}
	if !bytes.Contains(raw, []byte(`"rev"`)) {
		t.Errorf("rev missing from page frame: %.120s", raw)
	}

	// Pre-compact pages: normal and boundary-vanished both stamp seq and
	// echo the nonce.
	mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
		return nil, 0, true
	}
	p = parseHistPage(t, c.buildHistory(mutA2Slot(mock), "precompact:0", 30, 9))
	if p.Nonce != 9 || p.Seq == 0 {
		t.Errorf("precompact page nonce/seq = %d/%d, want 9/stamped", p.Nonce, p.Seq)
	}
	mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
		return nil, 0, false
	}
	p = parseHistPage(t, c.buildHistory(mutA2Slot(mock), "precompact:0", 30, 9))
	if p.Nonce != 9 || p.Seq == 0 {
		t.Errorf("vanished-boundary page nonce/seq = %d/%d, want 9/stamped", p.Nonce, p.Seq)
	}
}

// serveHistoryPage on a connector whose active slot is absent must be a
// no-op, not a panic.
func TestServeHistoryPage_NilSlotNoPanic(t *testing.T) {
	c := mutA2BareConnector()
	c.slots = make(map[string]*engineSlot)
	id := "main"
	c.active.Store(&id)
	c.serveHistoryPage("", 30, 1)
}

// The three stream-region birth points each seed a fresh generation: a
// lazily initialized region, a query_end replacement, and registerEngine's
// construction (process-level counter — never zero, never reused).
func TestStreamGenSeeding(t *testing.T) {
	// Lazy init: a bare slot with no streams map grows one on first event.
	c := mutA2BareConnector()
	c.slots = make(map[string]*engineSlot)
	id := "main"
	c.active.Store(&id)
	mock := &mockEngine{}
	slot := mutA2Slot(mock)
	c.slots["main"] = slot
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t0", Name: "X"}})
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t1", Name: "X"}})
	p := parseHistPage(t, c.buildHistory(slot, "", 1))
	_, s, gLazy := parseAnchor(t, p.NextCursor)
	if gLazy < 1 {
		t.Fatalf("lazy-init generation = %d, want >= 1 (seeded from the process counter)", gLazy)
	}
	if s != 1 {
		t.Fatalf("lazy-init anchor S = %d, want 1 (one block remains before the window)", s)
	}

	// query_end rebirth: the replacement region's generation must differ.
	c2 := newTestConnector(t)
	c2.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t0", Name: "X"}})
	c2.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t1", Name: "X"}})
	pOld := parseHistPage(t, c2.buildHistory(c2.activeSlot(), "", 1))
	_, _, gOld := parseAnchor(t, pOld.NextCursor)
	c2.Handle(types.QueryEvent{Type: types.EventQueryEnd})
	c2.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "n0", Name: "X"}})
	c2.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "n1", Name: "X"}})
	pNew := parseHistPage(t, c2.buildHistory(c2.activeSlot(), "", 1))
	_, _, gNew := parseAnchor(t, pNew.NextCursor)
	if gNew < 1 || gNew == gOld {
		t.Fatalf("query_end rebirth generation = %d after %d, want a fresh value >= 1", gNew, gOld)
	}

	// registerEngine: the production construction path seeds too. The main
	// slot in newTestConnector is built directly (gen 0), so a registered
	// engine's generation must be nonzero and distinct from it.
	hubMain := hub.NewHub()
	c3 := newTestConnectorWithHub(t, hubMain)
	eng2 := engine.New(&engine.Params{EngineID: "e2", Model: "m"})
	hub2 := hub.NewHub()
	eng2.SetDispatcher(hub2)
	c3.RegisterEngine(&engine.EngineViewState{ID: "e2", Engine: eng2, Name: "n2", Model: "m"})
	hubMain.Dispatch(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "m0", Name: "X"}})
	hubMain.Dispatch(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "m1", Name: "X"}})
	hub2.Dispatch(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "e20", Name: "X"}})
	hub2.Dispatch(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "e21", Name: "X"}})
	pMain := parseHistPage(t, c3.buildHistory(c3.activeSlot(), "", 1))
	_, _, gMain := parseAnchor(t, pMain.NextCursor)
	pE2 := parseHistPage(t, c3.buildHistory(c3.slots["e2"], "", 1))
	_, _, gE2 := parseAnchor(t, pE2.NextCursor)
	if gE2 < 1 || gE2 == gMain {
		t.Fatalf("registerEngine generation = %d (direct-built slot %d), want distinct and >= 1", gE2, gMain)
	}
}

// sessionRev boundary guards: idx == len, idx > len, and idx < 0 must all
// leave the full view intact (a mutated bounds check panics or truncates).
func TestSessionRev_BoundaryIndexes(t *testing.T) {
	mock := &mockEngine{}
	msgs := mutA2NMessages(6)
	mock.SetMessagesFn(func() []types.Message { return msgs })
	mock.SetQueryStartMsgIdxFn(func() int { return len(msgs) })
	if got, want := sessionRev(mock), "6:m05"; got != want {
		t.Errorf("rev at idx==len = %q, want %q (no truncation)", got, want)
	}
	mock.SetQueryStartMsgIdxFn(func() int { return len(msgs) + 2 })
	if got, want := sessionRev(mock), "6:m05"; got != want {
		t.Errorf("rev at idx>len = %q, want %q (no truncation)", got, want)
	}
	mock.SetQueryStartMsgIdxFn(func() int { return -1 })
	if got, want := sessionRev(mock), "6:m05"; got != want {
		t.Errorf("rev at idx<0 = %q, want %q (full view)", got, want)
	}
}

// Round-trip a valid anchor against a region whose generation is nonzero
// (registerEngine-seeded): the parsed G must address that region exactly —
// a dropped third Atoi field degrades G to 0 and mis-zeroes the window.
func TestBuildHistory_ExactAnchorRoundTripSeededGen(t *testing.T) {
	hubMain := hub.NewHub()
	c := newTestConnectorWithHub(t, hubMain)
	eng2 := engine.New(&engine.Params{EngineID: "e2", Model: "m"})
	hub2 := hub.NewHub()
	eng2.SetDispatcher(hub2)
	c.RegisterEngine(&engine.EngineViewState{ID: "e2", Engine: eng2, Name: "n2", Model: "m"})
	for i := range 3 {
		hub2.Dispatch(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{
			ID: fmt.Sprintf("e2-%d", i), Name: "X",
		}})
	}
	slot2 := c.slots["e2"]
	p1 := parseHistPage(t, c.buildHistory(slot2, "", 1))
	_, s, g := parseAnchor(t, p1.NextCursor)
	if g < 1 {
		t.Fatalf("seeded generation = %d, want >= 1", g)
	}
	p2 := parseHistPage(t, c.buildHistory(slot2, fmt.Sprintf("a:0:%d:%d", s, g), 30))
	if len(p2.Stream) != s || p2.Stream[0].ID != "e2-0" {
		t.Errorf("exact anchor round-trip = %d blocks starting %q, want %d starting e2-0",
			len(p2.Stream), p2.Stream[0].ID, s)
	}
}

// Stale-generation zeroing also applies at S == 1 (boundary of the s > 0
// guard), and each malformed shape logs its own exact warning.
func TestBuildHistory_StaleS1AndExactWarns(t *testing.T) {
	c := newTestConnector(t)
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t0", Name: "X"}})
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t1", Name: "X"}})
	p0 := parseHistPage(t, c.buildHistory(c.activeSlot(), "", 1))
	gOld := anchorG(t, p0.NextCursor)
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd})
	c.mock().SetMessagesFn(func() []types.Message {
		return []types.Message{mutA2UserMsg("m0", "t0")}
	})
	p := parseHistPage(t, c.buildHistory(c.activeSlot(), "a:1:1:"+gOld, 30))
	if len(p.Messages) != 1 || len(p.Stream) != 0 {
		t.Errorf("stale S=1 anchor page = %d msgs %d blocks, want 1/0 (S must zero)", len(p.Messages), len(p.Stream))
	}

	warns := swapMutA2WarnHandler(t)
	parseHistPage(t, c.buildHistory(c.activeSlot(), "a:1:2", 30))
	parseHistPage(t, c.buildHistory(c.activeSlot(), "junk", 30))
	wantAnchor := false
	wantJunk := false
	for _, m := range warns.records {
		if m == "wui: malformed anchor cursor, returning empty page" {
			wantAnchor = true
		}
		if m == "wui: malformed cursor, returning empty page" {
			wantJunk = true
		}
	}
	if !wantAnchor {
		t.Errorf("anchor-shaped junk did not log the anchor warning: %v", warns.records)
	}
	if !wantJunk {
		t.Errorf("plain junk did not log the cursor warning: %v", warns.records)
	}
}

// sessionRev at idx == 0 truncates to exactly the first row.
func TestSessionRev_ZeroIndex(t *testing.T) {
	mock := &mockEngine{}
	msgs := mutA2NMessages(6)
	mock.SetMessagesFn(func() []types.Message { return msgs })
	mock.SetQueryStartMsgIdxFn(func() int { return 0 })
	if got, want := sessionRev(mock), "1:m00"; got != want {
		t.Errorf("rev at idx 0 = %q, want %q", got, want)
	}
}

// An anchor whose generation outruns the target region's (harvested from a
// younger slot) must zero S there too — never slice the target's blocks.
func TestBuildHistory_CrossSlotStaleAnchor(t *testing.T) {
	hubMain := hub.NewHub()
	c := newTestConnectorWithHub(t, hubMain)
	eng2 := engine.New(&engine.Params{EngineID: "e2", Model: "m"})
	hub2 := hub.NewHub()
	eng2.SetDispatcher(hub2)
	c.RegisterEngine(&engine.EngineViewState{ID: "e2", Engine: eng2, Name: "n2", Model: "m"})
	hub2.Dispatch(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "e2-0", Name: "X"}})
	hub2.Dispatch(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "e2-1", Name: "X"}})
	slot2 := c.slots["e2"]
	gE2 := anchorG(t, parseHistPage(t, c.buildHistory(slot2, "", 1)).NextCursor)

	// The main slot's region (gen 0, direct-built) holds two blocks; the
	// e2-generation anchor with S=1 must zero and deliver committed only.
	c.mock().SetMessagesFn(func() []types.Message { return mutA2NMessages(1) })
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "m0", Name: "X"}})
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "m1", Name: "X"}})
	p := parseHistPage(t, c.buildHistory(c.activeSlot(), "a:1:1:"+gE2, 30))
	if len(p.Messages) != 1 || p.Messages[0].ID != "m00" {
		t.Errorf("cross-slot stale anchor messages = %+v, want the single committed row", p.Messages)
	}
	if len(p.Stream) != 0 {
		t.Errorf("cross-slot stale anchor stream = %v, want none (target region must not be sliced)", pageStreamIDs(p))
	}
}

// Wire-shape negatives: an idle page carries no busy key; a pre-compact page
// built without a request nonce carries no nonce key.
func TestBuildHistory_RawFieldAbsence(t *testing.T) {
	c := mutA2BareConnector()
	mock := &mockEngine{}
	mock.SetMessagesFn(func() []types.Message { return mutA2NMessages(2) })
	raw := c.buildHistory(mutA2Slot(mock), "", 30)
	if bytes.Contains(raw, []byte(`"busy"`)) {
		t.Errorf("idle page carries busy: %.120s", raw)
	}
	mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
		return nil, 0, true
	}
	raw = c.buildHistory(mutA2Slot(mock), "precompact:0", 30)
	if bytes.Contains(raw, []byte(`"nonce"`)) {
		t.Errorf("precompact page carries nonce without a request nonce: %.120s", raw)
	}
}

// hasMore must hold when exactly one unit remains above the window — the
// boundary of the remainder comparisons.
func TestBuildHistory_HasMoreRemainderOne(t *testing.T) {
	c := mutA2BareConnector()
	mock := &mockEngine{}
	mock.SetMessagesFn(func() []types.Message { return mutA2NMessages(31) })
	p := parseHistPage(t, c.buildHistory(mutA2Slot(mock), "", 30))
	if !p.HasMore || !regexp.MustCompile(`^a:1:0:\d+$`).MatchString(p.NextCursor) {
		t.Errorf("31-unit tail page hasMore/nextCursor = %v/%q, want true/a:1:0:<gen>", p.HasMore, p.NextCursor)
	}
}
