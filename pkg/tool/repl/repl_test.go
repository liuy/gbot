package repl

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// newTestSession creates a session for testing, fails test on error.
func newTestSession(t *testing.T) *Session {
	t.Helper()
	s, err := NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// mockToolFn returns a tool function that responds to known tool names.
// Unexpected tool calls fail the test immediately.
func mockToolFn(t *testing.T, responses map[string]string) func(ctx context.Context, name, argsJSON string) string {
	t.Helper()
	var mu sync.Mutex
	return func(_ context.Context, name, argsJSON string) string {
		mu.Lock()
		defer mu.Unlock()
		if resp, ok := responses[name]; ok {
			return resp
		}
		t.Fatalf("unexpected tool call: %s (args: %s)", name, argsJSON)
		return ""
	}
}

// ---------------------------------------------------------------------------
// Session creation + console.log tests
// ---------------------------------------------------------------------------

func TestConsoleLog(t *testing.T) {
	s := newTestSession(t)
	output, err := s.Execute(context.Background(), `console.log("hello")`, "", nil, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "hello") {
		t.Errorf("expected output containing 'hello', got %q", output)
	}
}

func TestConsoleLogVariable(t *testing.T) {
	s := newTestSession(t)
	output, err := s.Execute(context.Background(), `const x = 1; console.log(x)`, "", nil, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "1") {
		t.Errorf("expected output containing '1', got %q", output)
	}
}

func TestES6ArrowFunction(t *testing.T) {
	s := newTestSession(t)
	code := `const greet = (name) => "hello " + name; console.log(greet("world"))`
	output, err := s.Execute(context.Background(), code, "", nil, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "hello world") {
		t.Errorf("expected 'hello world', got %q", output)
	}
}

func TestTopLevelAwait(t *testing.T) {
	s := newTestSession(t)
	code := `const x = await Promise.resolve(42); console.log(x)`
	output, err := s.Execute(context.Background(), code, "", nil, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "42") {
		t.Errorf("expected output containing '42', got %q", output)
	}
}

// ---------------------------------------------------------------------------
// Persistent variables across Execute calls
// ---------------------------------------------------------------------------

func TestPersistentVariables(t *testing.T) {
	s := newTestSession(t)
	toolFn := mockToolFn(t, nil)

	// EvalModule-only: variables don't persist across calls.
	// Use store()/load() for cross-call data persistence.
	_, err := s.Execute(context.Background(), `store("count", 42)`, "", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute 1: %v", err)
	}

	output, err := s.Execute(context.Background(), `console.log(load("count"))`, "", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute 2: %v", err)
	}
	if !strings.Contains(output, "42") {
		t.Errorf("expected '42' from stored value, got %q", output)
	}
}

// ---------------------------------------------------------------------------
// Buffer lifecycle (Issue 4): two Executes produce correct independent output
// ---------------------------------------------------------------------------

func TestBufferLifecycle(t *testing.T) {
	s := newTestSession(t)

	output1, err := s.Execute(context.Background(), `console.log("first")`, "", nil, 10000)
	if err != nil {
		t.Fatalf("Execute 1: %v", err)
	}
	if !strings.Contains(output1, "first") {
		t.Errorf("first: expected 'first', got %q", output1)
	}

	output2, err := s.Execute(context.Background(), `console.log("second")`, "", nil, 10000)
	if err != nil {
		t.Fatalf("Execute 2: %v", err)
	}
	if !strings.Contains(output2, "second") {
		t.Errorf("second: expected 'second', got %q", output2)
	}
	// Issue 4: second output must NOT contain "first"
	if strings.Contains(output2, "first") {
		t.Errorf("buffer leak: second output contains 'first': %q", output2)
	}
}

// ---------------------------------------------------------------------------
// Reset
// ---------------------------------------------------------------------------

func TestReset(t *testing.T) {
	s := newTestSession(t)

	// Set a variable
	_, _ = s.Execute(context.Background(), `var resetTest = 99`, "", nil, 10000)

	// Reset session
	if err := s.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	// Variable should be gone
	output, err := s.Execute(context.Background(), `try { console.log(resetTest) } catch(e) { console.log("reset ok") }`, "", nil, 10000)
	if err != nil {
		t.Fatalf("Execute after reset: %v", err)
	}
	if !strings.Contains(output, "reset ok") {
		t.Errorf("expected variable to be cleared after reset, got %q", output)
	}
}

// ---------------------------------------------------------------------------
// Timeout (infinite loop → timeout error)
// ---------------------------------------------------------------------------

func TestTimeout(t *testing.T) {
	s := newTestSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	output, err := s.Execute(ctx, `while(true) {}`, "", nil, 1000) // 1s timeout
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "[JS Error]") {
		t.Errorf("expected timeout error, got %q", output)
	}
}

// ---------------------------------------------------------------------------
// Context cancellation → VM interrupt
// ---------------------------------------------------------------------------

func TestContextCancel(t *testing.T) {
	s := newTestSession(t)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan string, 1)
	go func() {
		output, _ := s.Execute(ctx, `store("running", "1"); while(true) { /* spin */ }`, "", nil, 60000)
		done <- output
	}()

	// Wait for JS eval to actually start (poll stored flag)
	pollTimeout := time.After(3 * time.Second)
	for {
		if val, _ := s.kv.Load("running"); val == "1" {
			break
		}
		select {
		case <-pollTimeout:
			t.Fatal("timed out waiting for JS eval to start")
		default:
			runtime.Gosched()
		}
	}
	cancel()

	select {
	case output := <-done:
		if !strings.Contains(output, "[JS Error]") && !strings.Contains(output, "cancelled") {
			t.Errorf("expected interrupt error, got %q", output)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Execute did not return after context cancellation")
	}
}

// ---------------------------------------------------------------------------
// tool() through mock toolFn
// ---------------------------------------------------------------------------

func TestToolFunction(t *testing.T) {
	s := newTestSession(t)
	toolFn := mockToolFn(t, map[string]string{
		"Echo": `{"result": "echoed"}`,
	})

	output, err := s.Execute(context.Background(),
		`const r = tool("Echo", JSON.stringify({msg: "hi"})); console.log(r)`,
		"", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "echoed") {
		t.Errorf("expected tool result 'echoed', got %q", output)
	}
}

func TestToolNotFound(t *testing.T) {
	s := newTestSession(t)
	// Use custom toolFn that returns error for unknown tools (mockToolFn would t.Fatalf)
	toolFn := func(_ context.Context, name, argsJSON string) string {
		return "ERROR: tool " + name + " not found"
	}

	output, err := s.Execute(context.Background(),
		`const r = tool("NoSuchTool", "{}"); console.log(r)`,
		"", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "ERROR:") {
		t.Errorf("expected ERROR prefix for missing tool, got %q", output)
	}
}

func TestToolErrorPrefix(t *testing.T) {
	s := newTestSession(t)
	toolFn := func(_ context.Context, name, argsJSON string) string {
		return "ERROR: something went wrong"
	}

	output, err := s.Execute(context.Background(),
		`const r = tool("Fail", "{}"); if (r.startsWith("ERROR:")) { console.log("caught") } else { console.log("missed:" + r) }`,
		"", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "caught") {
		t.Errorf("JS should detect ERROR: prefix, got %q", output)
	}
}

// ---------------------------------------------------------------------------
// store/load (Issue 11: toGoNative)
// ---------------------------------------------------------------------------

