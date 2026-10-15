package short

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// chainMsg builds a transcript message with a timestamp offset from the shared
// test base, so recovery ordering assertions stay deterministic.
func chainMsg(uuid, msgType, parentUUID, content string, offset time.Duration, seq int64) *TranscriptMessage {
	return &TranscriptMessage{
		UUID:       uuid,
		Type:       msgType,
		ParentUUID: parentUUID,
		Content:    content,
		CreatedAt:  testTimeBase.Add(offset),
		Seq:        seq,
	}
}

func assertChainUUIDs(t *testing.T, got []*TranscriptMessage, want ...string) {
	t.Helper()

	if got == nil {
		t.Fatalf("got nil slice, want %v", want)
	}
	if len(got) != len(want) {
		uuids := make([]string, len(got))
		for i, m := range got {
			uuids[i] = m.UUID
		}
		t.Fatalf("chain = %v (len %d), want %v (len %d)", uuids, len(got), want, len(want))
	}
	for i, m := range got {
		if m.UUID != want[i] {
			t.Errorf("chain[%d] = %q, want %q", i, m.UUID, want[i])
		}
	}
}

// --- buildChainFromMessages ---

func TestBuildChainFromMessages_EmptyInputReturnsEmptySlice(t *testing.T) {
	chain := buildChainFromMessages(nil)
	if chain == nil {
		t.Fatal("got nil slice, want an empty non-nil slice")
	}
	if len(chain) != 0 {
		t.Errorf("chain = %v, want empty", chain)
	}
}

func TestBuildChainFromMessages_NoLeafReturnsEmptySlice(t *testing.T) {
	// Every message is someone's parent, so no leaf exists and the walk cannot start.
	messages := []*TranscriptMessage{
		chainMsg("m1", "user", "m2", `[{"type":"text","text":"a"}]`, 0, 1),
		chainMsg("m2", "user", "m1", `[{"type":"text","text":"b"}]`, time.Second, 2),
	}

	chain := buildChainFromMessages(messages)
	if chain == nil {
		t.Fatal("got nil slice, want an empty non-nil slice")
	}
	if len(chain) != 0 {
		uuids := make([]string, len(chain))
		for i, m := range chain {
			uuids[i] = m.UUID
		}
		t.Errorf("chain = %v, want empty (no leaf in a fully circular set)", uuids)
	}
}

func TestBuildChainFromMessages_RecoversOrphanedSibling(t *testing.T) {
	root := chainMsg("root", "user", "", `[{"type":"text","text":"start"}]`, 0, 1)
	a1 := chainMsg("a1", "assistant", "root", `[{"type":"tool_use","id":"tu1","name":"Read"}]`, time.Second, 2)
	a2 := chainMsg("a2", "assistant", "root", `[{"type":"tool_use","id":"tu1","name":"Bash"}]`, 2*time.Second, 3)
	tr := chainMsg("tr", "user", "a2", `[{"type":"tool_result","tool_use_id":"tu1"}]`, 3*time.Second, 4)
	messages := []*TranscriptMessage{root, a1, a2, tr}

	// The leaf is tr, so the walk keeps a2 and drops its sibling a1; recovery must
	// splice a1 back in right after the anchor (a2).
	chain := buildChainFromMessages(messages)
	assertChainUUIDs(t, chain, "root", "a2", "a1", "tr")
}

// --- recoverOrphanedParallelToolResults ---

func TestRecoverOrphanedParallelToolResults_RecoversSiblingAndToolResult(t *testing.T) {
	u0 := chainMsg("u0", "user", "", `[{"type":"text","text":"start"}]`, 0, 1)
	a1 := chainMsg("a1", "assistant", "u0", `[{"type":"tool_use","id":"tu1","name":"Read"}]`, time.Second, 2)
	a2 := chainMsg("a2", "assistant", "u0", `[{"type":"tool_use","id":"tu1","name":"Bash"}]`, 2*time.Second, 3)
	tr1 := chainMsg("tr1", "user", "a2", `[{"type":"tool_result","tool_use_id":"tu1"}]`, 3*time.Second, 4)
	tr2 := chainMsg("tr2", "user", "a2", `[{"type":"text","text":"plain reply"}]`, 4*time.Second, 5)

	all := []*TranscriptMessage{u0, a1, a2, tr1, tr2}
	chain := []*TranscriptMessage{u0, a1}

	var logBuf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	recovered := recoverOrphanedParallelToolResults(all, chain)
	slog.SetDefault(orig)

	assertChainUUIDs(t, recovered, "u0", "a1", "a2", "tr1")
	if !strings.Contains(logBuf.String(), `msg="recovered orphaned messages in chain" count=1`) {
		t.Errorf("recovery log = %q, want a record with count=1", logBuf.String())
	}
}

