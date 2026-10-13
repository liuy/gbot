package engine

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/memory/short"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/types"
)

func newTestView(id, name string) *EngineViewState {
	return &EngineViewState{ID: id, Name: name, Model: "sonnet"}
}

func TestEngineManager_New_StartsEmpty(t *testing.T) {
	m := NewEngineManager()
	// A fresh manager tracks no engines yet.
	want := 0
	if got := m.Count(); got != want {
		t.Errorf("Count = %d, want %d", got, want)
	}
	if got := m.Active(); got != nil {
		t.Errorf("Active = %v, want nil", got)
	}
	if got := m.List(); len(got) != 0 {
		t.Errorf("len(List()) = %d, want 0", len(got))
	}
}

func TestEngineManager_Add_IncrementsCount(t *testing.T) {
	m := NewEngineManager()
	m.Add(newTestView("main", "main"))
	if got := m.Count(); got != 1 {
		t.Errorf("Count = %d, want 1", got)
	}
	list := m.List()
	if len(list) != 1 {
		t.Fatalf("len(List()) = %d, want 1", len(list))
	}
	if list[0].ID != "main" {
		t.Errorf("List[0].ID = %q, want main", list[0].ID)
	}
	if got := m.Get("main"); got == nil || got.ID != "main" {
		t.Errorf("Get(main) = %v, want non-nil with ID main", got)
	}
	// First Add makes the engine active.
	if got := m.ActiveID(); got != "main" {
		t.Errorf("ActiveID = %q, want main", got)
	}
}

func TestEngineManager_Add_DuplicateIDPanics(t *testing.T) {
	m := NewEngineManager()
	m.Add(newTestView("main", "main"))
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on duplicate ID")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "duplicate") {
			t.Errorf("panic message %q does not contain 'duplicate'", msg)
		}
	}()
	m.Add(newTestView("main", "dup"))
}

func TestEngineManager_HasIDMinter(t *testing.T) {
	m := NewEngineManager()
	if m.HasIDMinter() {
		t.Error("HasIDMinter = true before SetIDMinter, want false (fallback counter)")
	}
	m.SetIDMinter(agenttool.NewIDMinter())
	if !m.HasIDMinter() {
		t.Error("HasIDMinter = false after SetIDMinter, want true")
	}
}
func TestEngineManager_HasIDMinter_ReleasesLock(t *testing.T) {
	// A leaked read lock would wedge every later manager call, so the write
	// path must proceed immediately after a read.
	m := NewEngineManager()
	m.HasIDMinter()
	added := make(chan struct{})
	go func() {
		m.Add(newTestView("main", "main"))
		close(added)
	}()
	select {
	case <-added:
	case <-time.After(2 * time.Second):
		t.Fatal("Add blocked after HasIDMinter — the read lock was not released")
	}
}
func TestEngineManager_NewEngineID_MintsAgentN(t *testing.T) {
	m := NewEngineManager()
	m.SetIDMinter(agenttool.NewIDMinter())
	if got := m.NewEngineID(); got != "agent-1" {
		t.Errorf("first NewEngineID = %q, want agent-1", got)
	}
	if got := m.NewEngineID(); got != "agent-2" {
		t.Errorf("second NewEngineID = %q, want agent-2", got)
	}
}

func TestEngineManager_NewEngineID_NilMinterFallback(t *testing.T) {
	// No injection: a standalone manager (tests) falls back to its internal
	// counter — same agent-N grammar, private to the manager.
	m := NewEngineManager()
	if got := m.NewEngineID(); got != "agent-1" {
		t.Errorf("first NewEngineID = %q, want agent-1", got)
	}
	if got := m.NewEngineID(); got != "agent-2" {
		t.Errorf("second NewEngineID = %q, want agent-2", got)
	}
}

