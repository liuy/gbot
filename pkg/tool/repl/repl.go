package repl

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// REPLTool — concrete struct implementing tool.Tool (like AgentTool).

// replInput is the JSON input schema for the REPL tool.
// action dispatches wait/terminate/reset.
type replInput struct {
	Code      string `json:"code"`
	Reset     bool   `json:"reset"`
	Action    string `json:"action"`    // "wait" or "terminate"
	SessionID string `json:"session_id"` // for wait/terminate actions
}

// REPLTool implements tool.Tool for JavaScript REPL execution.
// Uses SetToolExecutor injection (like AgentTool's SetFactory) to get full
// permission-checked tool execution from the engine.
//
// Session lifecycle: ownership-based cleanup. Callers (engine/sub-engine)
// are responsible for calling CleanSession(sessionID) when they shut down.
// Close() releases all remaining sessions on process exit.
type REPLTool struct {
	sessions     sync.Map // sessionID → *Session
	toolExecutor func(ctx context.Context, name string, args json.RawMessage) (string, error)
}

// New creates a new REPLTool. Returns concrete type for SetToolExecutor injection.
// Not using BuildTool because post-construction injection is needed (like AgentTool).
func New() *REPLTool {
	return &REPLTool{}
}

// Close releases all remaining sessions. Call on process exit.
func (t *REPLTool) Close() {
	t.sessions.Range(func(key, value any) bool {
		value.(*Session).Close()
		t.sessions.Delete(key)
		return true
	})
}

// SetToolExecutor injects the tool execution function from main.go.
// The closure contains full three-phase permission checking:
//  1. permissionChecker.Check(name, args) → deny
//  2. permissionChecker.Check(name, args) → ask → askUser → TUI
//  3. checkContentPermissions → content-level rules
//
// Pattern mirrors AgentTool.SetFactory (injection to break circular dependency).
func (t *REPLTool) SetToolExecutor(fn func(ctx context.Context, name string, args json.RawMessage) (string, error)) {
	t.toolExecutor = fn
}

// Name returns the tool name.
func (t *REPLTool) Name() string { return "Repl" }

// Aliases returns tool aliases.
func (t *REPLTool) Aliases() []string { return nil }

// Description returns the tool description for API tool definitions and TUI display.
// nil/empty input → detailed description for LLM (API tool definition).
// Non-empty input → short snippet for TUI tool card.
func (t *REPLTool) Description(input json.RawMessage) (string, error) {
	if input == nil || string(input) == "null" || string(input) == "{}" {
		return replDescription, nil
	}
	var parsed replInput
	if err := json.Unmarshal(input, &parsed); err != nil {
		return "Execute JavaScript code", nil
	}
	switch parsed.Action {
	case "wait":
		return "Resume yielded REPL session", nil
	case "terminate":
		return "Terminate running REPL session", nil
	}
	if parsed.Code != "" {
		return parsed.Code, nil
	}
	return "Execute JavaScript code", nil
}

