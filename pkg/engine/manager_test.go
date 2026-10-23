package engine

import (
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/llm"
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
	reg := agenttool.NewAgentRegistryWithMinter(idm)

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
	// Every rendered column comes from one field of the view state; a dropped
	// field shows up as an empty picker row, so pin each one exactly.
	if views[0].Name != "main" || views[0].ActiveSessionID != "s1" || views[0].Model != "sonnet" {
		t.Errorf("main snapshot = %+v, want {Name:main ActiveSessionID:s1 Model:sonnet}", views[0])
	}
	if views[1].Name != "engine-2" || views[1].ActiveSessionID != "s2" || views[1].Model != "opus" {
		t.Errorf("engine-2 snapshot = %+v, want {Name:engine-2 ActiveSessionID:s2 Model:opus}", views[1])
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
	// A non-system engine registered AFTER the system one: skipping must not
	// end the walk, or everything created after dream silently loses its
	// meta.json entry.
	m.Add(&EngineViewState{
		ID:              "e3",
		Name:            "engine-3",
		Model:           "opus",
		ActiveSessionID: "sess-e3",
		Thinking:        llm.EffortHigh,
		Repl:            fakeRepl{},
	})
	if err := m.PersistMeta(dir); err != nil {
		t.Fatalf("PersistMeta: %v", err)
	}
	meta, err := short.ReadWorkspaceMeta(dir)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if len(meta.Engines) != 2 {
		t.Fatalf("expected 2 engines in meta (dream excluded), got %d", len(meta.Engines))
	}
	if meta.Engines[0].ID != "main" || meta.Engines[1].ID != "e3" {
		t.Errorf("engine IDs = [%s, %s], want [main, e3]", meta.Engines[0].ID, meta.Engines[1].ID)
	}
	// Every EngineMeta column must survive the JSON round trip: a dropped one
	// restores an engine with the wrong name, session, model or effort.
	want := []short.EngineMeta{
		{ID: "main", Name: "main", ActiveSessionID: "sess-main", Model: "sonnet"},
		{ID: "e3", Name: "engine-3", ActiveSessionID: "sess-e3", Model: "opus", Thinking: "high"},
	}
	for i, w := range want {
		if meta.Engines[i] != w {
			t.Errorf("Engines[%d] = %+v, want %+v", i, meta.Engines[i], w)
		}
	}
}

func TestPersistMeta_ActiveEngineFieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := NewEngineManager()
	for _, id := range []string{"e1", "e2", "e3"} {
		m.Add(&EngineViewState{
			ID:              id,
			Name:            "engine-" + id,
			Model:           "sonnet",
			ActiveSessionID: "sess-" + id,
			Repl:            fakeRepl{},
		})
	}
	if err := m.SetActive("e2"); err != nil {
		t.Fatalf("SetActive(e2): %v", err)
	}
	before := time.Now() // REAL-TIME: PersistMeta stamps time.Now() into meta.json
	if err := m.PersistMeta(dir); err != nil {
		t.Fatalf("PersistMeta: %v", err)
	}
	meta, err := short.ReadWorkspaceMeta(dir)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if meta.ActiveEngineID != "e2" {
		t.Errorf("ActiveEngineID = %q, want e2", meta.ActiveEngineID)
	}
	// The dual-write field carries the ACTIVE engine's session, not the last
	// engine visited — an old binary reads only this one.
	if meta.CurrentSessionID != "sess-e2" {
		t.Errorf("CurrentSessionID = %q, want sess-e2 (the active engine's session)", meta.CurrentSessionID)
	}
	assertFreshStamp(t, "meta LastActiveAt", meta.LastActiveAt)
	if meta.LastActiveAt.Before(before) {
		t.Errorf("meta LastActiveAt = %v, want no earlier than the PersistMeta call (%v)", meta.LastActiveAt, before)
	}
}

