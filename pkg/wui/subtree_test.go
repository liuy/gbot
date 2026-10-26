package wui

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/types"
)

// subtreeWireBlock is the test-side wire view of a stream block carrying the
// truncation fields. Parsing the raw JSON (instead of reusing streamBlock)
// holds the wire contract itself: Truncated is a pointer so "key absent" is
// distinguishable from an explicit zero.
type subtreeWireBlock struct {
	Kind            string             `json:"kind"`
	ID              string             `json:"id"`
	Text            string             `json:"text,omitempty"`
	Name            string             `json:"name,omitempty"`
	State           string             `json:"state,omitempty"`
	Children        []subtreeWireBlock `json:"children"`
	Truncated       *int               `json:"truncated,omitempty"`
	TruncatedCursor string             `json:"truncatedCursor,omitempty"`
}

// makeAgentWithKids builds an Agent tool block whose Children are n tool
// blocks k0..k(n-1) — the shape updateStreamState produces for a marathon
// inline sub-agent (every tool child carries a non-nil empty Children slice).
func makeAgentWithKids(id string, n int) streamBlock {
	kids := make([]streamBlock, n)
	for i := range kids {
		kids[i] = streamBlock{Kind: "tool", ID: fmt.Sprintf("k%d", i), Name: "Bash", State: "running", Children: []streamBlock{}}
	}
	return streamBlock{Kind: "tool", ID: id, Name: "Agent", State: "running", Children: kids}
}