func TestRecoverOrphanedParallelToolResults_SortsToolResultsByTime(t *testing.T) {
	a1 := chainMsg("a1", "assistant", "", `[{"type":"tool_use","id":"tu1","name":"Read"}]`, 0, 1)
	a2 := chainMsg("a2", "assistant", "", `[{"type":"tool_use","id":"tu1","name":"Bash"}]`, time.Second, 2)
	later := chainMsg("tr-later", "user", "a2", `[{"type":"tool_result","tool_use_id":"tu1"}]`, 3*time.Second, 3)
	earlier := chainMsg("tr-earlier", "user", "a2", `[{"type":"tool_result","tool_use_id":"tu1"}]`, 2*time.Second, 4)

	all := []*TranscriptMessage{a1, a2, later, earlier}
	chain := []*TranscriptMessage{a1}

	recovered := recoverOrphanedParallelToolResults(all, chain)
	assertChainUUIDs(t, recovered, "a1", "a2", "tr-earlier", "tr-later")
}

func TestRecoverOrphanedParallelToolResults_ToolResultInLaterBlock(t *testing.T) {
	a1 := chainMsg("a1", "assistant", "", `[{"type":"tool_use","id":"tu1","name":"Read"}]`, 0, 1)
	a2 := chainMsg("a2", "assistant", "", `[{"type":"tool_use","id":"tu1","name":"Bash"}]`, time.Second, 2)
	mixed := chainMsg("tr-mixed", "user", "a2",
		`[{"type":"text","text":"prefix"},{"type":"tool_result","tool_use_id":"tu1"}]`, 2*time.Second, 3)

	all := []*TranscriptMessage{a1, a2, mixed}
	chain := []*TranscriptMessage{a1}

	recovered := recoverOrphanedParallelToolResults(all, chain)
	assertChainUUIDs(t, recovered, "a1", "a2", "tr-mixed")
}

func TestRecoverOrphanedParallelToolResults_NoOrphansKeepsChainAndLogsNothing(t *testing.T) {
	u0 := chainMsg("u0", "user", "", `[{"type":"text","text":"start"}]`, 0, 1)
	a1 := chainMsg("a1", "assistant", "u0", `[{"type":"tool_use","id":"tu1","name":"Read"}]`, time.Second, 2)

	all := []*TranscriptMessage{u0, a1}
	chain := []*TranscriptMessage{u0, a1}

	var logBuf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	recovered := recoverOrphanedParallelToolResults(all, chain)
	slog.SetDefault(orig)

	assertChainUUIDs(t, recovered, "u0", "a1")
	if logBuf.String() != "" {
		t.Errorf("recovery log = %q, want no record when nothing is orphaned", logBuf.String())
	}
}

func TestRecoverOrphanedParallelToolResults_NoAssistantsKeepsChain(t *testing.T) {
	u0 := chainMsg("u0", "user", "", `[{"type":"text","text":"start"}]`, 0, 1)
	u1 := chainMsg("u1", "user", "u0", `[{"type":"text","text":"next"}]`, time.Second, 2)

	recovered := recoverOrphanedParallelToolResults([]*TranscriptMessage{u0, u1}, []*TranscriptMessage{u0, u1})
	assertChainUUIDs(t, recovered, "u0", "u1")
}

func TestRecoverOrphanedParallelToolResults_SkipsNonAssistantAndIDless(t *testing.T) {
	u0 := chainMsg("u0", "user", "", `[{"type":"text","text":"start"}]`, 0, 1)
	a0 := chainMsg("a0", "assistant", "u0", `[{"type":"text","text":"no tool use"}]`, time.Second, 2)
	a1 := chainMsg("a1", "assistant", "a0", `[{"type":"tool_use","id":"tu1","name":"Read"}]`, 2*time.Second, 3)
	a2 := chainMsg("a2", "assistant", "a1", `[{"type":"tool_use","id":"tu2","name":"Bash"}]`, 3*time.Second, 4)
	a3 := chainMsg("a3", "assistant", "a1", `[{"type":"tool_use","id":"tu2","name":"Grep"}]`, 4*time.Second, 5)

	all := []*TranscriptMessage{u0, a0, a1, a2, a3}
	chain := []*TranscriptMessage{u0, a0, a1, a2}

	var logBuf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	recovered := recoverOrphanedParallelToolResults(all, chain)
	slog.SetDefault(orig)

	// a0 has no tool_use id and a1's group has no orphans: both must be skipped
	// without aborting the walk over the remaining chain assistants.
	assertChainUUIDs(t, recovered, "u0", "a0", "a1", "a2", "a3")
	if !strings.Contains(logBuf.String(), `msg="recovered orphaned messages in chain" count=1`) {
		t.Errorf("recovery log = %q, want exactly one anchor with count=1", logBuf.String())
	}
}

