package wui

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// warnCountingHandler wraps slog.Default()'s handler and counts Warn calls.
// Used to assert that sendWS emits a warn when wsCh is full without racing
// on a bytes.Buffer.
type warnCountingHandler struct {
	count atomic.Int64
}

func (h *warnCountingHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}

func (h *warnCountingHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn {
		h.count.Add(1)
	}
	return nil
}

func (h *warnCountingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *warnCountingHandler) WithGroup(_ string) slog.Handler      { return h }

// TestSendWS_WarnWhenFull verifies that sendWS emits a slog.Warn when wsCh is
// full (slow WS client), before blocking on the second send attempt. Mirrors
// the TUIHandler appCh-full warn test.
func TestSendWS_WarnWhenFull(t *testing.T) {
	const bufSize = 4
	c := &WUIConnector{
		wsCh: make(chan wsMsg, bufSize),
		done: make(chan struct{}),
	}

	// Fill wsCh — no wsWriter goroutine is running, so it stays full.
	for range bufSize {
		c.wsCh <- wsMsg{data: []byte("x")}
	}

	// Swap in a warn-counting handler on the default logger.
	h := &warnCountingHandler{}
	oldDefault := slog.Default()
	slog.SetDefault(slog.New(h))
	defer slog.SetDefault(oldDefault)

	// Now sendWS should warn (wsCh full) then block on the second select.
	// Run it on a goroutine; it can't return because no one drains wsCh and
	// done isn't closed.
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		c.sendWS([]byte("payload"))
	}()

	// sendWS should still be blocked after the warn fires.
	select {
	case <-sendDone:
		t.Fatal("sendWS returned unexpectedly — expected it to block on full wsCh")
	case <-time.After(80 * time.Millisecond):
		// Expected: still blocked. The warn has fired by now.
	}

	if got := h.count.Load(); got != 1 {
		t.Errorf("warn count = %d, want 1", got)
	}

	// Release the blocked goroutine by closing done (the second select has
	// case <-c.done).
	close(c.done)
	select {
	case <-sendDone:
	case <-time.After(time.Second):
		t.Fatal("sendWS did not unblock after close(done)")
	}
}