func TestStoreLoad(t *testing.T) {
	s := newTestSession(t)
	toolFn := mockToolFn(t, nil)

	output, err := s.Execute(context.Background(),
		`store("key", "value"); const v = load("key"); console.log(v)`,
		"", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "value") {
		t.Errorf("expected loaded value 'value', got %q", output)
	}
}

func TestStoreLoadAcrossExecutions(t *testing.T) {
	s := newTestSession(t)
	toolFn := mockToolFn(t, nil)

	_, err := s.Execute(context.Background(),
		`store("persist", "data")`,
		"", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute 1: %v", err)
	}

	output, err := s.Execute(context.Background(),
		`const v = load("persist"); console.log(v)`,
		"", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute 2: %v", err)
	}
	if !strings.Contains(output, "data") {
		t.Errorf("expected persisted 'data', got %q", output)
	}
}

func TestLoadMissing(t *testing.T) {
	s := newTestSession(t)
	toolFn := mockToolFn(t, nil)

	output, err := s.Execute(context.Background(),
		`const v = load("nonexistent"); console.log(v)`,
		"", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(output, "ERROR") {
		t.Errorf("load missing key should not error, got %q", output)
	}
}

// ---------------------------------------------------------------------------
// notify
// ---------------------------------------------------------------------------

func TestNotify(t *testing.T) {
	s := newTestSession(t)
	toolFn := mockToolFn(t, nil)

	output, err := s.Execute(context.Background(),
		`notify("progress: 50%")`,
		"", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "[NOTIFY] progress: 50%") {
		t.Errorf("expected [NOTIFY] prefix, got %q", output)
	}
}

// ---------------------------------------------------------------------------
// Close prevents further Execute
// ---------------------------------------------------------------------------

func TestClose(t *testing.T) {
	s, err := NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	s.Close()

	_, err = s.Execute(context.Background(), `console.log("after close")`, "", nil, 10000)
	if err == nil {
		t.Error("expected error after Close()")
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Errorf("expected 'closed' error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// cwd injection
// ---------------------------------------------------------------------------

func TestCwdInjection(t *testing.T) {
	s := newTestSession(t)
	output, err := s.Execute(context.Background(), `console.log(cwd)`, "/tmp/test", nil, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "/tmp/test") {
		t.Errorf("expected cwd '/tmp/test', got %q", output)
	}
}

// ---------------------------------------------------------------------------
// parsePragma (Issue 12)
// ---------------------------------------------------------------------------

func TestParsePragmaDefault(t *testing.T) {
	code, ms, err := parsePragma(`console.log("hi")`)
	if err != nil {
		t.Fatalf("parsePragma: %v", err)
	}
	if ms != 120000 {
		t.Errorf("expected default 120000ms, got %d", ms)
	}
	if code != `console.log("hi")` {
		t.Errorf("code should be unchanged, got %q", code)
	}
}

func TestParsePragmaCustom(t *testing.T) {
	code, ms, err := parsePragma("// @timeout: 5000\nconsole.log(\"hi\")")
	if err != nil {
		t.Fatalf("parsePragma: %v", err)
	}
	if ms != 5000 {
		t.Errorf("expected 5000ms, got %d", ms)
	}
	if strings.Contains(code, "@timeout") {
		t.Errorf("pragma should be stripped, got %q", code)
	}
	if !strings.Contains(code, "console.log") {
		t.Errorf("code should contain console.log, got %q", code)
	}
}

func TestParsePragmaInvalid(t *testing.T) {
	_, _, err := parsePragma("// @timeout: 500\nconsole.log()")
	if err == nil {
		t.Error("expected error for timeout below minimum (1000ms)")
	}
	if !strings.Contains(err.Error(), "1000-600000") {
		t.Errorf("expected range error, got %v", err)
	}
}

func TestParsePragmaTooLarge(t *testing.T) {
	_, _, err := parsePragma("// @timeout: 700000\nconsole.log()")
	if err == nil {
		t.Fatal("expected error for timeout above maximum (600000ms)")
	}
	if !strings.Contains(err.Error(), "1000-600000") {
		t.Errorf("expected range error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// yield_control
// ---------------------------------------------------------------------------

func TestYieldControl(t *testing.T) {
	s := newTestSession(t)
	toolFn := mockToolFn(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan string, 1)
	go func() {
		output, _ := s.Execute(ctx, `console.log("before"); yield_control(); console.log("after")`, "", toolFn, 10000)
		done <- output
	}()

	select {
	case yielded := <-s.YieldCh():
		if !strings.Contains(yielded, "before") {
			t.Errorf("yielded output should contain 'before', got %q", yielded)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("yield_control did not yield")
	}

	select {
	case s.ResumeCh() <- struct{}{}:
	default:
	}

	select {
	case output := <-done:
		if !strings.Contains(output, "before") {
			t.Errorf("final output should contain 'before', got %q", output)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Execute did not complete after resume")
	}
}

// ---------------------------------------------------------------------------
// exit()
// ---------------------------------------------------------------------------

func TestExit(t *testing.T) {
	s := newTestSession(t)
	toolFn := mockToolFn(t, nil)

	output, err := s.Execute(context.Background(),
		`console.log("before exit"); exit(); console.log("after exit")`,
		"", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "before exit") {
		t.Errorf("expected 'before exit' in output, got %q", output)
	}
	if strings.Contains(output, "after exit") {
		t.Errorf("code after exit() should not execute, got %q", output)
	}
}

// ---------------------------------------------------------------------------
// setTimeout/clearTimeout
// ---------------------------------------------------------------------------

func TestSetTimeoutBasic(t *testing.T) {
	s := newTestSession(t)
	toolFn := mockToolFn(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	output, err := s.Execute(ctx, `
		var result = "before";
		setTimeout(function() { result = "fired" }, 10);
		console.log(result)
	`, "", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "before") {
		t.Errorf("expected 'before' from sync log, got %q", output)
	}
}

func TestSetTimeoutCallbackFires(t *testing.T) {
	s := newTestSession(t)
	toolFn := mockToolFn(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start code that sets a timeout
	done := make(chan string, 1)
	go func() {
		output, _ := s.Execute(ctx, `
			setTimeout(function() { store("timer_result", "callback_ran") }, 10);
			console.log("scheduled");
		`, "", toolFn, 10000)
		done <- output
	}()

	// Wait for the Execute to finish
	select {
	case output := <-done:
		if !strings.Contains(output, "scheduled") {
			t.Errorf("expected 'scheduled', got %q", output)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Execute did not complete")
	}


	// Check that the callback ran by reading stored value
	output, err := s.Execute(ctx, `console.log(load("timer_result"))`, "", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute check: %v", err)
	}
	if !strings.Contains(output, "callback_ran") {
		t.Errorf("callback did not fire, got %q", output)
	}
}

func TestClearTimeout(t *testing.T) {
	s := newTestSession(t)
	toolFn := mockToolFn(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Schedule and immediately cancel
	_, err := s.Execute(ctx, `
		var id = setTimeout(function() { store("cleared_test", "should_not_exist") }, 20);
		clearTimeout(id);
	`, "", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}


	// Verify callback did NOT run
	output, err := s.Execute(ctx, `console.log(load("cleared_test"))`, "", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute check: %v", err)
	}
	if strings.Contains(output, "should_not_exist") {
		t.Errorf("clearTimeout did not prevent callback from firing, got %q", output)
	}
}

func TestSetTimeoutReturnID(t *testing.T) {
	s := newTestSession(t)
	toolFn := mockToolFn(t, nil)

	output, err := s.Execute(context.Background(), `
		var id1 = setTimeout(function(){}, 10);
		var id2 = setTimeout(function(){}, 10);
		console.log(id1 + "," + id2)
	`, "", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "0,1") {
		t.Errorf("expected incrementing IDs '0,1', got %q", output)
	}
}

func TestSetTimeoutCallbackOutputInResult(t *testing.T) {
	s := newTestSession(t)
	toolFn := mockToolFn(t, nil)

	output, err := s.Execute(context.Background(), `
		setTimeout(function() { console.log("from callback") }, 5);
		console.log("main code")
	`, "", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Verify exact output: main code first, then callback, no duplication (P1 fix)
	lines := strings.Split(strings.TrimSpace(output), "\n")
	want := []string{"main code", "from callback"}
	if len(lines) != len(want) {
		t.Fatalf("expected %d lines, got %d: %q", len(want), len(lines), output)
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line %d: expected %q, got %q (full: %q)", i, w, lines[i], output)
		}
	}
}

// ---------------------------------------------------------------------------
// toGoNative (Issue 11)
// ---------------------------------------------------------------------------

func TestToGoNativeString(t *testing.T) {
	got := toGoNative("hello")
	if got != "hello" {
		t.Errorf("expected 'hello', got %v", got)
	}
}

func TestToGoNativeFloat(t *testing.T) {
	got := toGoNative(3.14)
	if got != 3.14 {
		t.Errorf("expected 3.14, got %v", got)
	}
}

func TestToGoNativeBool(t *testing.T) {
	got := toGoNative(true)
	if got != true {
		t.Errorf("expected true, got %v", got)
	}
}

func TestToGoNativeNil(t *testing.T) {
	got := toGoNative(nil)
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestToGoNativeMap(t *testing.T) {
	got := toGoNative(map[string]any{"key": "value"})
	s, ok := got.(string)
	if !ok {
		t.Fatalf("expected string from map, got %T: %v", got, got)
	}
	if !strings.Contains(s, "key") {
		t.Errorf("expected JSON string with 'key', got %q", s)
	}
}

// ---------------------------------------------------------------------------
// REPLTool tests
// ---------------------------------------------------------------------------

func TestREPLToolName(t *testing.T) {
	r := New()
	if r.Name() != "Repl" {
		t.Errorf("expected Name 'REPL', got %q", r.Name())
	}
}

func TestREPLToolCheckPermissions(t *testing.T) {
	r := New()
	result := r.CheckPermissions(nil, nil)
	if _, ok := result.(types.PermissionAllowDecision); !ok {
		t.Errorf("expected PermissionAllowDecision, got %T", result)
	}
}

func TestREPLToolCallExecute(t *testing.T) {
	r := New()
	r.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		return "mock result", nil
	})

	input, _ := json.Marshal(replInput{Code: `console.log("hello from repl")`})
	result, err := r.Call(context.Background(), input, &tool.ToolUseContext{
		Options:    tool.ToolUseOptions{SessionID: "test-session"},
		WorkingDir: "/tmp",
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	output, ok := result.Data.(string)
	if !ok {
		t.Fatalf("expected string result, got %T", result.Data)
	}
	if !strings.Contains(output, "hello from repl") {
		t.Errorf("expected 'hello from repl', got %q", output)
	}
	r.CleanSession("test-session")
}

func TestREPLToolReset(t *testing.T) {
	r := New()
	r.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		return "", nil
	})

	input1, _ := json.Marshal(replInput{Code: `var replResetTest = 99`})
	_, err := r.Call(context.Background(), input1, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "test-reset"},
	})
	if err != nil {
		t.Fatalf("Call 1: %v", err)
	}

	inputReset, _ := json.Marshal(replInput{Reset: true})
	result, err := r.Call(context.Background(), inputReset, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "test-reset"},
	})
	if err != nil {
		t.Fatalf("Call reset: %v", err)
	}
	if !strings.Contains(result.Data.(string), "reset") {
		t.Errorf("expected reset message, got %q", result.Data)
	}

	input2, _ := json.Marshal(replInput{Code: `try { console.log(replResetTest) } catch(e) { console.log("gone") }`})
	result2, err := r.Call(context.Background(), input2, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "test-reset"},
	})
	if err != nil {
		t.Fatalf("Call 2: %v", err)
	}
	if !strings.Contains(result2.Data.(string), "gone") {
		t.Errorf("expected variable cleared after reset, got %q", result2.Data)
	}
	r.CleanSession("test-reset")
}

func TestREPLToolSessionIsolation(t *testing.T) {
	r := New()
	r.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		return "", nil
	})

	inputA, _ := json.Marshal(replInput{Code: `var isoTest = "A"`})
	_, err := r.Call(context.Background(), inputA, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "session-a"},
	})
	if err != nil {
		t.Fatalf("Call A: %v", err)
	}

	inputB, _ := json.Marshal(replInput{Code: `try { console.log(isoTest) } catch(e) { console.log("isolated") }`})
	resultB, err := r.Call(context.Background(), inputB, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "session-b"},
	})
	if err != nil {
		t.Fatalf("Call B: %v", err)
	}
	if !strings.Contains(resultB.Data.(string), "isolated") {
		t.Errorf("sessions should be isolated, got %q", resultB.Data)
	}
	r.CleanSession("session-a")
	r.CleanSession("session-b")
}

