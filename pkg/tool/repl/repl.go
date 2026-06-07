package repl

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// REPLTool — concrete struct implementing tool.Tool (like AgentTool).

// replInput is the JSON input schema for the REPL tool.
type replInput struct {
	Code      string `json:"code"`
	Reset     bool   `json:"reset"`
	SessionID string `json:"session_id"`
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
	    "session_id": {"type": "string", "description": "Session ID to reuse or target for reset"}
	  }
	}`)
}

// Call executes the REPL tool.
// Dispatches: reset=true → clear session, otherwise execute JS code.
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

	// Reset action
	if parsed.Reset {
		return t.handleReset(sessionID)
	}

	// Normal execution
	if parsed.Code == "" {
		return nil, fmt.Errorf("REPL requires code input")
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

	return &tool.ToolResult{Data: output}, nil
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

func (t *REPLTool) NewResultType() any { return nil } // REPL returns string data

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

