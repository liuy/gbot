package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// Source: AgentTool.tsx:100 — cwd: z.string().optional().describe(...)
const cwdDescription = "Absolute path to run the agent in. Overrides the working directory for all filesystem and shell operations within this agent."

func TestInputSchema_CwdProperty(t *testing.T) {
	at := New()
	var schema struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(at.InputSchema(), &schema); err != nil {
		t.Fatalf("unmarshal InputSchema: %v", err)
	}
	cwd, ok := schema.Properties["cwd"]
	if !ok {
		t.Fatalf("schema properties missing \"cwd\": %v", schema.Properties)
	}
	if cwd.Type != "string" {
		t.Errorf("cwd type = %q, want string", cwd.Type)
	}
	if cwd.Description != cwdDescription {
		t.Errorf("cwd description = %q, want %q", cwd.Description, cwdDescription)
	}
	// Optional: a cwd-less call must stay valid.
	for _, r := range schema.Required {
		if r == "cwd" {
			t.Error("cwd must be optional, but it is listed as required")
		}
	}
}

// The tool layer only parses and forwards; the engine owns the override.
// This pins the hand-off so a dropped field cannot silently disable cwd.
func TestCall_PassesCwdToRunAgent(t *testing.T) {
	var captured AgentOpts
	at := New()
	at.SetEngine(&mockSubEngine{runFn: func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		captured = opts
		return &types.SubQueryResult{Content: "done"}, nil
	}})

	input := json.RawMessage(`{"description":"work elsewhere","prompt":"do it","subagent_type":"General","cwd":"/tmp/agent-cwd-9x"}`)
	if _, err := at.Call(context.Background(), input, nil); err != nil {
		t.Fatalf("Call with cwd input returned error: %v", err)
	}
	if captured.Cwd != "/tmp/agent-cwd-9x" {
		t.Errorf("AgentOpts.Cwd = %q, want %q", captured.Cwd, "/tmp/agent-cwd-9x")
	}
}

// TS wraps the cwd override around the whole runAgent call, fork included.
func TestCall_ForkPassesCwdToRunAgent(t *testing.T) {
	var captured AgentOpts
	at := New()
	at.SetBgRegistry(NewBackgroundAgentRegistry())
	at.SetNotifyFn(func(string) {}, func() string { return "" })
	at.SetEngine(&mockSubEngine{runFn: func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		captured = opts
		return &types.SubQueryResult{Content: "done"}, nil
	}})

	input := json.RawMessage(`{"description":"fork elsewhere","prompt":"do it","fork":true,"cwd":"/tmp/fork-cwd-4y"}`)
	if _, err := at.Call(context.Background(), input, nil); err != nil {
		t.Fatalf("Call with fork+cwd input returned error: %v", err)
	}
	if captured.Cwd != "/tmp/fork-cwd-4y" {
		t.Errorf("fork AgentOpts.Cwd = %q, want %q", captured.Cwd, "/tmp/fork-cwd-4y")
	}
}

// No cwd in the input means no override reaches the engine.
func TestCall_NoCwd_LeavesOverrideEmpty(t *testing.T) {
	var captured AgentOpts
	at := New()
	at.SetEngine(&mockSubEngine{runFn: func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		captured = opts
		return &types.SubQueryResult{Content: "done"}, nil
	}})

	input := json.RawMessage(`{"description":"default dir","prompt":"do it","subagent_type":"General"}`)
	if _, err := at.Call(context.Background(), input, nil); err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if captured.Cwd != "" {
		t.Errorf("AgentOpts.Cwd = %q, want empty", captured.Cwd)
	}
}