func TestREPLToolWithPragma(t *testing.T) {
	r := New()
	r.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		return "", nil
	})

	input, _ := json.Marshal(replInput{Code: "// @timeout: 5000\nconsole.log(\"pragma test\")"})
	result, err := r.Call(context.Background(), input, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "pragma-test"},
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(result.Data.(string), "pragma test") {
		t.Errorf("expected 'pragma test', got %q", result.Data)
	}
	r.CleanSession("pragma-test")
}

// ---------------------------------------------------------------------------
// Prompt test
// ---------------------------------------------------------------------------

func TestPromptContains(t *testing.T) {
	r := New()
	p := r.Prompt()

	checks := []string{
		"tool(name, argsJSON)",
		"console.log",
		"yield_control()",
		"exit()",
		"store",
		"load",
		"notify",
		"@timeout:",
		"setTimeout",
		"clearTimeout",
		"async/await",
	}
	for _, check := range checks {
		if !strings.Contains(p, check) {
			t.Errorf("prompt missing %q", check)
		}
	}
}

// ---------------------------------------------------------------------------
// Session cleanup tests
// ---------------------------------------------------------------------------

func TestCleanSession(t *testing.T) {
	r := New()
	r.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		return "", nil
	})

	ctx := context.Background()
	code := `console.log("hello")`

	// Create two sessions
	input1, _ := json.Marshal(replInput{Code: code})
	_, err := r.Call(ctx, input1, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "session-a"},
	})
	if err != nil {
		t.Fatalf("Call session-a: %v", err)
	}

	input2, _ := json.Marshal(replInput{Code: code})
	_, err = r.Call(ctx, input2, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "session-b"},
	})
	if err != nil {
		t.Fatalf("Call session-b: %v", err)
	}

	// Clean one session
	r.CleanSession("session-a")

	// session-a should be gone
	if _, ok := r.sessions.Load("session-a"); ok {
		t.Error("session-a should be removed after CleanSession")
	}
	// session-b should remain
	if _, ok := r.sessions.Load("session-b"); !ok {
		t.Error("session-b should still exist")
	}

	r.Close()
}

