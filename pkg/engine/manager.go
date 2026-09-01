package engine

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
)

// ReplSnapshot is the minimal interface EngineManager needs from a ReplState.
// Defining it here breaks the import cycle (engine must not import tui).
// The tui package supplies an adapter when it constructs EngineViewState.
type ReplSnapshot interface {
	IsStreaming() bool
	CurrentToolName() string // "" when idle
}

// EngineViewState bundles an Engine with the per-engine TUI state that must
// travel together. Owned by EngineManager; never copied after creation
// (callers always receive pointers).
//
// Engine is nil for lazy view states registered from meta.json before the
// user has switched to them. materializeEngine builds it on first switch.
type EngineViewState struct {
	Engine          *Engine
	Repl            ReplSnapshot // adapter supplied by tui package
	Handler         any          // *tui.TUIHandler; opaque to avoid tui→engine cycle
	History         any          // *tui.History; opaque to avoid tui→engine cycle
	ID              string
	Name            string
	ActiveSessionID string
	Model           string
	Thinking        llm.Effort
	CreatedAt       time.Time
	LastActiveAt    time.Time

	// ReadOnly marks an engine whose input box must be disabled in the TUI.
	// The engine is driven exclusively by an external connector (e.g. WeChat)
	// that calls engine.Query directly; a TUI submit would race with it.
	// Read by switchEngine to set the input into a read-only state and by
	// handleSubmitRepl to reject submits defensively.
	ReadOnly bool

	// System marks a system-level engine (e.g. dream) that is excluded from
	// PersistMeta — it is always created fresh after restore, never serialized
	// to meta.json. Its session IS persisted in SQLite (resumable via
	// ListSessionsByEngine).
	System bool
}

// EngineViewSnapshot is a point-in-time copy of an EngineViewState for
// rendering. It is decoupled from the manager's internal storage so
// callers can iterate without holding the manager mutex (lipgloss rendering
// takes long enough that holding the lock would starve writers).
type EngineViewSnapshot struct {
	ID              string
	Name            string
	ActiveSessionID string
	Model           string
	IsStreaming     bool
	CurrentToolName string
}

// EngineManager owns the set of engines in one TUI process.
// All methods are safe for concurrent use.
type EngineManager struct {
	mu       sync.RWMutex
	engines  map[string]*EngineViewState
	activeID string
	order    []string // creation order, stable for picker display
	nextID   int      // monotonically increasing counter for ID allocation
}

// NewEngineManager returns an empty manager.
func NewEngineManager() *EngineManager {
	return &EngineManager{engines: make(map[string]*EngineViewState)}
}

// Add registers a new view state. Panics on duplicate ID (programming error).
// Does NOT change activeID; call SetActive to switch. The first Add with an
// empty activeID implicitly makes that engine active (manager invariant:
// exactly one active engine at all times).
func (m *EngineManager) Add(vs *EngineViewState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.engines[vs.ID]; ok {
		panic(fmt.Sprintf("engine: duplicate engine ID %q", vs.ID))
	}
	if vs.CreatedAt.IsZero() {
		vs.CreatedAt = time.Now()
	}
	vs.LastActiveAt = vs.CreatedAt
	m.engines[vs.ID] = vs
	m.order = append(m.order, vs.ID)
	if m.activeID == "" {
		m.activeID = vs.ID
	}
}

// NewEngineID returns a fresh unique engine ID ("main" first, then "e2","e3",…).
// Counter-based allocation with a single collision check for the rare case
// where an ID was pre-registered out-of-band (test setup, future rename).
// The counter is reset only by process restart — safe because IDs are never
// persisted beyond meta.json, which stores whatever IDs the user has used.
//
// Caller is responsible for actually building the *Engine and ReplSnapshot
// before calling Add — this helper only allocates the ID and reserves nothing.
func (m *EngineManager) NewEngineID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.engines) == 0 {
		return "main"
	}
	for {
		candidate := fmt.Sprintf("e%d", m.nextID+2) // e2, e3, ...
		m.nextID++
		if _, ok := m.engines[candidate]; !ok {
			return candidate
		}
	}
}

// NewEngineName returns "main" if no engines exist, else "agent-N" where
// N=len+1, skipping any name already in use.
func (m *EngineManager) NewEngineName() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.engines) == 0 {
		return "main"
	}
	for n := len(m.engines) + 1; ; n++ {
		candidate := fmt.Sprintf("agent-%d", n)
		if !m.nameExistsLocked(candidate) {
			return candidate
		}
	}
}

func (m *EngineManager) nameExistsLocked(name string) bool {
	for _, vs := range m.engines {
		if vs.Name == name {
			return true
		}
	}
	return false
}

