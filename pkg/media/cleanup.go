package media

import (
	"context"
	"log/slog"
	"time"
)

// startCleanupLoop launches the background eviction loop bound to ctx. It
// returns a cancel func (which signals the loop to exit via ctx cancellation)
// and a done channel (closed after the loop has exited). Used internally by
// New(); StartCleanup wraps it for external callers that built the store via
// NewAt (which does not auto-start cleanup).
func (s *Store) startCleanupLoop(ctx context.Context, interval, maxAge time.Duration) (context.CancelFunc, chan struct{}) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if removed := s.CleanupAll(maxAge); removed > 0 {
					slog.Info("media: cleanup removed stale files", "count", removed)
				}
			}
		}
	}()
	return cancel, done
}

// StartCleanup launches a background cleanup goroutine bound to ctx and returns
// a stop function. Exported for tests and for callers that construct a Store
// via NewAt (which does not auto-start cleanup). New() callers should use
// Close() instead. The returned stop function blocks until the goroutine has
// exited, so callers can be sure no sweep is in flight after it returns.
func (s *Store) StartCleanup(ctx context.Context, interval, maxAge time.Duration) func() {
	cancel, done := s.startCleanupLoop(ctx, interval, maxAge)
	return func() {
		cancel()
		<-done
	}
}
