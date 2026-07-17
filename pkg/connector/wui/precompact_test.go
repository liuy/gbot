package wui

import (
	"fmt"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// seedPreCompactStore builds a *short.Store with the requested layout:
//   - `pre` user messages before the compact boundary
//   - one optional FlagCompactSummary-tainted message before the boundary
//     (used to verify filtering)
//   - one compact boundary message (via short.CreateCompactBoundaryMessage)
//   - `post` assistant messages after the boundary
//
// Returns the store and the sessionID holding the transcript.
func seedPreCompactStore(t *testing.T, sid string, pre int, injectSummary bool, post int) *short.Store {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	store, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.DB().Exec(
		"INSERT INTO sessions (session_id, project_dir) VALUES (?, '')",
		sid,
	)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range pre {
		em := types.Message{
			ID:        fmt.Sprintf("pre-%d", i),
			Role:      types.RoleUser,
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Content:   []types.ContentBlock{types.NewTextBlock(fmt.Sprintf("pre-%d", i))},
		}
		ts, err := short.EngineMessagesToStore([]types.Message{em})
		if err != nil {
			t.Fatalf("EngineMessagesToStore pre %d: %v", i, err)
		}
		if err := store.AppendMessage(sid, ts[0]); err != nil {
			t.Fatalf("AppendMessage pre %d: %v", i, err)
		}
	}

	if injectSummary {
		// Pre-compact summary message flagged with FlagCompactSummary so the
		// helper must filter it out from both the page and the total.
		em := types.Message{
			ID:        "summary-leak",
			Role:      types.RoleAssistant,
			Timestamp: base.Add(time.Duration(pre) * time.Second),
			Content:   []types.ContentBlock{types.NewTextBlock("summary")},
			Flags:     types.FlagCompactSummary,
		}
		ts, err := short.EngineMessagesToStore([]types.Message{em})
		if err != nil {
			t.Fatalf("EngineMessagesToStore summary: %v", err)
		}
		if err := store.AppendMessage(sid, ts[0]); err != nil {
			t.Fatalf("AppendMessage summary: %v", err)
		}
	}

	var lastPreUUID string
	if pre > 0 {
		lastPreUUID = fmt.Sprintf("pre-%d", pre-1)
	}
	boundary := short.CreateCompactBoundaryMessage("tokens", 1000, lastPreUUID)
	if err := store.AppendMessage(sid, boundary); err != nil {
		t.Fatalf("AppendMessage boundary: %v", err)
	}

	for i := range post {
		em := types.Message{
			ID:        fmt.Sprintf("post-%d", i),
			Role:      types.RoleAssistant,
			Timestamp: base.Add(time.Duration(pre+i+1) * time.Second),
			Content:   []types.ContentBlock{types.NewTextBlock(fmt.Sprintf("post-%d", i))},
		}
		ts, err := short.EngineMessagesToStore([]types.Message{em})
		if err != nil {
			t.Fatalf("EngineMessagesToStore post %d: %v", i, err)
		}
		if err := store.AppendMessage(sid, ts[0]); err != nil {
			t.Fatalf("AppendMessage post %d: %v", i, err)
		}
	}

	return store
}

func TestPreCompactMessages_FirstPage(t *testing.T) {
	store := seedPreCompactStore(t, "s1", 5, false, 2)
	page, total, hasBoundary := preCompactMessages(store, "s1", 0, 2)
	if !hasBoundary {
		t.Fatal("hasBoundary = false, want true")
	}
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(page) != 2 {
		t.Fatalf("len(page) = %d, want 2", len(page))
	}
	if page[0].ID != "pre-0" || page[1].ID != "pre-1" {
		t.Errorf("page IDs = %q,%q; want pre-0,pre-1", page[0].ID, page[1].ID)
	}
}

func TestPreCompactMessages_LastPartialPage(t *testing.T) {
	store := seedPreCompactStore(t, "s1", 5, false, 1)
	page, total, hasBoundary := preCompactMessages(store, "s1", 4, 10)
	if !hasBoundary {
		t.Fatal("hasBoundary = false, want true")
	}
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(page) != 1 {
		t.Fatalf("len(page) = %d, want 1 (last message)", len(page))
	}
	if page[0].ID != "pre-4" {
		t.Errorf("page[0].ID = %q, want pre-4", page[0].ID)
	}
}

func TestPreCompactMessages_ProbeLimitZero(t *testing.T) {
	store := seedPreCompactStore(t, "s1", 5, false, 0)
	page, total, hasBoundary := preCompactMessages(store, "s1", 0, 0)
	if !hasBoundary {
		t.Fatal("hasBoundary = false, want true (probe must still report boundary)")
	}
	if total != 0 {
		t.Errorf("total = %d, want 0 (probe skips total computation)", total)
	}
	if len(page) != 0 {
		t.Errorf("len(page) = %d, want 0 (probe skips loading)", len(page))
	}
}

func TestPreCompactMessages_NoBoundary(t *testing.T) {
	// Seed a session with only regular messages (no boundary).
	store := seedPreCompactStore(t, "s1", 0, false, 0)
	// Now seed a DIFFERENT session that has no boundary at all (just one message).
	const noBoundary = "no-boundary"
	if _, err := store.DB().Exec(
		"INSERT INTO sessions (session_id, project_dir) VALUES (?, '')",
		noBoundary,
	); err != nil {
		t.Fatalf("insert no-boundary session: %v", err)
	}
	em := types.Message{
		ID:      "lonely",
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("hi")},
	}
	ts, err := short.EngineMessagesToStore([]types.Message{em})
	if err != nil {
		t.Fatalf("EngineMessagesToStore lonely: %v", err)
	}
	if err := store.AppendMessage(noBoundary, ts[0]); err != nil {
		t.Fatalf("AppendMessage lonely: %v", err)
	}

	page, total, hasBoundary := preCompactMessages(store, noBoundary, 0, 10)
	if hasBoundary {
		t.Fatal("hasBoundary = true, want false (no boundary in session)")
	}
	if total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
	if page != nil {
		t.Errorf("page = %v, want nil", page)
	}
}

func TestPreCompactMessages_FiltersFlagCompactSummary(t *testing.T) {
	// 2 regular pre-compact + 1 FlagCompactSummary message before boundary.
	store := seedPreCompactStore(t, "s1", 2, true, 1)
	page, total, hasBoundary := preCompactMessages(store, "s1", 0, 10)
	if !hasBoundary {
		t.Fatal("hasBoundary = false, want true")
	}
	if total != 2 {
		t.Errorf("total = %d, want 2 (FlagCompactSummary excluded)", total)
	}
	if len(page) != 2 {
		t.Fatalf("len(page) = %d, want 2", len(page))
	}
	for _, m := range page {
		if m.ID == "summary-leak" {
			t.Errorf("FlagCompactSummary message leaked into page")
		}
	}
}

func TestPreCompactMessages_DeliveredBeyondTotal(t *testing.T) {
	store := seedPreCompactStore(t, "s1", 3, false, 0)
	page, total, hasBoundary := preCompactMessages(store, "s1", 100, 10)
	if !hasBoundary {
		t.Fatal("hasBoundary = false, want true")
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if len(page) != 0 {
		t.Errorf("len(page) = %d, want 0 when delivered >= total", len(page))
	}
}
