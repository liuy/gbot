package bash

// Mutation-kill tests for bash.go, batch 2 (go-mutesting escapes: arithmetic,
// branch, composite, conditional, expression, loop, numbers mutators on the
// first seven mutator groups). Each assertion is chosen so the specific
// mutation flips an asserted value; equivalence arguments for the remaining
// escapes are recorded in the batch report, not here.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// withFreshRegistry swaps the global default registry for an isolated one so
// job IDs never collide with tests that use the real default.
func withFreshRegistry(t *testing.T) *BackgroundJobRegistry {
	t.Helper()
	orig := defaultRegistry
	fresh := NewBackgroundJobRegistry()
	defaultRegistry = fresh
	t.Cleanup(func() { defaultRegistry = orig })
	return fresh
}

func forceNonPTY(t *testing.T) {
	t.Helper()
	orig := ptySupported
	ptySupported = false
	t.Cleanup(func() { ptySupported = orig })
}

// askRecorder captures what the PTY session asked and answers "secret".
type askRecorder struct {
	mu       sync.Mutex
	calls    int
	masked   bool
	deadline time.Time
	at       time.Time
}

func (r *askRecorder) onAskInput(prompt string, masked bool, deadline time.Time) chan types.AskResponse {
	r.mu.Lock()
	r.calls++
	r.masked = masked
	r.deadline = deadline
	r.at = time.Now() // REAL-TIME: wall-clock delta between ask and deadline is the assertion
	r.mu.Unlock()
	ch := make(chan types.AskResponse, 1)
	ch <- types.AskResponse{Text: "secret"}
	return ch
}

func (r *askRecorder) snapshot() (calls int, masked bool, delta time.Duration, ahead bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.calls == 0 {
		return 0, false, 0, false
	}
	return r.calls, r.masked, r.deadline.Sub(r.at), r.deadline.After(r.at)
}

// waitJobDone waits for a job to reach a terminal state, failing the test on
// timeout instead of hanging the whole suite.
func waitJobDone(t *testing.T, reg *BackgroundJobRegistry, id string) {
	t.Helper()
	job, found := reg.Get(id)
	if !found {
		t.Fatalf("job %q not found in registry", id)
	}
	select {
	case <-job.done:
	case <-time.After(8 * time.Second):
		t.Fatalf("job %q did not finish within 8s", id)
	}
}

// ---------------------------------------------------------------------------
// Tool wiring (New / ToolDef fields)
// ---------------------------------------------------------------------------

// Foreground Call must forward the caller's context: a nil ctx panics inside
// context.WithDeadline at the PTY/non-PTY run layer.
func TestBashToolCall_ForegroundForwardsContext(t *testing.T) {
	tt := New(nil)
	res, err := tt.Call(context.Background(), json.RawMessage(`{"command":"echo call-fg"}`), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	out, ok := res.Data.(*Output)
	if !ok {
		t.Fatalf("Data = %T, want *Output", res.Data)
	}
	if out.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.ExitCode)
	}
	if !strings.Contains(out.Stdout, "call-fg") {
		t.Errorf("Stdout = %q, want call-fg", out.Stdout)
	}
}

// New(nil) must fall back to the default registry; otherwise the first
// background spawn dereferences a nil registry.
func TestBashToolCall_NilRegistryBackgroundSpawn(t *testing.T) {
	reg := withFreshRegistry(t)
	tt := New(nil)
	res, err := tt.Call(context.Background(), json.RawMessage(`{"command":"echo bg-nilreg","run_in_background":true}`), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	out, ok := res.Data.(*Output)
	if !ok {
		t.Fatalf("Data = %T, want *Output", res.Data)
	}
	id := extractBgID(out.Stdout)
	if id == "" {
		t.Fatalf("no job id in %q", out.Stdout)
	}
	waitJobDone(t, reg, id)
	job, _ := reg.Get(id)
	if job.ExitCode != 0 {
		t.Errorf("job ExitCode = %d, want 0", job.ExitCode)
	}
	reg.Remove(id)
}

// A JSON input whose command parses but a later field has a type error still
// yields Command in the partial struct — the unmarshal-error branch must keep
// returning the safe default (false), not classify the half-parsed command.
func TestBashTool_IsReadOnly_TypeErrorJSONStaysUnsafe(t *testing.T) {
	t.Parallel()
	tt := New(nil)
	// "timeout":"x" fails to unmarshal AFTER command was already decoded.
	got := tt.IsReadOnly(json.RawMessage(`{"command":"ls -la","timeout":"x"}`))
	if got {
		t.Error("IsReadOnly(partially-invalid JSON) = true, want false")
	}
}

func TestBashTool_MaxResultSizeIs30k(t *testing.T) {
	t.Parallel()
	tt := New(nil)
	if got := tt.MaxResultSize(); got != 30000 {
		t.Errorf("MaxResultSize() = %d, want 30000", got)
	}
}

// Dropping IsSearchOrRead_ from the ToolDef removes the ToolWithSearchOrRead
// interface implementation entirely — the type assertion must still hold.
func TestBashTool_ImplementsSearchOrRead(t *testing.T) {
	t.Parallel()
	tt := New(nil)
	sr, ok := tt.(tool.ToolWithSearchOrRead)
	if !ok {
		t.Fatal("Bash tool must implement ToolWithSearchOrRead")
	}
	kind := sr.IsSearchOrRead(json.RawMessage(`{"command":"grep needle hay.go"}`))
	if !kind.IsSearch {
		t.Errorf("IsSearchOrRead(grep …) = %+v, want IsSearch=true", kind)
	}
}

// ---------------------------------------------------------------------------
// RenderResult / DecodeResult
// ---------------------------------------------------------------------------

// The separator guards compare sb.Len() > 0; a single-character preceding
// segment still needs its newline separator.
func TestRenderResult_OneCharSegmentsKeepSeparators(t *testing.T) {
	t.Parallel()
	tt := New(nil)
	cases := []struct {
		name string
		out  *Output
		want string
	}{
		{"stdout+stderr", &Output{Stdout: "a", Stderr: "b"}, "a\nb"},
		{"stdout+timeout", &Output{Stdout: "a", TimedOut: true}, "a\nCommand timed out"},
		{
			"stdout+background",
			&Output{Stdout: "a", BackgroundJobID: "bg-9"},
			"a\nCommand timed out and was moved to background (job ID: bg-9)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.RenderResult(tc.out); got != tc.want {
				t.Errorf("RenderResult(%+v) = %q, want %q", tc.out, got, tc.want)
			}
		})
	}
}