func TestRecoverOrphanedParallelToolResults_RecoversToolResultWithoutSiblings(t *testing.T) {
	a1 := chainMsg("a1", "assistant", "", `[{"type":"tool_use","id":"tu1","name":"Read"}]`, 0, 1)
	tr := chainMsg("tr", "user", "a1", `[{"type":"tool_result","tool_use_id":"tu1"}]`, time.Second, 2)

	recovered := recoverOrphanedParallelToolResults([]*TranscriptMessage{a1, tr}, []*TranscriptMessage{a1})
	assertChainUUIDs(t, recovered, "a1", "tr")
}

func TestRecoverOrphanedParallelToolResults_NilGroupFallbackRecoversToolResult(t *testing.T) {
	// The chain assistants are absent from allMessages, so siblingsByMsgId has no
	// entry for their message id and the group must fall back to the anchor itself.
	a1 := chainMsg("a1", "assistant", "", `[{"type":"tool_use","id":"tu1","name":"Read"}]`, 0, 1)
	a2 := chainMsg("a2", "assistant", "a1", `[{"type":"tool_use","id":"tu1","name":"Bash"}]`, time.Second, 2)
	tr := chainMsg("tr", "user", "a1", `[{"type":"tool_result","tool_use_id":"tu1"}]`, 2*time.Second, 3)

	recovered := recoverOrphanedParallelToolResults(
		[]*TranscriptMessage{tr},
		[]*TranscriptMessage{a1, a2},
	)
	assertChainUUIDs(t, recovered, "a1", "a2", "tr")
}

func TestRecoverOrphanedParallelToolResults_ProcessedGroupNotReprocessed(t *testing.T) {
	// Both chain assistants share message id tu1 and neither is in allMessages.
	// The first pass marks the group processed, so the second assistant's tool
	// result must not be recovered from a later iteration.
	a1 := chainMsg("a1", "assistant", "", `[{"type":"tool_use","id":"tu1","name":"Read"}]`, 0, 1)
	a2 := chainMsg("a2", "assistant", "a1", `[{"type":"tool_use","id":"tu1","name":"Bash"}]`, time.Second, 2)
	tr := chainMsg("tr", "user", "a2", `[{"type":"tool_result","tool_use_id":"tu1"}]`, 2*time.Second, 3)

	recovered := recoverOrphanedParallelToolResults(
		[]*TranscriptMessage{tr},
		[]*TranscriptMessage{a1, a2},
	)
	assertChainUUIDs(t, recovered, "a1", "a2")
}

func TestRecoverOrphanedParallelToolResults_IDlessAssistantHasNoAnchor(t *testing.T) {
	// An assistant without a tool_use block has no message id, so its group must
	// be skipped: there is no anchor to splice recovered tool results after.
	a0 := chainMsg("a0", "assistant", "", `[{"type":"text","text":"no tool use"}]`, 0, 1)
	tr := chainMsg("tr", "user", "a0", `[{"type":"tool_result","tool_use_id":"tu1"}]`, time.Second, 2)

	recovered := recoverOrphanedParallelToolResults(
		[]*TranscriptMessage{a0, tr},
		[]*TranscriptMessage{a0},
	)
	assertChainUUIDs(t, recovered, "a0")
}

func TestRecoverOrphanedParallelToolResults_TextBlockIDIsNotMessageID(t *testing.T) {
	// Only tool_use blocks carry the message id; a text block with an "id" field
	// must not group this assistant with the sibling that shares the same id.
	a1 := chainMsg("a1", "assistant", "", `[{"type":"text","text":"hi","id":"tuX"}]`, 0, 1)
	a2 := chainMsg("a2", "assistant", "", `[{"type":"tool_use","id":"tuX","name":"Read"}]`, time.Second, 2)

	recovered := recoverOrphanedParallelToolResults(
		[]*TranscriptMessage{a1, a2},
		[]*TranscriptMessage{a1},
	)
	assertChainUUIDs(t, recovered, "a1")
}