func TestCloseCleansAllSessions(t *testing.T) {
	r := New()
	r.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		return "", nil
	})

	ctx := context.Background()
	code := `console.log("hello")`

	for _, id := range []string{"s1", "s2", "s3"} {
		input, _ := json.Marshal(replInput{Code: code})
		_, err := r.Call(ctx, input, &tool.ToolUseContext{
			Options: tool.ToolUseOptions{SessionID: id},
		})
		if err != nil {
			t.Fatalf("Call %s: %v", id, err)
		}
	}

	r.Close()

	// All sessions should be gone
	for _, id := range []string{"s1", "s2", "s3"} {
		if _, ok := r.sessions.Load(id); ok {
			t.Errorf("session %q should be removed after Close()", id)
		}
	}
}

// ---------------------------------------------------------------------------
// Wait/Terminate integration tests
// ---------------------------------------------------------------------------

func TestREPLToolWaitSessionNotFound(t *testing.T) {
	r := New()
	input, _ := json.Marshal(replInput{Action: "wait", SessionID: "nonexistent"})
	_, err := r.Call(context.Background(), input, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got %q", err.Error())
	}
}

func TestREPLToolTerminateSessionNotFound(t *testing.T) {
	r := New()
	input, _ := json.Marshal(replInput{Action: "terminate", SessionID: "nonexistent"})
	_, err := r.Call(context.Background(), input, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got %q", err.Error())
	}
}

func TestREPLToolWaitResumesYieldedSession(t *testing.T) {
	r := New()
	r.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		return "", nil
	})

	ctx := context.Background()
	sessID := "wait-test"

	// Start a yielding execution in a goroutine.
	// Execute blocks at yield_control — yieldCh gets output but handleExecute
	// can't consume it because it's blocked waiting for Execute to return.
	done := make(chan string, 1)
	input, _ := json.Marshal(replInput{Code: `console.log("yielding"); yield_control()`})
	go func() {
		result, _ := r.Call(ctx, input, &tool.ToolUseContext{
			Options: tool.ToolUseOptions{SessionID: sessID},
		})
		if result != nil {
			done <- result.Data.(string)
		}
	}()

	// Wait for the session to appear (poll instead of fixed sleep)
	pollTimeout := time.After(3 * time.Second)
	for {
		if _, ok := r.sessions.Load(sessID); ok {
			break
		}
		select {
		case <-pollTimeout:
			t.Fatal("timed out waiting for session to appear")
		default:
			runtime.Gosched()
		}
	}
	if _, ok := r.sessions.Load(sessID); !ok {
		t.Fatal("session should exist")
	}

	// Call wait action — sends resume + reads yield output from yieldCh
	waitInput, _ := json.Marshal(replInput{Action: "wait", SessionID: sessID})
	waitResult, err := r.Call(ctx, waitInput, nil)
	if err != nil {
		t.Fatalf("wait Call: %v", err)
	}
	waitData := waitResult.Data.(string)
	if !strings.Contains(waitData, "YIELDED") {
		t.Errorf("wait result should contain YIELDED, got %q", waitData)
	}
	if !strings.Contains(waitData, "yielding") {
		t.Errorf("wait result should contain 'yielding', got %q", waitData)
	}

	r.CleanSession(sessID)
}

func TestREPLToolTerminateInterruptsSession(t *testing.T) {
	r := New()
	r.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		return "", nil
	})

	ctx := context.Background()
	sessID := "terminate-test"

	// Start an infinite loop execution.
	// JS stores a flag before looping — we poll for it as a reliable signal.
	done := make(chan error, 1)
	input, _ := json.Marshal(replInput{Code: `store("running", "1"); while(true) { }`})
	go func() {
		_, err := r.Call(ctx, input, &tool.ToolUseContext{
			Options: tool.ToolUseOptions{SessionID: sessID},
		})
		done <- err
	}()

	// Wait for JS eval to actually start (poll stored flag, not just session existence)
	pollTimeout := time.After(3 * time.Second)
	for {
		if v, ok := r.sessions.Load(sessID); ok {
			sess := v.(*Session)
			if val, _ := sess.kv.Load("running"); val == "1" {
				break
			}
		}
		select {
		case <-pollTimeout:
			t.Fatal("timed out waiting for JS eval to start")
		default:
			runtime.Gosched()
		}
	}
	if _, ok := r.sessions.Load(sessID); !ok {
		t.Fatal("session should exist")
	}

	// Terminate it
	termInput, _ := json.Marshal(replInput{Action: "terminate", SessionID: sessID})
	termResult, err := r.Call(ctx, termInput, nil)
	if err != nil {
		t.Fatalf("terminate Call: %v", err)
	}
	if termResult.Data.(string) != "Session terminated" {
		t.Errorf("terminate should return 'Session terminated', got %q", termResult.Data)
	}

	// Wait for the execution to finish
	select {
	case <-done:
		// Execution was interrupted as expected
	case <-time.After(3 * time.Second):
		t.Fatal("session execution did not terminate")
	}

	r.CleanSession(sessID)
}

// ---------------------------------------------------------------------------
// Async fallback test
// ---------------------------------------------------------------------------

func TestAsyncAwaitModule(t *testing.T) {
	s := newTestSession(t)
	ctx := context.Background()

	// Async function with await — must work via Compile+EvalBytecodeValue (EvalModule)
	output, err := s.Execute(ctx, `
async function fetchDouble(x) {
	return await Promise.resolve(x * 2);
}
const result = await fetchDouble(21);
console.log("result=" + result);
`, "", nil, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "result=42") {
		t.Errorf("expected 'result=42', got %q", output)
	}
}

func TestPromiseChain(t *testing.T) {
	s := newTestSession(t)
	ctx := context.Background()

	output, err := s.Execute(ctx, `
const p = Promise.resolve("hello");
const result = await p.then(s => s.toUpperCase());
console.log(result);
`, "", nil, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "HELLO") {
		t.Errorf("expected 'HELLO', got %q", output)
	}
}

func TestAsyncWithStoreLoad(t *testing.T) {
	s := newTestSession(t)
	ctx := context.Background()

	// Async code that uses store/load
	output, err := s.Execute(ctx, `
const data = await Promise.resolve([1, 2, 3]);
store("items", JSON.stringify(data));
const loaded = load("items");
console.log("loaded=" + loaded);
`, "", nil, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "loaded=[1,2,3]") {
		t.Errorf("expected 'loaded=[1,2,3]', got %q", output)
	}
}

// ---------------------------------------------------------------------------
// cwd accessibility from module scope
// ---------------------------------------------------------------------------

