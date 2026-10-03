// Package repl implements the REPL tool for gbot.
//
// Powered by goja — Pure Go JavaScript engine with ES6+ support.
// Event loop provided by goja_nodejs for setTimeout/Promise integration.
package repl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/eventloop"
	"github.com/dop251/goja_nodejs/require"
	// Node-compatible builtin modules — import for side effects (init registers them).
	_ "github.com/dop251/goja_nodejs/buffer"
	_ "github.com/dop251/goja_nodejs/process"
	_ "github.com/dop251/goja_nodejs/url"
	_ "github.com/dop251/goja_nodejs/util"
)

// Timeout constants.
const (
	defaultTimeout = 120000 // 120s default JS execution timeout
	minTimeout     = 1000   // minimum @timeout pragma value (ms)
	maxTimeout     = 600000 // maximum @timeout pragma value (ms)
)

// replToolName is the engine-registered name of this tool. The tools object
// filters it out so JS code cannot re-enter the REPL recursively.
const replToolName = "Repl"

// maxImagesPerExecute caps image() evidence per Execute — images ride the
// tool_result wire and would otherwise blow up token cost unchecked.
const maxImagesPerExecute = 8

// ToolMeta is one entry of the fresh tool inventory injected per Execute.
type ToolMeta struct {
	Name        string
	Description string
}

// ToolCallFn executes one engine tool call with full permission checking.
// The error return replaces the old "ERROR: " string-prefix convention:
// session.go turns it into a JS throw with the original message.
type ToolCallFn func(ctx context.Context, name, argsJSON string) (string, error)

// Session wraps a goja event loop for JavaScript execution.
// Each SessionID maps to one Session in REPLTool's sync.Map.
type Session struct {
	loop       *eventloop.EventLoop
	vm         *goja.Runtime
	mu         sync.Mutex
	currentBuf *bytes.Buffer // swapped per Execute
	// currentEvidence collects image() paths for this Execute; Execute
	// snapshots it under the lock before returning so callers never touch
	// the live field.
	currentEvidence []string
	ctx             context.Context
	toolFn          ToolCallFn // per-Execute tool executor
	toolLister      func() []ToolMeta
	closed          bool
}

// NewSession creates a new goja event loop session with custom console and JS globals.
func NewSession() (*Session, error) {
	s := &Session{
		loop: eventloop.NewEventLoop(eventloop.EnableConsole(false)),
	}

	var regErr error
	s.loop.Run(func(vm *goja.Runtime) {
		s.vm = vm
		// Enable require + Node-compatible builtins (buffer, process, url, util).
		// Modules self-register via init(); importing them is sufficient.
		require.NewRegistry().Enable(vm)
		regErr = s.registerGlobals(vm)
	})
	if regErr != nil {
		return nil, fmt.Errorf("register globals: %w", regErr)
	}

	return s, nil
}