// Each single non-zero identifying field must decode successfully; removing
// any operand from the identifying-fields condition rejects one of these.
func TestBashDecodeResult_EverySingleFieldIdentifies(t *testing.T) {
	t.Parallel()
	tt := New(nil)
	cases := []struct {
		name  string
		text  string
		check func(*Output) bool
	}{
		{"stdout only", `{"output":"hi"}`, func(o *Output) bool { return o.Stdout == "hi" }},
		{"stderr only", `{"stderr":"boom"}`, func(o *Output) bool { return o.Stderr == "boom" }},
		{"timed out only", `{"timed_out":true}`, func(o *Output) bool { return o.TimedOut }},
		{"background only", `{"backgroundTaskId":"bg-7"}`, func(o *Output) bool { return o.BackgroundJobID == "bg-7" }},
		{"cwd only", `{"cwd":"/w"}`, func(o *Output) bool { return o.CWD == "/w" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(tool.WrapSingleBlock(tc.text))
			if err != nil {
				t.Fatalf("DecodeResult(%s): %v", tc.name, err)
			}
			o, ok := v.(*Output)
			if !ok {
				t.Fatalf("DecodeResult returned %T, want *Output", v)
			}
			if !tc.check(o) {
				t.Errorf("DecodeResult(%s) = %+v, field not recovered", tc.name, o)
			}
		})
	}
}

// Non-array wire input must surface the UnmarshalSingleBlock error, not a
// downstream empty-text unmarshal error.
func TestBashDecodeResult_NonArrayWireKeepsUnwrapError(t *testing.T) {
	t.Parallel()
	tt := New(nil)
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(json.RawMessage(`{"not":"array"}`))
	if err == nil {
		t.Fatal("DecodeResult(non-array) = nil error, want error")
	}
	if !strings.Contains(err.Error(), "expects array-form content") {
		t.Errorf("error = %q, want UnmarshalSingleBlock's array-form message", err.Error())
	}
}

// ---------------------------------------------------------------------------
// executeBash input handling
// ---------------------------------------------------------------------------

func TestExecute_ParseInputErrorWrapsCause(t *testing.T) {
	t.Parallel()
	_, err := Execute(context.Background(), json.RawMessage(`{bad json`), nil)
	if err == nil {
		t.Fatal("expected parse error")
	}
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) {
		t.Errorf("error %v does not wrap the json.SyntaxError cause (Unwrap chain broken)", err)
	}
}

