package repl

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/fileread"
	"github.com/liuy/gbot/pkg/types"
)

// replResult is the structured output of one Execute: captured console output
// plus image evidence attached via image(). Unexported on purpose — it only
// crosses the wire through FormatWireBlocks/DecodeResult, never as raw JSON.
type replResult struct {
	Text   string               `json:"text"`
	Images []types.ContentBlock `json:"images,omitempty"`
}

// REPLTool — concrete struct implementing tool.Tool (like AgentTool).

// replInput is the JSON input schema for the REPL tool.
type replInput struct {
	Code      string `json:"code"`
	Reset     bool   `json:"reset"`
	SessionID string `json:"session_id"`
}

// REPLTool implements tool.Tool for JavaScript REPL execution.
// Uses SetToolExecutor injection (like AgentTool's SetEngine) to get full
// permission-checked tool execution from the engine.
//
// Session lifecycle: ownership-based cleanup. Callers (engine/sub-engine)
// are responsible for calling CleanSession(sessionID) when they shut down.
// Close() releases all remaining sessions on process exit.
type REPLTool struct {
	sessions     sync.Map // sessionID → *Session
	toolExecutor func(ctx context.Context, name string, args json.RawMessage) (string, error)
	toolLister   func() []ToolMeta
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
// Pattern mirrors AgentTool.SetEngine (injection to break circular dependency).
func (t *REPLTool) SetToolExecutor(fn func(ctx context.Context, name string, args json.RawMessage) (string, error)) {
	t.toolExecutor = fn
}

// SetToolLister injects the fresh tool inventory provider from bootstrap.
// The lister is invoked on every Execute (not snapshotted) so MCP tools that
// connect/disconnect mid-session appear and disappear in the JS tools object.
func (t *REPLTool) SetToolLister(fn func() []ToolMeta) {
	t.toolLister = fn
}

// replScripts are plugin-supplied harness sources preloaded into every new
// session (set once at startup from LoadedPlugins; engine-agnostic because
// sessions are created lazily inside whatever engine runs the tool).
var replScripts []ReplScript

// ReplScript is one preloaded plugin JS file. Same shape as
// plugins.ReplScript (duplicated here to avoid an import cycle; bootstrap
// converts between them).
type ReplScript struct {
	Name   string
	Source string
}

// SetReplScripts sets the harness scripts evaluated into new sessions.
// Call once at startup; later calls only affect sessions created afterwards.
func SetReplScripts(scripts []ReplScript) {
	replScripts = scripts
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

	// Build tool adapter: errors propagate to JS as throws — no string prefix.
	var toolFn ToolCallFn
	if t.toolExecutor != nil {
		toolFn = func(ctx context.Context, name, argsJSON string) (string, error) {
			return t.toolExecutor(ctx, name, json.RawMessage(argsJSON))
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

	// Execute the code — the lister rides along so the session installs a
	// fresh tools inventory under its own lock (per-Execute state must not
	// be written from outside it).
	output, evidence, execErr := session.Execute(ctx, cleanCode, cwd, toolFn, timeoutMs, t.toolLister)
	if execErr != nil {
		return nil, execErr
	}

	res := replResult{Text: output}

	// Attach image evidence collected via image(). Cap enforced at read time:
	// each image rides the tool_result wire, so extra calls are dropped with
	// a warning instead of silently flooding the context.
	if n := len(evidence); n > maxImagesPerExecute {
		res.Text += fmt.Sprintf("\n[image] limit reached: kept first %d of %d", maxImagesPerExecute, n)
	}
	for _, p := range evidence {
		if len(res.Images) == maxImagesPerExecute {
			break
		}
		block, ok := fileread.ReadAsImageBlock(ctx, p)
		if !ok {
			res.Text += "\n[image] unreadable: " + p
			continue
		}
		res.Images = append(res.Images, block)
	}

	return &tool.ToolResult{Data: res}, nil
}

// handleReset clears a session.
func (t *REPLTool) handleReset(sessionID string) (*tool.ToolResult, error) {
	sessionVal, ok := t.sessions.Load(sessionID)
	if !ok {
		return &tool.ToolResult{Data: replResult{Text: "Session reset (new)"}}, nil
	}
	session := sessionVal.(*Session)
	if err := session.Reset(); err != nil {
		return nil, fmt.Errorf("reset session: %w", err)
	}
	return &tool.ToolResult{Data: replResult{Text: "Session reset"}}, nil
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
	switch v := data.(type) {
	case string:
		return v
	case replResult:
		out := v.Text
		if len(v.Images) > 0 {
			// TUI cards are text-only; the images themselves travel on the
			// wire, so the card just shows a placeholder count line.
			out += fmt.Sprintf("\n📎 %d images", len(v.Images))
		}
		return out
	default:
		b, _ := json.Marshal(data)
		return string(b)
	}
}

// FormatWireBlocks sends the console output as one text block followed by one
// image block per evidence image. The string path preserves the pre-evidence
// wire shape (TS REPLTool has no mapToolResult override — raw output is the wire).
func (t *REPLTool) FormatWireBlocks(data any) []types.ContentBlock {
	switch v := data.(type) {
	case replResult:
		blocks := make([]types.ContentBlock, 0, 1+len(v.Images))
		blocks = append(blocks, types.NewTextBlock(v.Text))
		blocks = append(blocks, v.Images...)
		return blocks
	case string:
		return []types.ContentBlock{types.NewTextBlock(v)}
	default:
		raw, _ := json.Marshal(data)
		return []types.ContentBlock{types.NewTextBlock(string(raw))}
	}
}

func (t *REPLTool) DecodeResult(raw json.RawMessage) (any, error) {
	if len(raw) == 0 || raw[0] != '[' {
		preview := string(raw)
		if runes := []rune(preview); len(runes) > 80 {
			preview = string(runes[:80])
		}
		return nil, fmt.Errorf("repl: DecodeResult expects array-form content, got %q", preview)
	}
	var blocks []types.ContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}
	var text string
	var images []types.ContentBlock
	haveText := false
	for _, b := range blocks {
		switch b.Type {
		case types.ContentTypeText:
			// First text block wins; later ones are duplicates from old wires.
			if !haveText {
				text, haveText = b.Text, true
			}
		case types.ContentTypeImage:
			if b.Source != nil {
				images = append(images, b)
			}
		}
	}
	if !haveText {
		return nil, fmt.Errorf("repl: DecodeResult found no text block in array")
	}
	if len(images) == 0 {
		// Wire history: pre-plaintext sessions stored json.Marshal(string), so
		// the wire text is itself a JSON string literal — unwrap once more for
		// those. New wires carry the raw output; a raw output that happens to be
		// a valid JSON string literal loses one layer of quotes (accepted
		// ambiguity, same tradeoff as Lsp).
		var s string
		if json.Unmarshal([]byte(text), &s) == nil {
			return s, nil
		}
		return text, nil
	}
	return replResult{Text: text, Images: images}, nil
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
