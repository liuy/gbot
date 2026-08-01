package tui

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/types"
)

// TestNewEngineHubWithHandler_NoDaemonSkip verifies that daemon mode's
// hub-only construction (no TUIHandler subscribed) lets Dispatch return
// immediately without anyone consuming appCh — the bug was that a
// subscribed-but-unconsumed TUIHandler would block forever on appCh <- msg.
func TestDaemonHubOnly_DispatchDoesNotBlock(t *testing.T) {
	t.Parallel()
	// This mirrors what start.go does in daemon mode: bare hub, no handler.
	h := hub.NewHub()

	// Simulate the engine goroutine dispatching an event. With no handler
	// subscribed, this must return immediately.
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Dispatch(types.QueryEvent{Type: types.EventTextStart})
	}()

	select {
	case <-done:
		// Good — no handler means no blocking.
	case <-time.After(time.Second):
		t.Fatal("Dispatch blocked on hub with no subscribed handler")
	}
}

// TestNewEngineHubWithHandler_SubscribedHandlerWouldBlock confirms the test
// above is meaningful: with a TUIHandler subscribed and appCh full, Dispatch
// *does* block (this is the bug we avoid by not subscribing in daemon mode).
func TestSubscribedHandler_AppChFull_BlocksDispatch(t *testing.T) {
	t.Parallel()
	h := hub.NewHub()
	handler := NewTUIHandlerForEngine("test", nil)
	h.Subscribe(handler)

	// Fill appCh to capacity.
	for range handlerBufSize {
		handler.appCh <- tea.Msg(struct{}{})
	}

	// Now Dispatch should block on the full appCh (until we drain it).
	blocked := make(chan struct{})
	go func() {
		h.Dispatch(types.QueryEvent{Type: types.EventTextStart})
		close(blocked)
	}()

	select {
	case <-blocked:
		t.Fatal("Dispatch unexpectedly returned — should block on full appCh")
	case <-time.After(50 * time.Millisecond):
		// Good — still blocked, confirms the deadlock pattern.
	}

	// Drain so the goroutine can exit and we don't leak it.
	go func() {
		for range handlerBufSize + 1 {
			<-handler.appCh
		}
	}()
	<-blocked
}

// warnCountingHandler counts slog.Warn calls. Used to assert the
// "appCh full" warn fires from TUIHandler.Handle without racing on a
// bytes.Buffer.
type warnCountingHandler struct{ count atomic.Int64 }

func (h *warnCountingHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}
func (h *warnCountingHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn {
		h.count.Add(1)
	}
	return nil
}
func (h *warnCountingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *warnCountingHandler) WithGroup(_ string) slog.Handler      { return h }

// TestHandle_AppChFull_EmitsWarnBeforeBlocking verifies the warn-on-full path
// in Handle's default branch: when appCh is full, a slog.Warn fires before
// the (blocking) second send attempt.
func TestHandle_AppChFull_EmitsWarnBeforeBlocking(t *testing.T) {
	handler := NewTUIHandlerForEngine("warn-test", nil)

	// Fill appCh.
	for range handlerBufSize {
		handler.appCh <- tea.Msg(struct{}{})
	}

	// Swap in a warn-counting handler on the default logger.
	h := &warnCountingHandler{}
	oldDefault := slog.Default()
	slog.SetDefault(slog.New(h))
	defer slog.SetDefault(oldDefault)

	// Run Handle on a goroutine — it should warn, then block.
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.Handle(types.QueryEvent{Type: types.EventTextStart})
	}()

	// The warn fires synchronously before the blocking write, so we just
	// need to verify Handle is still running (blocked) after a short delay.
	select {
	case <-done:
		t.Fatal("Handle returned; expected it to block on full appCh")
	case <-time.After(50 * time.Millisecond):
		// Good — still blocked, confirms the warn-then-block path.
	}

	if got := h.count.Load(); got != 1 {
		t.Errorf("warn count = %d, want 1", got)
	}

	// Drain to release the goroutine.
	go func() {
		for range handlerBufSize + 1 {
			<-handler.appCh
		}
	}()
	<-done
}