// timeout=1 must be honored ("> 0" guard): a 1ms budget auto-backgrounds the
// still-running command instead of letting it run to completion.
func TestExecute_TimeoutOfOneMsHonored(t *testing.T) {
	reg := withFreshRegistry(t)
	res, err := Execute(context.Background(), json.RawMessage(`{"command":"echo marker; sleep 4","timeout":1}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if out.BackgroundJobID == "" {
		t.Fatalf("BackgroundJobID empty (TimedOut=%v Stdout=%q) — 1ms timeout was not honored", out.TimedOut, out.Stdout)
	}
	_ = reg.Kill(out.BackgroundJobID)
	_, _ = reg.Wait(out.BackgroundJobID)
	reg.Remove(out.BackgroundJobID)
}

// ---------------------------------------------------------------------------
// Dead-cwd recovery
// ---------------------------------------------------------------------------

// A deleted session cwd with a live original dir must move the session to the
// fallback and report it as CWD.
func TestExecute_DeadCwdFallsBackAndUpdatesSession(t *testing.T) {
	deadA := filepath.Join(t.TempDir(), "gone")
	liveB := t.TempDir()

	var setTo []string
	tctx := &tool.ToolUseContext{
		WorkingDir:         deadA,
		OriginalWorkingDir: liveB,
		SetWorkingDir:      func(d string) { setTo = append(setTo, d) },
	}
	res, err := Execute(context.Background(), json.RawMessage(`{"command":"pwd"}`), tctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if len(setTo) != 1 || setTo[0] != liveB {
		t.Errorf("SetWorkingDir calls = %v, want exactly [%s]", setTo, liveB)
	}
	if out.CWD != liveB {
		t.Errorf("CWD = %q, want %q", out.CWD, liveB)
	}
	if !strings.Contains(out.Stdout, liveB) {
		t.Errorf("Stdout = %q, want command to run in %q", out.Stdout, liveB)
	}
}

// A one-shot cwd override that points at a deleted dir must run in the
// fallback without rewriting session state: only the session-tracked cwd is
// allowed to move, so the cwdFromSession guard in the SetWorkingDir
// condition must hold (dropping it makes the session jump to the fallback).
func TestExecute_DeadOneShotCwdDoesNotMoveSession(t *testing.T) {
	dead := filepath.Join(t.TempDir(), "oneshot-gone")
	if err := os.Mkdir(dead, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.RemoveAll(dead); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	// Distinct live dirs so out.CWD == original proves the fallback fired: with
	// WorkingDir == OriginalWorkingDir a dropped input cwd would pass trivially.
	workLive := t.TempDir()
	origLive := t.TempDir()

	var setTo []string
	tctx := &tool.ToolUseContext{
		WorkingDir:         workLive,
		OriginalWorkingDir: origLive,
		SetWorkingDir:      func(d string) { setTo = append(setTo, d) },
	}
	res, err := Execute(context.Background(), json.RawMessage(`{"command":"pwd","cwd":"`+dead+`"}`), tctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if len(setTo) != 0 {
		t.Errorf("SetWorkingDir calls = %v, want none — a one-shot cwd override must not move the session", setTo)
	}
	if out.CWD != origLive {
		t.Errorf("CWD = %q, want fallback %q", out.CWD, origLive)
	}
	if !strings.Contains(out.Stdout, origLive) {
		t.Errorf("Stdout = %q, want command to run in fallback %q", out.Stdout, origLive)
	}
}

// When both the requested cwd and the fallback are gone, the failure result
// must carry the dead cwd so callers can see what was attempted.
func TestExecute_DeadCwdNoFallbackReportsDeadCwd(t *testing.T) {
	dead := filepath.Join(t.TempDir(), "also-gone")
	res, err := Execute(context.Background(), json.RawMessage(`{"command":"pwd","cwd":"`+dead+`"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if out.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", out.ExitCode)
	}
	if !strings.Contains(out.Stderr, "no longer exists") {
		t.Errorf("Stderr = %q, want 'no longer exists'", out.Stderr)
	}
	if out.CWD != dead {
		t.Errorf("CWD = %q, want the dead dir %q", out.CWD, dead)
	}
}

// ---------------------------------------------------------------------------
// Output capping (spilled output)
// ---------------------------------------------------------------------------

// The 30000-byte default cap must truncate spilled output at exactly
// MaxOutputSize with an exact byte-count marker.
func TestExecute_SpilledOutputCapExact(t *testing.T) {
	forceNonPTY(t)
	// A non-nil tctx without UncappedOutput must still get the 30000 cap —
	// a nil tctx short-circuits the condition and hides cap-selection bugs.
	tctx := &tool.ToolUseContext{}
	cwd := t.TempDir()
	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"head -c 9000000 /dev/zero | tr '\\0' x","timeout":30000,"cwd":"`+cwd+`"}`), tctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	const wantTail = "\n... [8970000 bytes truncated]"
	if !strings.HasSuffix(out.Stdout, wantTail) {
		t.Errorf("Stdout tail = %q, want %q", tail45(out.Stdout), wantTail)
	}
	if got := len(out.Stdout); got != 30000+len(wantTail) {
		t.Errorf("len(Stdout) = %d, want %d", got, 30000+len(wantTail))
	}
	if out.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.ExitCode)
	}
	if out.CWD != cwd {
		t.Errorf("CWD = %q, want %q", out.CWD, cwd)
	}
}

func tail45(s string) string {
	if len(s) > 45 {
		return s[len(s)-45:]
	}
	return s
}

// UncappedOutput must bypass the 30000 cap: 9MB stays whole.
func TestExecute_UncappedOutputBypassesCap(t *testing.T) {
	forceNonPTY(t)
	tctx := &tool.ToolUseContext{UncappedOutput: true}
	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"head -c 9000000 /dev/zero | tr '\\0' x","timeout":30000}`), tctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	stdout := out.Stdout
	if got := len(stdout); got != 9000000 {
		t.Errorf("len(Stdout) = %d, want 9000000 (uncapped)", got)
	}
	if strings.Contains(stdout, "bytes truncated") {
		t.Error("uncapped output must not carry a truncation marker")
	}
}

// ---------------------------------------------------------------------------
// Non-PTY sync path
// ---------------------------------------------------------------------------

