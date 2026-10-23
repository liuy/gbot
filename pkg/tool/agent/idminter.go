package agent

import (
	"fmt"
	"regexp"
	"sync"
)

// agentIDGrammar is the reserved id grammar minted by IDMinter. Engine names
// matching it are rejected (they would be unresolvable against real agent
// ids), and boot seeding scans persisted ids/names for it so a minted id can
// never shadow one already in use.
var agentIDGrammar = regexp.MustCompile(`^agent-\d+$`)

// IsReservedAgentID reports whether name matches the agent-N id grammar
// minted by IDMinter.
func IsReservedAgentID(name string) bool {
	return agentIDGrammar.MatchString(name)
}

// IDMinter is the process-global counter behind every agent-N id: engine ids,
// subagent ids and fork ids all come from one minter so the namespaces can
// never collide. Created once in app/start.go and injected into both the
// AgentRegistry and the EngineManager.
type IDMinter struct {
	mu   sync.Mutex
	next int
}

// NewIDMinter creates a minter whose first Mint returns "agent-1".
func NewIDMinter() *IDMinter {
	return &IDMinter{}
}

// Init sets the high-water mark so the next Mint returns agent-(maxSeen+1).
// Never moves backwards; Init(0) is a no-op. Boot-time seeding calls this
// with the max persisted agent-N suffix so runtime mints cannot clash with
// ids restored from meta.json.
func (m *IDMinter) Init(maxSeen int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if maxSeen >= m.next {
		m.next = maxSeen
	}
}

// Mint returns the next id in the "agent-N" sequence, N strictly increasing.
func (m *IDMinter) Mint() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	return fmt.Sprintf("agent-%d", m.next)
}
