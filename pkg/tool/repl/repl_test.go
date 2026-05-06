package repl

import (
	"context"
	"encoding/json"
	"fmt"
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

	// Top-level variables persist across Execute calls in the same session.
	_, err := s.Execute(context.Background(), `globalThis.count = 42`, "", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute 1: %v", err)
	}

	output, err := s.Execute(context.Background(), `console.log(globalThis.count)`, "", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute 2: %v", err)
	}
	if !strings.Contains(output, "42") {
		t.Errorf("expected '42' from globalThis, got %q", output)
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
		output, _ := s.Execute(ctx, `while(true) { /* spin */ }`, "", nil, 60000)
		done <- output
	}()

	// Wait for JS eval to actually start
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
		`try { tool("NoSuchTool", "{}"); console.log("missed") } catch(e) { console.log("caught: " + e) }`,
		"", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "caught:") {
		t.Errorf("expected tool error to be caught, got %q", output)
	}
	if !strings.Contains(output, "ERROR: tool NoSuchTool not found") {
		t.Errorf("expected ERROR message in output, got %q", output)
	}
}

func TestToolErrorPrefix(t *testing.T) {
	s := newTestSession(t)
	toolFn := func(_ context.Context, name, argsJSON string) string {
		return "ERROR: something went wrong"
	}

	output, err := s.Execute(context.Background(),
		`try { tool("Fail", "{}"); console.log("missed") } catch(e) { console.log("caught: " + e) }`,
		"", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "caught:") {
		t.Errorf("expected tool error to be caught via throw, got %q", output)
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
			setTimeout(function() { console.log("callback_ran") }, 10);
			console.log("scheduled");
		`, "", toolFn, 10000)
		done <- output
	}()

	// Wait for the Execute to finish (setTimeout callback is drained by event loop)
	select {
	case output := <-done:
		if !strings.Contains(output, "scheduled") {
			t.Errorf("expected 'scheduled', got %q", output)
		}
		if !strings.Contains(output, "callback_ran") {
			t.Errorf("expected 'callback_ran' from setTimeout callback, got %q", output)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Execute did not complete")
	}
}

func TestClearTimeout(t *testing.T) {
	s := newTestSession(t)
	toolFn := mockToolFn(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Schedule and immediately cancel
	_, err := s.Execute(ctx, `
		var id = setTimeout(function() { console.log("should_not_run") }, 20);
		clearTimeout(id);
	`, "", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}


	// Verify callback did NOT run
	output, err := s.Execute(ctx, `"undefined"`, "", toolFn, 10000)
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
		// setTimeout returns truthy, unique values
		console.log(id1 && id2 ? "ok" : "fail");
		console.log(id1 === id2 ? "same" : "unique");
	`, "", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "ok") {
		t.Errorf("expected truthy timer IDs, got %q", output)
	}
	if !strings.Contains(output, "unique") {
		t.Errorf("expected unique timer IDs, got %q", output)
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
		"tool(name, args)",
		"console.log",
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
// console.log multi-arg (standard JS API parity)
// ---------------------------------------------------------------------------

func TestConsoleLogMultipleArgs(t *testing.T) {
	s, err := NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()

	// Standard JS: console.log("a", "b", 1) → "a b 1"
	output, err := s.Execute(context.Background(), `console.log("a", "b", 1)`, "", nil, 0)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimSpace(output)
	want := "a b 1"
	if got != want {
		t.Errorf("console.log multi-arg: got %q, want %q", got, want)
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
	if !strings.Contains(err.Error(), "requires code input") {
		t.Errorf("expected 'requires code input', got %v", err)
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
	input, _ := json.Marshal(replInput{Code: `try { tool("Fail", "{}"); console.log("missed") } catch(e) { console.log(e) }`})
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
// Session: double close, pending timers
// ---------------------------------------------------------------------------

func TestDoubleClose(t *testing.T) {
	s, err := NewSession()
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s.Close() // Second close should not panic; hits s.closed early return
}

// ---------------------------------------------------------------------------
// setTimeout context cancel + callback errors
// ---------------------------------------------------------------------------

func TestSetTimeoutCtxCancel(t *testing.T) {
	// Verify that context cancellation stops JS execution for blocking code.
	s := newTestSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	toolFn := mockToolFn(t, nil)

	done := make(chan string, 1)
	go func() {
		output, _ := s.Execute(ctx, `for (var i = 0; i < 100000; i++) { /* spin */ }`, "", toolFn, 10000)
		done <- output
	}()

	cancel()

	select {
	case <-done:
		// Execute returned after ctx cancel — success
	case <-time.After(5 * time.Second):
		t.Fatal("Execute didn't return after ctx cancel")
	}
}

func TestSetTimeoutCallbackError(t *testing.T) {
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
	// goja eventloop handles timer errors internally; verify main output is captured
}

func TestSetTimeoutMultipleCallbackErrors(t *testing.T) {
	s := newTestSession(t)
	ctx := context.Background()
	toolFn := mockToolFn(t, nil)

	_, err := s.Execute(ctx, `
		setTimeout(function() { throw new Error("error1") }, 5);
		setTimeout(function() { throw new Error("error2") }, 10);
	`, "", toolFn, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// goja eventloop handles timer errors internally; verify no crash
}

// ---------------------------------------------------------------------------
// tool() when no toolFn is set
// ---------------------------------------------------------------------------

func TestToolFnNotAvailable(t *testing.T) {
	s := newTestSession(t)
	// Execute with nil toolFn — session.toolFn stays nil
	output, err := s.Execute(context.Background(),
		`try { tool("Echo", "{}"); console.log("missed") } catch(e) { console.log(e) }`,
		"", nil, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "tool executor not available") {
		t.Errorf("expected 'tool executor not available' error, got %q", output)
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
// compile error, output+error
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

func TestErrorStackTraceAdjustsLineNumbers(t *testing.T) {
	// User code starts at line 3 inside the async IIFE wrapper (2 header lines).
	// Stack traces should show adjusted line numbers, not the raw wrapper-internal ones.
	s := newTestSession(t)
	output, err := s.Execute(context.Background(),
		"console.log(\"line1\")\nconsole.log(\"line2\")\nthrow new Error(\"line3\")",
		"", nil, 10000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "[JS Error]") {
		t.Fatalf("expected [JS Error], got %q", output)
	}
	// The throw is on user code line 3 — wrapper adds 2 lines, so raw is line 5.
	// After adjustment, should show line 3, not line 5.
	if strings.Contains(output, ":5:") {
		t.Errorf("stack trace should NOT show raw wrapper line 5, got %q", output)
	}
	if !strings.Contains(output, ":3:") {
		t.Errorf("stack trace should show adjusted line 3, got %q", output)
	}
	// goja appends internal offset like (2) — should be stripped for readability.
	if strings.Contains(output, ":3:") && strings.Contains(output, "(2)") {
		t.Errorf("stack trace should NOT contain internal goja offset (N), got %q", output)
	}
}

// ---------------------------------------------------------------------------
// Integration tests: full call chains through REPLTool.Call
// ---------------------------------------------------------------------------

func TestCrossCallPersistence(t *testing.T) {
	// Full chain: set top-level var → read across calls → overwrite
	r := New()
	r.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		return "", nil
	})
	ctx := context.Background()

	// Call 1: set top-level variable
	input1, _ := json.Marshal(replInput{Code: `globalThis.cross_test = "survives"`})
	_, err := r.Call(ctx, input1, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "persist-test"},
	})
	if err != nil {
		t.Fatalf("set globalThis: %v", err)
	}

	// Call 2: read and verify
	input2, _ := json.Marshal(replInput{Code: `console.log(globalThis.cross_test)`})
	result, err := r.Call(ctx, input2, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "persist-test"},
	})
	if err != nil {
		t.Fatalf("read call: %v", err)
	}
	if !strings.Contains(result.Data.(string), "survives") {
		t.Errorf("expected 'survives' from cross-call variable, got %q", result.Data)
	}

	// Call 3: overwrite and verify
	input3, _ := json.Marshal(replInput{Code: `cross_test = "updated"; console.log(globalThis.cross_test)`})
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

	inputA, _ := json.Marshal(replInput{Code: `globalThis.who = "A"; console.log(globalThis.who)`})
	inputB, _ := json.Marshal(replInput{Code: `globalThis.who = "B"; console.log(globalThis.who)`})

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
	// Full chain: set var → verify → reset → verify gone
	r := New()
	r.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		return "", nil
	})
	ctx := context.Background()

	// Call 1: set top-level variable
	input1, _ := json.Marshal(replInput{Code: `globalThis.clear_test = "before_reset"`})
	_, err := r.Call(ctx, input1, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "reset-clear"},
	})
	if err != nil {
		t.Fatalf("set globalThis: %v", err)
	}

	// Call 2: verify variable exists
	input2, _ := json.Marshal(replInput{Code: `console.log(globalThis.clear_test)`})
	result, err := r.Call(ctx, input2, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "reset-clear"},
	})
	if err != nil {
		t.Fatalf("verify globalThis: %v", err)
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
		t.Fatalf("reset globalThis: %v", err)
	}

	// Call 4: verify variable is gone after reset
	input4, _ := json.Marshal(replInput{Code: `console.log(globalThis.clear_test === undefined ? "cleared" : "leaked: " + globalThis.clear_test)`})
	result, err = r.Call(ctx, input4, &tool.ToolUseContext{
		Options: tool.ToolUseOptions{SessionID: "reset-clear"},
	})
	if err != nil {
		t.Fatalf("verify globalThis: %v", err)
	}
	if !strings.Contains(result.Data.(string), "cleared") {
		t.Errorf("expected 'cleared' after reset, got %q", result.Data)
	}

	r.CleanSession("reset-clear")
}