// probingRepl asks, from inside the read lock of Snapshot, whether the
// manager mutex is still held. IsStreaming runs after the snapshot fields are
// copied, so the RUnlock must still be pending at that moment.
type probingRepl struct {
	m      *EngineManager
	probes int
	held   bool
}

func (p *probingRepl) IsStreaming() bool {
	if p.m.mu.TryLock() {
		p.m.mu.Unlock()
	} else {
		p.held = true
	}
	p.probes++
	return false
}

func (p *probingRepl) CurrentToolName() string { return "" }

func TestEngineManager_Snapshot_HoldsReadLockWhileReadingRepl(t *testing.T) {
	m := NewEngineManager()
	pr := &probingRepl{m: m}
	m.Add(&EngineViewState{ID: "main", Name: "main", Repl: pr})
	views, activeID := m.Snapshot()
	if activeID != "main" || len(views) != 1 || views[0].ID != "main" {
		t.Fatalf("Snapshot = %+v (active %q), want one entry main", views, activeID)
	}
	if pr.probes != 1 {
		t.Errorf("probe calls = %d, want exactly 1 (Snapshot reads each Repl once)", pr.probes)
	}
	if !pr.held {
		t.Error("Snapshot's read lock was already released while the Repl adapter was read — the RUnlock must stay deferred")
	}
}

// fakeBgSink is a stand-in SubEngine used only to assert that the sink
// handle survives BgAgent resolution intact.
type fakeBgSink struct{}

func (fakeBgSink) EnqueueAttachment(types.QueuedItem) {}

// spawnHoldingAgent spawns a background agent whose runFn blocks until its
// context is cancelled; cleanup cancels and reaps the goroutine. Returns the
// minted id.
func spawnHoldingAgent(t *testing.T, reg *agenttool.AgentRegistry) string {
	t.Helper()
	state, err := reg.Spawn(context.Background(),
		func(ctx context.Context, _ string) (*types.SubQueryResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		nil, "hold", "tooluse-1", "General")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() {
		reg.Cancel(state.ID)
		reg.Wait(state.ID)
	})
	return state.ID
}

// addTableOwner registers a minimal engine whose Agent tool holds reg, so
// BgAgent can discover the table through the engines map.
func addTableOwner(t *testing.T, m *EngineManager, engineID string, reg *agenttool.AgentRegistry) *Engine {
	t.Helper()
	at := agenttool.New()
	at.SetRegistry(reg)
	eng := New(&Params{Provider: &mockProvider{}, Model: "test", EngineID: engineID})
	eng.SetToolRefs(ToolRefs{Agent: at})
	m.Add(&EngineViewState{Engine: eng, ID: engineID})
	t.Cleanup(func() { eng.Close() })
	return eng
}

func TestEngineManager_BgAgent_FindsSpawnedState(t *testing.T) {
	m := NewEngineManager()
	reg := agenttool.NewAgentRegistry()
	addTableOwner(t, m, "owner", reg)
	id := spawnHoldingAgent(t, reg)
	sink := fakeBgSink{}
	reg.AttachSink(id, sink)

	got, ok := m.BgAgent(id)
	if !ok {
		t.Fatalf("BgAgent(%q) not found through the owner engine", id)
	}
	if got.ID != id {
		t.Errorf("BgAgent.ID = %q, want %q", got.ID, id)
	}
	if got.Status != agenttool.AgentRunning {
		t.Errorf("BgAgent.Status = %q, want %q", got.Status, agenttool.AgentRunning)
	}
	if got.Description != "hold" {
		t.Errorf("BgAgent.Description = %q, want hold", got.Description)
	}
	if got.Sink != sink {
		t.Errorf("BgAgent.Sink = %v, want the attached fakeBgSink", got.Sink)
	}
	// Snapshot copy: mutating the returned state must not corrupt the
	// registry entry.
	got.Status = agenttool.AgentCompleted
	again, ok := m.BgAgent(id)
	if !ok || again.Status != agenttool.AgentRunning {
		t.Errorf("BgAgent after mutating the snapshot: ok=%v status=%q, want ok=true status=%q (result must be a copy)", ok, again.Status, agenttool.AgentRunning)
	}
}