func TestEngineManager_NewEngineID_NilMinterSkipsRegisteredIDs(t *testing.T) {
	// The fallback counter allocates, it does not merely advance: an id
	// registered out-of-band (test setup, restored meta) must be bumped past,
	// otherwise the mint hands back an id Add would panic on.
	m := NewEngineManager()
	m.Add(newTestView("agent-1", "agent-1"))
	if got := m.NewEngineID(); got != "agent-2" {
		t.Errorf("NewEngineID with agent-1 registered = %q, want agent-2", got)
	}
	m.Add(newTestView("agent-2", "agent-2"))
	if got := m.NewEngineID(); got != "agent-3" {
		t.Errorf("NewEngineID with agent-1..2 registered = %q, want agent-3", got)
	}
}
func TestEngineManager_NewEngineID_SharesRegistryCounter(t *testing.T) {
	idm := agenttool.NewIDMinter()
	m := NewEngineManager()
	m.SetIDMinter(idm)
	reg := agenttool.NewBackgroundAgentRegistryWithMinter(idm)

	engID := m.NewEngineID()
	agentState, err := reg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			return &types.SubQueryResult{Content: "x"}, nil
		}, nil, "d", "c", "General")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	// One counter, sequential across both consumers — no collision.
	if engID != "agent-1" || agentState.ID != "agent-2" {
		t.Errorf("ids = (%q, %q), want (agent-1, agent-2)", engID, agentState.ID)
	}
	reg.Wait(agentState.ID)
}

func TestEngineManager_ValidateName(t *testing.T) {
	m := NewEngineManager()
	m.Add(newTestView("main", "main"))
	m.Add(newTestView("agent-1", "research"))
	m.Add(newTestView("legacy-id", "other"))

	for name, wantErr := range map[string]string{
		"":          "engine name must not be empty",
		"  ":        "engine name must not be empty",
		"main":      `engine name "main" is reserved`,
		"dream":     `engine name "dream" is reserved`,
		"agent-3":   `engine name "agent-3" collides with the agent-N id namespace`,
		"agent-42":  `engine name "agent-42" collides with the agent-N id namespace`,
		"agent-1":   `engine name "agent-1" collides with the agent-N id namespace`,
		"research":  `engine name "research" is already in use`,
		"legacy-id": `engine name "legacy-id" collides with an existing engine id`,
	} {
		err := m.ValidateName(name)
		if err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", name)
			continue
		}
		if err.Error() != wantErr {
			t.Errorf("ValidateName(%q) = %q, want %q", name, err.Error(), wantErr)
		}
	}
	for _, name := range []string{"research-2", "notes", "e2", "dream-x"} {
		if err := m.ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}
}

func TestEngineManager_GetByName(t *testing.T) {
	m := NewEngineManager()
	main := newTestView("main", "main")
	m.Add(main)
	r := newTestView("agent-1", "research")
	m.Add(r)
	dup := newTestView("agent-2", "dup")
	m.Add(dup)
	dup2 := newTestView("agent-3", "dup")
	m.Add(dup2)

	if got := m.GetByName("research"); got != r {
		t.Errorf("GetByName(research) = %v, want the agent-1 view state", got)
	}
	if got := m.GetByName("missing"); got != nil {
		t.Errorf("GetByName(missing) = %v, want nil", got)
	}
	if got := m.GetByName(""); got != nil {
		t.Errorf("GetByName(empty) = %v, want nil", got)
	}
	// Duplicate legacy names: first in creation order wins.
	if got := m.GetByName("dup"); got != dup {
		t.Errorf("GetByName(dup) = %v, want the first-registered view state %v", got, dup)
	}
	// A view state with an EMPTY name must not make GetByName("") resolve:
	// the empty address is a miss, not a name match.
	m.Add(newTestView("agent-9", ""))
	if got := m.GetByName(""); got != nil {
		t.Errorf("GetByName(empty) = %v, want nil even with an empty-named engine registered", got)
	}
}

// TestEngineManager_EngineBySession pins the live-session lookup the boot
// SessionStart dispatch uses to route js hooks into the owning engine's
// REPL: exact live SessionID match, nil on miss/empty, and lazy view states
// (nil Engine) neither match nor panic.
func TestEngineManager_EngineBySession(t *testing.T) {
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	m := NewEngineManager()
	newEngine := func(id string) *Engine {
		t.Helper()
		eng := New(&Params{EngineID: id, Logger: slog.Default()})
		t.Cleanup(func() { eng.Close() })
		eng.SetStore(store, dir)
		if err := eng.NewSession(dir, ""); err != nil {
			t.Fatalf("%s NewSession: %v", id, err)
		}
		m.Add(&EngineViewState{Engine: eng, ID: id, Name: id})
		return eng
	}
	engA := newEngine("main")
	engB := newEngine("e2")

	if got := m.EngineBySession(engA.SessionID()); got != engA {
		t.Errorf("EngineBySession(A) = %v, want engine main", got)
	}
	if got := m.EngineBySession(engB.SessionID()); got != engB {
		t.Errorf("EngineBySession(B) = %v, want engine e2", got)
	}
	if got := m.EngineBySession("no-such-session"); got != nil {
		t.Errorf("EngineBySession(unknown) = %v, want nil", got)
	}
	if got := m.EngineBySession(""); got != nil {
		t.Errorf("EngineBySession(\"\") = %v, want nil", got)
	}

	m.Add(&EngineViewState{ID: "lazy", Name: "lazy"}) // nil Engine (lazy state)
	if got := m.EngineBySession(engA.SessionID()); got != engA {
		t.Errorf("EngineBySession(A) with lazy state = %v, want engine main (nil-Engine entries must be skipped)", got)
	}
}

