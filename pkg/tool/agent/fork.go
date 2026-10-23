package agent

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Background agent lifecycle types
// ---------------------------------------------------------------------------

// BackgroundAgentStatus represents the current state of a background agent.
type BackgroundAgentStatus string

const (
	AgentRunning   BackgroundAgentStatus = "running"
	AgentCompleted BackgroundAgentStatus = "completed"
	AgentFailed    BackgroundAgentStatus = "failed"
	AgentCancelled BackgroundAgentStatus = "cancelled"
)

// SubEngine is the deliverable handle a registry entry can hold: a running
// background agent's engine, reachable through its attachment queue. Declared
// here (not imported from engine) to avoid the agent → engine import cycle.
type SubEngine interface {
	EnqueueAttachment(item types.QueuedItem)
}

// BackgroundAgentState tracks the lifecycle of a single background agent.
type BackgroundAgentState struct {
	ID          string
	Description string
	Status      BackgroundAgentStatus
	StartTime   time.Time
	Result      *types.SubQueryResult
	// AgentType/ParentToolUseID feed the wui jobs panel: the display type
	// ("fork", "Explore", ...) and the parent Agent tool call whose hub
	// events stream this agent's activity.
	AgentType       string
	ParentToolUseID string
	// Sink is the running sub-engine that messages can be delivered to. Nil
	// until RunAgent attaches it post-construction (status "starting" before
	// that), and meaningless once terminal.
	Sink   SubEngine
	done   chan struct{}
	cancel context.CancelFunc
}

// AgentRegistry manages background agent lifecycle.
type AgentRegistry struct {
	mu     sync.Mutex
	agents map[string]*BackgroundAgentState
	minter *IDMinter
	// onJobEvent is called on every lifecycle transition (started at Spawn,
	// terminated at finalize). Called outside r.mu — the emission chain runs
	// synchronously into the engine hub and the wui panel.
	onJobEvent func(types.QueryEventType, types.JobEvent)
}

// SetOnJobEvent installs the lifecycle-event emitter (engine wiring).
func (r *AgentRegistry) SetOnJobEvent(fn func(types.QueryEventType, types.JobEvent)) {
	r.onJobEvent = fn
}

// NewAgentRegistry creates a new registry for background agents.
func NewAgentRegistry() *AgentRegistry {
	return NewAgentRegistryWithMinter(nil)
}

// NewAgentRegistryWithMinter creates a registry that mints ids from
// the given process-global minter (shared with the EngineManager so engine ids
// and background agent ids can never collide). A nil minter falls back to a
// private one — production wiring always shares via app/start.go.
func NewAgentRegistryWithMinter(m *IDMinter) *AgentRegistry {
	if m == nil {
		m = NewIDMinter()
	}
	return &AgentRegistry{
		agents: make(map[string]*BackgroundAgentState),
		minter: m,
	}
}