func TestEngineManager_BgAgent_SearchesAllTables(t *testing.T) {
	m := NewEngineManager()
	addTableOwner(t, m, "owner-a", agenttool.NewAgentRegistry())
	// Engines the walk must pass WITHOUT stopping: a lazy view (Engine nil),
	// a bare engine (no SetToolRefs — Agent nil), and a registry-less Agent
	// tool. All precede the holding owner in walk order, so a guard that
	// breaks instead of skipping hides the sought id behind them.
	m.Add(&EngineViewState{ID: "lazy"})
	bare := New(&Params{Provider: &mockProvider{}, Model: "test"})
	t.Cleanup(func() { bare.Close() })
	m.Add(&EngineViewState{Engine: bare, ID: "bare"})
	noRegEng := New(&Params{Provider: &mockProvider{}, Model: "test"})
	t.Cleanup(func() { noRegEng.Close() })
	noRegEng.SetToolRefs(ToolRefs{Agent: agenttool.New()}) // no SetRegistry — nil Registry()
	m.Add(&EngineViewState{Engine: noRegEng, ID: "no-reg"})
	regB := agenttool.NewAgentRegistry()
	addTableOwner(t, m, "owner-b", regB)
	id := spawnHoldingAgent(t, regB)

	if got, ok := m.BgAgent(id); !ok || got.ID != id {
		t.Errorf("BgAgent(%q) = (%v, %v), want the entry held by the second owner engine", id, got, ok)
	}
}

func TestEngineManager_BgAgent_UnknownIDNotFound(t *testing.T) {
	m := NewEngineManager()
	if got, ok := m.BgAgent("agent-1"); ok || got != nil {
		t.Errorf("BgAgent on empty manager = (%v, %v), want (nil, false)", got, ok)
	}
	addTableOwner(t, m, "owner", agenttool.NewAgentRegistry())
	if got, ok := m.BgAgent("agent-999"); ok || got != nil {
		t.Errorf("BgAgent(unknown) = (%v, %v), want (nil, false)", got, ok)
	}
	// Lazy view (meta-only, engine not yet built) must be skipped, not
	// dereferenced.
	m.Add(&EngineViewState{ID: "lazy"})
	if got, ok := m.BgAgent("agent-999"); ok || got != nil {
		t.Errorf("BgAgent with a lazy view present = (%v, %v), want (nil, false)", got, ok)
	}
	// A nil view-state entry cannot enter the map through any public API
	// (Add stores only non-nil pointers) — the vs==nil guard keeps the walk
	// panic-free if that invariant ever breaks. White-box because only
	// package internals can produce the state.
	m.engines["ghost"] = nil
	m.order = append(m.order, "ghost")
	if got, ok := m.BgAgent("agent-999"); ok || got != nil {
		t.Errorf("BgAgent with a nil view-state entry = (%v, %v), want (nil, false) without panicking", got, ok)
	}
}

// A background table's reachability is the engine's reachability: once the
// owning engine leaves the manager, its spawns stop resolving. The registry
// itself keeps serving direct callers (Get) — removal only ends manager-side
// resolution.
func TestEngineManager_RemoveEngine_DropsBgResolution(t *testing.T) {
	mgr := NewEngineManager()
	reg := agenttool.NewAgentRegistry()
	addTableOwner(t, mgr, "owner", reg)
	addTableOwner(t, mgr, "keeper", agenttool.NewAgentRegistry())
	id := spawnHoldingAgent(t, reg)

	if _, ok := mgr.BgAgent(id); !ok {
		t.Fatalf("BgAgent(%q) not found before Remove", id)
	}
	if err := mgr.Remove("owner"); err != nil {
		t.Fatalf("Remove(owner): %v", err)
	}
	if got, ok := mgr.BgAgent(id); ok || got != nil {
		t.Errorf("BgAgent(%q) after Remove = (%v, %v), want (nil, false)", id, got, ok)
	}
	state, ok := reg.Get(id)
	if !ok || state.Status != agenttool.AgentRunning {
		t.Errorf("reg.Get after Remove = (%v, %v), want the entry still running", state, ok)
	}
}

