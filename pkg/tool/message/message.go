// Package message implements the message tool — inter-agent text delivery
// through each target engine's attachment queue. Delivery itself reuses the
// existing queued-message machinery (busy: turn-boundary insert, idle:
// auto-turn), so this package only routes an id to a deliverable target.
package message

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Routing contracts (implemented engine-side; no engine import here)
// ---------------------------------------------------------------------------

// Target is a deliverable engine handle.
type Target interface {
	EnqueueAttachment(item types.QueuedItem)
}

// Resolver maps a flat id to a deliverable target.
type Resolver interface {
	// Resolve maps a flat id to a deliverable target.
	// ok=false                            → unknown id
	// status ""                           → engine target, always deliverable
	// status "running"                    → background agent, sink attached, deliverable
	// status "starting"                   → running but sub-engine not attached yet
	// status "completed|failed|cancelled" → known but not deliverable (target nil)
	Resolve(id string) (target Target, status string, ok bool)
}

// ---------------------------------------------------------------------------
// MessageTool
// ---------------------------------------------------------------------------

// MessageTool sends a text message to another agent or engine.
type MessageTool struct {
	// Tool is the built scaffold — embedded so *MessageTool satisfies
	// tool.Tool directly, the same call surface the wiring and tests use.
	tool.Tool
	resolver Resolver
	selfID   string
}

// New creates the message tool. The resolver is required for delivery; nil
// only yields "unknown target" for every address.
func New(resolver Resolver) *MessageTool {
	t := &MessageTool{resolver: resolver}
	t.Tool = tool.BuildTool(t.def())
	return t
}

// SetSelfID records this tool's own flat id, used for self-send rejection and
// the envelope's from= header. Injected by WireEngine.
func (t *MessageTool) SetSelfID(id string) { t.selfID = id }

// SelfID returns the id injected by SetSelfID. Exported because cross-package
// wiring tests (package engine) cannot read the unexported field.
func (t *MessageTool) SelfID() string { return t.selfID }

// FormatWireBlocks re-exposes the built scaffold's wire rendering: the
// embedded tool.Tool interface hides it, but callers (and the engine's wire
// path) need *MessageTool to satisfy ToolWithWireBlocks.
func (t *MessageTool) FormatWireBlocks(data any) []types.ContentBlock {
	return t.Tool.(tool.ToolWithWireBlocks).FormatWireBlocks(data)
}

// messageInput is the input schema: exactly to + message.
type messageInput struct {
	To      string `json:"to"`
	Message string `json:"message"`
}

var messageSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "to": {
      "type": "string",
      "description": "Target ID — background agent ID (agent-3, fork-2) or engine ID (main, e2) from the Job tool's list output"
    },
    "message": {
      "type": "string",
      "description": "Text to deliver to the target"
    }
  },
  "required": ["to", "message"]
}`)

// New builds the message tool on the shared BuildTool scaffold, like the Job tool.
func (t *MessageTool) def() tool.ToolDef {
	return tool.ToolDef{
		Name_:        "Message",
		Aliases_:     nil,
		InputSchema_: func() json.RawMessage { return messageSchema },
		Description_: func(input json.RawMessage) (string, error) {
			var in messageInput
			if err := json.Unmarshal(input, &in); err != nil || strings.TrimSpace(in.To) == "" {
				return "Send a text message to a running agent or engine (target IDs from the Job tool's list output).", nil
			}
			return "-> " + strings.TrimSpace(in.To), nil
		},
		RenderResult_: func(data any) string {
			if r, ok := data.(MessageResult); ok {
				return r.Message
			}
			if s, ok := data.(string); ok {
				return s
			}
			b, _ := json.Marshal(data)
			return string(b)
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return t.call(input)
		},
		IsConcurrencySafe_: func(json.RawMessage) bool { return true },
		ShouldDefer_:       false,
		MaxResultSizeChars: 10000,
		Prompt_:            messagePromptText,
		FormatWireBlocks_: func(data any) []types.ContentBlock {
			if r, ok := data.(MessageResult); ok {
				return []types.ContentBlock{types.NewTextBlock("Delivered to " + r.To)}
			}
			if s, ok := data.(string); ok {
				return []types.ContentBlock{types.NewTextBlock(s)}
			}
			b, _ := json.Marshal(data)
			return []types.ContentBlock{types.NewTextBlock(string(b))}
		},
	}
}

// messagePromptText documents mechanics only — sub-engines never receive tool
// Prompt_ text, so the Description_ carries what a child minimally needs.
const messagePromptText = `Send a text message to another agent. Delivery lands in the target's conversation:
immediately if it is idle, otherwise at its next turn boundary.

Use the Job tool with action "list" to see running agents (ID and description). Addresses are
flat IDs: background agent IDs (agent-3, fork-2) or engine IDs (main, e2).

Examples:
{"to": "agent-3", "message": "Found it: the config lives at config/app.yaml"}
{"to": "e2", "message": "Please summarize your progress so far."}`

func (t *MessageTool) call(input json.RawMessage) (*tool.ToolResult, error) {
	var in messageInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	to := strings.TrimSpace(in.To)
	if to == "" {
		return nil, fmt.Errorf("to is required")
	}
	if strings.TrimSpace(in.Message) == "" {
		return nil, fmt.Errorf("message must not be empty")
	}
	if to == t.selfID {
		return nil, fmt.Errorf("cannot send a message to yourself (%s)", to)
	}

	target, status, ok := t.resolver.Resolve(to)
	if !ok {
		return nil, fmt.Errorf("unknown target %q — run the Job tool with action \"list\" to see running agents", to)
	}
	switch status {
	case "":
		// Engine target — always deliverable.
	case "running":
		// Deliverable; target must be non-nil.
	case "starting":
		return nil, fmt.Errorf("agent %s is still starting — try again shortly", to)
	default:
		return nil, fmt.Errorf("agent %s is not running (status: %s)", to, status)
	}
	if target == nil {
		return nil, fmt.Errorf("agent %s is not deliverable (status: %s)", to, status)
	}

	target.EnqueueAttachment(types.QueuedItem{
		Value:     buildEnvelope(t.selfID, in.Message),
		Mode:      types.ItemModeJob,
		Timestamp: time.Now(),
	})
	return &tool.ToolResult{Data: MessageResult{To: to, Message: in.Message}}, nil
}

// MessageResult carries the sent content so the TUI card can display it;
// the wire text stays the short "Delivered to X" confirmation.
type MessageResult struct {
	To      string
	Message string
}

// buildEnvelope wraps the body so the receiver sees the sender and can reply
// to its id. Bodies are XML-escaped so hostile text cannot break the tag.
func buildEnvelope(from, body string) string {
	return fmt.Sprintf("<message from=%q>\n%s\n</message>", xmlEscape(from), xmlEscape(body))
}

func xmlEscape(s string) string {
	var buf strings.Builder
	// Error is unreachable: strings.Builder writes never fail.
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}