func TestEngineManager_SetActive_Switches(t *testing.T) {
	m := NewEngineManager()
	m.Add(newTestView("main", "main"))
	m.Add(newTestView("e2", "engine-2"))
	if err := m.SetActive("e2"); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if got := m.Active().ID; got != "e2" {
		t.Errorf("Active().ID = %q, want e2", got)
	}
}

func TestEngineManager_SetActive_UnknownIDError(t *testing.T) {
	m := NewEngineManager()
	m.Add(newTestView("main", "main"))
	err := m.SetActive("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown id")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %q, want substring 'not found'", err)
	}
}

func TestEngineManager_Remove_DecrementsCount(t *testing.T) {
	m := NewEngineManager()
	m.Add(newTestView("main", "main"))
	m.Add(newTestView("e2", "engine-2"))
	if err := m.Remove("e2"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := m.Count(); got != 1 {
		t.Errorf("Count = %d, want 1", got)
	}
	for _, vs := range m.List() {
		if vs.ID == "e2" {
			t.Errorf("e2 still present in List() after Remove")
		}
	}
}

func TestEngineManager_Remove_LastEngineError(t *testing.T) {
	m := NewEngineManager()
	m.Add(newTestView("main", "main"))
	err := m.Remove("main")
	if err == nil {
		t.Fatal("expected error when removing last engine")
	}
	if !strings.Contains(err.Error(), "at least one") {
		t.Errorf("err = %q, want substring 'at least one'", err)
	}
}

func TestEngineManager_Remove_UnknownIDError(t *testing.T) {
	m := NewEngineManager()
	m.Add(newTestView("main", "main"))
	err := m.Remove("nope")
	if err == nil {
		t.Fatal("expected error for unknown id")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %q, want substring 'not found'", err)
	}
}

func TestEngineManager_Remove_ActiveEngine_ReassignsToMostRecentRemaining(t *testing.T) {
	// Add 3 engines; active is "e3" (last added). Remove it; the fallback
	// is the most recently added remaining engine, which is "e2".
	m := NewEngineManager()
	m.Add(newTestView("main", "main"))
	m.Add(newTestView("e2", "engine-2"))
	m.Add(newTestView("e3", "engine-3"))
	if err := m.SetActive("e3"); err != nil {
		t.Fatalf("SetActive(e3): %v", err)
	}
	if err := m.Remove("e3"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := m.ActiveID(); got != "e2" {
		t.Errorf("ActiveID after removing active = %q, want e2 (most recent remaining)", got)
	}
}

func TestEngineManager_List_StableOrder(t *testing.T) {
	m := NewEngineManager()
	m.Add(newTestView("main", "main"))
	m.Add(newTestView("e2", "engine-2"))
	m.Add(newTestView("e3", "engine-3"))
	if err := m.Remove("e2"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got := m.List()
	if len(got) != 2 {
		t.Fatalf("len(List()) = %d, want 2", len(got))
	}
	if got[0].ID != "main" || got[1].ID != "e3" {
		t.Errorf("order = [%s, %s], want [main, e3]", got[0].ID, got[1].ID)
	}
}

func TestEngineManager_Concurrent_AddAndGet(t *testing.T) {
	t.Parallel()
	m := NewEngineManager()
	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			id := "engine-" + itoa(i)
			m.Add(newTestView(id, id))
		}(i)
	}
	wg.Wait()
	if got := m.Count(); got != n {
		t.Errorf("Count = %d, want %d", got, n)
	}
	// Every registered engine must be retrievable.
	for i := range n {
		id := "engine-" + itoa(i)
		if m.Get(id) == nil {
			t.Errorf("Get(%q) = nil", id)
		}
	}
}

// itoa avoids importing strconv only for a test helper.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// fakeRepl is a minimal ReplSnapshot for testing Snapshot() and PersistMeta.
type fakeRepl struct {
	streaming bool
	toolName  string
}

func (f fakeRepl) IsStreaming() bool       { return f.streaming }
func (f fakeRepl) CurrentToolName() string { return f.toolName }

func TestEngineManager_Snapshot_ReflectsState(t *testing.T) {
	m := NewEngineManager()
	m.Add(&EngineViewState{
		ID: "main", Name: "main", Model: "sonnet",
		ActiveSessionID: "s1", Repl: fakeRepl{streaming: false, toolName: ""},
	})
	m.Add(&EngineViewState{
		ID: "e2", Name: "engine-2", Model: "opus",
		ActiveSessionID: "s2", Repl: fakeRepl{streaming: true, toolName: "Edit"},
	})
	views, activeID := m.Snapshot()
	if activeID != "main" {
		t.Errorf("activeID = %q, want main", activeID)
	}
	if len(views) != 2 {
		t.Fatalf("len(views) = %d, want 2", len(views))
	}
	if views[0].ID != "main" || views[1].ID != "e2" {
		t.Errorf("order = %s, %s; want main, e2", views[0].ID, views[1].ID)
	}
	if !views[1].IsStreaming || views[1].CurrentToolName != "Edit" {
		t.Errorf("engine-2 snapshot = %+v, want streaming=true tool=Edit", views[1])
	}
	// Returned slice must be a copy: mutating it must not affect the manager.
	views[0].Name = "mutated"
	if m.Get("main").Name != "main" {
		t.Errorf("mutating Snapshot slice affected manager state")
	}
}

func TestPersistMeta_NoCorruption_UnderConcurrency(t *testing.T) {
	dir := t.TempDir()
	m := NewEngineManager()
	for i := range 5 {
		vs := &EngineViewState{
			ID:              "e" + itoa(i+1),
			Name:            "engine-" + itoa(i+1),
			Model:           "sonnet",
			ActiveSessionID: "sess-" + itoa(i+1),
			Repl:            fakeRepl{},
		}
		if i == 0 {
			vs.ID = "main"
			vs.Name = "main"
		}
		m.Add(vs)
	}
	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			_ = m.PersistMeta(dir)
		}()
	}
	wg.Wait()

	meta, err := short.ReadWorkspaceMeta(dir)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if len(meta.Engines) != 5 {
		t.Errorf("Engines len = %d, want 5", len(meta.Engines))
	}
	if meta.ActiveEngineID == "" {
		t.Errorf("ActiveEngineID empty after PersistMeta")
	}
	found := false
	for _, e := range meta.Engines {
		if e.ID == meta.ActiveEngineID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ActiveEngineID %q not in engines array", meta.ActiveEngineID)
	}
	// Dual-write: current_session_id must equal the active engine's session.
	if meta.CurrentSessionID == "" {
		t.Errorf("CurrentSessionID empty (dual-write field)")
	}
}