// T3: a marathon block's frozen window keeps only the tail K children and
// carries the hidden-head count as an anchor, while the source tree stays
// untouched (live events keep appending to it after the freeze).
func TestBuildHistory_MarathonWindowTruncated(t *testing.T) {
	c := mutA2BareConnector()
	tree := []streamBlock{makeAgentWithKids("ag", 1000)}
	slot := mutA2Slot(&mockEngine{})
	slot.streams = map[string]*streamState{"": {blocks: tree, gen: 5}}

	payload := c.buildHistory(slot, "", 30)
	var env struct {
		Type   string             `json:"type"`
		Stream []subtreeWireBlock `json:"stream"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		t.Fatalf("unmarshal history page: %v", err)
	}
	if env.Type != "history" {
		t.Fatalf("frame type = %q, want history", env.Type)
	}
	if len(env.Stream) != 1 {
		t.Fatalf("stream window = %d blocks, want 1", len(env.Stream))
	}
	got := env.Stream[0]
	if len(got.Children) != 30 {
		t.Fatalf("frozen children = %d, want 30", len(got.Children))
	}
	for i, k := range got.Children {
		if want := fmt.Sprintf("k%d", 970+i); k.ID != want {
			t.Errorf("child %d id = %q, want %q (tail window)", i, k.ID, want)
		}
	}
	if got.Truncated == nil || *got.Truncated != 970 {
		t.Errorf("truncated = %v, want 970", got.Truncated)
	}
	if got.TruncatedCursor != "s:970:5" {
		t.Errorf("truncatedCursor = %q, want s:970:5", got.TruncatedCursor)
	}
	if len(tree[0].Children) != 1000 || tree[0].Children[0].ID != "k0" {
		t.Fatalf("source tree mutated: len=%d first=%q", len(tree[0].Children), tree[0].Children[0].ID)
	}
}

// wireNodeKeys marshals v, strips the given keys, and re-marshals the
// remaining map — Go maps marshal with sorted keys, so two nodes equal on
// the kept keys produce identical bytes.
func wireNodeKeys(t *testing.T, v any, drop ...string) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal node: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal node map: %v", err)
	}
	for _, k := range drop {
		delete(m, k)
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("re-marshal node map: %v", err)
	}
	return string(out)
}

// wireHasKey reports whether marshaling v emits the JSON key.
func wireHasKey(v any, key string) bool {
	raw, _ := json.Marshal(v)
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	_, ok := m[key]
	return ok
}

// assertFreezeEquivalent checks got against its source slice want: every got
// node must be the corresponding tail node of want, byte-identical except
// for children/truncated/truncatedCursor, and carry truncated exactly when
// children were dropped.
func assertFreezeEquivalent(t *testing.T, got, want []streamBlock, gen int) {
	t.Helper()
	if len(got) > len(want) {
		t.Fatalf("frozen slice (%d) longer than source (%d)", len(got), len(want))
	}
	off := len(want) - len(got)
	for i := range got {
		g, w := got[i], want[off+i]
		gotKeys := wireNodeKeys(t, g, "children", "truncated", "truncatedCursor")
		wantKeys := wireNodeKeys(t, w, "children", "truncated", "truncatedCursor")
		if gotKeys != wantKeys {
			t.Errorf("node %d body changed by freeze:\ngot  %s\nwant %s", i, gotKeys, wantKeys)
		}
		hidden := len(w.Children) - len(g.Children)
		if hidden > 0 {
			if g.Truncated != hidden {
				t.Errorf("node %d truncated = %d, want hidden count %d", i, g.Truncated, hidden)
			}
			var e, gg int
			if _, err := fmt.Sscanf(g.TruncatedCursor, "s:%d:%d", &e, &gg); err != nil {
				t.Errorf("node %d truncatedCursor %q unparseable: %v", i, g.TruncatedCursor, err)
			} else if e != hidden || gg != gen {
				t.Errorf("node %d truncatedCursor = %q, want s:%d:%d", i, g.TruncatedCursor, hidden, gen)
			}
		} else {
			if g.Truncated != 0 || g.TruncatedCursor != "" {
				t.Errorf("node %d truncated=%d cursor=%q with nothing hidden", i, g.Truncated, g.TruncatedCursor)
			}
			if wireHasKey(g, "truncated") {
				t.Errorf("node %d emits a truncated key with nothing hidden", i)
			}
			if wireHasKey(g, "truncatedCursor") {
				t.Errorf("node %d emits a truncatedCursor key with nothing hidden", i)
			}
		}
		assertFreezeEquivalent(t, g.Children, w.Children, gen)
	}
}

// countNodes recursively counts every node in the forest (headers included).
func countNodes(blocks []streamBlock) int {
	n := 0
	for i := range blocks {
		n += 1 + countNodes(blocks[i].Children)
	}
	return n
}

// countWireNodes is countNodes over the wire-side parse tree.
func countWireNodes(blocks []subtreeWireBlock) int {
	n := 0
	for i := range blocks {
		n += 1 + countWireNodes(blocks[i].Children)
	}
	return n
}

// T1: the freeze is purely subtractive-plus-marker — every kept node is a
// value copy of its source tail node with only the three additive keys
// allowed to differ, truncated is exactly the hidden head length, and
// untruncated nodes emit no truncated keys at all.
func TestFreezeWindow_Equivalence(t *testing.T) {
	kids := make([]streamBlock, 80)
	for i := range kids {
		if i%2 == 0 {
			kids[i] = streamBlock{Kind: "text", Text: fmt.Sprintf("c%d", i)}
		} else {
			kids[i] = streamBlock{Kind: "tool", ID: fmt.Sprintf("k%d", i), Name: "Bash", State: "running", Children: []streamBlock{}}
		}
	}
	grand := make([]streamBlock, 40)
	for i := range grand {
		grand[i] = streamBlock{Kind: "text", Text: fmt.Sprintf("g%d", i)}
	}
	kids[79] = streamBlock{Kind: "tool", ID: "nested", Name: "Agent", State: "running", Children: grand}
	tree := []streamBlock{{Kind: "tool", ID: "ag", Name: "Agent", State: "running", Children: kids}}

	got := freezeWindow(tree, 7)
	if len(got) != 1 {
		t.Fatalf("frozen window = %d blocks, want 1", len(got))
	}
	if len(got[0].Children) != 30 {
		t.Fatalf("kept children = %d, want 30 (tail)", len(got[0].Children))
	}
	if got[0].Children[0].Text != "c50" {
		t.Errorf("first kept child = %+v, want the c50 tail head", got[0].Children[0])
	}
	nested := got[0].Children[29]
	if nested.ID != "nested" || len(nested.Children) != 30 || nested.Children[0].Text != "g10" {
		t.Errorf("nested agent tail wrong: id=%s kids=%d first=%+v", nested.ID, len(nested.Children), nested.Children[0])
	}
	assertFreezeEquivalent(t, got, tree, 7)
	if len(tree[0].Children) != 80 || len(tree[0].Children[79].Children) != 40 {
		t.Fatalf("source tree mutated by freezeWindow")
	}
}

// T2: with 30 blocks × 10 children the global budget binds — the youngest
// 20 blocks keep everything, the oldest 10 fold to kept=0 with truncated=10,
// and no content vanishes: truncated sums + kept counts == the source total.
func TestFreezeWindow_BudgetCapAndConservation(t *testing.T) {
	blocks := make([]streamBlock, 30)
	for i := range blocks {
		blocks[i] = makeAgentWithKids(fmt.Sprintf("a%d", i), 10)
	}
	got := freezeWindow(blocks, 3)
	if len(got) != 30 {
		t.Fatalf("frozen window = %d blocks, want all 30 headers", len(got))
	}
	keptTotal, truncTotal := 0, 0
	for i := range got {
		keptTotal += countNodes(got[i].Children)
		truncTotal += got[i].Truncated
	}
	if keptTotal != 200 {
		t.Errorf("kept child nodes = %d, want exactly the 200 budget", keptTotal)
	}
	if truncTotal != 100 {
		t.Errorf("truncated sum = %d, want 100", truncTotal)
	}
	if want := countNodes(blocks); truncTotal+keptTotal != want-30 {
		// want counts headers too; subtract the 30 always-kept top headers.
		t.Errorf("conservation broken: %d+%d != %d", truncTotal, keptTotal, want-30)
	}
	if len(got[29].Children) != 10 || got[29].Truncated != 0 {
		t.Errorf("youngest block folded: kids=%d truncated=%d, want 10/0", len(got[29].Children), got[29].Truncated)
	}
	if len(got[10].Children) != 10 || got[10].Truncated != 0 {
		t.Errorf("block 10 folded: kids=%d truncated=%d, want 10/0 (budget covers it)", len(got[10].Children), got[10].Truncated)
	}
	for _, i := range []int{0, 9} {
		if len(got[i].Children) != 0 || got[i].Truncated != 10 {
			t.Errorf("oldest block %d: kids=%d truncated=%d, want 0/10", i, len(got[i].Children), got[i].Truncated)
		}
		if got[i].Children == nil {
			t.Errorf("block %d kept-0 children is nil, want non-nil [] wire shape", i)
		}
		if got[i].TruncatedCursor != "s:10:3" {
			t.Errorf("block %d truncatedCursor = %q, want s:10:3", i, got[i].TruncatedCursor)
		}
	}
}

// T4: a tree under every threshold serializes exactly as before the
// recursive freeze — byte-identical wire including "children":null on text
// blocks and no truncated keys anywhere. This is what keeps the phase-1
// suite adaptation-free.
func TestBuildHistory_SmallTreeUnchanged(t *testing.T) {
	c := mutA2BareConnector()
	texts := make([]streamBlock, 5)
	for i := range texts {
		texts[i] = streamBlock{Kind: "text", Text: fmt.Sprintf("c%d", i)}
	}
	agent := streamBlock{Kind: "tool", ID: "ag", Name: "Agent", State: "running", Children: texts}
	slot := mutA2Slot(&mockEngine{})
	slot.streams = map[string]*streamState{"": {blocks: []streamBlock{agent}, gen: 5}}

	payload := c.buildHistory(slot, "", 30)
	var env struct {
		Stream json.RawMessage `json:"stream"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		t.Fatalf("unmarshal history page: %v", err)
	}
	want := `[{"kind":"tool","id":"ag","name":"Agent","state":"running","children":[{"kind":"text","id":"","text":"c0","children":null},{"kind":"text","id":"","text":"c1","children":null},{"kind":"text","id":"","text":"c2","children":null},{"kind":"text","id":"","text":"c3","children":null},{"kind":"text","id":"","text":"c4","children":null}]}]`
	if string(env.Stream) != want {
		t.Fatalf("small-tree stream wire =\n%s\nwant\n%s", env.Stream, want)
	}
}