func TestRecoverOrphanedParallelToolResults_SkipsToolUseWithEmptyID(t *testing.T) {
	// The message id is the first tool_use id that is actually set.
	a1 := chainMsg("a1", "assistant", "",
		`[{"type":"tool_use","id":"","name":"a"},{"type":"tool_use","id":"tu2","name":"b"}]`, 0, 1)
	a2 := chainMsg("a2", "assistant", "", `[{"type":"tool_use","id":"tu2","name":"c"}]`, time.Second, 2)

	recovered := recoverOrphanedParallelToolResults(
		[]*TranscriptMessage{a1, a2},
		[]*TranscriptMessage{a1},
	)
	assertChainUUIDs(t, recovered, "a1", "a2")
}

// --- walkToRoot ---

// chain.go:272-273 — an empty parent_uuid ends the walk on its own; the walk
// must not follow the empty key into whatever the map happens to hold there.
func TestWalkToRoot_EmptyParentUUIDEndsTheWalk(t *testing.T) {
	msgMap := map[string]*TranscriptMessage{
		"a": {UUID: "a", ParentUUID: ""},
		"":  {UUID: "", ParentUUID: ""},
	}

	chain := walkToRoot(msgMap["a"], msgMap)
	assertChainUUIDs(t, chain, "a")
}

// --- buildChainFromMessages ---

// chain.go:185 — the spliced chain is pre-allocated for the recovered
// messages, so the append never reallocates.
func TestBuildChainFromMessages_SpliceKeepsThePreallocatedCapacity(t *testing.T) {
	root := chainMsg("root", "user", "", `[{"type":"text","text":"start"}]`, 0, 1)
	a1 := chainMsg("a1", "assistant", "root", `[{"type":"tool_use","id":"tu1","name":"Read"}]`, time.Second, 2)
	a2 := chainMsg("a2", "assistant", "root", `[{"type":"tool_use","id":"tu1","name":"Bash"}]`, 2*time.Second, 3)

	chain := buildChainFromMessages([]*TranscriptMessage{root, a1, a2})
	assertChainUUIDs(t, chain, "root", "a2", "a1")
	if got, want := cap(chain), 3; got != want {
		t.Errorf("cap(chain) = %d, want %d (2 chain messages + 1 insert group)", got, want)
	}
}

// --- recoverOrphanedParallelToolResults: group membership ---

// chain.go:94-95 — a message.id group is built from assistant messages only:
// a user message that repeats the tool_use id is not a sibling to recover.
func TestRecoverOrphanedParallelToolResults_UserMessageIsNotASibling(t *testing.T) {
	u0 := chainMsg("u0", "user", "", `[{"type":"text","text":"start"}]`, 0, 1)
	a1 := chainMsg("a1", "assistant", "u0", `[{"type":"tool_use","id":"tu1","name":"Read"}]`, time.Second, 2)
	u1 := chainMsg("u1", "user", "a1", `[{"type":"tool_use","id":"tu1","name":"Read"}]`, 2*time.Second, 3)

	recovered := recoverOrphanedParallelToolResults(
		[]*TranscriptMessage{u0, a1, u1},
		[]*TranscriptMessage{u0, a1},
	)
	assertChainUUIDs(t, recovered, "u0", "a1")
}

// chain.go:107-108 — only user messages carry tool_results; an assistant
// holding a tool_result block is not one to recover.
func TestRecoverOrphanedParallelToolResults_AssistantToolResultIsNotRecovered(t *testing.T) {
	a1 := chainMsg("a1", "assistant", "", `[{"type":"tool_use","id":"tu1","name":"Read"}]`, 0, 1)
	x1 := chainMsg("x1", "assistant", "a1", `[{"type":"tool_result","tool_use_id":"tu1"}]`, time.Second, 2)

	recovered := recoverOrphanedParallelToolResults(
		[]*TranscriptMessage{a1, x1},
		[]*TranscriptMessage{a1},
	)
	assertChainUUIDs(t, recovered, "a1")
}

