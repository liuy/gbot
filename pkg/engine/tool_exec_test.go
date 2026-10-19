package engine

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// ctxKeyToolExec marks the caller context so the test can tell "the caller's
// ctx was threaded through" from "some context was passed".
type ctxKeyToolExec struct{}

// ctxRecordingTool captures the ToolUseContext it was invoked with, which is
// the only way to observe what ExecuteTool assembled for the tool.
type ctxRecordingTool struct {
	name string
	mu   sync.Mutex
	got  *tool.ToolUseContext
}

func (r *ctxRecordingTool) Name() string      { return r.name }
func (r *ctxRecordingTool) Aliases() []string { return nil }
func (r *ctxRecordingTool) Description(json.RawMessage) (string, error) {
	return r.name, nil
}
func (r *ctxRecordingTool) InputSchema() json.RawMessage { return json.RawMessage(`{}`) }
func (r *ctxRecordingTool) RenderResult(data any) string { return "rendered" }
func (r *ctxRecordingTool) Call(_ context.Context, _ json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	r.mu.Lock()
	r.got = tctx
	r.mu.Unlock()
	return &tool.ToolResult{Data: "ok"}, nil
}
func (r *ctxRecordingTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (r *ctxRecordingTool) IsReadOnly(json.RawMessage) bool        { return true }
func (r *ctxRecordingTool) IsDestructive(json.RawMessage) bool     { return false }
func (r *ctxRecordingTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (r *ctxRecordingTool) IsEnabled() bool                        { return true }
func (r *ctxRecordingTool) InterruptBehavior() tool.InterruptBehavior {
	return tool.InterruptCancel
}
func (r *ctxRecordingTool) MaxResultSize() int { return 1000 }
func (r *ctxRecordingTool) Prompt() string     { return "" }

func (r *ctxRecordingTool) captured() *tool.ToolUseContext {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.got
}

// fixedDecisionChecker returns one canned decision for every tool.
type fixedDecisionChecker struct{ decision permission.Decision }

func (c *fixedDecisionChecker) Check(string, json.RawMessage) permission.Decision { return c.decision }
func (c *fixedDecisionChecker) HasRules() bool                                    { return true }

// recordingDispatcher forwards every event to a channel and runs an optional
// inline hook (used to answer asks and to probe lock state mid-dispatch).
type recordingDispatcher struct {
	mu     sync.Mutex
	events []types.QueryEvent
	askCh  chan types.QueryEvent
	onAsk  func(types.QueryEvent)
}

func newRecordingDispatcher() *recordingDispatcher {
	return &recordingDispatcher{askCh: make(chan types.QueryEvent, 8)}
}

func (d *recordingDispatcher) Dispatch(evt types.QueryEvent) {
	d.mu.Lock()
	d.events = append(d.events, evt)
	hook := d.onAsk
	d.mu.Unlock()
	if evt.Type == types.EventAsk {
		if d.askCh != nil {
			d.askCh <- evt
		}
		if hook != nil {
			hook(evt)
		}
	}
}

func (d *recordingDispatcher) asked() []types.QueryEvent {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []types.QueryEvent
	for _, evt := range d.events {
		if evt.Type == types.EventAsk {
			out = append(out, evt)
		}
	}
	return out
}

// nextAsk waits for the next ask event; a missing ask is a test failure, not
// a hang.
func (d *recordingDispatcher) nextAsk(t *testing.T) types.QueryEvent {
	t.Helper()
	select {
	case evt := <-d.askCh:
		return evt
	case <-time.After(5 * time.Second):
		t.Fatal("no ask event was emitted")
		return types.QueryEvent{}
	}
}

func newToolExecEngine(rec *ctxRecordingTool, checker permission.PermissionChecker, disp *recordingDispatcher) *Engine {
	eng := &Engine{
		logger:             slog.Default(),
		permissionChecker:  checker,
		dispatcher:         disp,
		workingDir:         "/work/current",
		originalWorkingDir: "/work/original",
		sessionID:          "sess-42",
	}
	eng.tools = map[string]tool.Tool{rec.name: rec}
	return eng
}

// ExecuteTool is the REPL/hook entry point: the tool must see the caller's
// ctx, the session's tool map and id, uncapped output, and the cwd trio.
func TestToolExec_ToolUseContextFields(t *testing.T) {
	t.Parallel()
	rec := &ctxRecordingTool{name: "ctx_tool"}
	eng := newToolExecEngine(rec, nil, nil)
	ctx := context.WithValue(context.Background(), ctxKeyToolExec{}, "marker")

	if _, err := eng.ExecuteTool(ctx, "ctx_tool", json.RawMessage(`{"a":1}`), nil, nil); err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	got := rec.captured()
	if got == nil {
		t.Fatal("tool was never called")
	}
	if got.Ctx == nil || got.Ctx.Value(ctxKeyToolExec{}) != "marker" {
		t.Errorf("tctx.Ctx did not carry the caller context: %v", got.Ctx)
	}
	if got.Options.Tools["ctx_tool"] == nil {
		t.Errorf("tctx.Options.Tools = %v, want the session tool map", got.Options.Tools)
	}
	if got.Options.SessionID != "sess-42" {
		t.Errorf("tctx.Options.SessionID = %q, want %q", got.Options.SessionID, "sess-42")
	}
	if !got.UncappedOutput {
		t.Error("tctx.UncappedOutput = false, want true (REPL sub-calls get full output)")
	}
	if got.WorkingDir != "/work/current" {
		t.Errorf("tctx.WorkingDir = %q, want %q", got.WorkingDir, "/work/current")
	}
	if got.OriginalWorkingDir != "/work/original" {
		t.Errorf("tctx.OriginalWorkingDir = %q, want %q", got.OriginalWorkingDir, "/work/original")
	}
	if got.SetWorkingDir == nil {
		t.Fatal("tctx.SetWorkingDir = nil, want the engine's cwd setter")
	}
	got.SetWorkingDir("/work/moved")
	if eng.getWorkingDir() != "/work/moved" {
		t.Errorf("SetWorkingDir did not reach the engine: workingDir = %q", eng.getWorkingDir())
	}
}

// A deny decision carries the rule's own message; falling back to the generic
// "permission denied" hides which rule fired.
func TestToolExec_DenyUsesDecisionMessage(t *testing.T) {
	t.Parallel()
	rec := &ctxRecordingTool{name: "deny_tool"}
	eng := newToolExecEngine(rec, &fixedDecisionChecker{decision: permission.Decision{
		Action:  permission.ActionDeny,
		Message: "rm is blocked by project settings",
	}}, nil)

	_, err := eng.ExecuteTool(context.Background(), "deny_tool", json.RawMessage(`{}`), nil, nil)
	if err == nil {
		t.Fatal("deny decision must produce an error")
	}
	want := "deny_tool: rm is blocked by project settings"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if rec.captured() != nil {
		t.Error("denied tool must not run")
	}
}

// With no message on the rule the generic text is the contract.
func TestToolExec_DenyFallsBackToGenericMessage(t *testing.T) {
	t.Parallel()
	rec := &ctxRecordingTool{name: "deny_tool"}
	eng := newToolExecEngine(rec, &fixedDecisionChecker{decision: permission.Decision{
		Action: permission.ActionDeny,
	}}, nil)

	_, err := eng.ExecuteTool(context.Background(), "deny_tool", json.RawMessage(`{}`), nil, nil)
	if err == nil {
		t.Fatal("deny decision must produce an error")
	}
	if err.Error() != "deny_tool: permission denied" {
		t.Errorf("error = %q, want %q", err.Error(), "deny_tool: permission denied")
	}
}

// The ask event is the UI contract: kind, tool, input, message, rule detail
// and the reply channel all have to be populated.
func TestToolExec_AskEventFields(t *testing.T) {
	t.Parallel()
	rec := &ctxRecordingTool{name: "ask_tool"}
	disp := newRecordingDispatcher()
	ruleContent := "dangerous*"
	eng := newToolExecEngine(rec, &fixedDecisionChecker{decision: permission.Decision{
		Action:  permission.ActionAsk,
		Message: "let this run?",
		Rule: &permission.Rule{
			Value:  permission.RuleValue{ToolName: "ask_tool", RuleContent: &ruleContent},
			Action: permission.ActionAsk,
			Source: "project",
		},
	}}, disp)

	resultCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		r, e := eng.ExecuteTool(context.Background(), "ask_tool", json.RawMessage(`{"q":1}`), map[string]bool{}, &sync.Mutex{})
		if e != nil {
			errCh <- e
			return
		}
		resultCh <- r
	}()

	ask := disp.nextAsk(t).Ask
	if ask.Kind != types.AskPermission {
		t.Errorf("ask.Kind = %v, want %v", ask.Kind, types.AskPermission)
	}
	if ask.ToolName != "ask_tool" {
		t.Errorf("ask.ToolName = %q, want %q", ask.ToolName, "ask_tool")
	}
	if string(ask.Input) != `{"q":1}` {
		t.Errorf("ask.Input = %s, want %s", ask.Input, `{"q":1}`)
	}
	if ask.Message != "let this run?" {
		t.Errorf("ask.Message = %q, want %q", ask.Message, "let this run?")
	}
	if ask.RuleDetail != "ask_tool(dangerous*) from project settings" {
		t.Errorf("ask.RuleDetail = %q, want %q", ask.RuleDetail, "ask_tool(dangerous*) from project settings")
	}
	if ask.ResponseCh == nil {
		t.Fatal("ask.ResponseCh = nil")
	}
	ask.ResponseCh <- types.AskResponse{Decision: types.DecisionAllow}

	select {
	case r := <-resultCh:
		if r != "rendered" {
			t.Errorf("result = %q, want %q (RenderResult output)", r, "rendered")
		}
	case e := <-errCh:
		t.Fatalf("ExecuteTool after allow: %v", e)
	case <-time.After(5 * time.Second):
		t.Fatal("ExecuteTool did not return after the ask was answered")
	}
}

// ruleDetail names the rule that fired: tool name, optional content pattern,
// and the settings source. Dropping any part makes the prompt unattributable.
func TestToolExec_AskRuleDetail(t *testing.T) {
	t.Parallel()
	ruleContent := "rm -rf*"
	cases := []struct {
		name string
		rule *permission.Rule
		want string
	}{
		{
			name: "content rule",
			rule: &permission.Rule{Value: permission.RuleValue{ToolName: "ask_tool", RuleContent: &ruleContent}, Source: "user"},
			want: "ask_tool(rm -rf*) from user settings",
		},
		{
			name: "bare tool rule",
			rule: &permission.Rule{Value: permission.RuleValue{ToolName: "ask_tool"}, Source: "local"},
			want: "ask_tool from local settings",
		},
		{name: "no rule", rule: nil, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &ctxRecordingTool{name: "ask_tool"}
			disp := newRecordingDispatcher()
			eng := newToolExecEngine(rec, &fixedDecisionChecker{decision: permission.Decision{
				Action: permission.ActionAsk,
				Rule:   tc.rule,
			}}, disp)

			errCh := make(chan error, 1)
			go func() {
				_, e := eng.ExecuteTool(context.Background(), "ask_tool", json.RawMessage(`{"q":1}`), map[string]bool{}, &sync.Mutex{})
				errCh <- e
			}()
			ask := disp.nextAsk(t).Ask
			if ask.RuleDetail != tc.want {
				t.Errorf("RuleDetail = %q, want %q", ask.RuleDetail, tc.want)
			}
			ask.ResponseCh <- types.AskResponse{Decision: types.DecisionAllow}
			select {
			case e := <-errCh:
				if e != nil {
					t.Fatalf("ExecuteTool: %v", e)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("ExecuteTool did not return")
			}
		})
	}
}

// A cached session allow must short-circuit before the ask is emitted; an
// empty (but non-nil) cache must not.
func TestToolExec_SessionCacheShortCircuit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		allowed   map[string]bool
		wantAsked bool
	}{
		{name: "cached allow skips the ask", allowed: map[string]bool{"ask_tool": true}, wantAsked: false},
		{name: "empty cache still asks", allowed: map[string]bool{}, wantAsked: true},
		{name: "nil cache still asks", allowed: nil, wantAsked: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &ctxRecordingTool{name: "ask_tool"}
			disp := newRecordingDispatcher()
			eng := newToolExecEngine(rec, &fixedDecisionChecker{decision: permission.Decision{
				Action: permission.ActionAsk,
			}}, disp)

			errCh := make(chan error, 1)
			go func() {
				_, e := eng.ExecuteTool(context.Background(), "ask_tool", json.RawMessage(`{"q":1}`), tc.allowed, &sync.Mutex{})
				errCh <- e
			}()
			if tc.wantAsked {
				ask := disp.nextAsk(t).Ask
				ask.ResponseCh <- types.AskResponse{Decision: types.DecisionAllow}
			}
			select {
			case e := <-errCh:
				if e != nil {
					t.Fatalf("ExecuteTool: %v", e)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("ExecuteTool did not return")
			}
			if n := len(disp.asked()); n != bool2int(tc.wantAsked) {
				t.Errorf("ask events = %d, want %d", n, bool2int(tc.wantAsked))
			}
		})
	}
}