// Non-PTY sync commands keep cwd tracking: cd must be reported and persisted,
// and a completed command is not timed out.
func TestExecuteNonPTYSync_CdTrackingAndCompletion(t *testing.T) {
	forceNonPTY(t)
	home := t.TempDir()
	var setTo []string
	tctx := &tool.ToolUseContext{
		WorkingDir:    home,
		SetWorkingDir: func(d string) { setTo = append(setTo, d) },
	}
	// "sleep" first word keeps this on the sync (non-auto-bg) path.
	res, err := Execute(context.Background(), json.RawMessage(`{"command":"sleep 0.05 && cd /"}`), tctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if out.TimedOut {
		t.Error("TimedOut = true, want false for a completed command")
	}
	if out.CWD != "/" {
		t.Errorf("CWD = %q, want / (cwd tracking dropped?)", out.CWD)
	}
	if len(setTo) != 1 || setTo[0] != "/" {
		t.Errorf("SetWorkingDir calls = %v, want exactly [/]", setTo)
	}
}

// Group-kill on timeout must actually terminate the tree: without it cmd.Run
// stays blocked on the output pipe until the child exits on its own.
func TestExecuteNonPTYSync_TimeoutKillsPromptly(t *testing.T) {
	forceNonPTY(t)
	type execResult struct {
		out *Output
		err error
	}
	ch := make(chan execResult, 1)
	go func() {
		res, err := Execute(context.Background(), json.RawMessage(`{"command":"sleep 3","timeout":400}`), nil)
		ch <- execResult{res.Data.(*Output), err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("Execute: %v", r.err)
		}
		if !r.out.TimedOut {
			t.Error("TimedOut = false, want true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("command not terminated within 2s — process-tree kill on timeout did not fire")
	}
}

// ---------------------------------------------------------------------------
// Non-PTY auto-background path
// ---------------------------------------------------------------------------

// Done-path results: exit code propagates (0 for success, 3 for failure) and
// the spawn cwd is reported.
func TestExecuteNonPTYAutoBg_ExitCodeAndCwdPropagation(t *testing.T) {
	forceNonPTY(t)
	withFreshRegistry(t)
	cwd := t.TempDir()

	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"echo oops >&2; exit 3","timeout":10000,"cwd":"`+cwd+`"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if out.BackgroundJobID != "" {
		t.Errorf("fast command backgrounded unexpectedly: %q", out.BackgroundJobID)
	}
	if out.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3 (failure exit code lost)", out.ExitCode)
	}
	if out.CWD != cwd {
		t.Errorf("CWD = %q, want %q", out.CWD, cwd)
	}
	if !strings.Contains(out.Stderr, "oops") {
		t.Errorf("Stderr = %q, want captured stderr 'oops'", out.Stderr)
	}
}

// Timer-path: a 1-byte stderr must be flushed into the job output, and a job
// that finishes on its own is not marked interrupted.
func TestExecuteNonPTYAutoBg_TimeoutFlushesStderrAndCompletesClean(t *testing.T) {
	forceNonPTY(t)
	reg := withFreshRegistry(t)
	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"printf x >&2; sleep 2","timeout":250}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if out.BackgroundJobID == "" {
		t.Fatalf("BackgroundJobID empty — command was not auto-backgrounded")
	}
	waitJobDone(t, reg, out.BackgroundJobID)
	job, _ := reg.Get(out.BackgroundJobID)
	if job.ExitCode != 0 {
		t.Errorf("job ExitCode = %d, want 0", job.ExitCode)
	}
	if job.Interrupted {
		t.Error("job Interrupted = true, want false for a natural completion")
	}
	jobOut := job.Output.String()
	if !strings.Contains(jobOut, "x") {
		t.Errorf("job output = %q, want flushed 1-byte stderr", jobOut)
	}
	reg.Remove(out.BackgroundJobID)
}

// Timer-path completion: a command that exits non-zero after being
// backgrounded must report that exit code on the job.
func TestExecuteNonPTYAutoBg_TimeoutPropagatesFailureExit(t *testing.T) {
	forceNonPTY(t)
	reg := withFreshRegistry(t)
	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"printf x; sleep 2; exit 3","timeout":250}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if out.BackgroundJobID == "" {
		t.Fatalf("BackgroundJobID empty — expected auto-bg transition")
	}
	waitJobDone(t, reg, out.BackgroundJobID)
	job, _ := reg.Get(out.BackgroundJobID)
	if job.ExitCode != 3 {
		t.Errorf("job ExitCode = %d, want 3 (failure exit code lost on transition)", job.ExitCode)
	}
	reg.Remove(out.BackgroundJobID)
}

// ---------------------------------------------------------------------------
// PTY sync path
// ---------------------------------------------------------------------------

