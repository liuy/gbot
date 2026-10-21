package wui

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// warnCountingHandler wraps slog.Default()'s handler and counts Warn calls.
// Used to assert that a blocked push/sendWS warns about the full queue
// without racing on a bytes.Buffer.
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

// swapWarnHandler installs a handler on slog.Default() that counts Warn
// records, returning a load function and the restore callback.
func swapWarnHandler(t *testing.T) (load func() int64, restore func()) {
	t.Helper()
	h := &warnCountingHandler{}
	oldDefault := slog.Default()
	slog.SetDefault(slog.New(h))
	return h.count.Load, func() { slog.SetDefault(oldDefault) }
}

func TestOutQueue_PushTakeFIFO(t *testing.T) {
	q := newOutQueue(4)
	q.push(wsMsg{data: []byte("a")}, nil)
	q.push(wsMsg{data: []byte("b")}, nil)
	q.push(wsMsg{data: []byte("c")}, nil)

	if got := q.len(); got != 3 {
		t.Fatalf("len = %d, want 3", got)
	}
	frames := q.take()
	if len(frames) != 3 {
		t.Fatalf("take returned %d frames, want 3", len(frames))
	}
	for i, want := range []string{"a", "b", "c"} {
		if string(frames[i].data) != want {
			t.Fatalf("frame %d = %q, want %q", i, frames[i].data, want)
		}
	}
	// take empties: a second take returns nil and len is 0.
	if frames := q.take(); frames != nil {
		t.Fatalf("take after empty returned %d frames, want nil", len(frames))
	}
	if n := q.len(); n != 0 {
		t.Fatalf("len after take = %d, want 0", n)
	}
}

func TestOutQueue_PushBlocksAtCapAndWarnsOnce(t *testing.T) {
	load, restore := swapWarnHandler(t)
	defer restore()

	q := newOutQueue(2)
	q.push(wsMsg{data: []byte("a")}, nil)
	q.push(wsMsg{data: []byte("b")}, nil)

	pushed := make(chan struct{})
	go func() {
		defer close(pushed)
		q.push(wsMsg{data: []byte("c")}, nil)
	}()

	// The warn firing proves the pusher entered its blocking wait.
	if !waitFor(2*time.Second, func() bool { return load() == 1 }) {
		t.Fatal("blocked pusher did not warn about the full queue")
	}
	select {
	case <-pushed:
		t.Fatal("push returned while queue was full and no take happened")
	case <-time.After(80 * time.Millisecond): // REAL-TIME
	}
	if got := load(); got != 1 {
		t.Fatalf("warn count = %d, want exactly 1 for one blocking episode", got)
	}

	// take() makes room; the parked pusher appends its frame.
	frames := q.take()
	if len(frames) != 2 || string(frames[0].data) != "a" || string(frames[1].data) != "b" {
		t.Fatalf("first take = %v, want [a b]", frames)
	}
	select {
	case <-pushed:
	case <-time.After(time.Second): // REAL-TIME
		t.Fatal("blocked pusher did not resume after take made room")
	}
	frames = q.take()
	if len(frames) != 1 || string(frames[0].data) != "c" {
		t.Fatalf("second take = %v, want the parked frame [c]", frames)
	}
}

// TestOutQueue_CloseReleasesBlockedPusherWithoutDone is the deterministic
// killer for a dropped close(): the pusher is parked on a full queue with
// done still OPEN, and only the era close (close(room)) can release it. The
// pushed frame must be discarded, not delivered.
func TestOutQueue_CloseReleasesBlockedPusherWithoutDone(t *testing.T) {
	load, restore := swapWarnHandler(t)
	defer restore()

	q := newOutQueue(1)
	q.push(wsMsg{data: []byte("a")}, nil)

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		// done never closes: only q.close() may end this wait.
		q.push(wsMsg{data: []byte("b")}, make(chan struct{}))
	}()

	if !waitFor(2*time.Second, func() bool { return load() == 1 }) {
		t.Fatal("pusher did not enter its blocking wait")
	}
	q.close()
	select {
	case <-returned:
	case <-time.After(2 * time.Second): // REAL-TIME
		t.Fatal("close did not release the blocked pusher")
	}

	// Pending and in-flight frames are both discarded, forever.
	if frames := q.take(); frames != nil {
		t.Fatalf("take after close returned %d frames, want nil", len(frames))
	}
	if n := q.len(); n != 0 {
		t.Fatalf("len after close = %d, want 0", n)
	}
}

func TestOutQueue_CloseDropsPendingFrames(t *testing.T) {
	q := newOutQueue(4)
	q.push(wsMsg{data: []byte("a")}, nil)
	q.push(wsMsg{data: []byte("b")}, nil)
	q.close()

	if n := q.len(); n != 0 {
		t.Fatalf("len after close = %d, want 0 (pending frames dropped)", n)
	}
	if frames := q.take(); frames != nil {
		t.Fatalf("take after close = %d frames, want nil", len(frames))
	}
	if frames := q.take(); frames != nil {
		t.Fatalf("second take after close = %d frames, want nil forever", len(frames))
	}

	// Push after close drops silently and never blocks.
	done := make(chan struct{})
	go func() {
		defer close(done)
		q.push(wsMsg{data: []byte("late")}, nil)
	}()
	select {
	case <-done:
	case <-time.After(time.Second): // REAL-TIME
		t.Fatal("push after close blocked instead of dropping")
	}
	if n := q.len(); n != 0 {
		t.Fatalf("len after post-close push = %d, want 0", n)
	}
}

func TestOutQueue_DoneReleasesBlockedPusher(t *testing.T) {
	load, restore := swapWarnHandler(t)
	defer restore()

	q := newOutQueue(1)
	q.push(wsMsg{data: []byte("a")}, nil)
	done := make(chan struct{})

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		q.push(wsMsg{data: []byte("b")}, done)
	}()

	if !waitFor(2*time.Second, func() bool { return load() == 1 }) {
		t.Fatal("pusher did not enter its blocking wait")
	}
	close(done)
	select {
	case <-returned:
	case <-time.After(2 * time.Second): // REAL-TIME
		t.Fatal("done did not release the blocked pusher")
	}
	if got := q.len(); got != 1 {
		t.Fatalf("len after done-release = %d, want 1 (only the pre-block frame)", got)
	}
}

func TestOutQueue_IsPristineOnlyBeforeFirstPush(t *testing.T) {
	q := newOutQueue(2)
	if !q.isPristine() {
		t.Fatal("fresh queue is not pristine")
	}
	q.push(wsMsg{data: []byte("a")}, nil)
	if q.isPristine() {
		t.Fatal("queue with one push reported pristine")
	}
	// Draining does not restore pristine: pushes counts history, not
	// occupancy, so sendMetadata never reuses a queue a sender saw.
	q.take()
	if q.isPristine() {
		t.Fatal("drained queue reported pristine; pushes must count history")
	}
}

// TestOutQueueCapacityBackpressureParity pins the outbound capacity: 1024
// is the wsCh capacity this queue replaces, and drift would change the
// backpressure profile of a slow current-era client.
func TestOutQueueCapacityBackpressureParity(t *testing.T) {
	if outQueueCapacity != 1024 {
		t.Fatalf("outQueueCapacity = %d, want 1024 (backpressure parity with the replaced wsCh)", outQueueCapacity)
	}
}