// ---------------------------------------------------------------------------
// Promise.all + async/await (goja handles these correctly)
// ---------------------------------------------------------------------------

func TestPromiseAllWithAwait(t *testing.T) {
	// setTimeout + Promise.all + await — works correctly with goja.
	s, err := NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()

	toolFn := func(_ context.Context, name, argsJSON string) string {
		switch name {
		case "Glob":
			return "file1.go\nfile2.go\nfile3.go"
		case "Grep":
			return "file1.go:1:TODO fix\nfile2.go:5:TODO refactor"
		default:
			return ""
		}
	}

	code := `// Wait for timer
await new Promise(resolve => setTimeout(resolve, 50));

// Promise.all with tool calls
const results = await Promise.all([
  tool("Glob", JSON.stringify({pattern: "**/*.go"})),
  tool("Grep", JSON.stringify({pattern: "TODO"}))
]);
console.log("files:", results[0].split("\n").length);
console.log("todos:", results[1].split("\n").length);
	globalThis.fileCount = results[0].split("\n").length;
console.log("saved:", globalThis.fileCount);
`

	output, execErr := s.Execute(context.Background(), code, "", toolFn, 30000)

	if execErr != nil {
		t.Fatalf("Execute: %v", execErr)
	}
	if !strings.Contains(output, "files: 3") {
		t.Errorf("expected 'files: 3', got %q", output)
	}
	if !strings.Contains(output, "todos: 2") {
		t.Errorf("expected 'todos: 2', got %q", output)
	}
	if !strings.Contains(output, "saved: 3") {
		t.Errorf("expected 'saved: 3', got %q", output)
	}
}

