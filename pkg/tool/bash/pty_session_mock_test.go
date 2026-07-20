package bash

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/aymanbagabas/go-pty"
	"github.com/liuy/gbot/pkg/tool"
)

// fakePty is a stub pty.Pty used to drive PTYSession.Drain without spawning
// a real process. Only Read/Write/Close are exercised by Drain — the
// Command/CommandContext/Fd methods return zero values.
type fakePty struct {
	mu     sync.Mutex
	r      *bytes.Reader
	w      *bytes.Buffer
	closed bool
}

func newFakePty(data []byte) *fakePty {
	return &fakePty{
		r: bytes.NewReader(data),
		w: &bytes.Buffer{},
	}
}

func (f *fakePty) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, io.ErrClosedPipe
	}
	return f.r.Read(p)
}

func (f *fakePty) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, io.ErrClosedPipe
	}
	return f.w.Write(p)
}

func (f *fakePty) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakePty) Name() string { return "fake-pty" }

func (f *fakePty) Command(string, ...string) *pty.Cmd { return nil }

func (f *fakePty) CommandContext(context.Context, string, ...string) *pty.Cmd {
	return nil
}

func (f *fakePty) Resize(int, int) error { return nil }

func (f *fakePty) Fd() uintptr { return 0 }

// TestPTYSession_Drain_FeedAndClose drives Drain with a fake pty that emits
// one chunk of bytes then EOF. Verifies the Screen receives the data and
// Drain returns cleanly (no goroutine leaks, no panic on nil Cmd since the
// ctx.Done path isn't reached).
func TestPTYSession_Drain_FeedAndClose(t *testing.T) {
	// Lengthen the stall gate so a slow CI box can't trip the stall timer
	// between data arrival and EOF. emitAskInput=nil means a stall firing
	// would be a no-op anyway, but this keeps the test deterministic.
	cleanup := setDrainStallThresholdForTest(t, 30*time.Second)
	defer cleanup()

	payload := []byte("hello world\nsecond line\n")
	fp := newFakePty(payload)

	var collected []string
	screen := tool.NewScreen(func(ev tool.ScreenEvent) {
		collected = append(collected, ev.Content)
	})

	session := &PTYSession{
		Pty:    fp,
		Screen: screen,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		session.Drain(context.Background(), nil)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(5 * time.Second):
		t.Fatal("Drain did not return within 5s")
	}

	screen.Flush()

	joined := bytes.Join(toBytesSlice(collected), []byte{'\n'})
	if !bytes.Contains(joined, []byte("hello world")) {
		t.Errorf("screen output = %q, want substring 'hello world'", joined)
	}
	if !bytes.Contains(joined, []byte("second line")) {
		t.Errorf("screen output = %q, want substring 'second line'", joined)
	}

	if fp.closed {
		t.Error("fakePty.closed = true, Drain should not close the pty (Close is caller-owned)")
	}
}

// TestPTYSession_Drain_EmptyReadEOF verifies Drain returns immediately when
// the pty yields EOF with no preceding data (covers the err != nil branch
// with len(res.data) == 0).
func TestPTYSession_Drain_EmptyReadEOF(t *testing.T) {
	cleanup := setDrainStallThresholdForTest(t, 30*time.Second)
	defer cleanup()

	fp := newFakePty(nil)

	var collected []string
	screen := tool.NewScreen(func(ev tool.ScreenEvent) {
		collected = append(collected, ev.Content)
	})

	session := &PTYSession{
		Pty:    fp,
		Screen: screen,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		session.Drain(context.Background(), nil)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Drain did not return within 5s on empty EOF")
	}

	if len(collected) != 0 {
		t.Errorf("collected = %v, want empty (no data was read)", collected)
	}
}

// setDrainStallThresholdForTest overrides the global drain stall gate and
// returns a cleanup that restores the production value. Uses the exported
// setter so the test does not reach into unexported state.
func setDrainStallThresholdForTest(t *testing.T, d time.Duration) func() {
	t.Helper()
	orig := getDrainStallThreshold()
	SetDrainStallThreshold(d)
	return func() { SetDrainStallThreshold(orig) }
}

func toBytesSlice(ss []string) [][]byte {
	out := make([][]byte, len(ss))
	for i, s := range ss {
		out[i] = []byte(s)
	}
	return out
}