func TestCwdAccessibleFromModule(t *testing.T) {
	s := newTestSession(t)
	ctx := context.Background()

	// cwd must be accessible from ES module code (EvalModule).
	// Bug: var cwd in EvalGlobal is NOT visible from module scope.
	output, err := s.Execute(ctx, `console.log(cwd);`, "/home/test/dir", nil, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	output = strings.TrimSpace(output)
	if output != "/home/test/dir" {
		t.Errorf("cwd: got %q, want %q", output, "/home/test/dir")
	}
}

func TestCwdFallbackToGetwd(t *testing.T) {
	// When tctx.WorkingDir is empty, cwd should fall back to os.Getwd().
	// This mirrors Bash/Grep/Glob tools which all have the same fallback.
	s := newTestSession(t)
	ctx := context.Background()

	wd, _ := os.Getwd()
	output, err := s.Execute(ctx, `console.log(cwd);`, wd, nil, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	output = strings.TrimSpace(output)
	if output != wd {
		t.Errorf("cwd fallback: got %q, want %q", output, wd)
	}
}

func TestCall_CwdFallbackWhenWorkingDirEmpty(t *testing.T) {
	// Bug: handleExecute uses tctx.WorkingDir for cwd, but the engine
	// never sets WorkingDir on ToolUseContext. When WorkingDir is empty,
	// cwd must fall back to os.Getwd(), matching Bash/Grep/Glob tools.
	r := New()
	r.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		return "", nil
	})

	wd, _ := os.Getwd()
	code := `console.log(cwd);`
	input, _ := json.Marshal(replInput{Code: code})

	// Call with empty WorkingDir — should still inject cwd via os.Getwd() fallback
	result, err := r.Call(context.Background(), input, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "cwd-test"},
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	got := strings.TrimSpace(result.Data.(string))
	if got != wd {
		t.Errorf("cwd with empty WorkingDir: got %q, want %q (os.Getwd)", got, wd)
	}
}

// ---------------------------------------------------------------------------
// REPLTool: Aliases, Description, InputSchema, property accessors
// ---------------------------------------------------------------------------

func TestAliases(t *testing.T) {
	r := New()
	if r.Aliases() != nil {
		t.Errorf("expected nil aliases, got %v", r.Aliases())
	}
}