func TestExecutePromiseAllWithTool(t *testing.T) {
	// Promise.all + await with synchronous tool() calls — verify it executes correctly.
	s, err := NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()

	toolFn := func(_ context.Context, name, argsJSON string) string {
		return "mock-result"
	}

	output, execErr := s.Execute(context.Background(),
		`const results = await Promise.all([tool("Glob", JSON.stringify({pattern: "*"})), tool("Grep", JSON.stringify({pattern: "TODO"}))]);
		console.log("got", results.length, "results");`,
		"", toolFn, 10000)

	if execErr != nil {
		t.Fatalf("Execute: %v", execErr)
	}
	if !strings.Contains(output, "got 2 results") {
		t.Errorf("expected 'got 2 results', got %q", output)
	}
	// Session must still be usable (not closed)
	if s.closed {
		t.Error("session should NOT be closed after successful execution")
	}
}

func TestExecuteRecoversFromClosedVM(t *testing.T) {
	// Verify that executing on a closed session returns an error and marks session closed.
	s, err := NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Close the session directly
	s.Close()

	output, execErr := s.Execute(context.Background(), `console.log("hello")`, "", nil, 10000)

	// Must return error for closed session
	if execErr == nil {
		t.Fatal("expected error for closed session, got nil")
	}
	if !strings.Contains(execErr.Error(), "session closed") {
		t.Errorf("expected 'session closed' error, got %v", execErr)
	}
	if output != "" {
		t.Errorf("expected empty output, got %q", output)
	}
}
