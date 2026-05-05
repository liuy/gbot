// Package repl implements the REPL tool for gbot.
//
// Source reference: Codex RS code_mode (active, codex-rs/code-mode/src/)
// Powered by modernc.org/quickjs — Pure Go QuickJS engine with ES2023 support.
package repl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"modernc.org/quickjs"
)

// Resource and timeout constants.
const (
	vmMemoryLimit  = 32 * 1024 * 1024 // 32MB per VM
	defaultTimeout = 120000           // 120s default JS execution timeout
	maxTimers      = 1000             // max concurrent setTimeout timers
	minTimeout     = 1000             // minimum @timeout pragma value (ms)
	maxTimeout     = 600000           // maximum @timeout pragma value (ms)
)

// Session — wraps a QuickJS VM instance.

// Session wraps a QuickJS VM for JavaScript execution.
// Each SessionID maps to one Session in REPLTool's sync.Map.
type Session struct {
	vm            *quickjs.VM
	mu            sync.Mutex
	currentBuf    *bytes.Buffer // swapped per Execute
	kv            sync.Map      // store()/load() persistent storage
	ctx           context.Context
	toolFn        func(ctx context.Context, name, argsJSON string) string // per-Execute tool executor
	closed        bool
	pendingTimers map[int64]*timerEntry // id → timer (accessed under mu)
}

// timerEntry pairs a timer with its scheduled fire time.
type timerEntry struct {
	timer    *time.Timer
	fireTime time.Time
}

// NewSession creates a new QuickJS VM session with custom console and JS globals.
func NewSession() (*Session, error) {
	vm, err := quickjs.NewVM()
	if err != nil {
		return nil, fmt.Errorf("quickjs NewVM: %w", err)
	}
	vm.SetMemoryLimit(vmMemoryLimit)

	s := &Session{
		vm:            vm,
		pendingTimers: make(map[int64]*timerEntry),
	}

	if err := s.registerGlobals(); err != nil {
		_ = vm.Close()
		return nil, fmt.Errorf("register globals: %w", err)
	}

	return s, nil
}