func TestDescription(t *testing.T) {
	r := New()

	// nil input → detailed description
	desc, err := r.Description(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(desc, "JavaScript") {
		t.Errorf("nil: expected JS description, got %q", desc)
	}

	// null input → detailed description
	desc, err = r.Description(json.RawMessage("null"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(desc, "JavaScript") {
		t.Errorf("null: expected JS description, got %q", desc)
	}

	// empty object → detailed description
	desc, err = r.Description(json.RawMessage("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(desc, "JavaScript") {
		t.Errorf("{}: expected JS description, got %q", desc)
	}

	// wait action
	waitInput, _ := json.Marshal(replInput{Action: "wait"})
	desc, err = r.Description(waitInput)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(desc, "Resume") {
		t.Errorf("wait: expected 'Resume', got %q", desc)
	}

	// terminate action
	termInput, _ := json.Marshal(replInput{Action: "terminate"})
	desc, err = r.Description(termInput)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(desc, "Terminate") {
		t.Errorf("terminate: expected 'Terminate', got %q", desc)
	}

	// with code → code as description
	codeInput, _ := json.Marshal(replInput{Code: "console.log(42)"})
	desc, err = r.Description(codeInput)
	if err != nil {
		t.Fatal(err)
	}
	if desc != "console.log(42)" {
		t.Errorf("code: expected code as description, got %q", desc)
	}

	// invalid json → fallback
	desc, err = r.Description(json.RawMessage("not json"))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "Execute JavaScript code" {
		t.Errorf("invalid json: expected fallback, got %q", desc)
	}

	// valid json but no action and no code → fallback
	resetInput, _ := json.Marshal(replInput{Reset: true})
	desc, err = r.Description(resetInput)
	if err != nil {
		t.Fatal(err)
	}
	if desc != "Execute JavaScript code" {
		t.Errorf("reset-only: expected fallback, got %q", desc)
	}
}

func TestInputSchema(t *testing.T) {
	r := New()
	schema := r.InputSchema()
	if !strings.Contains(string(schema), "properties") {
		t.Errorf("expected schema with properties, got %q", string(schema))
	}
	if !strings.Contains(string(schema), "code") {
		t.Errorf("expected schema with 'code', got %q", string(schema))
	}
}

func TestREPLToolProperties(t *testing.T) {
	r := New()
	if r.IsReadOnly(nil) != false {
		t.Error("IsReadOnly should return false")
	}
	if r.IsDestructive(nil) != false {
		t.Error("IsDestructive should return false")
	}
	if r.IsConcurrencySafe(nil) != false {
		t.Error("IsConcurrencySafe should return false")
	}
	if r.IsEnabled() != true {
		t.Error("IsEnabled should return true")
	}
	if r.InterruptBehavior() != tool.InterruptCancel {
		t.Errorf("InterruptBehavior should return InterruptCancel, got %v", r.InterruptBehavior())
	}
	if r.MaxResultSize() != 50000 {
		t.Errorf("MaxResultSize should return 50000, got %d", r.MaxResultSize())
	}
}

func TestRenderResult(t *testing.T) {
	r := New()
	if got := r.RenderResult(nil); got != "" {
		t.Errorf("nil: expected empty string, got %q", got)
	}
	if got := r.RenderResult("hello"); got != "hello" {
		t.Errorf("string: expected 'hello', got %q", got)
	}
	if got := r.RenderResult(42); got != "42" {
		t.Errorf("int: expected '42', got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Call dispatch edge cases
// ---------------------------------------------------------------------------

func TestREPLToolCallInvalidJSON(t *testing.T) {
	r := New()
	_, err := r.Call(context.Background(), json.RawMessage(`not json`), nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid REPL input") {
		t.Errorf("expected 'invalid REPL input', got %v", err)
	}
}

func TestREPLToolCallNoCodeNoAction(t *testing.T) {
	r := New()
	input, _ := json.Marshal(replInput{})
	_, err := r.Call(context.Background(), input, nil)
	if err == nil {
		t.Fatal("expected error for empty input")
	}
	if !strings.Contains(err.Error(), "requires code input or an action") {
		t.Errorf("expected 'requires code input or an action', got %v", err)
	}
}

func TestREPLToolCallNilTctx(t *testing.T) {
	r := New()
	r.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		return "", nil
	})
	// No tctx → generateSessionID() + cwd fallback to os.Getwd()
	input, _ := json.Marshal(replInput{Code: `console.log("auto session")`})
	result, err := r.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(result.Data.(string), "auto session") {
		t.Errorf("expected 'auto session', got %q", result.Data)
	}
	r.Close()
}

func TestREPLToolResetNonexistentSession(t *testing.T) {
	r := New()
	input, _ := json.Marshal(replInput{Reset: true})
	result, err := r.Call(context.Background(), input, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "no-such-session"},
	})
	if err != nil {
		t.Fatalf("expected no error for reset of nonexistent session, got %v", err)
	}
	if result.Data.(string) != "Session reset (new)" {
		t.Errorf("expected 'Session reset (new)', got %q", result.Data)
	}
}

func TestREPLToolNoToolExecutor(t *testing.T) {
	r := New() // No SetToolExecutor
	input, _ := json.Marshal(replInput{Code: `console.log("no executor")`})
	result, err := r.Call(context.Background(), input, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "no-exec-test"},
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(result.Data.(string), "no executor") {
		t.Errorf("expected 'no executor', got %q", result.Data)
	}
	r.CleanSession("no-exec-test")
}

func TestREPLToolToolExecutorError(t *testing.T) {
	r := New()
	r.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		return "", fmt.Errorf("tool execution failed")
	})
	input, _ := json.Marshal(replInput{Code: `const r = tool("Fail", "{}"); console.log(r)`})
	result, err := r.Call(context.Background(), input, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "tool-err-test"},
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	output := result.Data.(string)
	if !strings.Contains(output, "ERROR: tool execution failed") {
		t.Errorf("expected error prefix in output, got %q", output)
	}
	r.CleanSession("tool-err-test")
}

// ---------------------------------------------------------------------------
// handleWait edge cases: timeout and context cancel
// ---------------------------------------------------------------------------

func TestREPLToolWaitTimeout(t *testing.T) {
	r := New()
	sess, err := NewSession()
	if err != nil {
		t.Fatal(err)
	}
	r.sessions.Store("timeout-test", sess)

	// Mock timeAfter to fire immediately
	origTimeAfter := timeAfter
	timeAfter = func(d time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	defer func() { timeAfter = origTimeAfter }()

	input, _ := json.Marshal(replInput{Action: "wait", SessionID: "timeout-test"})
	_, err = r.Call(context.Background(), input, nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected timeout error, got %v", err)
	}
	r.CleanSession("timeout-test")
}

func TestREPLToolWaitContextCancel(t *testing.T) {
	r := New()
	sess, err := NewSession()
	if err != nil {
		t.Fatal(err)
	}
	r.sessions.Store("ctx-cancel-test", sess)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	input, _ := json.Marshal(replInput{Action: "wait", SessionID: "ctx-cancel-test"})
	go func() {
		_, err := r.Call(ctx, input, nil)
		done <- err
	}()

	runtime.Gosched() // yield to let goroutine enter handleWait
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from context cancel")
		}
		if !strings.Contains(err.Error(), "context") {
			t.Errorf("expected context error, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("wait did not return after context cancel")
	}
	r.CleanSession("ctx-cancel-test")
}

func TestREPLToolWaitResumeDefault(t *testing.T) {
	r := New()
	sess, err := NewSession()
	if err != nil {
		t.Fatal(err)
	}
	r.sessions.Store("resume-default", sess)

	// Fill resumeCh so the default branch fires in handleWait
	sess.resumeCh <- struct{}{}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	input, _ := json.Marshal(replInput{Action: "wait", SessionID: "resume-default"})
	_, err = r.Call(ctx, input, nil)
	if err == nil {
		t.Fatal("expected error (no yield available)")
	}
	if !strings.Contains(err.Error(), "context") {
		t.Errorf("expected context error, got %v", err)
	}
	r.CleanSession("resume-default")
}

// ---------------------------------------------------------------------------
// handleExecute yield path (yield_control + ctx cancel → YIELDED result)
// ---------------------------------------------------------------------------

func TestREPLToolYieldExecutePath(t *testing.T) {
	r := New()
	r.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		return "", nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	sessID := "yield-exec-path"

	done := make(chan string, 1)
	input, _ := json.Marshal(replInput{Code: `store("pre_yield", "1"); yield_control()`})
	go func() {
		result, _ := r.Call(ctx, input, &tool.ToolUseContext{
			Options: tool.ToolUseOptions{SessionID: sessID},
		})
		if result != nil {
			done <- result.Data.(string)
		}
	}()

	// Wait for yield by polling kv store (don't consume yieldCh)
	pollTimeout := time.After(3 * time.Second)
	for {
		if v, ok := r.sessions.Load(sessID); ok {
			sess := v.(*Session)
			if val, _ := sess.kv.Load("pre_yield"); val == "1" {
				break
			}
		}
		select {
		case <-pollTimeout:
			t.Fatal("timed out waiting for yield")
		default:
			runtime.Gosched()
		}
	}

	// yield_control sends to yieldCh before blocking on resumeCh.
	// Since C code is non-preemptible, yieldCh has data by the time
	// our poll loop observes "pre_yield" in the kv store.

	// Cancel context — yield_control returns, Execute completes,
	// handleExecute reads yieldCh → "YIELDED|..."
	cancel()

	select {
	case result := <-done:
		if !strings.Contains(result, "YIELDED") {
			t.Errorf("expected YIELDED result, got %q", result)
		}
		if !strings.Contains(result, sessID) {
			t.Errorf("expected session ID in result, got %q", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Call did not return after yield + cancel")
	}
	r.CleanSession(sessID)
}

// ---------------------------------------------------------------------------
// Session: console.error, double close, pending timers
// ---------------------------------------------------------------------------

func TestConsoleError(t *testing.T) {
	s := newTestSession(t)
	output, err := s.Execute(context.Background(), `console.error("oops")`, "", nil, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "[ERROR] oops") {
		t.Errorf("expected '[ERROR] oops', got %q", output)
	}
}

func TestDoubleClose(t *testing.T) {
	s, err := NewSession()
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s.Close() // Second close should not panic; hits s.closed early return
}

func TestResetWithPendingTimers(t *testing.T) {
	s := newTestSession(t)
	// Inject a pending timer directly (can't be created via Execute because
	// drainPendingTimers always runs before Execute returns)
	s.pendingTimers[1] = &timerEntry{
		timer:    time.NewTimer(10 * time.Second),
		fireTime: time.Time{}.Add(10 * time.Second),
	}
	if err := s.Reset(); err != nil {
		t.Fatalf("Reset with pending timers: %v", err)
	}
	if len(s.pendingTimers) != 0 {
		t.Errorf("pending timers should be cleared after reset, got %d", len(s.pendingTimers))
	}
}

func TestStopPendingTimersWithEntries(t *testing.T) {
	s := newTestSession(t)
	s.pendingTimers[1] = &timerEntry{
		timer:    time.NewTimer(10 * time.Second),
		fireTime: time.Time{}.Add(10 * time.Second),
	}
	s.pendingTimers[2] = &timerEntry{
		timer:    time.NewTimer(5 * time.Second),
		fireTime: time.Time{}.Add(5 * time.Second),
	}
	s.stopPendingTimers()
	if len(s.pendingTimers) != 0 {
		t.Errorf("expected no pending timers, got %d", len(s.pendingTimers))
	}
}

// ---------------------------------------------------------------------------
// drainPendingTimers: context cancel + callback errors
// ---------------------------------------------------------------------------

func TestDrainPendingTimersCtxCancel(t *testing.T) {
	s := newTestSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	toolFn := mockToolFn(t, nil)

	done := make(chan string, 1)
	go func() {
		output, _ := s.Execute(ctx, `setTimeout(function(){ store("fired", "yes") }, 5000); store("scheduled", "1")`, "", toolFn, 10000)
		done <- output
	}()

	// Wait for JS to schedule the timer (poll kv store)
	pollTimeout := time.After(3 * time.Second)
	for {
		if val, _ := s.kv.Load("scheduled"); val == "1" {
			break
		}
		select {
		case <-pollTimeout:
			t.Fatal("timed out waiting for timer to be scheduled")
		default:
			runtime.Gosched()
		}
	}
	cancel()

	select {
	case output := <-done:
		if strings.Contains(output, "yes") {
			t.Errorf("timer callback should not have fired after ctx cancel, got %q", output)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Execute didn't return after ctx cancel")
	}
}

func TestDrainPendingTimersCallbackError(t *testing.T) {
	s := newTestSession(t)
	ctx := context.Background()
	toolFn := mockToolFn(t, nil)

	output, err := s.Execute(ctx, `
		setTimeout(function() { throw new Error("timer callback error") }, 5);
		console.log("main code")
	`, "", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "main code") {
		t.Errorf("expected 'main code', got %q", output)
	}
	if !strings.Contains(output, "[Timer Error]") {
		t.Errorf("expected timer error in output, got %q", output)
	}
	if !strings.Contains(output, "timer callback error") {
		t.Errorf("expected 'timer callback error' in output, got %q", output)
	}
}

func TestDrainPendingTimersMultipleCallbackErrors(t *testing.T) {
	s := newTestSession(t)
	ctx := context.Background()
	toolFn := mockToolFn(t, nil)

	output, err := s.Execute(ctx, `
		setTimeout(function() { throw new Error("error1") }, 5);
		setTimeout(function() { throw new Error("error2") }, 10);
	`, "", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "error1") {
		t.Errorf("expected 'error1', got %q", output)
	}
	if !strings.Contains(output, "error2") {
		t.Errorf("expected 'error2', got %q", output)
	}
}

// ---------------------------------------------------------------------------
// tool() when no toolFn is set
// ---------------------------------------------------------------------------

func TestToolFnNotAvailable(t *testing.T) {
	s := newTestSession(t)
	// Execute with nil toolFn — session.toolFn stays nil
	output, err := s.Execute(context.Background(),
		`const r = tool("Echo", "{}"); console.log(r)`,
		"", nil, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "ERROR: tool executor not available") {
		t.Errorf("expected 'tool executor not available' error, got %q", output)
	}
}

// ---------------------------------------------------------------------------
// toGoNative: int, int64, default (fmt.Sprintf fallback)
// ---------------------------------------------------------------------------

func TestToGoNativeExtended(t *testing.T) {
	// int → float64
	got := toGoNative(42)
	if f, ok := got.(float64); !ok || f != 42.0 {
		t.Errorf("int: expected float64(42), got %v", got)
	}
	// int64 → float64
	got = toGoNative(int64(99))
	if f, ok := got.(float64); !ok || f != 99.0 {
		t.Errorf("int64: expected float64(99), got %v", got)
	}
	// default: complex → fmt.Sprintf fallback
	got = toGoNative(complex(1, 2))
	s, ok := got.(string)
	if !ok {
		t.Fatalf("complex: expected string, got %T", got)
	}
	if !strings.Contains(s, "(") {
		t.Errorf("complex: expected formatted complex number, got %q", s)
	}
}

// ---------------------------------------------------------------------------
// toInt64: int64, float64 NaN/negative/overflow, default
// ---------------------------------------------------------------------------

func TestToInt64Extended(t *testing.T) {
	// int64
	if got := toInt64(int64(42)); got != 42 {
		t.Errorf("int64(42): expected 42, got %d", got)
	}
	// float64 NaN → 0
	if got := toInt64(math.NaN()); got != 0 {
		t.Errorf("NaN: expected 0, got %d", got)
	}
	// float64 negative → 0
	if got := toInt64(-1.0); got != 0 {
		t.Errorf("negative: expected 0, got %d", got)
	}
	// float64 overflow (> 1<<62) → 0
	if got := toInt64(float64(1 << 63)); got != 0 {
		t.Errorf("overflow: expected 0, got %d", got)
	}
	// default → 0
	if got := toInt64("abc"); got != 0 {
		t.Errorf("string: expected 0, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// parseUint: empty string, invalid character
// ---------------------------------------------------------------------------

func TestParseUintExtended(t *testing.T) {
	// empty string
	_, err := parseUint("")
	if err == nil {
		t.Error("empty: expected error")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("empty: expected 'empty' error, got %v", err)
	}
	// invalid character
	_, err = parseUint("12a3")
	if err == nil {
		t.Error("invalid char: expected error")
	}
	if !strings.Contains(err.Error(), "invalid character") {
		t.Errorf("invalid char: expected 'invalid character', got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tool args: object (non-string) through tool()
// ---------------------------------------------------------------------------

func TestToolWithObjectArgs(t *testing.T) {
	s := newTestSession(t)
	toolFn := mockToolFn(t, map[string]string{
		"Echo": `{"result": "ok"}`,
	})

	// Pass JS object (not a string) — hits default case in tool() args handling
	output, err := s.Execute(context.Background(),
		`const r = tool("Echo", {msg: "hi"}); console.log(r)`,
		"", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "ok") {
		t.Errorf("expected tool result, got %q", output)
	}
}

// ---------------------------------------------------------------------------
// Remaining coverage: parsePragma via Call, toolExecutor success, closed session,
// compile error, output+error, drainPendingTimers vm nil, toInt64 float64
// ---------------------------------------------------------------------------

func TestREPLToolCallInvalidPragma(t *testing.T) {
	r := New()
	r.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		return "", nil
	})
	// parsePragma rejects timeout below minimum → handleExecute returns error
	input, _ := json.Marshal(replInput{Code: "// @timeout: 500\nconsole.log()"})
	_, err := r.Call(context.Background(), input, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "bad-pragma"},
	})
	if err == nil {
		t.Fatal("expected error for invalid pragma")
	}
	if !strings.Contains(err.Error(), "invalid @timeout") {
		t.Errorf("expected 'invalid @timeout', got %v", err)
	}
}

func TestREPLToolToolExecutorSuccess(t *testing.T) {
	r := New()
	r.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		return "tool result: " + name, nil
	})
	input, _ := json.Marshal(replInput{Code: `const r = tool("Echo", "{}"); console.log(r)`})
	result, err := r.Call(context.Background(), input, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "tool-ok-test"},
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(result.Data.(string), "tool result: Echo") {
		t.Errorf("expected 'tool result: Echo', got %q", result.Data)
	}
	r.CleanSession("tool-ok-test")
}

func TestREPLToolExecuteOnClosedSession(t *testing.T) {
	r := New()
	r.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		return "", nil
	})
	// Create a session via Call
	input, _ := json.Marshal(replInput{Code: `console.log("setup")`})
	_, err := r.Call(context.Background(), input, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "will-close"},
	})
	if err != nil {
		t.Fatalf("Call 1: %v", err)
	}
	// Close session directly without removing from map
	v, _ := r.sessions.Load("will-close")
	v.(*Session).Close()

	// Execute on closed session → "session closed" error
	input2, _ := json.Marshal(replInput{Code: `console.log("after")`})
	_, err = r.Call(context.Background(), input2, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "will-close"},
	})
	if err == nil {
		t.Fatal("expected error executing on closed session")
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Errorf("expected 'closed' error, got %v", err)
	}
	r.sessions.Delete("will-close")
}