// chain.go:110-111 — a tool_result without a parent link is not attached to
// any assistant, even one whose own UUID is the empty string.
func TestRecoverOrphanedParallelToolResults_ToolResultNeedsAParentLink(t *testing.T) {
	a1 := chainMsg("a1", "assistant", "", `[{"type":"tool_use","id":"tu1","name":"Read"}]`, 0, 1)
	empty := chainMsg("", "assistant", "", `[{"type":"tool_use","id":"tu1","name":"Bash"}]`, time.Second, 2)
	tr := chainMsg("tr", "user", "", `[{"type":"tool_result","tool_use_id":"tu1"}]`, 2*time.Second, 3)

	recovered := recoverOrphanedParallelToolResults(
		[]*TranscriptMessage{a1, empty, tr},
		[]*TranscriptMessage{a1},
	)
	assertChainUUIDs(t, recovered, "a1", "")
}

func TestWalkToRoot_StopsAtRootWithoutCycleWarning(t *testing.T) {
	msgMap := map[string]*TranscriptMessage{
		"a": {UUID: "a", ParentUUID: ""},
		"b": {UUID: "b", ParentUUID: "a"},
		"c": {UUID: "c", ParentUUID: "b"},
	}

	var logBuf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	chain := walkToRoot(msgMap["c"], msgMap)
	slog.SetDefault(orig)

	assertChainUUIDs(t, chain, "c", "b", "a")
	if logBuf.String() != "" {
		t.Errorf("walk log = %q, want no cycle warning for a root-terminated walk", logBuf.String())
	}
}

func TestWalkToRoot_CycleWarnsAndTruncates(t *testing.T) {
	msgMap := map[string]*TranscriptMessage{
		"a": {UUID: "a", ParentUUID: "b"},
		"b": {UUID: "b", ParentUUID: "c"},
		"c": {UUID: "c", ParentUUID: "a"},
	}

	var logBuf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	chain := walkToRoot(msgMap["c"], msgMap)
	slog.SetDefault(orig)

	assertChainUUIDs(t, chain, "c", "a", "b")
	if !strings.Contains(logBuf.String(), `msg="cycle detected in chain, truncating" uuid=c`) {
		t.Errorf("cycle log = %q, want a warning naming uuid=c", logBuf.String())
	}
}

// --- sortByCreated ---

func TestSortByCreated_OrdersByTimestamp(t *testing.T) {
	cases := []struct {
		name string
		in   []*TranscriptMessage
		want []string
	}{
		{"pair inverted", []*TranscriptMessage{
			chainMsg("b", "user", "", "[]", 2*time.Second, 1),
			chainMsg("a", "user", "", "[]", 1*time.Second, 2),
		}, []string{"a", "b"}},
		{"triple rotated", []*TranscriptMessage{
			chainMsg("c", "user", "", "[]", 3*time.Second, 1),
			chainMsg("a", "user", "", "[]", 1*time.Second, 2),
			chainMsg("b", "user", "", "[]", 2*time.Second, 3),
		}, []string{"a", "b", "c"}},
		{"already sorted", []*TranscriptMessage{
			chainMsg("a", "user", "", "[]", 1*time.Second, 1),
			chainMsg("b", "user", "", "[]", 2*time.Second, 2),
		}, []string{"a", "b"}},
		{"single", []*TranscriptMessage{
			chainMsg("a", "user", "", "[]", 1*time.Second, 1),
		}, []string{"a"}},
		{"empty", []*TranscriptMessage{}, []string{}},
	}

	for _, c := range cases {
		sortByCreated(c.in)
		assertChainUUIDs(t, c.in, c.want...)
	}
}

// --- findLeafMessage ---

// leafMsg builds a message that is always a leaf: its parent uuid is not part of
// the message set, so nothing in the set points at it.
func leafMsg(uuid, msgType string, unixSec int64, seq int64, sidechain int) *TranscriptMessage {
	return &TranscriptMessage{
		UUID:        uuid,
		Type:        msgType,
		ParentUUID:  "not-in-set",
		IsSidechain: sidechain,
		Seq:         seq,
		CreatedAt:   time.Unix(unixSec, 0),
	}
}