func bool2int(b bool) int {
	if b {
		return 1
	}
	return 0
}

// askMu serialises asks: it must still be held while the ask event is being
// dispatched, otherwise two concurrent callers both prompt the user.
func TestToolExec_AskMuHeldDuringAsk(t *testing.T) {
	t.Parallel()
	rec := &ctxRecordingTool{name: "ask_tool"}
	askMu := &sync.Mutex{}
	held := make(chan bool, 1)
	disp := newRecordingDispatcher()
	disp.onAsk = func(evt types.QueryEvent) {
		if askMu.TryLock() {
			askMu.Unlock()
			held <- false
		} else {
			held <- true
		}
		evt.Ask.ResponseCh <- types.AskResponse{Decision: types.DecisionAllow}
	}
	eng := newToolExecEngine(rec, &fixedDecisionChecker{decision: permission.Decision{
		Action: permission.ActionAsk,
	}}, disp)

	if _, err := eng.ExecuteTool(context.Background(), "ask_tool", json.RawMessage(`{}`), map[string]bool{}, askMu); err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	select {
	case got := <-held:
		if !got {
			t.Error("askMu was not held while the ask event was dispatched")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ask hook never ran")
	}
}

// "Always allow" with no session cache must not write into a nil map.
func TestToolExec_AllowAlwaysWithNilCache(t *testing.T) {
	t.Parallel()
	rec := &ctxRecordingTool{name: "ask_tool"}
	disp := newRecordingDispatcher()
	eng := newToolExecEngine(rec, &fixedDecisionChecker{decision: permission.Decision{
		Action: permission.ActionAsk,
	}}, disp)

	errCh := make(chan error, 1)
	go func() {
		_, e := eng.ExecuteTool(context.Background(), "ask_tool", json.RawMessage(`{}`), nil, &sync.Mutex{})
		errCh <- e
	}()
	ask := disp.nextAsk(t).Ask
	ask.ResponseCh <- types.AskResponse{Decision: types.DecisionAllowAlways}

	select {
	case e := <-errCh:
		if e != nil {
			t.Fatalf("ExecuteTool with nil session cache: %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExecuteTool did not return")
	}
	if got := rec.captured(); got == nil {
		t.Error("tool did not run after an always-allow")
	}
}

// The pre-lock cache check is a fast path: a tool already allowed this session
// must run even while another tool's ask is parked on askMu. Without it, an
// open prompt serialises every already-allowed call behind it.
func TestToolExec_CachedAllowDoesNotTakeAskMutex(t *testing.T) {
	t.Parallel()
	rec := &ctxRecordingTool{name: "ask_tool"}
	askMu := &sync.Mutex{}
	askMu.Lock()
	t.Cleanup(func() { askMu.Unlock() })
	eng := newToolExecEngine(rec, &fixedDecisionChecker{decision: permission.Decision{
		Action: permission.ActionAsk,
	}}, newRecordingDispatcher())

	done := make(chan error, 1)
	go func() {
		_, e := eng.ExecuteTool(context.Background(), "ask_tool", json.RawMessage(`{}`),
			map[string]bool{"ask_tool": true}, askMu)
		done <- e
	}()
	select {
	case e := <-done:
		if e != nil {
			t.Fatalf("cached allow failed: %v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cached allow blocked on askMu instead of short-circuiting before it")
	}
}