// stampFloor is the lower bound for a time the manager stamped itself; the
// upper bound is the clock read inside the helper.
var stampFloor = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// assertFreshStamp pins a time.Time that manager code wrote. The zero value
// (an assignment the mutant dropped) and a stale copy both fall outside the
// window.
func assertFreshStamp(t *testing.T, label string, got time.Time) {
	t.Helper()
	now := time.Now() // REAL-TIME: the stamp under test is wall clock; no fake clock is in play
	if got.Before(stampFloor) || got.After(now) {
		t.Errorf("%s = %v, want a stamp inside [%v, %v]", label, got, stampFloor, now)
	}
}

func TestEngineManager_Add_DefaultsZeroCreatedAt(t *testing.T) {
	m := NewEngineManager()
	restoredAt := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	fresh := &EngineViewState{ID: "main", Name: "main"}
	restored := &EngineViewState{ID: "e2", Name: "engine-2", CreatedAt: restoredAt}
	m.Add(fresh)
	m.Add(restored)
	assertFreshStamp(t, "Add left CreatedAt", fresh.CreatedAt)
	if !fresh.LastActiveAt.Equal(fresh.CreatedAt) {
		t.Errorf("fresh LastActiveAt = %v, want the CreatedAt Add just stamped (%v)", fresh.LastActiveAt, fresh.CreatedAt)
	}
	// The guard is IsZero, not "always": a restored meta.json's CreatedAt is
	// the engine's real birth time and must survive re-registration.
	if !restored.CreatedAt.Equal(restoredAt) {
		t.Errorf("restored CreatedAt = %v, want %v", restored.CreatedAt, restoredAt)
	}
	if !restored.LastActiveAt.Equal(restoredAt) {
		t.Errorf("restored LastActiveAt = %v, want %v", restored.LastActiveAt, restoredAt)
	}
}

func TestEngineManager_SetActiveModel_WritesActiveEngineOnly(t *testing.T) {
	m := NewEngineManager()
	m.Add(&EngineViewState{ID: "main", Name: "main", Model: "sonnet"})
	m.Add(&EngineViewState{ID: "e2", Name: "engine-2", Model: "opus"})
	if err := m.SetActive("e2"); err != nil {
		t.Fatalf("SetActive(e2): %v", err)
	}
	m.SetActiveModel("opus-4-1")
	if got := m.Get("e2").Model; got != "opus-4-1" {
		t.Errorf("active engine Model = %q, want opus-4-1", got)
	}
	if got := m.Get("main").Model; got != "sonnet" {
		t.Errorf("inactive engine Model = %q, want sonnet (untouched)", got)
	}
	// No active engine: the lookup misses and the write is a no-op, not a
	// write through a nil view state.
	empty := NewEngineManager()
	empty.SetActiveModel("ghost")
	if got := empty.Count(); got != 0 {
		t.Errorf("Count after SetActiveModel on an empty manager = %d, want 0", got)
	}
}