func TestFindLeafMessage_SelectionMatrix(t *testing.T) {
	cases := []struct {
		name     string
		messages []*TranscriptMessage
		want     string // "" means the function must return nil
	}{
		{
			name: "non-sidechain non-progress wins over later timestamps",
			messages: []*TranscriptMessage{
				leafMsg("prog", "progress", 10, 1, 0),
				leafMsg("norm", "user", 5, 2, 0),
				leafMsg("side", "user", 20, 3, 1),
			},
			want: "norm",
		},
		{
			name: "equal timestamps keep the earlier leaf when the later one has a lower seq",
			messages: []*TranscriptMessage{
				leafMsg("first", "user", 5, 2, 0),
				leafMsg("second", "user", 5, 1, 0),
			},
			want: "first",
		},
		{
			name: "equal timestamps pick the higher seq",
			messages: []*TranscriptMessage{
				leafMsg("first", "user", 5, 2, 0),
				leafMsg("second", "user", 5, 3, 0),
			},
			want: "second",
		},
		{
			name: "progress leaf with a later timestamp must not win",
			messages: []*TranscriptMessage{
				leafMsg("norm", "user", 5, 2, 0),
				leafMsg("prog", "progress", 9, 0, 0),
			},
			want: "norm",
		},
		{
			name: "descending timestamps keep the first maximum",
			messages: []*TranscriptMessage{
				leafMsg("high", "user", 100, 1, 0),
				leafMsg("low", "user", 5, 2, 0),
			},
			want: "high",
		},
		{
			// The -1 sentinel is a timestamp, not a "no candidate" flag: a leaf at
			// exactly -1 is only accepted through the seq tiebreaker.
			name: "leaf at the sentinel timestamp is accepted via seq",
			messages: []*TranscriptMessage{
				leafMsg("norm", "user", -1, 5, 0),
				leafMsg("prog", "progress", 100, 1, 0),
			},
			want: "norm",
		},
		{
			name: "leaf one second below the sentinel timestamp is rejected",
			messages: []*TranscriptMessage{
				leafMsg("norm", "user", -2, 5, 0),
				leafMsg("prog", "progress", 100, 1, 0),
			},
			want: "prog",
		},
		{
			name: "leaf at the sentinel timestamp with seq -1 is rejected",
			messages: []*TranscriptMessage{
				leafMsg("norm", "user", -1, -1, 0),
				leafMsg("prog", "progress", 100, 1, 0),
			},
			want: "prog",
		},
		{
			// The seq sentinel is -1, not 0, so seq 0 still wins the tie at the
			// timestamp sentinel and the preferred pass must return this leaf
			// instead of falling back to the progress message.
			name: "leaf at the sentinel timestamp with seq 0 is accepted",
			messages: []*TranscriptMessage{
				leafMsg("norm", "user", -1, 0, 0),
				leafMsg("prog", "progress", 100, 1, 0),
			},
			want: "norm",
		},
		{
			name: "sidechain leaf at the fallback sentinel timestamp",
			messages: []*TranscriptMessage{
				leafMsg("side", "user", -1, 0, 1),
			},
			want: "side",
		},
		{
			name: "sidechain leaf below the fallback sentinel timestamp",
			messages: []*TranscriptMessage{
				leafMsg("side", "user", -2, 0, 1),
			},
			want: "",
		},
		{
			name: "sidechain leaf at the fallback sentinel with seq -1",
			messages: []*TranscriptMessage{
				leafMsg("side", "user", -1, -1, 1),
			},
			want: "",
		},
		{
			name: "fallback keeps the earlier leaf when the later one has a lower seq",
			messages: []*TranscriptMessage{
				leafMsg("first", "user", 5, 2, 1),
				leafMsg("second", "user", 5, 1, 1),
			},
			want: "first",
		},
		{
			name: "fallback picks the higher seq on equal timestamps",
			messages: []*TranscriptMessage{
				leafMsg("first", "user", 5, 2, 1),
				leafMsg("second", "user", 5, 3, 1),
			},
			want: "second",
		},
		{
			name: "fallback picks the later timestamp across kinds",
			messages: []*TranscriptMessage{
				leafMsg("side", "user", 5, 2, 1),
				leafMsg("prog", "progress", 9, 0, 0),
			},
			want: "prog",
		},
		{
			name: "fallback keeps the first maximum on descending timestamps",
			messages: []*TranscriptMessage{
				leafMsg("high", "user", 100, 1, 1),
				leafMsg("low", "user", 5, 2, 1),
			},
			want: "high",
		},
	}

	for _, c := range cases {
		leaf := findLeafMessage(c.messages)
		if c.want == "" {
			if leaf != nil {
				t.Errorf("%s: leaf = %q, want nil", c.name, leaf.UUID)
			}
			continue
		}
		if leaf == nil {
			t.Errorf("%s: got nil leaf, want %q", c.name, c.want)
			continue
		}
		if leaf.UUID != c.want {
			t.Errorf("%s: leaf = %q, want %q", c.name, leaf.UUID, c.want)
		}
	}
}