// registerGlobals sets up all JS global functions on the VM.
// Called once at session creation and after Reset().
func (s *Session) registerGlobals() error {
	vm := s.vm

	// --- console (captures to currentBuf) ---
	if err := vm.RegisterFunc("__consoleLog", func(msg any) string {
		if s.currentBuf != nil && msg != nil {
			fmt.Fprintf(s.currentBuf, "%v\n", msg)
		}
		return ""
	}, false); err != nil {
		return fmt.Errorf("register __consoleLog: %w", err)
	}

	if err := vm.RegisterFunc("__consoleError", func(msg any) string {
		if s.currentBuf != nil && msg != nil {
			fmt.Fprintf(s.currentBuf, "[ERROR] %v\n", msg)
		}
		return ""
	}, false); err != nil {
		return fmt.Errorf("register __consoleError: %w", err)
	}

	if _, err := vm.Eval(`console = {
		log: function() { __consoleLog(Array.from(arguments).map(String).join(' ')) },
		error: function() { __consoleError(Array.from(arguments).map(String).join(' ')) }
	}`, quickjs.EvalGlobal); err != nil {
		return fmt.Errorf("setup console: %w", err)
	}

	// --- tool (reads s.toolFn directly — no global sync.Map needed) ---
	if err := vm.RegisterFunc("tool", func(name string, args any) string {
		var argsJSON string
		if args == nil {
			argsJSON = "{}"
		} else {
			switch v := args.(type) {
			case string:
				argsJSON = v
			default:
				b, err := json.Marshal(v)
				if err != nil {
					return "ERROR: failed to marshal tool args: " + err.Error()
				}
				argsJSON = string(b)
			}
		}
		if s.toolFn == nil {
			return "ERROR: tool executor not available"
		}
		return s.toolFn(s.ctx, name, argsJSON)
	}, false); err != nil {
		return fmt.Errorf("register tool: %w", err)
	}

	// --- exit (throws to stop JS execution) ---
	if _, err := vm.Eval("function exit() { throw new Error('__EXIT__') }", quickjs.EvalGlobal); err != nil {
		return fmt.Errorf("register exit: %w", err)
	}

	// --- store/load ---
	if err := vm.RegisterFunc("store", func(key string, value any) string {
		s.kv.Store(key, toGoNative(value))
		return ""
	}, false); err != nil {
		return fmt.Errorf("register store: %w", err)
	}

	if err := vm.RegisterFunc("load", func(key string) string {
		v, ok := s.kv.Load(key)
		if !ok {
			return ""
		}
		switch val := v.(type) {
		case string:
			return val
		default:
			b, _ := json.Marshal(val)
			return string(b)
		}
	}, false); err != nil {
		return fmt.Errorf("register load: %w", err)
	}

	// --- notify ---
	if err := vm.RegisterFunc("notify", func(value string) string {
		if s.currentBuf != nil {
			s.currentBuf.WriteString("[NOTIFY] ")
			s.currentBuf.WriteString(value)
			s.currentBuf.WriteByte('\n')
		}
		return ""
	}, false); err != nil {
		return fmt.Errorf("register notify: %w", err)
	}

	// --- setTimeout/clearTimeout ---
	// Go stores timerEntry (timer + fireTime), JS stores function references.
	// __scheduleTimeout creates a timer. Execute() drains pending timers synchronously after eval.
	if err := vm.RegisterFunc("__scheduleTimeout", func(id any, ms any) string {
		timerID := toInt64(id)
		delay := max(time.Duration(toInt64(ms))*time.Millisecond, 0)
		// Enforce timer limit
		if len(s.pendingTimers) >= maxTimers {
			return "ERROR: maximum timer limit (" + fmt.Sprintf("%d", maxTimers) + ") reached"
		}
		s.pendingTimers[timerID] = &timerEntry{
			timer:    time.NewTimer(delay),
			fireTime: time.Now().Add(delay),
		}
		return ""
	}, false); err != nil {
		return fmt.Errorf("register __scheduleTimeout: %w", err)
	}

	if err := vm.RegisterFunc("__cancelTimeout", func(id any) string {
		timerID := toInt64(id)
		if te, ok := s.pendingTimers[timerID]; ok {
			te.timer.Stop()
			delete(s.pendingTimers, timerID)
		}
		return ""
	}, false); err != nil {
		return fmt.Errorf("register __cancelTimeout: %w", err)
	}

	// JS-side wrappers: function references stay in JS, Go only sees IDs
	if _, err := vm.Eval(`var __timeouts = {};
	var __nextTimeoutId = 0;
	setTimeout = function(fn, ms) {
		var id = __nextTimeoutId++;
		__timeouts[id] = fn;
		var result = __scheduleTimeout(id, ms === undefined ? 0 : ms);
		if (result.startsWith("ERROR:")) {
			delete __timeouts[id];
			throw new Error(result);
		}
		return id;
	};
	clearTimeout = function(id) {
		delete __timeouts[id];
		__cancelTimeout(id);
	};
	function __fireTimeout(id) {
		var fn = __timeouts[id];
		delete __timeouts[id];
		if (fn) fn();
	}`, quickjs.EvalGlobal); err != nil {
		return fmt.Errorf("setup setTimeout: %w", err)
	}

	return nil
}

// Execute runs JavaScript code in the session's QuickJS VM.
// toolFn receives context for cancellation — threaded through to the injected toolExecutor.
func (s *Session) Execute(ctx context.Context, code string, cwd string, toolFn func(ctx context.Context, name, argsJSON string) string, timeoutMs int64) (output string, err error) {
	// Catch panics from QuickJS C library — mark session unusable.
	defer func() {
		if r := recover(); r != nil {
			s.mu.Lock()
			s.closed = true
			s.mu.Unlock()
			output = fmt.Sprintf("[QuickJS fatal] %v", r)
			err = nil
		}
	}()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return "", fmt.Errorf("session closed")
	}

	s.ctx = ctx

	if toolFn != nil {
		s.toolFn = toolFn
	}

	buf := new(bytes.Buffer)
	s.currentBuf = buf

	if timeoutMs <= 0 {
		timeoutMs = defaultTimeout
	}
	if err := s.vm.SetEvalTimeout(time.Duration(timeoutMs) * time.Millisecond); err != nil {
		return "", fmt.Errorf("set eval timeout: %w", err)
	}

	// ctx guard goroutine — defer close(done) prevents leak on panic
	vm := s.vm
	done := make(chan struct{})
	var guardWg sync.WaitGroup
	guardWg.Go(func() {
		select {
		case <-ctx.Done():
			if vm != nil {
				vm.Interrupt()
			}
		case <-done:
		}
	})
	defer func() {
		close(done)
		guardWg.Wait()
	}()

	// Inject cwd as globalThis property so it's visible from EvalModule scope.
	// Using var in EvalGlobal does not create a property accessible from module code.
	if cwd != "" {
		cwdJSON, err := json.Marshal(cwd)
		if err != nil {
			return "", fmt.Errorf("marshal cwd: %w", err)
		}
		if _, err := s.vm.Eval("globalThis.cwd = "+string(cwdJSON), quickjs.EvalGlobal); err != nil {
			return buf.String() + "\n[JS Error] " + err.Error(), nil
		}
	}

	// EvalModule-only: all code compiled and executed as ES module.
	// Module mode supports top-level await via js_std_await.
	// Variables are module-scoped (not global) — use store()/load() for persistence.
	var evalErr error
	bytecode, compileErr := s.vm.Compile(code, quickjs.EvalModule)
	if compileErr != nil {
		evalErr = compileErr
	} else {
		_, evalErr = s.vm.EvalBytecodeValue(bytecode)
	}

	// Build output — capture buf length before timer drain
	output = buf.String()
	bufLen := buf.Len()

	if evalErr != nil && strings.Contains(evalErr.Error(), "__EXIT__") {
		return output, nil
	}

	if evalErr != nil {
		errMsg := evalErr.Error()
		if output != "" {
			output += "\n"
		}
		output += "[JS Error] " + errMsg
	}

	// Drain pending setTimeout timers synchronously.
	// Callbacks run in the same Execute call, output captured to buf.
	// Returns only timer error strings — callback output read from buf separately.
	timerErrors := s.drainPendingTimers(ctx)

	// Capture new content added by timer callbacks (after bufLen)
	if buf.Len() > bufLen {
		output += buf.String()[bufLen:]
	}
	output += timerErrors

	return output, nil
}

