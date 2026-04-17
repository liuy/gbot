package agent

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"sync"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Fork agent lifecycle types
// ---------------------------------------------------------------------------

// ForkAgentStatus represents the current state of a fork agent.
type ForkAgentStatus string

const (
	ForkRunning   ForkAgentStatus = "running"
	ForkCompleted ForkAgentStatus = "completed"
	ForkFailed    ForkAgentStatus = "failed"
	ForkCancelled ForkAgentStatus = "cancelled"
)

// ForkAgentState tracks the lifecycle of a single fork agent.
type ForkAgentState struct {
	ID          string
	Description string
	Status      ForkAgentStatus
	StartTime   time.Time
	Result      *types.SubQueryResult
	done        chan struct{}
	cancel      context.CancelFunc
}

// ForkAgentRegistry manages fork agent lifecycle.
type ForkAgentRegistry struct {
	mu     sync.Mutex
	agents map[string]*ForkAgentState
	nextID int
}

// NewForkAgentRegistry creates a new registry for fork agents.
func NewForkAgentRegistry() *ForkAgentRegistry {
	return &ForkAgentRegistry{
		agents: make(map[string]*ForkAgentState),
	}
}

// Spawn starts a new fork agent in a background goroutine.
// The runFn is called in a goroutine with a derived context.
// On completion, notifyFn is called with the result.
func (r *ForkAgentRegistry) Spawn(
	ctx context.Context,
	runFn func(ctx context.Context) (*types.SubQueryResult, error),
	notifyFn func(agentID string, toolUseID string, result *types.SubQueryResult, err error),
	description string,
	parentToolUseID string,
) (*ForkAgentState, error) {
	if runFn == nil {
		return nil, fmt.Errorf("runFn is required")
	}

	childCtx, cancel := context.WithCancel(ctx)

	r.mu.Lock()
	r.nextID++
	id := fmt.Sprintf("fork-%d", r.nextID)
	state := &ForkAgentState{
		ID:          id,
		Description: description,
		Status:      ForkRunning,
		StartTime:   time.Now(),
		done:        make(chan struct{}),
		cancel:      cancel,
	}
	r.agents[id] = state
	r.mu.Unlock()

	go func() {
		defer close(state.done)

		result, err := runFn(childCtx)

		r.mu.Lock()
		if err != nil {
			if childCtx.Err() != nil {
				state.Status = ForkCancelled
			} else {
				state.Status = ForkFailed
			}
		} else {
			state.Status = ForkCompleted
			state.Result = result
		}
		r.mu.Unlock()

		if notifyFn != nil {
			notifyFn(id, parentToolUseID, result, err)
		}
	}()

	return state, nil
}

// Wait blocks until the fork agent with the given ID completes.
func (r *ForkAgentRegistry) Wait(id string) (*ForkAgentState, bool) {
	r.mu.Lock()
	state, ok := r.agents[id]
	r.mu.Unlock()
	if !ok {
		return nil, false
	}
	<-state.done
	cp := *state
	if state.Result != nil {
		r := *state.Result
		cp.Result = &r
	}
	return &cp, true
}

// Cancel cancels the fork agent with the given ID.
func (r *ForkAgentRegistry) Cancel(id string) bool {
	r.mu.Lock()
	state, ok := r.agents[id]
	r.mu.Unlock()
	if !ok {
		return false
	}
	state.cancel()
	return true
}

// Get returns a snapshot of the fork agent state with the given ID.
// Returns a copy — callers can safely read fields without mutex concerns.
func (r *ForkAgentRegistry) Get(id string) (*ForkAgentState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.agents[id]
	if !ok {
		return nil, false
	}
	cp := *state
	if state.Result != nil {
		r := *state.Result
		cp.Result = &r
	}
	return &cp, true
}

// List returns snapshots of all fork agents.
// Returns copies — callers can safely read fields without mutex concerns.
func (r *ForkAgentRegistry) List() []*ForkAgentState {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]*ForkAgentState, 0, len(r.agents))
	for _, state := range r.agents {
		cp := *state
		if state.Result != nil {
			r := *state.Result
			cp.Result = &r
		}
		result = append(result, &cp)
	}
	return result
}

// CleanupCompleted removes all agents in terminal states (completed, failed, cancelled).
// Call after processing results to prevent unbounded memory growth.
func (r *ForkAgentRegistry) CleanupCompleted() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, state := range r.agents {
		if state.Status == ForkCompleted || state.Status == ForkFailed || state.Status == ForkCancelled {
			delete(r.agents, id)
		}
	}
}

// ---------------------------------------------------------------------------
// Notification XML builder
// ---------------------------------------------------------------------------

// buildForkNotificationXML generates the task-notification XML for a fork
// agent completion. Injected as a user message into the parent conversation.
func buildForkNotificationXML(agentID, toolUseID string, result *types.SubQueryResult, err error, description string) string {
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
	if result != nil {
		durationMs = result.TotalDurationMs
		tokens = result.TotalTokens
	}

	return fmt.Sprintf(`<task-notification>
<task-id>%s</task-id>
<tool-use-id>%s</tool-use-id>
<status>%s</status>
<agent-type>fork</agent-type>
<duration-ms>%d</duration-ms>
<tokens>%d</tokens>
<summary>Fork agent %s %s</summary>
<result>
%s
</result>
</task-notification>`, xe(agentID), xe(toolUseID), status, durationMs, tokens, xe(description), status, xe(content))
}

// xe escapes a string for safe inclusion in XML element content.
func xe(s string) string {
	var buf bytes.Buffer
	xml.Escape(&buf, []byte(s))
	return buf.String()
}