func TestExecutePTYSync_CdReportedInResult(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}
	// "sleep" first word forces the sync (non-auto-bg) PTY path.
	res, err := Execute(context.Background(), json.RawMessage(`{"command":"sleep 0.05 && cd /"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if out.CWD != "/" {
		t.Errorf("CWD = %q, want /", out.CWD)
	}
	if out.TimedOut {
		t.Error("TimedOut = true, want false")
	}
}

// PTY-mode commands must see a tty on stdout; the non-PTY fallback does not.
func TestExecutePTY_StdoutIsATTY(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}
	res, err := Execute(context.Background(), json.RawMessage(`{"command":"test -t 1 && echo is-tty || echo not-tty"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if out.Stdout != "is-tty" {
		t.Errorf("Stdout = %q, want is-tty (command ran without a PTY?)", out.Stdout)
	}
}

// \r-overwritten spinner lines must replace the previous line in the capture.
func TestExecutePTY_ScreenReplaceOverwritesLine(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}
	res, err := Execute(context.Background(), json.RawMessage(`{"command":"printf 'spin1\\rspin2\\n'"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if out.Stdout != "spin2" {
		t.Errorf("Stdout = %q, want spin2 (replace event dropped?)", out.Stdout)
	}
}

// ---------------------------------------------------------------------------
// PTY ask-input (executePTY closure)
// ---------------------------------------------------------------------------

// A stalled password prompt must be answered through OnAskInput, and the
// deadline handed to the UI is the remaining budget (< full timeout).
func TestExecutePTY_AskInputAnswersPrompt(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}
	// Restore the prior value, not the production default: the package's
	// pty_integration tests lower the gate to 100ms in init().
	prevStall := getDrainStallThreshold()
	SetDrainStallThreshold(150 * time.Millisecond)
	t.Cleanup(func() { SetDrainStallThreshold(prevStall) })

	rec := &askRecorder{}
	tctx := &tool.ToolUseContext{OnAskInput: rec.onAskInput}
	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"printf 'Password: '; read pw < /dev/tty; echo \"got:$pw\"","timeout":4000}`), tctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if !strings.Contains(out.Stdout, "got:secret") {
		t.Fatalf("Stdout = %q, want the answered input echoed back (ask-input never fired)", out.Stdout)
	}
	calls, masked, delta, ahead := rec.snapshot()
	if calls != 1 {
		t.Errorf("OnAskInput calls = %d, want 1", calls)
	}
	if !masked {
		t.Error("masked = false, want true for a Password prompt")
	}
	if !ahead {
		t.Error("deadline is not in the future")
	}
	// remaining = 4s - elapsed; the ADD mutant computes 4s + elapsed and
	// always exceeds the upper bound.
	if delta < 1500*time.Millisecond || delta > 4040*time.Millisecond {
		t.Errorf("deadline delta = %v, want within [1.5s, 4.04s] of remaining budget", delta)
	}
}

// With a budget above 60s the ask deadline is clamped to now+60s exactly.
func TestExecutePTY_AskInputDeadlineClampedTo60s(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}
	// Restore the prior value, not the production default: the package's
	// pty_integration tests lower the gate to 100ms in init().
	prevStall := getDrainStallThreshold()
	SetDrainStallThreshold(150 * time.Millisecond)
	t.Cleanup(func() { SetDrainStallThreshold(prevStall) })

	rec := &askRecorder{}
	tctx := &tool.ToolUseContext{OnAskInput: rec.onAskInput}
	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"printf 'Password: '; read pw < /dev/tty; echo \"got:$pw\"","timeout":65000}`), tctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if !strings.Contains(out.Stdout, "got:secret") {
		t.Fatalf("Stdout = %q, want answered input", out.Stdout)
	}
	calls, _, delta, ahead := rec.snapshot()
	if calls != 1 {
		t.Fatalf("OnAskInput calls = %d, want 1", calls)
	}
	if !ahead {
		t.Error("deadline is not in the future")
	}
	if d := delta - 60*time.Second; d < -750*time.Millisecond || d > 750*time.Millisecond {
		t.Errorf("deadline delta = %v, want 60s ± 750ms (clamp at min(remaining, 60s))", delta)
	}
}

// A non-nil tctx without OnAskInput must be tolerated: the command simply
// times out instead of dereferencing a nil callback.
func TestExecutePTY_AskInputNilCallbackTimesOut(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}
	// Restore the prior value, not the production default: the package's
	// pty_integration tests lower the gate to 100ms in init().
	prevStall := getDrainStallThreshold()
	SetDrainStallThreshold(150 * time.Millisecond)
	t.Cleanup(func() { SetDrainStallThreshold(prevStall) })
	reg := withFreshRegistry(t)

	tctx := &tool.ToolUseContext{} // OnAskInput nil
	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"printf 'Password: '; read pw < /dev/tty","timeout":1500}`), tctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if out.BackgroundJobID == "" {
		t.Fatalf("BackgroundJobID empty (TimedOut=%v) — expected timeout transition", out.TimedOut)
	}
	_ = reg.Kill(out.BackgroundJobID)
	_, _ = reg.Wait(out.BackgroundJobID)
	reg.Remove(out.BackgroundJobID)
}

// After an auto-bg transition the foreground budget is spent: a later stall
// must NOT reach OnAskInput (the remaining<=0 guard returns nil).
func TestExecutePTYAutoBg_NoAskAfterBudgetSpent(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}
	// Restore the prior value, not the production default: the package's
	// pty_integration tests lower the gate to 100ms in init().
	prevStall := getDrainStallThreshold()
	SetDrainStallThreshold(150 * time.Millisecond)
	t.Cleanup(func() { SetDrainStallThreshold(prevStall) })
	reg := withFreshRegistry(t)

	rec := &askRecorder{}
	tctx := &tool.ToolUseContext{OnAskInput: rec.onAskInput}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, err := Execute(ctx,
		json.RawMessage(`{"command":"echo start; sleep 1; printf 'Password: '; read pw < /dev/tty","timeout":300}`), tctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if out.BackgroundJobID == "" {
		t.Fatalf("BackgroundJobID empty — expected auto-bg transition at 300ms")
	}
	// Prompt lands ~1s in, well past the 300ms budget.
	time.Sleep(2500 * time.Millisecond) // REAL-TIME: real PTY stall detection runs on wall clock
	if calls, _, _, _ := rec.snapshot(); calls != 0 {
		t.Errorf("OnAskInput calls = %d after budget spent, want 0", calls)
	}
	cancel()
	waitJobDone(t, reg, out.BackgroundJobID)
	reg.Remove(out.BackgroundJobID)
}

// ---------------------------------------------------------------------------
// tmux env overrides
// ---------------------------------------------------------------------------

func mockTmuxSocket(t *testing.T) {
	t.Helper()
	resetSocketState()
	markTmuxToolUsed()
	origOverride := execTmuxOverride
	execTmuxOverride = func(args []string) (tmuxResult, bool) {
		for _, a := range args {
			if a == "display-message" {
				return tmuxResult{Code: 0, Stdout: "/tmp/gbot-mut-tsocket,12345"}, true
			}
		}
		return tmuxResult{Code: 0}, true
	}
	t.Cleanup(func() {
		execTmuxOverride = origOverride
		resetSocketState()
	})
}

