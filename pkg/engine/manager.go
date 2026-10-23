package engine

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
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
	minter   *agenttool.IDMinter
	// nextID backs the fallback counter when no minter was injected
	// (standalone managers in tests); production always injects via
	// app/start.go so engines and background agents share one sequence.
	nextID int
}

// NewEngineManager returns an empty manager.
func NewEngineManager() *EngineManager {
	return &EngineManager{engines: make(map[string]*EngineViewState)}
}

// SetIDMinter injects the process-global id minter so runtime engine ids
// come from the same agent-N counter as subagent and fork ids (no cross-
// family collision). Must be called before the first NewEngineID.
func (m *EngineManager) SetIDMinter(idm *agenttool.IDMinter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.minter = idm
}

// HasIDMinter reports whether the process-global minter was injected. The boot
// restore path uses it to catch a wiring mismatch: a manager that mints from
// the shared counter must be paired with that same counter when seeding, or a
// runtime mint can shadow a persisted id.
func (m *EngineManager) HasIDMinter() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.minter != nil
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

// NewEngineID returns a fresh engine ID from the agent-N sequence. Engines,
// subagents and forks share one process-global counter (see
// pkg/tool/agent/idminter.go), so an engine id can never collide with a
// background agent id. Callers use the returned string as the engine's
// display name too (name == id): one mint per engine, no second sequence to
// keep in sync. The internal fallback counter exists only for a standalone
// manager (tests) — app/start.go always injects the process-global minter.
// "main" is never minted here; it is synthesized by the boot restore path
// (planRestore).
func (m *EngineManager) NewEngineID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.minter != nil {
		return m.minter.Mint()
	}
	// Allocation, not just advancement: an id can already be registered
	// out-of-band (restored meta, test setup), so the candidate must be
	// bumped past anything the manager already holds — otherwise the mint
	// hands back an id Add would reject as a duplicate.
	for {
		m.nextID++
		candidate := fmt.Sprintf("agent-%d", m.nextID)
		if _, ok := m.engines[candidate]; !ok {
			return candidate
		}
	}
}

// BgAgent resolves a background agent id across every live engine's
// background table and returns the owning registry's snapshot copy. First
// match wins; tables hold disjoint ids in production (one shared id minter
// across engines).
//
// Tables are discovered through mgr.engines — the table's lifetime IS the
// engine's lifetime, so a dead engine's spawns stop resolving the moment it
// leaves the manager (no shadow registry list to keep in sync). Lock order
// stays mgr.mu → reg.mu: Get enters the registry while the manager read lock
// is held, and registries never call back into the manager.
//
// Reads vs.Engine.toolRefs, which is written once by SetToolRefs BEFORE the
// engine enters the manager (every production creation path: start.go,
// dream, wechat, wui) — the mgr.mu handed off by Add makes that write
// visible here. Engines without wired Agent tools (lazy views, hand-built
// test engines) are skipped.
func (m *EngineManager) BgAgent(id string) (*agenttool.BackgroundAgentState, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, eid := range m.order {
		vs := m.engines[eid]
		if vs == nil || vs.Engine == nil {
			continue
		}
		at := vs.Engine.toolRefs.Agent
		if at == nil {
			continue
		}
		reg := at.Registry()
		if reg == nil {
			continue
		}
		if state, ok := reg.Get(id); ok {
			return state, true
		}
	}
	return nil, false
}

// ValidateName reports whether name is acceptable as a CUSTOM engine name
// (an auto-created engine's name is its minted id, which never passes through
// here). The agent-N grammar is reserved for counter-minted ids — a hand-
// picked name matching it could never be resolved unambiguously. "main" and
// "dream" are reserved ids, and a new name must not duplicate an existing
// engine's name or collide with its id.
func (m *EngineManager) ValidateName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("engine name must not be empty")
	}
	if name == "main" || name == "dream" {
		return fmt.Errorf("engine name %q is reserved", name)
	}
	if agenttool.IsReservedAgentID(name) {
		return fmt.Errorf("engine name %q collides with the agent-N id namespace", name)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, id := range m.order {
		vs := m.engines[id]
		if vs.Name == name {
			return fmt.Errorf("engine name %q is already in use", name)
		}
		if vs.ID == name {
			return fmt.Errorf("engine name %q collides with an existing engine id", name)
		}
	}
	return nil
}

// GetByName returns the first view state whose Name equals name (creation
// order), or nil. Empty name returns nil.
func (m *EngineManager) GetByName(name string) *EngineViewState {
	if name == "" {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, id := range m.order {
		if vs := m.engines[id]; vs.Name == name {
			return vs
		}
	}
	return nil
}

// Get returns the view state for an ID, or nil if not found.
func (m *EngineManager) Get(id string) *EngineViewState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.engines[id]
}

// EngineBySession returns the engine whose live session matches sessionID,
// or nil. Boot-time SessionStart uses it to route js hooks into the owning
// engine's REPL. Matches Engine.SessionID() (live), not ActiveSessionID —
// the view-state copy goes stale when engines switch sessions. Lazy view
// states (nil Engine) are skipped.
func (m *EngineManager) EngineBySession(sessionID string) *Engine {
	if sessionID == "" {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, vs := range m.engines {
		if vs.Engine != nil && vs.Engine.SessionID() == sessionID {
			return vs.Engine
		}
	}
	return nil
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