func TestEngineManager_SetActiveThinking_WritesActiveEngineOnly(t *testing.T) {
	m := NewEngineManager()
	m.Add(&EngineViewState{ID: "main", Name: "main", Thinking: llm.EffortLow})
	m.Add(&EngineViewState{ID: "e2", Name: "engine-2", Thinking: llm.EffortNone})
	if err := m.SetActive("e2"); err != nil {
		t.Fatalf("SetActive(e2): %v", err)
	}
	m.SetActiveThinking(llm.EffortMax)
	if got := m.Get("e2").Thinking; got != llm.EffortMax {
		t.Errorf("active engine Thinking = %q, want %q", got, llm.EffortMax)
	}
	if got := m.Get("main").Thinking; got != llm.EffortLow {
		t.Errorf("inactive engine Thinking = %q, want %q (untouched)", got, llm.EffortLow)
	}
	empty := NewEngineManager()
	empty.SetActiveThinking(llm.EffortHigh)
	if got := empty.Count(); got != 0 {
		t.Errorf("Count after SetActiveThinking on an empty manager = %d, want 0", got)
	}
}

func TestEngineManager_SetActive_TouchesOnlyNewActiveEngine(t *testing.T) {
	m := NewEngineManager()
	createdAt := time.Date(2026, 2, 2, 2, 2, 2, 0, time.UTC)
	m.Add(&EngineViewState{ID: "main", Name: "main", CreatedAt: createdAt})
	m.Add(&EngineViewState{ID: "e2", Name: "engine-2", CreatedAt: createdAt})
	if err := m.SetActive("e2"); err != nil {
		t.Fatalf("SetActive(e2): %v", err)
	}
	got := m.Get("e2").LastActiveAt
	assertFreshStamp(t, "e2 LastActiveAt", got)
	if !got.After(createdAt) {
		t.Errorf("e2 LastActiveAt = %v, want a stamp after its CreatedAt %v (SetActive re-stamps the newly active engine)", got, createdAt)
	}
	if stale := m.Get("main").LastActiveAt; !stale.Equal(createdAt) {
		t.Errorf("main LastActiveAt = %v, want %v (only the newly active engine is stamped)", stale, createdAt)
	}
}