// The PTY spawn must apply the TMUX socket override so children see the
// isolated socket, not an inherited TMUX value.
func TestExecutePTY_TmuxOverrideAppliedToChild(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}
	mockTmuxSocket(t)
	res, err := Execute(context.Background(), json.RawMessage(`{"command":"echo \"TMUX=[$TMUX]\""}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	want := "TMUX=[/tmp/gbot-mut-tsocket,12345,0]"
	if !strings.Contains(out.Stdout, want) {
		t.Errorf("Stdout = %q, want %q (env override not applied)", out.Stdout, want)
	}
}

func TestSpawnBackground_TmuxOverrideAppliedToChild(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}
	mockTmuxSocket(t)
	reg := withFreshRegistry(t)
	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"echo \"TMUX=[$TMUX]\"","run_in_background":true}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	id := extractBgID(out.Stdout)
	if id == "" {
		t.Fatalf("no job id in %q", out.Stdout)
	}
	waitJobDone(t, reg, id)
	job, _ := reg.Get(id)
	want := "TMUX=[/tmp/gbot-mut-tsocket,12345,0]"
	if jobOut := job.Output.String(); !strings.Contains(jobOut, want) {
		t.Errorf("job output = %q, want %q (env override not applied)", jobOut, want)
	}
	reg.Remove(id)
}

// ---------------------------------------------------------------------------
// spawnBackground
// ---------------------------------------------------------------------------

// Background spawn with a nil tctx must not panic while wiring ask-input.
func TestSpawnBackground_NilTctxNoPanic(t *testing.T) {
	reg := withFreshRegistry(t)
	res, err := Execute(context.Background(), json.RawMessage(`{"command":"echo bg-niltctx","run_in_background":true}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	id := extractBgID(out.Stdout)
	if id == "" {
		t.Fatalf("no job id in %q", out.Stdout)
	}
	waitJobDone(t, reg, id)
	reg.Remove(id)
}

// The immediate background result carries the spawn cwd; the job then
// completes cleanly (exit 0, not interrupted).
func TestSpawnBackground_ResultCwdAndCleanCompletion(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}
	reg := withFreshRegistry(t)
	cwd := t.TempDir()
	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"echo bg-clean","run_in_background":true,"cwd":"`+cwd+`"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if out.CWD != cwd {
		t.Errorf("CWD = %q, want %q", out.CWD, cwd)
	}
	id := extractBgID(out.Stdout)
	if id == "" {
		t.Fatalf("no job id in %q", out.Stdout)
	}
	waitJobDone(t, reg, id)
	job, _ := reg.Get(id)
	if job.ExitCode != 0 {
		t.Errorf("job ExitCode = %d, want 0", job.ExitCode)
	}
	if job.Interrupted {
		t.Error("job Interrupted = true, want false")
	}
	if job.Status != JobCompleted {
		t.Errorf("job Status = %q, want %q", job.Status, JobCompleted)
	}
	reg.Remove(id)
}

// The PTY screen callback must write appended lines into the job output.
func TestSpawnBackground_PTYCapturesAppendLines(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}
	reg := withFreshRegistry(t)
	res, err := Execute(context.Background(), json.RawMessage(`{"command":"echo bg-append-910","run_in_background":true}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	id := extractBgID(out.Stdout)
	if id == "" {
		t.Fatalf("no job id in %q", out.Stdout)
	}
	waitJobDone(t, reg, id)
	job, _ := reg.Get(id)
	if jobOut := job.Output.String(); !strings.Contains(jobOut, "bg-append-910") {
		t.Errorf("job output = %q, want bg-append-910 (append events dropped)", jobOut)
	}
	reg.Remove(id)
}

// The PTY screen callback must apply \r replacements to the job output.
func TestSpawnBackground_PTYAppliesScreenReplace(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}
	reg := withFreshRegistry(t)
	res, err := Execute(context.Background(), json.RawMessage(`{"command":"printf 'spin1\\rspin2\\n'","run_in_background":true}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	id := extractBgID(out.Stdout)
	if id == "" {
		t.Fatalf("no job id in %q", out.Stdout)
	}
	waitJobDone(t, reg, id)
	job, _ := reg.Get(id)
	if jobOut := job.Output.String(); jobOut != "spin2" {
		t.Errorf("job output = %q, want spin2 (replace events dropped)", jobOut)
	}
	reg.Remove(id)
}

// Background ask-input mirrors the foreground closure: answer delivered,
// deadline = remaining budget (< timeout).
func TestSpawnBackground_AskInputAnswersPrompt(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}
	// Restore the prior value, not the production default: the package's
	// pty_integration tests lower the gate to 100ms in init().
	prevStall := getDrainStallThreshold()
	SetDrainStallThreshold(150 * time.Millisecond)
	t.Cleanup(func() { SetDrainStallThreshold(prevStall) })
	reg := withFreshRegistry(t)

	rec := &askRecorder{}
	tctx := &tool.ToolUseContext{OnAskInput: rec.onAskInput}
	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"printf 'Password: '; read pw < /dev/tty; echo \"got:$pw\"","timeout":4000,"run_in_background":true}`), tctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	id := extractBgID(out.Stdout)
	if id == "" {
		t.Fatalf("no job id in %q", out.Stdout)
	}
	waitJobDone(t, reg, id)
	job, _ := reg.Get(id)
	if jobOut := job.Output.String(); !strings.Contains(jobOut, "got:secret") {
		t.Fatalf("job output = %q, want answered input (ask-input never wired)", jobOut)
	}
	calls, masked, delta, ahead := rec.snapshot()
	if calls != 1 {
		t.Errorf("OnAskInput calls = %d, want 1", calls)
	}
	if !masked {
		t.Error("masked = false, want true")
	}
	if !ahead {
		t.Error("deadline is not in the future")
	}
	if delta < 1500*time.Millisecond || delta > 4040*time.Millisecond {
		t.Errorf("deadline delta = %v, want within [1.5s, 4.04s]", delta)
	}
	reg.Remove(id)
}

func TestSpawnBackground_AskInputDeadlineClampedTo60s(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}
	// Restore the prior value, not the production default: the package's
	// pty_integration tests lower the gate to 100ms in init().
	prevStall := getDrainStallThreshold()
	SetDrainStallThreshold(150 * time.Millisecond)
	t.Cleanup(func() { SetDrainStallThreshold(prevStall) })
	reg := withFreshRegistry(t)

	rec := &askRecorder{}
	tctx := &tool.ToolUseContext{OnAskInput: rec.onAskInput}
	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"printf 'Password: '; read pw < /dev/tty; echo \"got:$pw\"","timeout":65000,"run_in_background":true}`), tctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	id := extractBgID(out.Stdout)
	if id == "" {
		t.Fatalf("no job id in %q", out.Stdout)
	}
	waitJobDone(t, reg, id)
	job, _ := reg.Get(id)
	if jobOut := job.Output.String(); !strings.Contains(jobOut, "got:secret") {
		t.Fatalf("job output = %q, want answered input", jobOut)
	}
	calls, _, delta, ahead := rec.snapshot()
	if calls != 1 {
		t.Fatalf("OnAskInput calls = %d, want 1", calls)
	}
	if !ahead {
		t.Error("deadline is not in the future")
	}
	if d := delta - 60*time.Second; d < -750*time.Millisecond || d > 750*time.Millisecond {
		t.Errorf("deadline delta = %v, want 60s ± 750ms", delta)
	}
	reg.Remove(id)
}