// Get returns the view state for an ID, or nil if not found.
func (m *EngineManager) Get(id string) *EngineViewState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.engines[id]
}

// SetActiveModel updates the active engine's Model field. Per-engine model
// is authoritative at runtime; settings.json is only the default for
// newly-created engines.
func (m *EngineManager) SetActiveModel(model string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if vs, ok := m.engines[m.activeID]; ok {
		vs.Model = model
	}
}

// SetActiveThinking updates the active engine's sticky effort override (the
// view-state copy is what PersistMeta writes to meta.json).
func (m *EngineManager) SetActiveThinking(e llm.Effort) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if vs, ok := m.engines[m.activeID]; ok {
		vs.Thinking = e
	}
}

// Active returns the currently active view state, or nil if the manager is empty.
func (m *EngineManager) Active() *EngineViewState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.engines[m.activeID]
}

// ActiveID returns the active engine ID ("" if empty).
func (m *EngineManager) ActiveID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeID
}

// SetActive marks id active. Returns error if id is not registered.
func (m *EngineManager) SetActive(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.engines[id]; !ok {
		return fmt.Errorf("engine: %q not found", id)
	}
	if m.activeID != id {
		m.activeID = id
		if vs := m.engines[id]; vs != nil {
			vs.LastActiveAt = time.Now()
		}
	}
	return nil
}

// List returns view states in creation order. The slice is freshly allocated
// so callers may reorder / filter it without mutating manager state.
func (m *EngineManager) List() []*EngineViewState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*EngineViewState, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.engines[id])
	}
	return out
}

// Remove deletes an engine. Returns an error if id is not found or if it's
// the only remaining engine (must always keep >= 1). Caller is responsible
// for calling Engine.Close() on the removed engine before invoking Remove.
//
// When the active engine is removed, the fallback is the most recently added
// remaining engine (tail of order). This matches the picker's mental model
// (newest remaining surfaces to the top).
func (m *EngineManager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.engines[id]; !ok {
		return fmt.Errorf("engine: %q not found", id)
	}
	if len(m.engines) == 1 {
		return errors.New("engine: must keep at least one engine")
	}
	delete(m.engines, id)
	for i, oid := range m.order {
		if oid == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	if m.activeID == id {
		// Fall back to the most recently added remaining engine.
		m.activeID = m.order[len(m.order)-1]
	}
	return nil
}

// Count returns the number of registered engines.
func (m *EngineManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.engines)
}

// Snapshot returns a slice of EngineViewSnapshot in creation order plus the
// active engine ID. Safe to call from any goroutine; the returned slice is
// a fresh copy and may be mutated freely. The IsStreaming/CurrentToolName
// values are read from each ReplSnapshot adapter under the manager's read
// lock, so the result reflects a consistent point in time.
func (m *EngineManager) Snapshot() ([]EngineViewSnapshot, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	views := make([]EngineViewSnapshot, 0, len(m.order))
	for _, id := range m.order {
		vs := m.engines[id]
		s := EngineViewSnapshot{
			ID:              vs.ID,
			Name:            vs.Name,
			ActiveSessionID: vs.ActiveSessionID,
			Model:           vs.Model,
		}
		if vs.Repl != nil {
			s.IsStreaming = vs.Repl.IsStreaming()
			s.CurrentToolName = vs.Repl.CurrentToolName()
		}
		views = append(views, s)
	}
	return views, m.activeID
}

// PersistMeta writes meta.json reflecting the current state of all
// engines. Serialized via the manager mutex — safe to call concurrently
// even when multiple background engines finish queries near-simultaneously.
// No-op when projectDir == "" (tests, headless runs).
//
// Always emits BOTH current_session_id (dual-write for one release cycle so
// an old binary opening the file still works) and the engines array.
func (m *EngineManager) PersistMeta(projectDir string) error {
	if projectDir == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	meta := &short.WorkspaceMeta{
		Engines:        make([]short.EngineMeta, 0, len(m.order)),
		ActiveEngineID: m.activeID,
		LastActiveAt:   time.Now(),
	}
	for _, id := range m.order {
		vs := m.engines[id]
		if vs.System {
			continue
		}
		em := short.EngineMeta{
			ID:              vs.ID,
			Name:            vs.Name,
			ActiveSessionID: vs.ActiveSessionID,
			Model:           vs.Model,
			Thinking:        string(vs.Thinking),
		}
		meta.Engines = append(meta.Engines, em)
		if id == m.activeID {
			meta.CurrentSessionID = vs.ActiveSessionID // dual-write field
		}
	}
	return short.WriteWorkspaceMeta(projectDir, meta)
}