// TestEngineManager_EngineBySession_EmptyAddressNeverMatches pins the empty
// guard: an engine that has not started a session reports SessionID() == "",
// so the guard — not the loop — is what keeps EngineBySession("") a miss.
func TestEngineManager_EngineBySession_EmptyAddressNeverMatches(t *testing.T) {
	eng := New(&Params{EngineID: "nosession", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	if sid := eng.SessionID(); sid != "" {
		t.Fatalf("fixture engine already has a session: %q", sid)
	}
	m := NewEngineManager()
	m.Add(&EngineViewState{Engine: eng, ID: "nosession", Name: "nosession"})
	if got := m.EngineBySession(""); got != nil {
		t.Errorf("EngineBySession(\"\") = %v, want nil even with a session-less engine registered", got)
	}
}

// ---------------------------------------------------------------------------
// Critical-section scope. The manager's documented contract is "all methods
// are safe for concurrent use", which holds only while each method's lock
// covers its whole body. An Unlock that runs at the top of the method leaves
// the body unsynchronized: sequentially identical, fatally wrong under
// concurrency. These tests run each access path against engines-map churn, so
// an unsynchronized body trips the runtime's map guard ("concurrent map read
// and map write" / "concurrent map writes") instead of quietly returning
// consistent state. That abort is the kill these tests are built to produce: a
// run that dies with it has pinned a lock-scope regression, it is not
// infrastructure flakiness.
// ---------------------------------------------------------------------------

// lockScopeChurn churns the engines map from writer goroutines while every
// reader goroutine hammers probe. Seeds stay registered for the whole run, so
// the probe's expectations (non-nil lookups, no error) hold while the table
// grows and shrinks around them.
func lockScopeChurn(t *testing.T, probe func(m *EngineManager, seeds []string)) {
	t.Helper()
	const (
		seeds   = 64
		writers = 4
		readers = 2
		rounds  = 1200
	)
	m := NewEngineManager()
	ids := make([]string, 0, seeds)
	for i := range seeds {
		id := "seed-" + itoa(i)
		m.Add(newTestView(id, id))
		ids = append(ids, id)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			for i := range rounds {
				id := "churn-" + itoa(w) + "-" + itoa(i)
				m.Add(newTestView(id, id))
				if err := m.Remove(id); err != nil {
					t.Errorf("writer Remove(%q): %v", id, err)
					return
				}
			}
		}(w)
	}
	for range readers {
		wg.Go(func() {
			<-start
			for range rounds {
				probe(m, ids)
			}
		})
	}
	close(start)
	wg.Wait()
	if got := m.Count(); got != seeds {
		t.Errorf("Count after churn = %d, want %d (each churn engine must be removed again)", got, seeds)
	}
}

func TestEngineManager_ReadPathsStaySerializedWithMapChurn(t *testing.T) {
	metaDir := t.TempDir()
	cases := []struct {
		name  string
		probe func(m *EngineManager, seeds []string)
	}{
		{"Get", func(m *EngineManager, seeds []string) {
			for _, id := range seeds {
				if vs := m.Get(id); vs == nil {
					t.Errorf("Get(%q) = nil while churned engines come and go", id)
					return
				}
			}
		}},
		{"Active", func(m *EngineManager, seeds []string) {
			// One lookup per seed: the probe's unsynchronized window has to be
			// comparable to the writers' map-write window, or the runtime
			// guard never sees an overlap.
			for range seeds {
				if vs := m.Active(); vs == nil || vs.ID == "" {
					t.Errorf("Active() = %v, want the seeded active engine", vs)
					return
				}
			}
		}},
		{"ActiveID", func(m *EngineManager, _ []string) {
			if id := m.ActiveID(); id == "" {
				t.Error("ActiveID() = \"\" while a seeded engine is active")
				return
			}
		}},
		{"Count", func(m *EngineManager, seeds []string) {
			if got := m.Count(); got < len(seeds) {
				t.Errorf("Count() = %d, want at least %d (seeds are never removed)", got, len(seeds))
				return
			}
		}},
		{"List", func(m *EngineManager, seeds []string) {
			list := m.List()
			if len(list) < len(seeds) {
				t.Errorf("len(List()) = %d, want at least %d", len(list), len(seeds))
				return
			}
			for _, vs := range list {
				if vs == nil {
					t.Error("List() returned a nil view state")
					return
				}
			}
		}},
		{"GetByName", func(m *EngineManager, _ []string) {
			if vs := m.GetByName("probe-name"); vs != nil {
				t.Errorf("GetByName(probe-name) = %v, want nil (no such engine)", vs)
				return
			}
		}},
		{"ValidateName", func(m *EngineManager, _ []string) {
			if err := m.ValidateName("probe-name"); err != nil {
				t.Errorf("ValidateName(probe-name) = %v, want nil", err)
				return
			}
		}},
		{"EngineBySession", func(m *EngineManager, _ []string) {
			if eng := m.EngineBySession("no-such-session"); eng != nil {
				t.Errorf("EngineBySession(unknown) = %v, want nil", eng)
				return
			}
		}},
		{"Snapshot", func(m *EngineManager, seeds []string) {
			views, activeID := m.Snapshot()
			if len(views) < len(seeds) || activeID == "" {
				t.Errorf("Snapshot() = %d views (active %q), want at least %d views and a set active id",
					len(views), activeID, len(seeds))
				return
			}
		}},
		{"BgAgent", func(m *EngineManager, _ []string) {
			if st, ok := m.BgAgent("agent-1"); ok || st != nil {
				t.Errorf("BgAgent(unknown) = (%v, %v), want (nil, false)", st, ok)
				return
			}
		}},
		// Only the fallback path is visible here: the loop that skips taken ids
		// reads m.engines, which is what the map guard catches. With a minter
		// injected NewEngineID returns from m.minter.Mint() without touching the
		// map, so a lock hoisted out of that body can only show up as a race on
		// m.minter — the minter case of FieldChurnStaysSerialized is what pins it.
		{"NewEngineID", func(m *EngineManager, _ []string) {
			if id := m.NewEngineID(); !strings.HasPrefix(id, "agent-") {
				t.Errorf("NewEngineID() = %q, want an agent-N id", id)
				return
			}
		}},
		{"SetActiveModel", func(m *EngineManager, _ []string) {
			m.SetActiveModel("churned-model")
		}},
		{"SetActiveThinking", func(m *EngineManager, _ []string) {
			m.SetActiveThinking(llm.EffortMedium)
		}},
		{"SetActive", func(m *EngineManager, seeds []string) {
			if err := m.SetActive(seeds[len(seeds)-1]); err != nil {
				t.Errorf("SetActive(%q) = %v, want nil (seeds stay registered)", seeds[len(seeds)-1], err)
				return
			}
		}},
		{"PersistMeta", func(m *EngineManager, _ []string) {
			if err := m.PersistMeta(metaDir); err != nil {
				t.Errorf("PersistMeta: %v", err)
				return
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lockScopeChurn(t, tc.probe)
		})
	}
}

// TestEngineManager_FieldChurnStaysSerialized covers the accesses the map guard
// cannot see. activeID and minter are a string field and a pointer: neither
// has a runtime consistency check, so a body hoisted out of its critical
// section still returns the same values and stays green under a plain go
// test. The pair has to be produced here — each probe call writes the field
// it is about to read, so the two probe goroutines of lockScopeChurn form
// the write/read race, and the kill signal is the race detector's DATA RACE
// report rather than a failed assertion.
func TestEngineManager_FieldChurnStaysSerialized(t *testing.T) {
	cases := []struct {
		name  string
		probe func(m *EngineManager, seeds []string)
	}{
		{"activeID", func(m *EngineManager, seeds []string) {
			for _, id := range seeds {
				if err := m.SetActive(id); err != nil {
					t.Errorf("SetActive(%q) = %v, want nil (seeds stay registered)", id, err)
					return
				}
			}
			for range seeds {
				if got := m.ActiveID(); !slices.Contains(seeds, got) {
					t.Errorf("ActiveID() = %q, want one of the %d seeded ids", got, len(seeds))
					return
				}
			}
		}},
		{"minter", func(m *EngineManager, _ []string) {
			// Both the injected and the fallback path must hand back an id;
			// HasIDMinter is read, not asserted, because a churned minter is
			// legitimately either present or absent.
			m.SetIDMinter(agenttool.NewIDMinter())
			m.SetIDMinter(nil)
			m.HasIDMinter()
			if id := m.NewEngineID(); id == "" {
				t.Error(`NewEngineID() = "", want an id from the minter or the fallback counter`)
				return
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lockScopeChurn(t, tc.probe)
		})
	}
}

// TestEngineManager_WritePathsStaySerializedWithEachOther is the write/write
// half: Add and Remove racing each other on the same map with no lock between
// them, with no reader to blame.
func TestEngineManager_WritePathsStaySerializedWithEachOther(t *testing.T) {
	const (
		seeds   = 8
		writers = 6
		rounds  = 1500
	)
	m := NewEngineManager()
	for i := range seeds {
		id := "seed-" + itoa(i)
		m.Add(newTestView(id, id))
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			for i := range rounds {
				id := "churn-" + itoa(w) + "-" + itoa(i)
				m.Add(newTestView(id, id))
				if err := m.Remove(id); err != nil {
					t.Errorf("writer Remove(%q): %v", id, err)
					return
				}
			}
		}(w)
	}
	close(start)
	wg.Wait()
	list := m.List()
	if len(list) != seeds {
		t.Fatalf("len(List()) = %d, want %d (every churn engine must be gone)", len(list), seeds)
	}
	for _, vs := range list {
		if strings.HasPrefix(vs.ID, "churn-") {
			t.Errorf("%q survived Remove: still registered", vs.ID)
		}
	}
	if got := m.Count(); got != seeds {
		t.Errorf("Count = %d, want %d", got, seeds)
	}
}
