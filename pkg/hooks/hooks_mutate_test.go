package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type recPrompt struct {
	prompt, model string
	timeout       time.Duration
	ok            bool
	reason        string
	err           error
}

func (r *recPrompt) ExecutePromptHook(_ context.Context, prompt, model string, timeout time.Duration) (bool, string, error) {
	r.prompt, r.model, r.timeout = prompt, model, timeout
	return r.ok, r.reason, r.err
}

type recAgent struct {
	prompt, model string
	tools         []string
	maxTurns      int
	timeout       time.Duration
	ok            bool
	reason        string
	err           error
}

func (r *recAgent) ExecuteAgentHook(_ context.Context, prompt, model string, tools []string, maxTurns int, timeout time.Duration) (bool, string, error) {
	r.prompt, r.model, r.tools, r.maxTurns, r.timeout = prompt, model, tools, maxTurns, timeout
	return r.ok, r.reason, r.err
}

type recJS struct {
	source  string
	input   json.RawMessage
	timeout time.Duration
	out     string
	err     error
}

func (r *recJS) RunHook(_ context.Context, source string, input json.RawMessage, timeout time.Duration) (string, error) {
	r.source, r.input, r.timeout = source, input, timeout
	return r.out, r.err
}

func input() *HookInput {
	return &HookInput{HookEventName: string(HookPreToolUse), ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`)}
}

// Prompt hooks substitute $ARGUMENTS and default to inheriting the parent model.
func TestExecPromptHook_Contract(t *testing.T) {
	t.Parallel()
	pe := &recPrompt{ok: true}
	res := execPromptHook(context.Background(), pe, HookConfig{Prompt: "check $ARGUMENTS", Model: "zhipu/glm-5.3-flash"}, input(), 5*time.Second)
	if pe.prompt != `check {"command":"ls"}` {
		t.Errorf("prompt = %q, want $ARGUMENTS substituted", pe.prompt)
	}
	if pe.model != "zhipu/glm-5.3-flash" {
		t.Errorf("model = %q, want zhipu/glm-5.3-flash", pe.model)
	}
	if pe.timeout != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", pe.timeout)
	}
	if res.Outcome != HookOutcomeSuccess {
		t.Errorf("outcome = %v, want success", res.Outcome)
	}

	pe = &recPrompt{ok: true}
	res = execPromptHook(context.Background(), pe, HookConfig{Prompt: "p"}, input(), 0)
	if pe.model != "" {
		t.Errorf("default model = %q, want empty (inherit parent)", pe.model)
	}
	if res.Outcome != HookOutcomeSuccess {
		t.Errorf("outcome = %v, want success", res.Outcome)
	}

	pe = &recPrompt{ok: false, reason: "denied"}
	res = execPromptHook(context.Background(), pe, HookConfig{Prompt: "p"}, input(), 0)
	if res.Outcome != HookOutcomeBlocking || res.Stderr != "denied" {
		t.Errorf("blocking result = %+v, want blocking with stderr denied", res)
	}

	pe = &recPrompt{err: errors.New("llm down")}
	res = execPromptHook(context.Background(), pe, HookConfig{Prompt: "p"}, input(), 0)
	if res.Outcome != HookOutcomeNonBlockingError || res.Stderr != "llm down" {
		t.Errorf("error result = %+v, want non-blocking error with stderr llm down", res)
	}
}

// Agent hooks get the default turn budget and no tool allowlist.
func TestExecAgentHook_Contract(t *testing.T) {
	t.Parallel()
	ae := &recAgent{ok: true}
	res := execAgentHook(context.Background(), ae, HookConfig{Prompt: "run $ARGUMENTS"}, input(), 3*time.Second)
	if ae.prompt != `run {"command":"ls"}` {
		t.Errorf("prompt = %q, want $ARGUMENTS substituted", ae.prompt)
	}
	if ae.model != "" {
		t.Errorf("model = %q, want empty (inherit parent)", ae.model)
	}
	if ae.maxTurns != DefaultAgentMaxTurns {
		t.Errorf("maxTurns = %d, want %d", ae.maxTurns, DefaultAgentMaxTurns)
	}
	if ae.tools != nil {
		t.Errorf("tools = %v, want nil", ae.tools)
	}
	if res.Outcome != HookOutcomeSuccess {
		t.Errorf("outcome = %v, want success", res.Outcome)
	}

	ae = &recAgent{ok: false, reason: "nope"}
	res = execAgentHook(context.Background(), ae, HookConfig{Prompt: "p"}, input(), 0)
	if res.Outcome != HookOutcomeBlocking || res.Stderr != "nope" {
		t.Errorf("result = %+v, want blocking / nope", res)
	}

	ae = &recAgent{err: errors.New("boom")}
	res = execAgentHook(context.Background(), ae, HookConfig{Prompt: "p"}, input(), 0)
	if res.Outcome != HookOutcomeNonBlockingError || res.Stderr != "boom" {
		t.Errorf("result = %+v, want non-blocking error / boom", res)
	}
}

// JS hooks receive the same stdin JSON as command hooks and pass output through.
func TestExecJsHook_Contract(t *testing.T) {
	t.Parallel()
	jr := &recJS{out: "hook-output"}
	res := execJsHook(context.Background(), jr, HookConfig{Code: "async () => 1"}, input(), 2*time.Second)
	if jr.source != "async () => 1" {
		t.Errorf("source = %q, want the hook code", jr.source)
	}
	want := `{"hook_event_name":"PreToolUse","session_id":"","transcript_path":"","cwd":"","tool_name":"Bash","tool_input":{"command":"ls"}}`
	if string(jr.input) != want {
		t.Errorf("input = %s, want %s", jr.input, want)
	}
	if jr.timeout != 2*time.Second {
		t.Errorf("timeout = %v, want 2s", jr.timeout)
	}
	if res.Outcome != HookOutcomeSuccess || res.Stdout != "hook-output" {
		t.Errorf("result = %+v, want success with stdout hook-output", res)
	}

	jr = &recJS{err: errors.New("syntax error")}
	res = execJsHook(context.Background(), jr, HookConfig{Code: "x"}, input(), 0)
	if res.Outcome != HookOutcomeNonBlockingError || res.Stderr != "syntax error" {
		t.Errorf("result = %+v, want non-blocking error / syntax error", res)
	}
}

// Async hooks rewake the engine only when blocking and AsyncRewake is set.
func TestRunAsyncHook_Rewake(t *testing.T) {
	for _, c := range []struct {
		name       string
		cfg        HookConfig
		result     HookResult
		wantReason string
	}{
		{"blocking command", HookConfig{Type: HookTypeCommand, Command: "my-cmd", AsyncRewake: true},
			HookResult{Outcome: HookOutcomeBlocking, Stderr: "because"}, "because"},
		{"blocking without stderr", HookConfig{Type: HookTypeCommand, Command: "my-cmd", AsyncRewake: true},
			HookResult{Outcome: HookOutcomeBlocking}, "async hook blocked"},
		{"success no rewake", HookConfig{Type: HookTypeCommand, Command: "my-cmd", AsyncRewake: true},
			HookResult{Outcome: HookOutcomeSuccess}, ""},
		{"blocking without rewake flag", HookConfig{Type: HookTypeCommand, Command: "my-cmd"},
			HookResult{Outcome: HookOutcomeBlocking, Stderr: "x"}, ""},
	} {
		done := make(chan HookResult, 1)
		exec := &recExec{result: c.result, done: done}
		h := NewHooks(HooksConfig{}, exec)
		rewake := make(chan string, 1)
		h.OnRewake = func(reason string) { rewake <- reason }
		h.runAsyncHook(context.Background(), exec, nil, nil, c.cfg, input(), 0, "")
		<-done
		if c.wantReason == "" {
			// Give a spurious rewake a chance to land before asserting absence.
			select {
			case r := <-rewake:
				t.Errorf("%s: unexpected rewake %q", c.name, r)
			case <-time.After(150 * time.Millisecond):
			}
			continue
		}
		select {
		case reason := <-rewake:
			if reason != c.wantReason {
				t.Errorf("%s: rewake = %q, want %q", c.name, reason, c.wantReason)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("%s: OnRewake was not called", c.name)
		}
	}
}

type recExec struct {
	result HookResult
	done   chan HookResult
	env    []string
}

func (r *recExec) ExecuteHook(_ context.Context, _ string, _ *HookInput, _ time.Duration, extraEnv []string) HookResult {
	r.env = extraEnv
	r.done <- r.result
	return r.result
}

// Plugin hooks get GBOT_PLUGIN_ROOT in their environment.
func TestRunAsyncHook_PluginRootEnv(t *testing.T) {
	done := make(chan HookResult, 1)
	exec := &recExec{result: HookResult{Outcome: HookOutcomeSuccess}, done: done}
	h := NewHooks(HooksConfig{}, exec)
	h.runAsyncHook(context.Background(), exec, nil, nil,
		HookConfig{Type: HookTypeCommand, Command: "c"}, input(), 0, "/plugins/p1")
	<-done
	if len(exec.env) != 1 || exec.env[0] != "GBOT_PLUGIN_ROOT=/plugins/p1" {
		t.Errorf("extraEnv = %v, want [GBOT_PLUGIN_ROOT=/plugins/p1]", exec.env)
	}
}

// Prompt-type async hooks run through the prompt executor and rewake with the
// blocking reason.
func TestRunAsyncHook_PromptName(t *testing.T) {
	pe := &recPrompt{ok: false, reason: "blocked by prompt"}
	h := NewHooks(HooksConfig{}, nil)
	h.SetPromptExecutor(pe)
	rewake := make(chan string, 1)
	h.OnRewake = func(reason string) { rewake <- reason }
	h.runAsyncHook(context.Background(), nil, pe, nil,
		HookConfig{Type: HookTypePrompt, Prompt: "is this safe?", AsyncRewake: true}, input(), 0, "")
	select {
	case reason := <-rewake:
		if reason != "blocked by prompt" {
			t.Errorf("rewake reason = %q, want blocked by prompt", reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnRewake was not called")
	}
	if pe.prompt != "is this safe?" {
		t.Errorf("prompt = %q, want the hook prompt", pe.prompt)
	}
}