// registerGlobals sets up all JS global functions on the VM.
// Called once at session creation and after Reset().
func (s *Session) registerGlobals(vm *goja.Runtime) error {
	// --- console (captures to currentBuf, multi-arg) ---
	consoleObj := vm.NewObject()
	if err := consoleObj.Set("log", func(call goja.FunctionCall) goja.Value {
		if s.currentBuf != nil {
			parts := make([]string, len(call.Arguments))
			for i, arg := range call.Arguments {
				parts[i] = jsValueToString(arg)
			}
			fmt.Fprintf(s.currentBuf, "%s\n", strings.Join(parts, " "))
		}
		return goja.Undefined()
	}); err != nil {
		return fmt.Errorf("set console.log: %w", err)
	}

	if err := vm.Set("console", consoleObj); err != nil {
		return fmt.Errorf("set console: %w", err)
	}

	// --- unhandled Promise rejection tracking ---
	vm.SetPromiseRejectionTracker(func(p *goja.Promise, op goja.PromiseRejectionOperation) {
		if op == goja.PromiseRejectionReject && s.currentBuf != nil {
			if s.currentBuf.Len() > 0 {
				s.currentBuf.WriteByte('\n')
			}
			fmt.Fprintf(s.currentBuf, "[JS Error] Unhandled promise rejection: %v\n", p.Result())
		}
	})

	// --- image(path): queue a local image file as evidence for this Execute ---
	if err := vm.Set("image", func(call goja.FunctionCall) goja.Value {
		pathVal := call.Argument(0)
		path := pathVal.String()
		if goja.IsUndefined(pathVal) || goja.IsNull(pathVal) || path == "" {
			panic(vm.ToValue("image: path required"))
		}
		s.currentEvidence = append(s.currentEvidence, path)
		return vm.ToValue(path)
	}); err != nil {
		return fmt.Errorf("set image: %w", err)
	}

	// setTimeout/clearTimeout provided by eventloop — no manual registration needed.

	// tools/ALL_TOOLS with an empty inventory; Execute reinstalls a fresh
	// snapshot from the lister before every run.
	s.installToolsGlobals(vm)

	// Plugin harness scripts: evaluated once per session, after tools.* are
	// registered so they can compose over them. Scripts hang state off
	// globalThis (e.g. closures on browser.*); re-evaluating per Execute
	// would silently wipe it. A broken script is skipped (warn) — one plugin
	// must not take down the session for every other plugin.
	for _, ps := range replScripts {
		if _, err := vm.RunString(ps.Source); err != nil {
			slog.Warn("repl: plugin script failed to load", "script", ps.Name, "error", err)
		}
	}

	// --- __reportError (internal, used by async IIFE wrapper) ---
	if err := vm.Set("__reportError", func(call goja.FunctionCall) goja.Value {
		msg := call.Argument(0).String()
		if s.currentBuf != nil {
			if s.currentBuf.Len() > 0 {
				s.currentBuf.WriteByte('\n')
			}
			adjusted := adjustStackLines(msg, 2)
			fmt.Fprintf(s.currentBuf, "[JS Error] %s\n", adjusted)
		}
		return goja.Undefined()
	}); err != nil {
		return fmt.Errorf("set __reportError: %w", err)
	}

	return nil
}

// installToolsGlobals rebuilds the tools object and ALL_TOOLS from the current
// tool lister. Runs on every Execute: MCP servers connect and disconnect
// between runs, so a snapshot taken at session creation would go stale.
func (s *Session) installToolsGlobals(vm *goja.Runtime) {
	toolsObj := vm.NewObject()
	entries := []any{}
	if s.toolLister != nil {
		// Distinct names can sanitize to the same identifier ("a-b" and
		// "a_b" both become "a_b"); first arrival wins so tools and
		// ALL_TOOLS never disagree on the callable set.
		seen := make(map[string]bool)
		for _, meta := range s.toolLister() {
			if meta.Name == replToolName {
				continue
			}
			key := normalizeIdentifier(meta.Name)
			if seen[key] {
				continue
			}
			seen[key] = true
			if err := toolsObj.Set(key, s.newToolInvoker(vm, meta.Name)); err != nil {
				continue
			}
			entry := vm.NewObject()
			_ = entry.Set("name", key)
			_ = entry.Set("description", meta.Description)
			entries = append(entries, entry)
		}
	}
	// vm.Set with fixed string keys and freshly built values cannot fail.
	_ = vm.Set("tools", toolsObj)
	_ = vm.Set("ALL_TOOLS", vm.NewArray(entries...))
}