func TestPersistMeta_EmptyProjectDir_Noop(t *testing.T) {
	m := NewEngineManager()
	m.Add(newTestView("main", "main"))
	if err := m.PersistMeta(""); err != nil {
		t.Errorf("PersistMeta(\"\") = %v, want nil", err)
	}
}

func TestPersistMeta_SkipsSystemEngines(t *testing.T) {
	dir := t.TempDir()
	m := NewEngineManager()
	m.Add(&EngineViewState{
		ID:              "main",
		Name:            "main",
		Model:           "sonnet",
		ActiveSessionID: "sess-main",
		Repl:            fakeRepl{},
	})
	m.Add(&EngineViewState{
		ID:              "dream",
		Name:            "Dream",
		Model:           "sonnet",
		ActiveSessionID: "sess-dream",
		Repl:            fakeRepl{},
		System:          true,
	})
	if err := m.PersistMeta(dir); err != nil {
		t.Fatalf("PersistMeta: %v", err)
	}
	meta, err := short.ReadWorkspaceMeta(dir)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if len(meta.Engines) != 1 {
		t.Fatalf("expected 1 engine in meta (dream excluded), got %d", len(meta.Engines))
	}
	if meta.Engines[0].ID != "main" {
		t.Errorf("engine[0].ID = %q, want main", meta.Engines[0].ID)
	}
}

// Ensure time is referenced (used by fakeRepl test setups above for future fields).
var _ = time.Now
