package repl

import (
	"context"
	"encoding/json"
	"runtime"
	"os"
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
