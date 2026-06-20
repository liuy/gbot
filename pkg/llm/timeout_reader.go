package llm

import (
	"errors"
	"io"
	"sync/atomic"
	"time"
)

// DefaultSSETimeout is the idle timeout for SSE event streams.
// If no data arrives within this duration, Read returns an error.
// 90s matches Anthropic's default (STREAM_IDLE_TIMEOUT_MS in claude.ts).
// The tool input phase disables this timeout entirely.
const DefaultSSETimeout = 90 * time.Second

// timeoutReader wraps an io.Reader with per-read idle timeout.
// Each Read() call has a deadline — if no data arrives within the
// timeout, Read returns an error instead of blocking indefinitely.
//
// When disabled is true (tool input phase), timeout is skipped — the LLM
// is actively generating tool parameters and may go quiet for extended
// periods without being stuck.
type timeoutReader struct {
	reader   io.Reader
	timeout  time.Duration
	disabled atomic.Bool
}

// TimeoutDisabler allows callers to temporarily disable the idle timeout.
type TimeoutDisabler interface {
	SetTimeoutDisabled(disabled bool)
}

// SetTimeoutDisabled temporarily disables the idle timeout.
// Used during tool input streaming where the LLM may pause for
// extended periods while generating large parameters.
func (r *timeoutReader) SetTimeoutDisabled(disabled bool) {
	r.disabled.Store(disabled)
}

type readResult struct {
	n   int
	err error
}

func (r *timeoutReader) Read(p []byte) (int, error) {
	if r.disabled.Load() {
		return r.reader.Read(p)
	}
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