func TestCompileError(t *testing.T) {
	s := newTestSession(t)
	output, err := s.Execute(context.Background(), `invalid {{{js`, "", nil, 10000)
	if err != nil {
		t.Fatalf("Execute should not return Go error for JS compile error: %v", err)
	}
	if !strings.Contains(output, "[JS Error]") {
		t.Errorf("expected JS error in output, got %q", output)
	}
}

func TestOutputAndError(t *testing.T) {
	s := newTestSession(t)
	output, err := s.Execute(context.Background(),
		`console.log("before error"); throw new Error("boom")`,
		"", nil, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "before error") {
		t.Errorf("expected 'before error', got %q", output)
	}
	if !strings.Contains(output, "[JS Error]") {
		t.Errorf("expected '[JS Error]', got %q", output)
	}
	if !strings.Contains(output, "boom") {
		t.Errorf("expected 'boom', got %q", output)
	}
	// Verify newline between console output and error
	if !strings.Contains(output, "before error\n\n[JS Error]") {
		t.Errorf("expected newline between output and error, got %q", output)
	}
}

func TestDrainPendingTimersVMNil(t *testing.T) {
	s := newTestSession(t)
	// Timer(0) fires immediately — channel has value without waiting
	timer := time.NewTimer(0)
	// Yield to runtime so the timer can deliver to the channel
	runtime.Gosched()
	s.pendingTimers[1] = &timerEntry{
		timer:    timer,
		fireTime: time.Time{},
	}
	// Nil out the VM — drainPendingTimers hits the `s.vm == nil` guard
	s.vm = nil
	errors := s.drainPendingTimers(context.Background())
	if errors != "" {
		t.Errorf("expected no errors, got %q", errors)
	}
	if len(s.pendingTimers) != 0 {
		t.Errorf("expected no pending timers, got %d", len(s.pendingTimers))
	}
}

