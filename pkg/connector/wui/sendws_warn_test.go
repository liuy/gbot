package wui

import (
	"testing"
	"time"
)

// TestSendWS_WarnWhenFull verifies that sendWS emits a slog.Warn when the
// outbound queue is full (slow WS client), before blocking in push. Mirrors
// the TUIHandler appCh-full warn test and the outQueue warn unit test, but
// through the sendWS entry point with a real connector's done channel.
func TestSendWS_WarnWhenFull(t *testing.T) {
	const bufSize = 4
	c := &WUIConnector{
		done: make(chan struct{}),
	}
	c.outQ.Store(newOutQueue(bufSize))

	load, restore := swapWarnHandler(t)
	defer restore()

	// Fill the queue — no wsWriter goroutine is running, so it stays full.
	for range bufSize {
		c.sendWS([]byte("x"))
	}
	if got := c.outQ.Load().len(); got != bufSize {
		t.Fatalf("queue length after fill = %d, want %d", got, bufSize)
	}

	// Now sendWS should warn (queue full) then block inside push. Run it on
	// a goroutine; it can't return because no one drains the queue and done
	// isn't closed.
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		c.sendWS([]byte("payload"))
	}()

	// The warn firing proves sendWS entered its blocking wait.
	if !waitFor(2*time.Second, func() bool { return load() == 1 }) {
		t.Fatal("blocked sendWS did not warn about the full queue")
	}
	select {
	case <-sendDone:
		t.Fatal("sendWS returned unexpectedly — expected it to block on full outbound queue")
	case <-time.After(80 * time.Millisecond): // REAL-TIME
	}
	if got := load(); got != 1 {
		t.Errorf("warn count = %d, want 1", got)
	}

	// Release the blocked goroutine by closing done (push's blocking select
	// has case <-done).
	close(c.done)
	select {
	case <-sendDone:
	case <-time.After(time.Second): // REAL-TIME
		t.Fatal("sendWS did not unblock after close(done)")
	}
	if got := c.outQ.Load().len(); got != bufSize {
		t.Errorf("queue length after done-release = %d, want %d (payload dropped, not appended)", got, bufSize)
	}
}
