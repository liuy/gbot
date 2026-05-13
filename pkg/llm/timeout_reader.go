package llm

import (
	"errors"
	"io"
	"time"
)

// DefaultSSETimeout is the idle timeout for SSE event streams.
// If no data arrives within this duration, Read returns an error.
const DefaultSSETimeout = 60 * time.Second

// timeoutReader wraps an io.Reader with per-read idle timeout.
// Each Read() call has a deadline — if no data arrives within the
// timeout, Read returns an error instead of blocking indefinitely.
type timeoutReader struct {
	reader  io.Reader
	timeout time.Duration
}

type readResult struct {
	n   int
	err error
}

func (r *timeoutReader) Read(p []byte) (int, error) {
	done := make(chan readResult, 1)
	go func() {
		n, err := r.reader.Read(p)
		done <- readResult{n, err}
	}()
	t := time.NewTimer(r.timeout)
	defer t.Stop()
	select {
	case res := <-done:
		return res.n, res.err
	case <-t.C:
		return 0, errors.New("SSE idle timeout: no data received")
	}
}