func TestToInt64Float64Normal(t *testing.T) {
	// float64 normal return path — quickjs passes numbers as int64, not float64,
	// so this path is never hit through JS execution
	if got := toInt64(float64(42)); got != 42 {
		t.Errorf("float64(42): expected 42, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// Integration tests: full call chains through REPLTool.Call
// ---------------------------------------------------------------------------

func TestMultiTurnYieldConversation(t *testing.T) {
	// Full chain: execute → yield → wait → continue → yield → wait → finish
	r := New()
	r.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		return "", nil
	})
	ctx := context.Background()
	sessID := "multi-turn-yield"

	// Start JS that yields twice then finishes
	done := make(chan string, 1)
	input, _ := json.Marshal(replInput{Code: `
		console.log("turn1")
		yield_control()
		console.log("turn2")
		yield_control()
		console.log("turn3")
	`})
	go func() {
		result, _ := r.Call(ctx, input, &tool.ToolUseContext{
			Options: tool.ToolUseOptions{SessionID: sessID},
		})
		if result != nil {
			done <- result.Data.(string)
		}
	}()

	// Wait for session creation
	pollTimeout := time.After(5 * time.Second)
	for {
		if _, ok := r.sessions.Load(sessID); ok {
			break
		}
		select {
		case <-pollTimeout:
			t.Fatal("session not created")
		default:
			runtime.Gosched()
		}
	}

	// Turn 1: read first yield
	waitInput, _ := json.Marshal(replInput{Action: "wait", SessionID: sessID})
	result1, err := r.Call(ctx, waitInput, nil)
	if err != nil {
		t.Fatalf("wait 1: %v", err)
	}
	data1 := result1.Data.(string)
	if !strings.Contains(data1, "YIELDED") {
		t.Fatalf("wait 1: expected YIELDED, got %q", data1)
	}
	if !strings.Contains(data1, "turn1") {
		t.Errorf("wait 1: expected 'turn1', got %q", data1)
	}

	// Turn 2: read second yield
	result2, err := r.Call(ctx, waitInput, nil)
	if err != nil {
		t.Fatalf("wait 2: %v", err)
	}
	data2 := result2.Data.(string)
	if !strings.Contains(data2, "YIELDED") {
		t.Fatalf("wait 2: expected YIELDED, got %q", data2)
	}
	if !strings.Contains(data2, "turn2") {
		t.Errorf("wait 2: expected 'turn2', got %q", data2)
	}

	// Wait for goroutine to finish — final output should include turn3
	select {
	case finalData := <-done:
		if !strings.Contains(finalData, "turn3") {
			t.Errorf("final: expected 'turn3', got %q", finalData)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("execution didn't complete after second resume")
	}

	r.CleanSession(sessID)
}

func TestCrossCallPersistence(t *testing.T) {
	// Full chain: Call(store) → Call(load) → verify data survives across calls
	r := New()
	r.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		return "", nil
	})
	ctx := context.Background()

	// Call 1: store data
	input1, _ := json.Marshal(replInput{Code: `store("cross_test", "survives")`})
	_, err := r.Call(ctx, input1, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "persist-test"},
	})
	if err != nil {
		t.Fatalf("store call: %v", err)
	}

	// Call 2: load and verify
	input2, _ := json.Marshal(replInput{Code: `console.log(load("cross_test"))`})
	result, err := r.Call(ctx, input2, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "persist-test"},
	})
	if err != nil {
		t.Fatalf("load call: %v", err)
	}
	if !strings.Contains(result.Data.(string), "survives") {
		t.Errorf("expected 'survives' from cross-call load, got %q", result.Data)
	}

	// Call 3: overwrite and verify
	input3, _ := json.Marshal(replInput{Code: `store("cross_test", "updated"); console.log(load("cross_test"))`})
	result, err = r.Call(ctx, input3, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "persist-test"},
	})
	if err != nil {
		t.Fatalf("update call: %v", err)
	}
	if !strings.Contains(result.Data.(string), "updated") {
		t.Errorf("expected 'updated' after overwrite, got %q", result.Data)
	}

	r.CleanSession("persist-test")
}

func TestConcurrentSessions(t *testing.T) {
	// Two sessions running simultaneously — verify isolation
	r := New()
	r.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		return "", nil
	})
	ctx := context.Background()

	doneA := make(chan string, 1)
	doneB := make(chan string, 1)

	inputA, _ := json.Marshal(replInput{Code: `store("who", "A"); console.log(load("who"))`})
	inputB, _ := json.Marshal(replInput{Code: `store("who", "B"); console.log(load("who"))`})

	go func() {
		result, _ := r.Call(ctx, inputA, &tool.ToolUseContext{
			Options: tool.ToolUseOptions{SessionID: "concurrent-A"},
		})
		if result != nil {
			doneA <- result.Data.(string)
		}
	}()

	go func() {
		result, _ := r.Call(ctx, inputB, &tool.ToolUseContext{
			Options: tool.ToolUseOptions{SessionID: "concurrent-B"},
		})
		if result != nil {
			doneB <- result.Data.(string)
		}
	}()

	select {
	case resultA := <-doneA:
		if !strings.Contains(resultA, "A") {
			t.Errorf("session A: expected 'A', got %q", resultA)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session A didn't complete")
	}

	select {
	case resultB := <-doneB:
		if !strings.Contains(resultB, "B") {
			t.Errorf("session B: expected 'B', got %q", resultB)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session B didn't complete")
	}

	r.Close()
}

func TestResetClearsStateViaCall(t *testing.T) {
	// Full chain: Call(store) → Call(load, verify) → Call(reset) → Call(load, verify gone)
	r := New()
	r.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		return "", nil
	})
	ctx := context.Background()

	// Call 1: store data
	input1, _ := json.Marshal(replInput{Code: `store("clear_test", "before_reset")`})
	_, err := r.Call(ctx, input1, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "reset-clear"},
	})
	if err != nil {
		t.Fatalf("store call: %v", err)
	}

	// Call 2: verify data exists
	input2, _ := json.Marshal(replInput{Code: `console.log(load("clear_test"))`})
	result, err := r.Call(ctx, input2, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "reset-clear"},
	})
	if err != nil {
		t.Fatalf("load call: %v", err)
	}
	if !strings.Contains(result.Data.(string), "before_reset") {
		t.Fatalf("expected 'before_reset' before reset, got %q", result.Data)
	}

	// Call 3: reset
	resetInput, _ := json.Marshal(replInput{Reset: true})
	_, err = r.Call(ctx, resetInput, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "reset-clear"},
	})
	if err != nil {
		t.Fatalf("reset call: %v", err)
	}

	// Call 4: verify data is gone
	input4, _ := json.Marshal(replInput{Code: `const v = load("clear_test"); console.log(v === "" ? "cleared" : "leaked: " + v)`})
	result, err = r.Call(ctx, input4, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "reset-clear"},
	})
	if err != nil {
		t.Fatalf("verify call: %v", err)
	}
	if !strings.Contains(result.Data.(string), "cleared") {
		t.Errorf("expected 'cleared' after reset, got %q", result.Data)
	}

	r.CleanSession("reset-clear")
}