// Spawn starts a new background agent in a goroutine.
// The runFn is called in a goroutine with a derived context and the minted
// agent id — the id exists only inside Spawn, so the identity injection and
// sink attachment in runFn need it handed back (post-return mutation of the
// returned snapshot would race the goroutine).
// On completion, notifyFn is called with the result.
// Ids are always "agent-N" from the shared process-global counter — forks and
// subagents live in one namespace. agentType is the display type surfaced in
// the jobs panel.
func (r *AgentRegistry) Spawn(
	ctx context.Context,
	runFn func(ctx context.Context, agentID string) (*types.SubQueryResult, error),
	notifyFn func(agentID string, toolUseID string, result *types.SubQueryResult, err error),
	description string,
	parentToolUseID string,
	agentType string,
) (*BackgroundAgentState, error) {
	if runFn == nil {
		return nil, fmt.Errorf("runFn is required")
	}

	childCtx, cancel := context.WithCancel(ctx)

	r.mu.Lock()
	id := r.minter.Mint()
	state := &BackgroundAgentState{
		ID:              id,
		Description:     description,
		Status:          AgentRunning,
		StartTime:       time.Now(),
		AgentType:       agentType,
		ParentToolUseID: parentToolUseID,
		done:            make(chan struct{}),
		cancel:          cancel,
	}
	r.agents[id] = state
	// Snapshot while still holding the lock — the goroutine below will
	// mutate state.Status and state.Result under the same mutex, so we
	// must copy before releasing to avoid a data race between the caller
	// reading the returned snapshot and the goroutine writing state.
	snapshot := *state
	r.mu.Unlock()

	// Started emission after unlock, before the run goroutine exists:
	// structurally first — no process event of this job can precede it.
	if r.onJobEvent != nil {
		r.onJobEvent(types.EventJobStarted, agentShellEvent(&snapshot, string(AgentRunning)))
	}

	go func() {
		defer close(state.done)
		defer func() {
			if p := recover(); p != nil {
				r.finalize(state, AgentFailed, nil)
				if notifyFn != nil {
					notifyFn(id, parentToolUseID, nil, fmt.Errorf("panic: %v", p))
				}
			}
		}()

		result, err := runFn(childCtx, id)

		switch {
		case childCtx.Err() != nil:
			// Killed via Cancel, regardless of how runFn reported it —
			// engine.RunAgent converts a cancellation into a normal
			// "(agent interrupted by user)" result with nil error, so the
			// error value alone cannot mark the entry AgentCancelled (the
			// job table maps that status to "killed"). childCtx is the
			// registry's own derived context: cancelled only by Cancel.
			// Preserve the partial result so the notification includes
			// what the agent accomplished before being killed.
			// Source: AgentTool.tsx:1006 + agentToolUtils.ts:658
			r.finalize(state, AgentCancelled, result)
		case err != nil:
			r.finalize(state, AgentFailed, nil)
		default:
			r.finalize(state, AgentCompleted, result)
		}

		if notifyFn != nil {
			notifyFn(id, parentToolUseID, result, err)
		}
	}()

	return &snapshot, nil
}

// agentShellEvent renders the wui panel row shell for an agent registry
// entry. The status vocabulary mirrors convertState (adapter.go:116-128):
// AgentCancelled maps to "killed" — the panel's terminal-status set never
// contains "cancelled", and a shell stuck on it would never freeze. started
// events carry no exit code (zero).
func agentShellEvent(state *BackgroundAgentState, status string) types.JobEvent {
	kind := "agent"
	if state.AgentType == "fork" {
		kind = "fork"
	}
	// The registry vocabulary and the wire vocabulary only diverge on
	// failed/cancelled exit codes and the cancelled→killed rename; completed
	// and running pass through the initializer unchanged.
	wireStatus, exitCode := status, 0
	switch status {
	case string(AgentFailed):
		exitCode = 1
	case string(AgentCancelled):
		wireStatus = "killed"
		exitCode = -1
	}
	return types.JobEvent{
		ID:              state.ID,
		Kind:            kind,
		Status:          wireStatus,
		Description:     state.Description,
		AgentType:       state.AgentType,
		ParentToolUseID: state.ParentToolUseID,
		ExitCode:        exitCode,
		StartedMs:       state.StartTime.UnixMilli(),
	}
}

// finalize is the single terminal-state write point: it records the
// transition under r.mu (branch semantics preserved — a cancelled agent
// keeps a partial result, a failed one stores nothing, a completed one
// stores the full result) and emits job_terminated outside the lock, BEFORE
// the caller's notification: the notification injects XML into the parent
// conversation and may kick a new parent query whose events must never
// overtake job_terminated. The emitted shell's AgentType override mirrors
// convertState (adapter.go:129-135): a resolved Result.AgentType wins,
// otherwise the spawn-time display type stands.
func (r *AgentRegistry) finalize(state *BackgroundAgentState,
	status BackgroundAgentStatus, result *types.SubQueryResult) {
	r.mu.Lock()
	state.Status = status
	switch status {
	case AgentCancelled:
		if result != nil {
			state.Result = result
		}
	case AgentCompleted:
		state.Result = result
	}
	stored := state.Result
	r.mu.Unlock()

	if r.onJobEvent != nil {
		shell := agentShellEvent(state, string(status))
		if stored != nil && stored.AgentType != "" {
			shell.AgentType = stored.AgentType
		}
		r.onJobEvent(types.EventJobTerminated, shell)
	}
}

// copyState clones a state entry including its Result. Callers read the
// snapshot without holding the owning registry's mutex, while the spawn
// goroutine keeps mutating the original under that mutex.
func copyState(s *BackgroundAgentState) *BackgroundAgentState {
	cp := *s
	if s.Result != nil {
		res := *s.Result
		cp.Result = &res
	}
	return &cp
}