// Background spawn with a tctx whose OnAskInput is nil must tolerate the
// stall (no dereference of the nil callback); the command dies at its timeout.
func TestSpawnBackground_AskInputNilCallbackTimesOut(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}
	// Restore the prior value, not the production default: the package's
	// pty_integration tests lower the gate to 100ms in init().
	prevStall := getDrainStallThreshold()
	SetDrainStallThreshold(150 * time.Millisecond)
	t.Cleanup(func() { SetDrainStallThreshold(prevStall) })
	reg := withFreshRegistry(t)

	tctx := &tool.ToolUseContext{} // OnAskInput nil
	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"printf 'Password: '; read pw < /dev/tty","timeout":1500,"run_in_background":true}`), tctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	id := extractBgID(out.Stdout)
	if id == "" {
		t.Fatalf("no job id in %q", out.Stdout)
	}
	waitJobDone(t, reg, id)
	job, _ := reg.Get(id)
	if !IsTerminalJobStatus(job.Status) {
		t.Errorf("job Status = %q, want terminal state after timeout", job.Status)
	}
	reg.Remove(id)
}

// ---------------------------------------------------------------------------
// transitionToBackground
// ---------------------------------------------------------------------------

// The transition must return promptly with the job id and spawn cwd while the
// process keeps running; running the completion inline would block the tool
// result until command exit.
func TestAutoBg_TransitionReturnsImmediatelyWithCwd(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}
	reg := withFreshRegistry(t)
	cwd := t.TempDir()

	type execResult struct {
		out *Output
		err error
	}
	ch := make(chan execResult, 1)
	go func() {
		res, err := Execute(context.Background(),
			json.RawMessage(`{"command":"echo hi; sleep 2","timeout":150,"cwd":"`+cwd+`"}`), nil)
		ch <- execResult{res.Data.(*Output), err}
	}()
	var out *Output
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("Execute: %v", r.err)
		}
		out = r.out
	case <-time.After(1200 * time.Millisecond):
		t.Fatal("transition did not return within 1.2s — completion ran inline")
	}
	if out.BackgroundJobID == "" {
		t.Fatalf("BackgroundJobID empty (TimedOut=%v Stdout=%q)", out.TimedOut, out.Stdout)
	}
	if out.CWD != cwd {
		t.Errorf("CWD = %q, want %q", out.CWD, cwd)
	}
	waitJobDone(t, reg, out.BackgroundJobID)
	reg.Remove(out.BackgroundJobID)
}

// ---------------------------------------------------------------------------
// cwd-file hygiene (trackCwd=false on background spawns)
// ---------------------------------------------------------------------------

// Background spawns pass trackCwd=false; enabling it leaks a pwd-tracking
// temp file nobody ever removes.
func TestSpawnBackground_NoCwdFileLeak_PTY(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}
	assertNoGbotTempFiles(t, func() {
		res, err := Execute(context.Background(), json.RawMessage(`{"command":"echo leak-pty","run_in_background":true}`), nil)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		id := extractBgID(res.Data.(*Output).Stdout)
		if id == "" {
			t.Fatalf("no job id")
		}
		waitJobDone(t, DefaultRegistry(), id)
		DefaultRegistry().Remove(id)
	})
}

func TestSpawnBackground_NoCwdFileLeak_NonPTY(t *testing.T) {
	forceNonPTY(t)
	reg := withFreshRegistry(t)
	assertNoGbotTempFiles(t, func() {
		res, err := Execute(context.Background(), json.RawMessage(`{"command":"echo leak-nonpty","run_in_background":true}`), nil)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		id := extractBgID(res.Data.(*Output).Stdout)
		if id == "" {
			t.Fatalf("no job id")
		}
		waitJobDone(t, reg, id)
		reg.Remove(id)
	})
}

func assertNoGbotTempFiles(t *testing.T, run func()) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	run()
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "gbot-") {
			t.Errorf("leaked temp file %q in TMPDIR — cwd tracking must stay off for background spawns", e.Name())
		}
	}
}

// ---------------------------------------------------------------------------
// Non-PTY background completion flags
// ---------------------------------------------------------------------------

// Non-PTY background jobs complete with their exit code, uninterruptedly.
func TestSpawnBackground_NonPTY_CompletesUninterrupted(t *testing.T) {
	forceNonPTY(t)
	reg := withFreshRegistry(t)
	res, err := Execute(context.Background(), json.RawMessage(`{"command":"echo nonpty-clean","run_in_background":true}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	id := extractBgID(out.Stdout)
	if id == "" {
		t.Fatalf("no job id in %q", out.Stdout)
	}
	waitJobDone(t, reg, id)
	job, _ := reg.Get(id)
	if job.ExitCode != 0 {
		t.Errorf("job ExitCode = %d, want 0", job.ExitCode)
	}
	if job.Interrupted {
		t.Error("job Interrupted = true, want false for a natural completion")
	}
	if jobOut := job.Output.String(); !strings.Contains(jobOut, "nonpty-clean") {
		t.Errorf("job output = %q, want nonpty-clean", jobOut)
	}
	reg.Remove(id)
}