// subtreePageFrame mirrors the subtree_page wire frame.
type subtreePageFrame struct {
	Type       string             `json:"type"`
	BlockID    string             `json:"blockID"`
	Children   []subtreeWireBlock `json:"children"`
	NextAnchor string             `json:"nextAnchor"`
	HasMore    bool               `json:"hasMore"`
	Remaining  int                `json:"remaining"`
	Stale      bool               `json:"stale"`
	Seq        uint64             `json:"seq"`
	Nonce      uint64             `json:"nonce"`
}

func parseSubtreePage(t *testing.T, payload []byte) subtreePageFrame {
	t.Helper()
	var f subtreePageFrame
	if err := json.Unmarshal(payload, &f); err != nil {
		t.Fatalf("unmarshal subtree_page: %v (raw: %.200s)", err, payload)
	}
	if f.Type != "subtree_page" {
		t.Fatalf("frame type = %q, want subtree_page (raw: %.200s)", f.Type, payload)
	}
	return f
}

// requestSubtree sends one subtree_request over the WS and parses the reply.
func requestSubtree(t *testing.T, ws *websocket.Conn, blockID, anchor string, limit int, nonce uint64) subtreePageFrame {
	t.Helper()
	req, err := json.Marshal(map[string]any{"type": "subtree_request", "blockID": blockID, "anchor": anchor, "limit": limit, "nonce": nonce})
	if err != nil {
		t.Fatalf("marshal subtree_request: %v", err)
	}
	if err := ws.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write subtree_request: %v", err)
	}
	return parseSubtreePage(t, readWSMessage(t, ws))
}

// dispatchAgentWithKids drives the real event path: a parent Agent tool_start
// plus n sub-agent tool_start events nesting under it.
func dispatchAgentWithKids(c *WUIConnector, parentID string, n int) {
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: parentID, Name: "Agent"}})
	agent := &types.AgentMeta{ParentToolUseID: parentID, AgentType: "Explore"}
	for i := range n {
		c.Handle(types.QueryEvent{Type: types.EventToolStart, Agent: agent, ToolUse: &types.ToolUseEvent{ID: fmt.Sprintf("k%d", i), Name: "Bash"}})
	}
}