// drainPendingTimers waits for pending setTimeout timers and fires their callbacks.
// Returns timer error strings only. Callback output is written to s.currentBuf by the caller.
// Runs under s.mu.Lock.
func (s *Session) drainPendingTimers(ctx context.Context) string {
	if len(s.pendingTimers) == 0 {
		return ""
	}

	var errors string
	for len(s.pendingTimers) > 0 {
		// Find the timer with the earliest fireTime
		var earliestID int64
		for id, te := range s.pendingTimers {
			if earliestID == 0 || te.fireTime.Before(s.pendingTimers[earliestID].fireTime) {
				earliestID = id
			}
		}
		earliestCh := s.pendingTimers[earliestID].timer.C

		// Wait for it to fire, context cancel, or no pending
		select {
		case <-earliestCh:
			delete(s.pendingTimers, earliestID)
			if s.closed || s.vm == nil {
				continue
			}
			if _, err := s.vm.Call("__fireTimeout", earliestID); err != nil {
				if errors != "" {
					errors += "\n"
				}
				errors += "[Timer Error] " + err.Error()
			}
			// Callback may have registered new timers — loop continues
		case <-ctx.Done():
			// Context cancelled, stop remaining timers
			for id, te := range s.pendingTimers {
				te.timer.Stop()
				delete(s.pendingTimers, id)
			}
			return errors
		}
	}

	return errors
}

// Reset clears the session by closing the old VM and creating a new one.
func (s *Session) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.stopPendingTimers()

	// Clear store/load data
	s.kv.Range(func(key, _ any) bool {
		s.kv.Delete(key)
		return true
	})

	if s.vm != nil {
		_ = s.vm.Close()
	}

	vm, err := quickjs.NewVM()
	if err != nil {
		s.closed = true
		return fmt.Errorf("reset NewVM: %w", err)
	}
	vm.SetMemoryLimit(vmMemoryLimit)
	s.vm = vm

	if err := s.registerGlobals(); err != nil {
		s.closed = true
		return fmt.Errorf("reset register globals: %w", err)
	}

	return nil
}

// Close releases the VM resources and marks the session as closed.
// Safe to call while Execute is running — Interrupts the VM first
// so Execute can complete and release the mutex.
func (s *Session) Close() {
	// Interrupt VM first so any in-flight Execute can complete
	if s.vm != nil {
		s.vm.Interrupt()
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return
	}

	s.stopPendingTimers()
	s.toolFn = nil
	if s.vm != nil {
		_ = s.vm.Close()
		s.vm = nil
	}
	s.closed = true
}

// stopPendingTimers stops and removes all pending timers.
func (s *Session) stopPendingTimers() {
	for id, te := range s.pendingTimers {
		te.timer.Stop()
		delete(s.pendingTimers, id)
	}
}

// Interrupt sends an interrupt signal to the VM (for terminate action).
func (s *Session) Interrupt() {
	if s.vm != nil {
		s.vm.Interrupt()
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

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		if n != n || n > float64(1<<62) || n < 0 { // NaN, Inf, or negative → 0
			return 0
		}
		return int64(n)
	default:
		return 0
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