// newToolInvoker builds the JS function for one tools.<Name> property. It
// captures the real tool name; normalizeIdentifier only shapes the property
// key the caller sees.
func (s *Session) newToolInvoker(vm *goja.Runtime, name string) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		argsVal := call.Argument(0)
		var argsJSON string
		if goja.IsUndefined(argsVal) || goja.IsNull(argsVal) {
			argsJSON = "{}"
		} else if str, ok := argsVal.Export().(string); ok {
			argsJSON = str
		} else {
			b, err := json.Marshal(argsVal.Export())
			if err != nil {
				panic(vm.ToValue("failed to marshal tool args: " + err.Error()))
			}
			argsJSON = string(b)
		}
		if s.toolFn == nil {
			panic(vm.ToValue("tool executor not available"))
		}
		result, err := s.toolFn(s.ctx, name, argsJSON)
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		// Mirror JSON.parse: JSON output reaches JS as a real object/array,
		// non-JSON output stays a string.
		var parsed any
		if json.Unmarshal([]byte(result), &parsed) == nil {
			return vm.ToValue(parsed)
		}
		return vm.ToValue(result)
	}
}

// normalizeIdentifier rewrites a tool name into a valid JS identifier,
// mirroring codex normalize_code_mode_identifier: invalid characters become
// '_', and a non-letter first character becomes '_' too.
func normalizeIdentifier(name string) string {
	runes := []rune(name)
	out := make([]rune, 0, len(runes))
	for i, ch := range runes {
		letter := (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
		digit := ch >= '0' && ch <= '9'
		if ch == '_' || ch == '$' || letter || (i > 0 && digit) {
			out = append(out, ch)
		} else {
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "_"
	}
	return string(out)
}

// Execute runs JavaScript code in the session's goja event loop.
// toolFn receives context for cancellation — threaded through to the injected toolExecutor.
// toolLister is this run's tool inventory source; evidence returns the image()
// paths collected during the run, snapshotted under the lock so the caller
// never races a concurrent Execute or Close on the live field.
func (s *Session) Execute(ctx context.Context, code string, cwd string, toolFn ToolCallFn, timeoutMs int64, toolLister func() []ToolMeta) (output string, evidence []string, err error) {
	// Catch panics from goja — mark session unusable.
	defer func() {
		if r := recover(); r != nil {
			s.mu.Lock()
			s.closed = true
			s.mu.Unlock()
			output = fmt.Sprintf("[JS fatal] %v", r)
			err = nil
		}
	}()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return "", nil, fmt.Errorf("session closed")
	}

	// Clear any leftover interrupt from previous execution.
	s.vm.ClearInterrupt()

	if timeoutMs <= 0 {
		timeoutMs = defaultTimeout
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	// Interrupt goroutine — signals VM on timeout or context cancel.
	// Uses WaitGroup to prevent race: after close(done), we wait for the
	// goroutine to exit before returning. Without this, a deferred cancel()
	// can fire while the goroutine is still in select, causing it to pick
	// timeoutCtx.Done() and poison the VM for the next Execute call.
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		select {
		case <-timeoutCtx.Done():
			s.vm.Interrupt(timeoutCtx.Err().Error())
		case <-done:
		}
	})

	s.ctx = timeoutCtx
	if toolFn != nil {
		s.toolFn = toolFn
	}
	s.toolLister = toolLister
	buf := new(bytes.Buffer)
	s.currentBuf = buf
	s.currentEvidence = nil

	// Execute on event loop — Run() processes all async work until done.
	var evalErr error
	s.loop.Run(func(vm *goja.Runtime) {
		if cwd != "" {
			// Error ignored: vm.Set with fixed string key and string value cannot fail.
			_ = vm.Set("cwd", cwd)
		}
		s.installToolsGlobals(vm)

		// Wrap in async IIFE with try/catch — enables top-level await and captures errors.
		wrapped := "(async () => {\ntry {\n" + code + "\n} catch(e) {\n__reportError(e instanceof Error ? e.stack || e.message : String(e));\n}\n})()"
		_, evalErr = vm.RunString(wrapped)
	})

	close(done) // release interrupt goroutine
	wg.Wait()   // ensure goroutine exits before we return

	// Build output.
	output = buf.String()
	if evalErr != nil {
		if output != "" {
			output += "\n"
		}
		// Distinguish interrupt (timeout/cancel) from regular JS errors.
		if _, ok := evalErr.(*goja.InterruptedError); ok {
			if timeoutCtx.Err() == context.DeadlineExceeded {
				output += "[JS Error] execution timeout"
			} else {
				output += "[JS Error] " + evalErr.Error()
			}
		} else {
			output += "[JS Error] " + evalErr.Error()
		}
	}

	// Snapshot evidence while the lock is still held — callers read the copy
	// after Execute returns, when another goroutine may already be running
	// the next Execute or Close.
	evidence = make([]string, len(s.currentEvidence))
	copy(evidence, s.currentEvidence)

	return output, evidence, nil
}