// T5: the full live chain — events → page0 → subtree_request over a real WS →
// paged children with the anchor walking down to the last page.
func TestSubtreeRequest_MainPathPaging(t *testing.T) {
	hermeticWUI(t, func(t *testing.T, c *WUIConnector, hs *chatServer) {
		dispatchAgentWithKids(c, "agent1", 100)
		ws := hs.dialDrained(t)

		page := requestHistory(t, ws, "", 30, 4241)
		var pEnv struct {
			Stream []subtreeWireBlock `json:"stream"`
		}
		if err := json.Unmarshal(page, &pEnv); err != nil {
			t.Fatalf("unmarshal page0: %v", err)
		}
		if len(pEnv.Stream) != 1 {
			t.Fatalf("page0 stream = %d blocks, want 1", len(pEnv.Stream))
		}
		agent := pEnv.Stream[0]
		if len(agent.Children) != 30 || agent.Truncated == nil || *agent.Truncated != 70 {
			t.Fatalf("page0 agent: kids=%d truncated=%v, want 30/70", len(agent.Children), agent.Truncated)
		}
		anchor := agent.TruncatedCursor

		g := anchor[len(anchor)-1:]
		f := requestSubtree(t, ws, "agent1", anchor, 30, 4242)
		if f.Stale {
			t.Fatal("page 1 unexpectedly stale")
		}
		if len(f.Children) != 30 || f.Children[0].ID != "k40" || f.Children[29].ID != "k69" {
			t.Fatalf("page 1 children = %d (%s..%s), want 30 k40..k69", len(f.Children), f.Children[0].ID, f.Children[len(f.Children)-1].ID)
		}
		if f.NextAnchor != "s:40:"+g || f.Remaining != 40 || !f.HasMore {
			t.Fatalf("page 1 anchor state = %q/%d/%v, want s:40:%s/40/true", f.NextAnchor, f.Remaining, f.HasMore, g)
		}
		if f.Nonce != 4242 || f.Seq == 0 {
			t.Fatalf("page 1 echo: nonce=%d seq=%d, want 4242/non-zero", f.Nonce, f.Seq)
		}

		f = requestSubtree(t, ws, "agent1", f.NextAnchor, 30, 4243)
		if len(f.Children) != 30 || f.Children[0].ID != "k10" || f.Children[29].ID != "k39" {
			t.Fatalf("page 2 children = %s..%s, want k10..k39", f.Children[0].ID, f.Children[len(f.Children)-1].ID)
		}
		if f.NextAnchor != "s:10:"+g || f.Remaining != 10 || !f.HasMore {
			t.Fatalf("page 2 anchor state = %q/%d/%v", f.NextAnchor, f.Remaining, f.HasMore)
		}

		f = requestSubtree(t, ws, "agent1", f.NextAnchor, 30, 4244)
		if len(f.Children) != 10 || f.Children[0].ID != "k0" || f.Children[9].ID != "k9" {
			t.Fatalf("tail page children = %s..%s, want k0..k9", f.Children[0].ID, f.Children[len(f.Children)-1].ID)
		}
		if f.HasMore || f.NextAnchor != "" || f.Remaining != 0 {
			t.Fatalf("tail page state = %q/%d/%v, want terminal", f.NextAnchor, f.Remaining, f.HasMore)
		}
	})
}

// subtreeSlotOf returns the active slot with its root region's generation.
func subtreeSlotOf(t *testing.T, c *WUIConnector) (*engineSlot, int) {
	t.Helper()
	slot := c.activeSlotTest(t)
	root := slot.streams[""]
	if root == nil {
		t.Fatal("root stream region missing")
	}
	return slot, root.gen
}

// T6: an anchor minted in a dead generation (query_end reset the region
// under it) is stale — its hidden children no longer exist to page into.
func TestSubtreeRequest_GenStale(t *testing.T) {
	c := newTestConnector(t)
	dispatchAgentWithKids(c, "agent1", 100)
	slot, g := subtreeSlotOf(t, c)

	f := parseSubtreePage(t, c.buildSubtreePage(slot, "agent1", fmt.Sprintf("s:70:%d", g+1), 30, 7))
	if !f.Stale {
		t.Fatal("wrong-generation anchor must be stale")
	}
	if f.BlockID != "agent1" || f.Nonce != 7 {
		t.Fatalf("stale frame echo: blockID=%q nonce=%d, want agent1/7", f.BlockID, f.Nonce)
	}
	if len(f.Children) != 0 || f.HasMore {
		t.Fatalf("stale frame carries content: children=%d hasMore=%v", len(f.Children), f.HasMore)
	}
}

