package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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

// TestCall_BackgroundPassesCwdToRunAgent pins the background half of the hand-off.
// Call builds one AgentOpts literal for both the sync and the background path, so
// the spawn is field-identical to the sync call — but the background route goes
// through BackgroundAgentRegistry.Spawn, whose goroutine mutates opts before
// RunAgent, a second chance to drop the override. Only the engine is mocked here;
// the registry, its goroutine and the completion notification are real.
func TestCall_BackgroundPassesCwdToRunAgent(t *testing.T) {
	captured := make(chan AgentOpts, 1)
	done := make(chan struct{})
	at := New()
	at.SetBgRegistry(NewBackgroundAgentRegistry())
	at.SetNotifyFn(func(string) { close(done) }, func() string { return "" })
	at.SetEngine(&mockSubEngine{runFn: func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error) {
		captured <- opts
		return &types.SubQueryResult{AgentType: opts.AgentType, Content: "ok"}, nil
	}})

	input := json.RawMessage(`{"description":"bg elsewhere","prompt":"do it","subagent_type":"General","run_in_background":true,"cwd":"/tmp/bg-cwd-7z"}`)
	result, err := at.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call with run_in_background+cwd returned error: %v", err)
	}
	sqr, ok := result.Data.(*types.SubQueryResult)
	if !ok {
		t.Fatalf("result Data = %T, want *types.SubQueryResult", result.Data)
	}
	if !sqr.AsyncLaunched {
		t.Error("AsyncLaunched = false, want true for a background spawn")
	}
	if sqr.AgentID == "" {
		t.Error("AgentID is empty, want the registry-minted id")
	}

	var got AgentOpts
	select {
	case got = <-captured:
	case <-time.After(5 * time.Second):
		t.Fatal("background RunAgent never reached the engine within 5s")
	}
	if got.Cwd != "/tmp/bg-cwd-7z" {
		t.Errorf("background AgentOpts.Cwd = %q, want %q", got.Cwd, "/tmp/bg-cwd-7z")
	}

	// Wait for the spawn goroutine to finish: this package's TestMain runs
	// goleak, so an in-flight goroutine would outlive the test.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("background completion notification never fired within 5s")
	}
}