// Reset clears the session by creating a new event loop and VM.
func (s *Session) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Create new event loop (old one is GC'd)
	s.loop = eventloop.NewEventLoop(eventloop.EnableConsole(false))
	var regErr error
	s.loop.Run(func(vm *goja.Runtime) {
		s.vm = vm
		regErr = s.registerGlobals(vm)
	})
	if regErr != nil {
		s.closed = true
		return fmt.Errorf("reset register globals: %w", regErr)
	}

	return nil
}

// Close marks the session as closed and clears state.
// Safe to call while Execute is running — Interrupts the VM first
// so Execute can complete and release the mutex.
func (s *Session) Close() {
	// Interrupt VM first so any in-flight Execute can complete
	if s.vm != nil {
		s.vm.Interrupt("closing")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return
	}
	s.toolFn = nil
	s.toolLister = nil
	s.closed = true
}

// Interrupt sends an interrupt signal to the VM (for terminate action).
func (s *Session) Interrupt() {
	if s.vm != nil {
		s.vm.Interrupt("interrupted")
	}
}

// jsValueToString converts a goja Value to a human-readable string.
// Objects/arrays are JSON-serialized; primitives use their string representation.
func jsValueToString(v goja.Value) string {
	if goja.IsUndefined(v) {
		return "undefined"
	}
	if goja.IsNull(v) {
		return "null"
	}
	exported := v.Export()
	switch val := exported.(type) {
	case string:
		return val
	case float64, int, int64, bool:
		return v.String()
	default:
		// Objects, arrays, maps — JSON serialize
		b, err := json.Marshal(val)
		if err != nil {
			return v.String()
		}
		return string(b)
	}
}

// adjustStackLines adjusts line numbers in a JS stack trace by subtracting offset.
// The async IIFE wrapper adds 2 lines before user code, so goja reports
// line N+2 for user code at line N. This function corrects the numbers.
var stackLineRe = regexp.MustCompile(`(<eval>:)(\d+)`)
var parenOffsetRe = regexp.MustCompile(`(<eval>:\d+:\d+)\(\d+\)`)

func adjustStackLines(msg string, offset int) string {
	// Strip goja internal offset suffix: <eval>:3:7(14) → <eval>:3:7
	msg = parenOffsetRe.ReplaceAllString(msg, "${1}")
	return stackLineRe.ReplaceAllStringFunc(msg, func(match string) string {
		parts := stackLineRe.FindStringSubmatch(match)
		if len(parts) == 3 {
			if n, err := strconv.Atoi(parts[2]); err == nil {
				adjusted := max(n-offset, 1)
				return parts[1] + strconv.Itoa(adjusted)
			}
		}
		return match
	})
}

func parsePragma(code string) (cleanCode string, timeoutMs int64, err error) {
	timeoutMs = defaultTimeout
	if !strings.HasPrefix(code, "// @timeout:") {
		return code, timeoutMs, nil
	}
	firstLine, rest, _ := strings.Cut(code, "\n")
	val := strings.TrimSpace(strings.TrimPrefix(firstLine, "// @timeout:"))
	ms, parseErr := parseUint(val)
	if parseErr != nil || ms < minTimeout || ms > maxTimeout {
		return "", 0, fmt.Errorf("invalid @timeout: must be %d-%d ms, got %q", minTimeout, maxTimeout, val)
	}
	return rest, ms, nil
}

func parseUint(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid character: %c", c)
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}
