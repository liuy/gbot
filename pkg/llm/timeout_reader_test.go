package llm

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestTimeoutReader_BasicRead(t *testing.T) {
	t.Parallel()
	r := &timeoutReader{
		reader:  strings.NewReader("hello"),
		timeout: 1 * time.Second,
	}
	buf := make([]byte, 5)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 5 || string(buf) != "hello" {
		t.Errorf("got %q, want %q", string(buf[:n]), "hello")
	}
}

func TestTimeoutReader_TimeoutFires(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	r := &timeoutReader{
		reader:  &channelReader{ch: done},
		timeout: 50 * time.Millisecond,
	}
	buf := make([]byte, 1)
	_, err := r.Read(buf)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "SSE idle timeout") {
		t.Errorf("expected timeout error, got: %v", err)
	}
	close(done)
}

func TestTimeoutReader_DisabledSkipsTimeout(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	unblock := make(chan struct{})
	done := make(chan error, 1)

	r := &timeoutReader{
		reader:  &syncReader{entered: entered, unblock: unblock, data: []byte("ok")},
		timeout: 50 * time.Millisecond,
	}

	r.SetTimeoutDisabled(true)
	buf := make([]byte, 10)
	go func() {
		n, err := r.Read(buf)
		if err != nil {
			done <- err
			return
		}
		if string(buf[:n]) != "ok" {
			done <- io.ErrClosedPipe
			return
		}
		done <- nil
	}()

	// Wait for goroutine to enter Read, then unblock
	<-entered
	close(unblock)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Read with disabled=true failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read with disabled=true timed out")
	}
}

func TestTimeoutReader_ToggleBack(t *testing.T) {
	t.Parallel()
	// Multi-byte reader so we can do two reads on the SAME reader instance.
	mr := &multiReader{
		chunks: [][]byte{[]byte("ok"), nil}, // first chunk returns data, second blocks
		block:  make(chan struct{}),
	}
	r := &timeoutReader{
		reader:  mr,
		timeout: 50 * time.Millisecond,
	}

	// Disable timeout → first read succeeds even though reader is fast
	r.SetTimeoutDisabled(true)
	buf := make([]byte, 10)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("unexpected error on first read: %v", err)
	}
	if string(buf[:n]) != "ok" {
		t.Errorf("got %q, want %q", string(buf[:n]), "ok")
	}

	// Re-enable timeout → second read on same reader should timeout
	// because the second chunk blocks forever.
	r.SetTimeoutDisabled(false)
	_, err = r.Read(buf)
	if err == nil {
		t.Fatal("expected timeout after re-enabling on same reader")
	}
	if !strings.Contains(err.Error(), "SSE idle timeout") {
		t.Errorf("expected timeout error, got: %v", err)
	}
	close(mr.block)
}

// channelReader blocks until ch is closed.
type channelReader struct {
	ch chan struct{}
}

func (r *channelReader) Read(p []byte) (int, error) {
	<-r.ch
	return 0, io.EOF
}

// multiReader serves chunks in sequence. A nil chunk blocks on ch.
type multiReader struct {
	chunks [][]byte
	idx    int
	block  chan struct{}
}

func (r *multiReader) Read(p []byte) (int, error) {
	if r.idx >= len(r.chunks) {
		return 0, io.EOF
	}
	chunk := r.chunks[r.idx]
	r.idx++
	if chunk == nil {
		<-r.block
		return 0, io.EOF
	}
	n := copy(p, chunk)
	return n, nil
}

// syncReader signals when Read is entered, blocks until unblock is closed.
type syncReader struct {
	entered chan struct{}
	unblock chan struct{}
	data    []byte
	sent    bool
}

func (r *syncReader) Read(p []byte) (int, error) {
	close(r.entered) // signal that Read has been entered
	<-r.unblock
	if !r.sent {
		r.sent = true
		n := copy(p, r.data)
		return n, nil
	}
	return 0, io.EOF
}