// Wait blocks until the background agent with the given ID completes.
func (r *AgentRegistry) Wait(id string) (*BackgroundAgentState, bool) {
	r.mu.Lock()
	state, ok := r.agents[id]
	r.mu.Unlock()
	if !ok {
		return nil, false
	}
	<-state.done
	// Re-acquire lock to read final state — the spawned goroutine writes
	// Status/Result under the lock, and we must read them under it too.
	r.mu.Lock()
	cp := copyState(state)
	r.mu.Unlock()
	return cp, true
}

// Cancel cancels the background agent with the given ID. Reports whether the
// id was found.
func (r *AgentRegistry) Cancel(id string) bool {
	r.mu.Lock()
	state, ok := r.agents[id]
	r.mu.Unlock()
	if !ok {
		return false
	}
	state.cancel()
	return true
}

// Get returns a snapshot of the background agent state with the given ID.
// Returns a copy — callers can safely read fields without mutex concerns.
func (r *AgentRegistry) Get(id string) (*BackgroundAgentState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.agents[id]
	if !ok {
		return nil, false
	}
	return copyState(state), true
}

// AttachSink stores the sub-engine handle on the registry entry so the
// message resolver can deliver to a running background agent. No-op for
// unknown ids — RunAgent attaches right after wiring, and CleanupCompleted
// may have removed a just-finished entry. Explicit Unlock rather than defer:
// the body has no panic-capable call, and a dropped unlock deadlocks the
// next registry call instead of silently widening the critical section.
func (r *AgentRegistry) AttachSink(id string, sink SubEngine) {
	r.mu.Lock()
	if state, ok := r.agents[id]; ok {
		state.Sink = sink
	}
	r.mu.Unlock()
}

// List returns snapshots of all background agents. Returns copies — callers
// can safely read fields without mutex concerns.
func (r *AgentRegistry) List() []*BackgroundAgentState {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]*BackgroundAgentState, 0, len(r.agents))
	for _, state := range r.agents {
		result = append(result, copyState(state))
	}
	return result
}

// CleanupCompleted removes all agents in terminal states (completed, failed,
// cancelled). Call after processing results to prevent unbounded memory
// growth.
func (r *AgentRegistry) CleanupCompleted() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, state := range r.agents {
		if state.Status == AgentCompleted || state.Status == AgentFailed || state.Status == AgentCancelled {
			delete(r.agents, id)
		}
	}
}

// ---------------------------------------------------------------------------
// Notification XML builder
// ---------------------------------------------------------------------------

// buildAgentNotificationXML generates the job-notification XML for a background
// agent completion. Injected as a user message into the parent conversation.
func buildAgentNotificationXML(agentID, toolUseID string, result *types.SubQueryResult, err error, description, agentName string) string {
	status := "completed"
	if err != nil {
		status = "failed"
	}

	var content string
	if err != nil {
		content = fmt.Sprintf("Error: %v", err)
	} else if result != nil {
		content = result.Content
	}

	var durationMs int64
	var tokens int
	var toolUses int
	if result != nil {
		durationMs = result.TotalDurationMs
		tokens = result.TotalTokens
		toolUses = result.TotalToolUseCount
	}

	agentType := "fork"
	if agentName != "" {
		agentType = agentName
	}
	return fmt.Sprintf(`<job-notification>
<job-id>%s</job-id>
<tool-use-id>%s</tool-use-id>
<status>%s</status>
<agent-type>%s</agent-type>
<duration-ms>%d</duration-ms>
<tokens>%d</tokens>
<tool-uses>%d</tool-uses>
<summary>Background agent "%s" %s</summary>
<result>
%s
</result>
</job-notification>`, xe(agentID), xe(toolUseID), status, xe(agentType), durationMs, tokens, toolUses, xe(description), status, xe(content))
}

// xe escapes a string for safe inclusion in XML element content.
func xe(s string) string {
	var buf bytes.Buffer
	xml.Escape(&buf, []byte(s))
	return buf.String()
}

// IsInForkChild returns true if the conversation history indicates we're
// already inside a fork agent. Prevents recursive forking.
// Source: forkSubagent.ts:78-89 — isInForkChild()
func IsInForkChild(messages []types.Message) bool {
	marker := "<" + types.ForkBoilerplateTag + ">"
	for _, msg := range messages {
		if msg.Role != types.RoleUser {
			continue
		}
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeText && strings.Contains(block.Text, marker) {
				return true
			}
		}
	}
	return false
}