// A failed spawn (dead cwd) completes the job with -1, not interrupted.
func TestSpawnBackground_NonPTY_StartErrorCompletesMinusOne(t *testing.T) {
	forceNonPTY(t)
	reg := NewBackgroundJobRegistry()
	dead := filepath.Join(t.TempDir(), "no-such-dir")
	res, err := spawnBackground(context.Background(), Input{Command: "echo nope"}, dead, 10*time.Second, reg, nil)
	if err != nil {
		t.Fatalf("spawnBackground: %v", err)
	}
	out := res.Data.(*Output)
	id := extractBgID(out.Stdout)
	if id == "" {
		t.Fatalf("no job id in %q", out.Stdout)
	}
	waitJobDone(t, reg, id)
	job, _ := reg.Get(id)
	if job.ExitCode != -1 {
		t.Errorf("job ExitCode = %d, want -1 (start error)", job.ExitCode)
	}
	if job.Interrupted {
		t.Error("job Interrupted = true, want false for a start error")
	}
}

// ---------------------------------------------------------------------------
// syncCwd unit tests
// ---------------------------------------------------------------------------

func TestSyncCwd_UpdatesSessionOnlyOnChange(t *testing.T) {
	t.Parallel()

	t.Run("unchanged cwd does not touch the session", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		f := filepath.Join(t.TempDir(), "cwdfile")
		if err := os.WriteFile(f, []byte(dir), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		var calls []string
		tctx := &tool.ToolUseContext{SetWorkingDir: func(d string) { calls = append(calls, d) }}
		if got := syncCwd(f, dir, tctx); got != dir {
			t.Errorf("syncCwd unchanged = %q, want %q", got, dir)
		}
		if len(calls) != 0 {
			t.Errorf("SetWorkingDir calls = %v, want none", calls)
		}
	})

	t.Run("changed cwd persists and reports the new dir", func(t *testing.T) {
		t.Parallel()
		spawn := t.TempDir()
		moved := t.TempDir()
		f := filepath.Join(t.TempDir(), "cwdfile")
		if err := os.WriteFile(f, []byte(moved), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		var calls []string
		tctx := &tool.ToolUseContext{SetWorkingDir: func(d string) { calls = append(calls, d) }}
		if got := syncCwd(f, spawn, tctx); got != moved {
			t.Errorf("syncCwd changed = %q, want %q", got, moved)
		}
		if len(calls) != 1 || calls[0] != moved {
			t.Errorf("SetWorkingDir calls = %v, want exactly [%s]", calls, moved)
		}
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Error("cwd tracking file not removed")
		}
	})

	t.Run("nil SetWorkingDir with a change must not panic", func(t *testing.T) {
		t.Parallel()
		spawn := t.TempDir()
		moved := t.TempDir()
		f := filepath.Join(t.TempDir(), "cwdfile")
		if err := os.WriteFile(f, []byte(moved), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		tctx := &tool.ToolUseContext{}
		if got := syncCwd(f, spawn, tctx); got != moved {
			t.Errorf("syncCwd nil-hook = %q, want %q", got, moved)
		}
	})

	t.Run("nil tctx with a change must not panic", func(t *testing.T) {
		t.Parallel()
		spawn := t.TempDir()
		moved := t.TempDir()
		f := filepath.Join(t.TempDir(), "cwdfile")
		if err := os.WriteFile(f, []byte(moved), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if got := syncCwd(f, spawn, nil); got != moved {
			t.Errorf("syncCwd nil-tctx = %q, want %q", got, moved)
		}
	})

	t.Run("missing file (failed command) keeps the spawn cwd", func(t *testing.T) {
		t.Parallel()
		var calls []string
		tctx := &tool.ToolUseContext{SetWorkingDir: func(d string) { calls = append(calls, d) }}
		got := syncCwd(filepath.Join(t.TempDir(), "never-written"), "/spawn", tctx)
		if got != "/spawn" {
			t.Errorf("syncCwd missing = %q, want /spawn", got)
		}
		if len(calls) != 0 {
			t.Errorf("SetWorkingDir calls = %v, want none", calls)
		}
	})
}

// ---------------------------------------------------------------------------
// Classifier
// ---------------------------------------------------------------------------

// A bare "sleep" (single word) is still on the disallowed list.
func TestIsAutobackgroundingAllowed_BareSleepDisallowed(t *testing.T) {
	t.Parallel()
	if isAutobackgroundingAllowed("sleep") {
		t.Error("isAutobackgroundingAllowed(\"sleep\") = true, want false")
	}
}
