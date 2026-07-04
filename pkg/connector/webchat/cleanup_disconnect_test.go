package webchat

import (
	"context"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/hub"
)

// When WebSocket disconnects (e.g. mobile browser backgrounding), cleanupConn
// must NOT abort the active query. The query should continue running; results
// land in history and are visible on reconnect. Only pending asks should be
// aborted (to prevent engine deadlock waiting on a disconnected client).
func TestCleanupConn_DoesNotAbortActiveQuery(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)

	// Simulate an active query that blocks until cancelled
	queryStarted := make(chan struct{})
	c.mock().queryFn = func(ctx context.Context, userMessage, systemPrompt string) {
		close(queryStarted)
		<-ctx.Done()
	}
	go c.engine.Query(context.Background(), "test", "")
	<-queryStarted

	// Simulate WebSocket disconnect
	c.cleanupConn()

	// The engine's Abort must NOT have been called.
	if c.mock().abortCount > 0 {
		t.Errorf("cleanupConn called engine.Abort() — disconnect should not abort active query (abortCount=%d)", c.mock().abortCount)
	}

	// Cleanup: cancel the query to let the goroutine exit
	c.mock().abortFn = func() {}
	c.engine.Abort()
	waitFor(time.Second, func() bool { return c.mock().abortCount >= 2 })
}