// T7: query_end replaces the whole region, so a pre-end anchor is stale.
func TestSubtreeRequest_QueryEndStale(t *testing.T) {
	c := newTestConnector(t)
	dispatchAgentWithKids(c, "agent1", 100)
	_, g := subtreeSlotOf(t, c)
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd})
	slot, g2 := subtreeSlotOf(t, c)
	if g2 == g {
		t.Fatal("query_end did not reseed the region generation")
	}

	f := parseSubtreePage(t, c.buildSubtreePage(slot, "agent1", fmt.Sprintf("s:70:%d", g), 30, 8))
	if !f.Stale {
		t.Fatal("pre-query_end anchor must be stale after the region reset")
	}
}

// T8: tool_end clears the Agent block's children entirely; an anchor
// pointing past the now-empty slice is stale.
func TestSubtreeRequest_ToolEndClearedStale(t *testing.T) {
	c := newTestConnector(t)
	dispatchAgentWithKids(c, "agent1", 100)
	slot, g := subtreeSlotOf(t, c)
	c.Handle(types.QueryEvent{Type: types.EventToolEnd, ToolResult: &types.ToolResultEvent{ToolUseID: "agent1"}})
	if b := findBlock(slot.streams[""].blocks, "agent1"); b == nil || len(b.Children) != 0 {
		t.Fatal("tool_end did not clear the agent children")
	}

	f := parseSubtreePage(t, c.buildSubtreePage(slot, "agent1", fmt.Sprintf("s:70:%d", g), 30, 9))
	if !f.Stale {
		t.Fatal("anchor past a tool_end-cleared children slice must be stale")
	}
}

// T9: children are append-only, so indexes minted by a freeze stay valid
// while live events keep landing — an old anchor pages the same slice.
func TestSubtreeRequest_LiveAppendStable(t *testing.T) {
	c := newTestConnector(t)
	dispatchAgentWithKids(c, "agent1", 100)
	slot, g := subtreeSlotOf(t, c)
	anchor := fmt.Sprintf("s:70:%d", g)

	agent := &types.AgentMeta{ParentToolUseID: "agent1", AgentType: "Explore"}
	for i := 100; i < 105; i++ {
		c.Handle(types.QueryEvent{Type: types.EventToolStart, Agent: agent, ToolUse: &types.ToolUseEvent{ID: fmt.Sprintf("k%d", i), Name: "Bash"}})
	}

	f := parseSubtreePage(t, c.buildSubtreePage(slot, "agent1", anchor, 30, 10))
	if f.Stale {
		t.Fatal("live append must not stale an old anchor")
	}
	if len(f.Children) != 30 || f.Children[0].ID != "k40" || f.Children[29].ID != "k69" {
		t.Fatalf("children = %s..%s, want the same k40..k69 slice", f.Children[0].ID, f.Children[29].ID)
	}
	if f.NextAnchor != fmt.Sprintf("s:40:%d", g) || f.Remaining != 40 {
		t.Fatalf("anchor state = %q/%d, want s:40:%d/40", f.NextAnchor, f.Remaining, g)
	}
}

// T10: with grandchildren beyond the budget the excess drops fold into the
// next page's cursor, and repeated pulls strictly decrease the hidden head
// to zero with at least one kept child per page — the no-infinite-paging
// invariant.
func TestSubtreeRequest_BudgetFoldAndTermination(t *testing.T) {
	kids := make([]streamBlock, 30)
	for i := range kids {
		kids[i] = makeAgentWithKids(fmt.Sprintf("k%d", i), 30)
	}
	tree := []streamBlock{{Kind: "tool", ID: "agent1", Name: "Agent", State: "running", Children: kids}}
	c := mutA2BareConnector()
	slot := mutA2Slot(&mockEngine{})
	slot.streams = map[string]*streamState{"": {blocks: tree, gen: 5}}

	e := 30
	pulls := 0
	for {
		pulls++
		if pulls > 20 {
			t.Fatal("subtree paging did not converge within 20 pulls")
		}
		f := parseSubtreePage(t, c.buildSubtreePage(slot, "agent1", fmt.Sprintf("s:%d:5", e), 30, 100+uint64(pulls)))
		if f.Stale {
			t.Fatalf("pull %d unexpectedly stale", pulls)
		}
		if len(f.Children) < 1 {
			t.Fatalf("pull %d kept 0 children — termination invariant broken", pulls)
		}
		if countWireNodes(f.Children) > 200 {
			t.Fatalf("pull %d kept %d nodes, over the budget", pulls, countWireNodes(f.Children))
		}
		if !f.HasMore {
			if f.NextAnchor != "" || f.Remaining != 0 {
				t.Fatalf("terminal pull has nextAnchor=%q remaining=%d", f.NextAnchor, f.Remaining)
			}
			break
		}
		if f.Remaining >= e {
			t.Fatalf("pull %d remaining %d not strictly below E %d", pulls, f.Remaining, e)
		}
		var ne int
		if _, err := fmt.Sscanf(f.NextAnchor, "s:%d:5", &ne); err != nil || ne != f.Remaining {
			t.Fatalf("nextAnchor %q does not encode remaining %d", f.NextAnchor, f.Remaining)
		}
		e = f.Remaining
	}
	if pulls != 5 {
		t.Errorf("converged in %d pulls, want 5", pulls)
	}

	// First pull specifics: budget cuts mid-page, the cut grandchild parent
	// carries its own truncated marker, and the fold is nextStart = 23.
	first := parseSubtreePage(t, c.buildSubtreePage(slot, "agent1", "s:30:5", 30, 1))
	if len(first.Children) != 7 || first.Children[0].ID != "k23" {
		t.Fatalf("first pull kept %s.., want 7 kids starting k23", first.Children[0].ID)
	}
	if first.Children[0].Truncated == nil || *first.Children[0].Truncated != 17 {
		t.Fatalf("kid k23 truncated = %v, want 17 (budget cut mid-page)", first.Children[0].Truncated)
	}
	if first.Remaining != 23 || first.NextAnchor != "s:23:5" {
		t.Fatalf("first pull fold = %d/%q, want 23/s:23:5", first.Remaining, first.NextAnchor)
	}
}

