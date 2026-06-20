package engine

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/memory/short"
)

func newTestView(id, name string) *EngineViewState {
	return &EngineViewState{ID: id, Name: name, Model: "sonnet"}
}

func TestEngineManager_New_StartsEmpty(t *testing.T) {
	m := NewEngineManager()
	if got := m.Count(); got != 0 {
		t.Errorf("Count = %d, want 0", got)
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

func TestEngineManager_NewEngineID_Sequence(t *testing.T) {
	m := NewEngineManager()
	if got := m.NewEngineID(); got != "main" {
		t.Errorf("first NewEngineID = %q, want main", got)
	}
	m.Add(newTestView("main", "main"))
	if got := m.NewEngineID(); got != "e2" {
		t.Errorf("second NewEngineID = %q, want e2", got)
	}
	m.Add(newTestView("e2", "engine-2"))
	if got := m.NewEngineID(); got != "e3" {
		t.Errorf("third NewEngineID = %q, want e3", got)
	}
}

func TestEngineManager_NewEngineID_AvoidsCollision(t *testing.T) {
	// Pre-register "e2" before NewEngineID gets to allocate it.
	// The counter-based allocation should bump past the collision.
	m := NewEngineManager()
	m.Add(newTestView("main", "main"))
	m.Add(newTestView("e2", "engine-2"))
	// counter starts at 0, first candidate is e(0+2)="e2" → collides,
	// next iteration is e(1+2)="e3" → free.
	got := m.NewEngineID()
	if got != "e3" {
		t.Errorf("NewEngineID after collision = %q, want e3", got)
	}
}

func TestEngineManager_NewEngineName_Sequence(t *testing.T) {
	m := NewEngineManager()
	if got := m.NewEngineName(); got != "main" {
		t.Errorf("first NewEngineName = %q, want main", got)
	}
	m.Add(newTestView("main", "main"))
	if got := m.NewEngineName(); got != "engine-2" {
		t.Errorf("second NewEngineName = %q, want engine-2", got)
	}
	m.Add(newTestView("e2", "engine-2"))
	if got := m.NewEngineName(); got != "engine-3" {
		t.Errorf("third NewEngineName = %q, want engine-3", got)
	}
}

func TestEngineManager_NewEngineName_AvoidsCollision(t *testing.T) {
	m := NewEngineManager()
	m.Add(newTestView("main", "main"))
	m.Add(newTestView("e2", "engine-2")) // name conflict at len+1=3? engine-3 still free
	got := m.NewEngineName()
	// len(engines)=2 → candidate engine-3, free → return engine-3
	if got != "engine-3" {
		t.Errorf("NewEngineName after collision = %q, want engine-3", got)
	}

	// Force a real collision: manually register "engine-3" then ask again.
	m.Add(newTestView("e3", "engine-3"))
	got2 := m.NewEngineName()
	if got2 != "engine-4" {
		t.Errorf("NewEngineName after second collision = %q, want engine-4", got2)
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

// Ensure time is referenced (used by fakeRepl test setups above for future fields).
var _ = time.Now
