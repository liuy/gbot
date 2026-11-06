package llm

import (
	"context"
	"errors"
	"testing"
	"time"
)

// gatedReader blocks every Read until its gate channel closes, then returns
// fixed data. Releasing the gate deterministically after each test keeps the
// spawned Read goroutine from leaking.
type gatedReader struct {
	gate chan struct{}
}

func (g *gatedReader) Read(p []byte) (int, error) {
	<-g.gate
	copy(p, "payload")
	return len("payload"), nil
}

func newGatedReader() *gatedReader {
	return &gatedReader{gate: make(chan struct{})}
}

// With the timeout disabled, a canceled context must still unblock Read and
// report n=0 — the disabled branch keeps the ctx.Done select arm.
func TestTimeoutReader_DisabledCtxCancelReturnsZero(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := newGatedReader()
	defer close(r.gate)
	tr := &timeoutReader{reader: r, timeout: time.Hour, ctx: ctx}
	tr.SetTimeoutDisabled(true)

	buf := make([]byte, 32)
	n, err := tr.Read(buf)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Read err = %v, want context.Canceled", err)
	}
	if n != 0 {
		t.Errorf("Read n = %d, want 0 (nothing was read)", n)
	}
}

// With the timeout enabled, ctx cancellation must beat both the pending read
// and the idle timer: Read returns (0, ctx.Err()) instead of data or
// ErrIdleTimeout.
func TestTimeoutReader_EnabledCtxCancelBeatsDataAndTimer(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := newGatedReader()
	defer close(r.gate)
	tr := &timeoutReader{reader: r, timeout: 200 * time.Millisecond, ctx: ctx}

	buf := make([]byte, 32)
	n, err := tr.Read(buf)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Read err = %v, want context.Canceled (not idle timeout, not data)", err)
	}
	if n != 0 {
		t.Errorf("Read n = %d, want 0 (nothing was read)", n)
	}
}

// The enabled branch must still deliver reader data when it arrives in time.
func TestTimeoutReader_EnabledDeliversData(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := &gatedReader{gate: make(chan struct{})}
	tr := &timeoutReader{reader: r, timeout: time.Hour, ctx: ctx}

	go func() {
		time.Sleep(10 * time.Millisecond) // REAL-TIME: schedules the gated read release after Read starts
		close(r.gate)
	}()

	buf := make([]byte, 32)
	n, err := tr.Read(buf)
	if err != nil {
		t.Fatalf("Read err = %v, want nil", err)
	}
	if n != len("payload") || string(buf[:n]) != "payload" {
		t.Errorf("Read n = %d buf = %q, want payload", n, buf[:n])
	}
}