// T11: malformed anchors warn and answer stale; a well-formed but
// wrong-generation anchor is silently stale.
func TestSubtreeRequest_BadAnchorMatrix(t *testing.T) {
	c := newTestConnector(t)
	dispatchAgentWithKids(c, "agent1", 100)
	slot, g := subtreeSlotOf(t, c)
	rec := mutCCaptureSlog(t)

	for _, anchor := range []string{"", "s:x:y", "a:1:2:3", "s:-1:" + fmt.Sprint(g), "s:x:" + fmt.Sprint(g), "s:70:-1"} {
		f := parseSubtreePage(t, c.buildSubtreePage(slot, "agent1", anchor, 30, 5))
		if !f.Stale {
			t.Errorf("anchor %q must be stale", anchor)
		}
	}
	n := 0
	for _, m := range rec.messages() {
		if m == "wui: malformed subtree anchor, returning stale page" {
			n++
		}
	}
	if n != 6 {
		t.Errorf("warn count = %d, want 6 (one per malformed anchor)", n)
	}
	warnsBefore := n
	f := parseSubtreePage(t, c.buildSubtreePage(slot, "agent1", fmt.Sprintf("s:70:%d", g+40), 30, 6))
	if !f.Stale {
		t.Error("out-of-generation anchor must be stale without a warn")
	}
	warnsAfter := 0
	for _, m := range rec.messages() {
		if m == "wui: malformed subtree anchor, returning stale page" {
			warnsAfter++
		}
	}
	if warnsAfter != warnsBefore {
		t.Errorf("well-formed anchor logged a malformed warn (%d → %d)", warnsBefore, warnsAfter)
	}
}