// InputSchema returns the JSON schema for REPL tool input.
func (t *REPLTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
	  "type": "object",
	  "properties": {
	    "code": {"type": "string", "description": "JavaScript code to execute"},
	    "reset": {"type": "boolean", "description": "Clear session state and VM"},
	    "action": {"type": "string", "enum": ["wait", "terminate"], "description": "Resume or terminate a yielded session"},
	    "session_id": {"type": "string", "description": "Session ID for wait/terminate actions"}
	  }
	}`)
}

// Call executes the REPL tool.
// Dispatches based on input action:
//   - no action + code → Execute JS
//   - action="wait" → resume yielded session
//   - action="terminate" → interrupt running session
//   - reset=true → clear session
func (t *REPLTool) Call(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	var parsed replInput
	if err := json.Unmarshal(input, &parsed); err != nil {
		return nil, fmt.Errorf("invalid REPL input: %w", err)
	}

	// Resolve session ID from input or ToolUseContext
	sessionID := parsed.SessionID
	if sessionID == "" && tctx != nil {
		sessionID = tctx.Options.SessionID
	}
	if sessionID == "" {
		sessionID = generateSessionID()
	}

	// Dispatch by action
	switch parsed.Action {
	case "wait":
		return t.handleWait(ctx, sessionID)
	case "terminate":
		return t.handleTerminate(sessionID)
	}

	// Reset action
	if parsed.Reset {
		return t.handleReset(sessionID)
	}

	// Normal execution
	if parsed.Code == "" {
		return nil, fmt.Errorf("REPL requires code input or an action")
	}

	return t.handleExecute(ctx, parsed.Code, sessionID, tctx)
}

// handleExecute runs JS code in a session.
func (t *REPLTool) handleExecute(ctx context.Context, code, sessionID string, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
		cleanCode, timeoutMs, err := parsePragma(code)
	if err != nil {
		return nil, err
	}

	// Get or create session
	sessionI, loaded := t.sessions.Load(sessionID)
	if !loaded {
		newSess, err := NewSession()
		if err != nil {
			return nil, fmt.Errorf("create session: %w", err)
		}
		t.sessions.Store(sessionID, newSess)
		sessionI = newSess
	}
	session := sessionI.(*Session)

	// Build tool adapter: toolExecutor(name, args) → string for toolFn field
	var toolFn func(ctx context.Context, name, argsJSON string) string
	if t.toolExecutor != nil {
		toolFn = func(ctx context.Context, name, argsJSON string) string {
			result, err := t.toolExecutor(ctx, name, json.RawMessage(argsJSON))
			if err != nil {
				return "ERROR: " + err.Error()
			}
			return result
		}
	}

	// Determine working directory — fall back to os.Getwd() if not set.
	// Mirrors Bash/Grep/Glob tools which all have the same fallback.
	cwd := ""
	if tctx != nil {
		cwd = tctx.WorkingDir
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	// Execute the code
	output, execErr := session.Execute(ctx, cleanCode, cwd, toolFn, timeoutMs)
	if execErr != nil {
		return nil, execErr
	}

	// Check if yield_control was triggered — output is in yieldCh
	select {
	case yielded := <-session.YieldCh():
		return &tool.ToolResult{
			Data: "YIELDED|" + sessionID + "|" + yielded,
		}, nil
	default:
	}

	return &tool.ToolResult{Data: output}, nil
}

// handleWait resumes a yielded session.
func (t *REPLTool) handleWait(ctx context.Context, sessionID string) (*tool.ToolResult, error) {
	sessionVal, ok := t.sessions.Load(sessionID)
	if !ok {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}
	session := sessionVal.(*Session)

	// Send resume signal
	select {
	case session.ResumeCh() <- struct{}{}:
	default:
		// Already has a pending resume
	}

	// Block for next yield output with timeout
	select {
	case yielded := <-session.YieldCh():
		return &tool.ToolResult{
			Data: "YIELDED|" + sessionID + "|" + yielded,
		}, nil
	case <-timeAfter(waitTimeout):
		return nil, fmt.Errorf("session %q wait timeout after %v", sessionID, waitTimeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// handleTerminate interrupts a running session.
// Issue 13: must call vm.Interrupt() AND Resume() (both required).
func (t *REPLTool) handleTerminate(sessionID string) (*tool.ToolResult, error) {
	sessionVal, ok := t.sessions.Load(sessionID)
	if !ok {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}
	session := sessionVal.(*Session)

	// Both Interrupt() and Resume() required to unblock yield_control
	session.Interrupt()
	select {
	case session.ResumeCh() <- struct{}{}:
	default:
	}

	return &tool.ToolResult{Data: "Session terminated"}, nil
}

// handleReset clears a session.
func (t *REPLTool) handleReset(sessionID string) (*tool.ToolResult, error) {
	sessionVal, ok := t.sessions.Load(sessionID)
	if !ok {
				return &tool.ToolResult{Data: "Session reset (new)"}, nil
	}
	session := sessionVal.(*Session)
	if err := session.Reset(); err != nil {
		return nil, fmt.Errorf("reset session: %w", err)
	}
	return &tool.ToolResult{Data: "Session reset"}, nil
}

// CheckPermissions returns PermissionAllowDecision.
// Permissions are handled by the injected toolExecutor (three-phase check).
func (t *REPLTool) CheckPermissions(input json.RawMessage, tctx *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}

func (t *REPLTool) IsReadOnly(input json.RawMessage) bool        { return false }
func (t *REPLTool) IsDestructive(input json.RawMessage) bool     { return false }
func (t *REPLTool) IsConcurrencySafe(input json.RawMessage) bool { return false }
func (t *REPLTool) IsEnabled() bool                              { return true }
func (t *REPLTool) InterruptBehavior() tool.InterruptBehavior    { return tool.InterruptCancel }
func (t *REPLTool) MaxResultSize() int                           { return 50000 }
func (t *REPLTool) Prompt() string                               { return toolPrompt }

// RenderResult formats the tool result for TUI display.
func (t *REPLTool) RenderResult(data any) string {
	if data == nil {
		return ""
	}
	s, ok := data.(string)
	if !ok {
		b, _ := json.Marshal(data)
		return string(b)
	}
	return s
}

// CleanSession removes a session from the map and closes it.
// Call this when the owning engine/sub-engine shuts down.
func (t *REPLTool) CleanSession(sessionID string) {
	if v, ok := t.sessions.LoadAndDelete(sessionID); ok {
		v.(*Session).Close()
	}
}

// generateSessionID creates a cryptographically random session ID.
func generateSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// timeAfter is a testable wrapper for time.After.
var timeAfter = time.After
