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
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/eventloop"
)

// Timeout constants.
const (
	defaultTimeout = 120000 // 120s default JS execution timeout
	minTimeout     = 1000   // minimum @timeout pragma value (ms)
	maxTimeout     = 600000 // maximum @timeout pragma value (ms)
)

// Session wraps a goja event loop for JavaScript execution.
// Each SessionID maps to one Session in REPLTool's sync.Map.
type Session struct {
	loop       *eventloop.EventLoop
	vm         *goja.Runtime
	mu         sync.Mutex
	currentBuf *bytes.Buffer // swapped per Execute
	kv         sync.Map      // store()/load() persistent storage
	ctx        context.Context
	toolFn     func(ctx context.Context, name, argsJSON string) string // per-Execute tool executor
	closed     bool
}

// NewSession creates a new goja event loop session with custom console and JS globals.
func NewSession() (*Session, error) {
	s := &Session{
		loop: eventloop.NewEventLoop(eventloop.EnableConsole(false)),
	}

	var regErr error
	s.loop.Run(func(vm *goja.Runtime) {
		s.vm = vm
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
			for i := range parts {
				parts[i] = call.Arguments[i].String()
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

	// --- tool (reads s.toolFn directly) ---
	if err := vm.Set("tool", func(call goja.FunctionCall) goja.Value {
		name := call.Argument(0).String()
		argsVal := call.Argument(1)
		var argsJSON string
		if goja.IsUndefined(argsVal) {
			argsJSON = "{}"
		} else {
			switch v := argsVal.Export().(type) {
			case string:
				argsJSON = v
			default:
				b, err := json.Marshal(v)
				if err != nil {
					return vm.ToValue("ERROR: failed to marshal tool args: " + err.Error())
				}
				argsJSON = string(b)
			}
		}
		if s.toolFn == nil {
			return vm.ToValue("ERROR: tool executor not available")
		}
		return vm.ToValue(s.toolFn(s.ctx, name, argsJSON))
	}); err != nil {
		return fmt.Errorf("set tool: %w", err)
	}

	// --- exit (throws to stop JS execution) ---
	if err := vm.Set("exit", func(call goja.FunctionCall) goja.Value {
		panic(vm.ToValue("__EXIT__"))
	}); err != nil {
		return fmt.Errorf("set exit: %w", err)
	}

	// --- store/load ---
	if err := vm.Set("store", func(call goja.FunctionCall) goja.Value {
		key := call.Argument(0).String()
		val := call.Argument(1).Export()
		s.kv.Store(key, toGoNative(val))
		return goja.Undefined()
	}); err != nil {
		return fmt.Errorf("set store: %w", err)
	}

	if err := vm.Set("load", func(call goja.FunctionCall) goja.Value {
		key := call.Argument(0).String()
		v, ok := s.kv.Load(key)
		if !ok {
			return goja.Undefined()
		}
		switch val := v.(type) {
		case string:
			return vm.ToValue(val)
		default:
			b, _ := json.Marshal(val)
			return vm.ToValue(string(b))
		}
	}); err != nil {
		return fmt.Errorf("set load: %w", err)
	}

	// --- notify ---
	if err := vm.Set("notify", func(call goja.FunctionCall) goja.Value {
		value := call.Argument(0).String()
		if s.currentBuf != nil {
			s.currentBuf.WriteString("[NOTIFY] ")
			s.currentBuf.WriteString(value)
			s.currentBuf.WriteByte('\n')
		}
		return goja.Undefined()
	}); err != nil {
		return fmt.Errorf("set notify: %w", err)
	}

	// setTimeout/clearTimeout provided by eventloop — no manual registration needed.

	// --- __reportError (internal, used by async IIFE wrapper) ---
	if err := vm.Set("__reportError", func(call goja.FunctionCall) goja.Value {
		msg := call.Argument(0).String()
		if s.currentBuf != nil {
			if s.currentBuf.Len() > 0 {
				s.currentBuf.WriteByte('\n')
			}
			fmt.Fprintf(s.currentBuf, "[JS Error] %s\n", msg)
		}
		return goja.Undefined()
	}); err != nil {
		return fmt.Errorf("set __reportError: %w", err)
	}

	return nil
}

// Execute runs JavaScript code in the session's goja event loop.
// toolFn receives context for cancellation — threaded through to the injected toolExecutor.
func (s *Session) Execute(ctx context.Context, code string, cwd string, toolFn func(ctx context.Context, name, argsJSON string) string, timeoutMs int64) (output string, err error) {
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
		return "", fmt.Errorf("session closed")
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
	buf := new(bytes.Buffer)
	s.currentBuf = buf

	// Execute on event loop — Run() processes all async work until done.
	var evalErr error
	s.loop.Run(func(vm *goja.Runtime) {
		if cwd != "" {
			// Error ignored: vm.Set with fixed string key and string value cannot fail.
			_ = vm.Set("cwd", cwd)
		}

		// Wrap in async IIFE with try/catch — enables top-level await and captures errors.
		wrapped := "(async () => {\ntry {\n" + code + "\n} catch(e) {\nif (String(e) === \"__EXIT__\") return;\n__reportError(e instanceof Error ? e.stack || e.message : String(e));\n}\n})()"
		_, evalErr = vm.RunString(wrapped)
	})

	close(done) // release interrupt goroutine
	wg.Wait()   // ensure goroutine exits before we return

	// Build output.
	output = buf.String()
	if evalErr != nil {
		if isExitError(evalErr) {
			return output, nil
		}
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

	return output, nil
}

// isExitError checks if the error is from the exit() function.
func isExitError(err error) bool {
	ex, ok := err.(*goja.Exception)
	if !ok {
		return false
	}
	val := ex.Value()
	if val == nil {
		return false
	}
	s, ok := val.Export().(string)
	return ok && s == "__EXIT__"
}

// Reset clears the session by creating a new event loop and VM.
func (s *Session) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear store/load data
	s.kv.Range(func(key, _ any) bool {
		s.kv.Delete(key)
		return true
	})

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

	s.kv.Range(func(key, _ any) bool {
		s.kv.Delete(key)
		return true
	})
	s.toolFn = nil
	s.closed = true
}

// Interrupt sends an interrupt signal to the VM (for terminate action).
func (s *Session) Interrupt() {
	if s.vm != nil {
		s.vm.Interrupt("interrupted")
	}
}

func toGoNative(value any) any {
	if value == nil {
		return nil
	}
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return v
	case bool:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
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
