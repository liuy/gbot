package wui

import (
	"context"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/types"
)

// When WebSocket disconnects (e.g. mobile browser backgrounding), cleanupConn
// must NOT abort the active query. The query should continue running; results
// land in history and are visible on reconnect. Only pending asks should be
// aborted (to prevent engine deadlock waiting on a disconnected client).
func TestCleanupConn_DoesNotAbortActiveQuery(t *testing.T) {
	hermeticWUIHub(t, hub.NewHub(), func(t *testing.T, c *WUIConnector, _ *chatServer) {
		// Simulate an active query that blocks until cancelled. The query ctx
		// must be cancellable so the goroutine exits before the synctest
		// bubble drains — a Background-context goroutine would block the
		// bubble forever (on the real transport it merely leaks past the test).
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		queryStarted := make(chan struct{})
		c.mock().queryFn = func(ctx context.Context, userMessage, systemPrompt string) {
			close(queryStarted)
			<-ctx.Done()
		}
		go c.activeEngineTest().Query(ctx, "test", "")
		<-queryStarted

		// Simulate WebSocket disconnect
		c.cleanupConn()

		// The engine's Abort must NOT have been called.
		if c.mock().abortCount > 0 {
			t.Errorf("cleanupConn called engine.Abort() — disconnect should not abort active query (abortCount=%d)", c.mock().abortCount)
		}

		// Cleanup: cancel the query to let the goroutine exit
		c.mock().abortFn = func() {}
		c.activeEngineTest().Abort()
	})
}

// TestDisconnect_DeactivatesSlotAndAbortsAsks pins the genuine-disconnect
// half of serveChatWS's cleanup: when the ACTIVE connection exits, the slot
// must be deactivated (no live client to stream to) and pending asks must be
// unblocked with Aborted=true. Its counterpart — a STALE readLoop exiting
// after a takeover doing neither — is pinned by
// TestServeChatWS_EraGuard_PreventsClobberOnManualSwap.
func TestDisconnect_DeactivatesSlotAndAbortsAsks(t *testing.T) {
	hermeticWUI(t, func(t *testing.T, c *WUIConnector, hs *chatServer) {
		ws := hs.dialDrained(t)
		slot := c.activeSlotTest(t)
		if !slot.active.Load() {
			t.Fatal("slot inactive after takeover — takeover must activate the engine")
		}
		askCh := make(chan types.AskResponse, 1)
		// Registered directly (not via Handle): a Handle-routed ask would
		// push an ask frame whose failed write lets the wsWriter's own CAS
		// clear activeWS, masking the handler-exit CAS this test pins.
		c.pendingMu.Lock()
		c.pendingAsks["ask1"] = &types.AskEvent{Kind: types.AskPermission, ToolName: "Bash", ResponseCh: askCh}
		c.pendingMu.Unlock()

		_ = ws.Close()
		if !waitFor(2*time.Second, func() bool { return !slot.active.Load() }) {
			t.Fatal("slot still active after the active connection disconnected — disconnect must deactivate it")
		}
		// The writer is idle here (no pending writes to fail), so only the
		// handler's exit CAS can clear activeWS — pin that it does.
		if !waitFor(2*time.Second, func() bool { return c.activeWS.Load() == nil }) {
			t.Fatal("activeWS not nil after the active connection disconnected — exit must clear it")
		}
		select {
		case resp := <-askCh:
			if !resp.Aborted {
				t.Fatal("pending ask: Aborted = false, want true on disconnect")
			}
		default:
			t.Fatal("pending ask was not aborted on disconnect")
		}
	})
}

// TestStop_UnsubscribesHubAndAbortsAsks pins Stop's teardown surface: the hub
// subscription is removed (post-Stop dispatch must not even state-capture
// into streamState) and pending asks are aborted via cleanupConn.
func TestStop_UnsubscribesHubAndAbortsAsks(t *testing.T) {
	h := hub.NewHub()
	hermeticWUIHub(t, h, func(t *testing.T, c *WUIConnector, hs *chatServer) {
		_ = hs.dialDrained(t)
		askCh := make(chan types.AskResponse, 1)
		c.Handle(types.QueryEvent{Type: types.EventAsk, Ask: &types.AskEvent{Kind: types.AskPermission, ToolName: "Bash", ResponseCh: askCh}})

		c.Stop()

		h.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "post-stop"})
		wantBlocks := 0
		if got := streamStateCount(c, "main"); got != wantBlocks {
			t.Fatalf("streamState blocks after Stop+dispatch = %d, want %d (hub subscription survived Stop)", got, wantBlocks)
		}
		select {
		case resp := <-askCh:
			if !resp.Aborted {
				t.Fatal("pending ask: Aborted = false, want true on Stop")
			}
		default:
			t.Fatal("pending ask was not aborted by Stop")
		}
	})
}