// T12: two in-flight requests with distinct nonces and blockIDs each get
// their own echo back over one connection.
func TestSubtreeRequest_PerRequestNonce(t *testing.T) {
	hermeticWUI(t, func(t *testing.T, c *WUIConnector, hs *chatServer) {
		dispatchAgentWithKids(c, "agent1", 100)
		dispatchAgentWithKids(c, "agent2", 80)
		ws := hs.dialDrained(t)

		page := requestHistory(t, ws, "", 30, 51)
		var pEnv struct {
			Stream []subtreeWireBlock `json:"stream"`
		}
		if err := json.Unmarshal(page, &pEnv); err != nil {
			t.Fatalf("unmarshal page0: %v", err)
		}
		if len(pEnv.Stream) != 2 {
			t.Fatalf("page0 stream = %d blocks, want 2", len(pEnv.Stream))
		}
		anchors := map[string]string{}
		for _, b := range pEnv.Stream {
			anchors[b.ID] = b.TruncatedCursor
		}

		for _, tc := range []struct {
			blockID string
			nonce   uint64
		}{{"agent1", 11}, {"agent2", 22}} {
			req, err := json.Marshal(map[string]any{"type": "subtree_request", "blockID": tc.blockID, "anchor": anchors[tc.blockID], "limit": 30, "nonce": tc.nonce})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := ws.WriteMessage(websocket.TextMessage, req); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
		for _, tc := range []struct {
			blockID string
			nonce   uint64
			first   string
			last    string
			remain  int
		}{{"agent1", 11, "k40", "k69", 40}, {"agent2", 22, "k20", "k49", 20}} {
			f := parseSubtreePage(t, readWSMessage(t, ws))
			if f.BlockID != tc.blockID || f.Nonce != tc.nonce {
				t.Fatalf("echo = %s/%d, want %s/%d", f.BlockID, f.Nonce, tc.blockID, tc.nonce)
			}
			if f.Stale {
				t.Fatalf("%s page unexpectedly stale", tc.blockID)
			}
			if len(f.Children) != 30 || f.Children[0].ID != tc.first || f.Children[29].ID != tc.last {
				t.Fatalf("%s children = %s..%s, want %s..%s", tc.blockID, f.Children[0].ID, f.Children[29].ID, tc.first, tc.last)
			}
			if f.Remaining != tc.remain {
				t.Fatalf("%s remaining = %d, want %d", tc.blockID, f.Remaining, tc.remain)
			}
		}
	})
}

// Sentinel/degenerate-shape coverage for the freeze: nil children on
// childless top blocks, exactly-one-child nodes at both depths, and
// hidden==0 / hidden==1 markers all have wire-visible behavior.
func TestFreezeWindow_EdgeShapes(t *testing.T) {
	grand := make([]streamBlock, 40)
	for i := range grand {
		grand[i] = streamBlock{Kind: "text", Text: fmt.Sprintf("g%d", i)}
	}
	oneKid := streamBlock{Kind: "tool", ID: "one", Name: "Agent", State: "running",
		Children: []streamBlock{{Kind: "tool", ID: "nested", Name: "Agent", State: "running", Children: grand}}}
	tree := []streamBlock{
		{Kind: "text", Text: "t0"},
		oneKid,
		makeAgentWithKids("small", 31),
	}

	got := freezeWindow(tree, 9)
	if got[0].Children != nil {
		t.Errorf("childless text block children = %v, want nil (children:null wire shape)", got[0].Children)
	}
	if len(got[1].Children) != 1 || got[1].Children[0].Truncated != 10 || got[1].Children[0].TruncatedCursor != "s:10:9" {
		t.Errorf("single-child top block's child not frozen: %+v", got[1].Children[0])
	}
	if got[2].Truncated != 1 || got[2].TruncatedCursor != "s:1:9" || len(got[2].Children) != 30 {
		t.Errorf("hidden==1 top block: truncated=%d cursor=%q kids=%d, want 1/s:1:9/30", got[2].Truncated, got[2].TruncatedCursor, len(got[2].Children))
	}
	assertFreezeEquivalent(t, got, tree, 9)

	// Depth-1 shapes through freezeChildren directly: hidden 1, a
	// single-child kid whose grandchildren must still freeze, hidden 0.
	src := []streamBlock{
		makeAgentWithKids("k31", 31),
		{Kind: "tool", ID: "k1", Name: "Agent", State: "running",
			Children: []streamBlock{{Kind: "tool", ID: "nested", Name: "Agent", State: "running", Children: grand}}},
		makeAgentWithKids("k3", 3),
	}
	budget := freezeNodeBudget
	kept, dropped := freezeChildren(src, &budget, 9)
	if dropped != 0 || len(kept) != 3 {
		t.Fatalf("kept=%d dropped=%d, want 3/0 (everything fits)", len(kept), dropped)
	}
	if kept[0].Truncated != 1 || kept[0].TruncatedCursor != "s:1:9" {
		t.Errorf("k31 truncated=%d cursor=%q, want 1/s:1:9", kept[0].Truncated, kept[0].TruncatedCursor)
	}
	if kept[1].Children[0].Truncated != 10 {
		t.Errorf("single-child kid's child not frozen: truncated=%d, want 10", kept[1].Children[0].Truncated)
	}
	if kept[2].Truncated != 0 || kept[2].TruncatedCursor != "" {
		t.Errorf("all-kept kid truncated=%d cursor=%q, want 0/\"\"", kept[2].Truncated, kept[2].TruncatedCursor)
	}
	assertFreezeEquivalent(t, kept, src, 9)
}

// Degenerate subtree requests: nil slot / missing root region answer stale
// (never panic), and the stale closure carries zero-value continuation
// fields.
func TestSubtreeRequest_NilSlotAndMissingRegion(t *testing.T) {
	c := mutA2BareConnector()
	if got := c.buildSubtreePage(nil, "a", "s:1:1", 30, 1); got != nil {
		t.Fatalf("buildSubtreePage(nil) = %.40s, want nil", got)
	}
	// No active slot on a bare connector: must return, not dereference nil.
	c.serveSubtreePage("a", "s:1:1", 30, 1)

	slot := mutA2Slot(&mockEngine{})
	f := parseSubtreePage(t, c.buildSubtreePage(slot, "a", "s:1:1", 30, 2))
	if !f.Stale {
		t.Fatal("missing root region must answer stale")
	}
	if f.Remaining != 0 || f.NextAnchor != "" || f.HasMore {
		t.Fatalf("stale continuation fields = %q/%d/%v, want zero values", f.NextAnchor, f.Remaining, f.HasMore)
	}
}

// limit<=0 falls back to the default page size; anchor E=0 is a well-formed
// empty page, not stale.
func TestSubtreeRequest_DefaultLimitAndZeroAnchor(t *testing.T) {
	c := mutA2BareConnector()
	slot := mutA2Slot(&mockEngine{})
	slot.streams = map[string]*streamState{"": {blocks: []streamBlock{makeAgentWithKids("agent1", 100)}, gen: 5}}

	f := parseSubtreePage(t, c.buildSubtreePage(slot, "agent1", "s:70:5", 0, 3))
	if f.Stale {
		t.Fatal("limit=0 must take the default, not go stale")
	}
	if len(f.Children) != 30 || f.Children[0].ID != "k40" || f.Children[29].ID != "k69" {
		t.Fatalf("default-limit children = %d (%s..%s), want 30 k40..k69", len(f.Children), f.Children[0].ID, f.Children[29].ID)
	}

	// limit=1 is a valid small page: exactly one child, not the default.
	f = parseSubtreePage(t, c.buildSubtreePage(slot, "agent1", "s:70:5", 1, 7))
	if f.Stale {
		t.Fatal("limit=1 must page, not go stale")
	}
	if len(f.Children) != 1 || f.Children[0].ID != "k69" || f.Remaining != 69 || f.NextAnchor != "s:69:5" || !f.HasMore {
		t.Fatalf("limit=1 page = %d kids (%s) %q/%d/%v, want 1 k69 s:69:5/69/true", len(f.Children), f.Children[0].ID, f.NextAnchor, f.Remaining, f.HasMore)
	}

	f = parseSubtreePage(t, c.buildSubtreePage(slot, "agent1", "s:0:5", 30, 4))
	if f.Stale {
		t.Fatal("E=0 is a well-formed (empty) anchor, not stale")
	}
	if len(f.Children) != 0 || f.HasMore || f.Remaining != 0 || f.NextAnchor != "" {
		t.Fatalf("E=0 page = %d kids/%q/%d/%v, want empty terminal", len(f.Children), f.NextAnchor, f.Remaining, f.HasMore)
	}
}

// A page ending at exactly one hidden block still reports hasMore, and the
// next pull is terminal; seqs strictly increase across pulls.
func TestSubtreeRequest_LastSingleHidden(t *testing.T) {
	c := mutA2BareConnector()
	slot := mutA2Slot(&mockEngine{})
	slot.streams = map[string]*streamState{"": {blocks: []streamBlock{makeAgentWithKids("agent1", 31)}, gen: 5}}

	f1 := parseSubtreePage(t, c.buildSubtreePage(slot, "agent1", "s:31:5", 30, 5))
	if len(f1.Children) != 30 || f1.Children[0].ID != "k1" {
		t.Fatalf("first pull children = %s.., want k1.. (30)", f1.Children[0].ID)
	}
	if !f1.HasMore || f1.Remaining != 1 || f1.NextAnchor != "s:1:5" {
		t.Fatalf("one-hidden-left state = %v/%d/%q, want true/1/s:1:5", f1.HasMore, f1.Remaining, f1.NextAnchor)
	}

	f2 := parseSubtreePage(t, c.buildSubtreePage(slot, "agent1", f1.NextAnchor, 30, 6))
	if len(f2.Children) != 1 || f2.Children[0].ID != "k0" || f2.HasMore {
		t.Fatalf("final pull = %d kids, want 1 (k0) terminal", len(f2.Children))
	}
	if f2.Seq-f1.Seq != 1 {
		t.Fatalf("seq delta across pages = %d, want exactly 1 (one seq stamp per frame)", f2.Seq-f1.Seq)
	}
}

// T13: subtree paging serves only the root region — a block living in a job
// region is invisible to it.
func TestSubtreeRequest_RootRegionOnly(t *testing.T) {
	c := mutA2BareConnector()
	slot := mutA2Slot(&mockEngine{})
	slot.streams = map[string]*streamState{
		"":    {blocks: []streamBlock{makeAgentWithKids("agent1", 40)}, gen: 5},
		"job": {blocks: []streamBlock{makeAgentWithKids("jagent", 40)}, gen: 5},
	}

	f := parseSubtreePage(t, c.buildSubtreePage(slot, "jagent", "s:10:5", 30, 12))
	if !f.Stale {
		t.Fatal("job-region block must be stale on the subtree endpoint")
	}
	if f := parseSubtreePage(t, c.buildSubtreePage(slot, "agent1", "s:10:5", 30, 13)); f.Stale {
		t.Fatal("root-region block must still page normally")
	}
}
