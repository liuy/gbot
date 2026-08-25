package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"
)

// newStallSSEServer starts an httptest.Server that:
//  1. Sends SSE headers
//  2. Sends a content_block_start (tool_use) event — this is what triggers
//     timeoutReader disable in production
//  3. Blocks on a channel, simulating an LLM that never sends tool params delta
//
// Returns the server and a cleanup function that unblocks the handler and
// closes the server.
func newStallSSEServer(t *testing.T) (*httptest.Server, func()) {
	t.Helper()
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)

		// Send a tool_use content_block_start — this is exactly the point
		// where the LLM pauses to generate tool params, and the caller
		// calls SetTimeoutDisabled(true).
		_, _ = w.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"Write\"}}\n\n"))
		flusher.Flush()

		// Stall: never send tool params delta. The handler blocks until
		// test cleanup closes the channel.
		<-done // REAL-TIME: unblocked by test cleanup
	}))
	cleanup := func() {
		close(done)
		srv.Close()
	}
	return srv, cleanup
}

// drainOne reads one chunk of data from resp.Body to consume the initial
// content_block_start event. After this, the next Read will block because
// the server handler is waiting on a channel (stalled).
func drainOne(t *testing.T, resp *http.Response) {
	t.Helper()
	buf := make([]byte, 4096)
	_, err := resp.Body.Read(buf)
	if err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// httptest based tests — real TCP, short timeouts, REAL-TIME exempt
// ---------------------------------------------------------------------------

// TestTimeoutReader_DisabledContextCancel verifies that timeoutReader
// unblocks on ctx cancel when disabled (tool input phase).
//
// Simulates the ESC/Abort() path: user presses ESC while the LLM
// is stalling on tool param generation.
func TestTimeoutReader_DisabledContextCancel(t *testing.T) {
	t.Parallel()

	srv, cleanup := newStallSSEServer(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	drainOne(t, resp)

	tr := &timeoutReader{reader: resp.Body, timeout: time.Minute, ctx: ctx}
	tr.SetTimeoutDisabled(true)

	errCh := make(chan error, 1)
	go func() {
		buf := make([]byte, 512)
		_, err := tr.Read(buf)
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond) // REAL-TIME: let Read enter select

	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Read returned %v (%T), want context.Canceled", err, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read did not unblock after context cancel — timeoutReader ignores ctx.Done() when disabled")
	}
}

// TestTimeoutReader_EnabledContextCancel verifies that ctx cancel works
// in the normal (enabled) mode via httptest.
func TestTimeoutReader_EnabledContextCancel(t *testing.T) {
	t.Parallel()

	srv, cleanup := newStallSSEServer(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	drainOne(t, resp)

	tr := &timeoutReader{reader: resp.Body, timeout: 5 * time.Second, ctx: ctx}

	errCh := make(chan error, 1)
	go func() {
		buf := make([]byte, 512)
		_, err := tr.Read(buf)
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond) // REAL-TIME: let Read enter select

	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Read returned %v (%T), want context.Canceled", err, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read did not unblock after context cancel (enabled mode)")
	}
}

// TestTimeoutReader_DisabledTimeout verifies that disabled mode does NOT
// fire the idle timeout via httptest.
func TestTimeoutReader_DisabledTimeout(t *testing.T) {
	t.Parallel()

	srv, cleanup := newStallSSEServer(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	drainOne(t, resp)

	// 50ms idle timeout — should NOT fire because disabled.
	tr := &timeoutReader{reader: resp.Body, timeout: 50 * time.Millisecond, ctx: ctx}
	tr.SetTimeoutDisabled(true)

	errCh := make(chan error, 1)
	go func() {
		buf := make([]byte, 512)
		_, err := tr.Read(buf)
		errCh <- err
	}()

	time.Sleep(150 * time.Millisecond) // REAL-TIME: past the disabled 50ms timeout

	select {
	case err := <-errCh:
		t.Fatalf("Read returned %v — idle timeout fired despite disabled", err)
	default:
		// Read is still blocking — good, disabled timeout worked.
	}

	cancel()

	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Read did not unblock after context cancel")
	}
}

// TestTimeoutReader_DisabledDeadline verifies that timeoutReader in disabled
// mode honors context deadline via httptest.
//
// This simulates the http.Client.Timeout path.
func TestTimeoutReader_DisabledDeadline(t *testing.T) {
	t.Parallel()

	srv, cleanup := newStallSSEServer(t)
	defer cleanup()

	// Cancel-based instead of a timed deadline: the original 100ms window
	// raced parallel CI runs (the Do/drain phase could exhaust it, flipping
	// the failure mode). Cancelling AFTER the connection is established and
	// drained is deterministic — the assertion (cancelled ctx surfaces as an
	// error from Read) is unchanged.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	drainOne(t, resp)
	cancel()

	tr := &timeoutReader{reader: resp.Body, timeout: time.Minute, ctx: ctx}
	tr.SetTimeoutDisabled(true)

	buf := make([]byte, 512)
	_, err = tr.Read(buf)
	// Cancel-based: ctx.Err() is context.Canceled (the deadline variant
	// would be DeadlineExceeded) — both prove the disabled reader honors
	// ctx.Done().
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("Read returned %v, want context.Canceled — timeoutReader ignored ctx.Done() when disabled", err)
	}
}

// ---------------------------------------------------------------------------
// synctest based test — mocked clock, io.Pipe, simulates 5m timeout instantly
// ---------------------------------------------------------------------------

// TestTimeoutReader_DisabledLongDeadline verifies that timeoutReader in
// disabled mode honors a long context deadline (e.g. 5m http.Client.Timeout)
// using synctest's fake clock + io.Pipe, so the test completes instantly
// instead of waiting 5 real minutes.
func TestTimeoutReader_DisabledLongDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Simulate a 5-minute http.Client.Timeout with a 5-minute deadline.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		r, w := io.Pipe()
		defer w.Close()

		tr := &timeoutReader{reader: r, timeout: time.Minute, ctx: ctx}
		tr.SetTimeoutDisabled(true)

		errCh := make(chan error, 1)
		go func() {
			buf := make([]byte, 1)
			_, err := tr.Read(buf)
			errCh <- err
		}()

		// Advance past the 5m deadline — synctest fast-forwards instantly.
		time.Sleep(6 * time.Minute)
		synctest.Wait()

		select {
		case err := <-errCh:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Read returned %v, want context.DeadlineExceeded", err)
			}
		default:
			t.Fatal("Read did not unblock after 5m deadline — timeoutReader ignored ctx.Done() when disabled")
		}
	})
}
