package wechat

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

func newPollLoopConnector(t *testing.T) *WeChatConnector {
	t.Helper()
	c := &WeChatConnector{
		client:    &http.Client{},
		inboundCh: make(chan inboundMessage, 100),
		dedup:     newDedupSet(MessageDedupTTLSeconds),
	}
	c.state = &State{AccountID: "bot", Token: "t", BaseURL: "http://x"}
	c.projectDir = t.TempDir()
	c.sendToUserFn = func(_ context.Context, _, _ string) error { return nil }
	return c
}

func startPollLoop(t *testing.T, c *WeChatConnector, ctx context.Context) {
	t.Helper()
	c.pollWg.Add(1)
	go c.pollLoop(ctx)
}

func TestPollLoop_ErrorRetryDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newPollLoopConnector(t)
		var calls atomic.Int32
		c.getUpdatesFn = func(_ context.Context, _ *http.Client, _, _, _ string, _ time.Duration) (*GetUpdatesResponse, error) {
			calls.Add(1)
			return nil, fmt.Errorf("connection refused")
		}

		ctx, cancel := context.WithCancel(context.Background())
		startPollLoop(t, c, ctx)

		time.Sleep(5 * time.Second)
		cancel()
		c.pollWg.Wait()

		if got := calls.Load(); got < 2 {
			t.Errorf("expected >= 2 calls, got %d", got)
		}
	})
}

func TestPollLoop_BackoffAfterMaxFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newPollLoopConnector(t)
		var calls atomic.Int32
		c.getUpdatesFn = func(_ context.Context, _ *http.Client, _, _, _ string, _ time.Duration) (*GetUpdatesResponse, error) {
			calls.Add(1)
			return nil, fmt.Errorf("connection refused")
		}

		ctx, cancel := context.WithCancel(context.Background())
		startPollLoop(t, c, ctx)

		time.Sleep(40 * time.Second)
		cancel()
		c.pollWg.Wait()

		if got := calls.Load(); got < 4 {
			t.Errorf("expected >= 4 calls, got %d", got)
		}
	})
}

func TestPollLoop_SessionExpired_Pauses10Min(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newPollLoopConnector(t)
		var calls atomic.Int32
		c.getUpdatesFn = func(ctx context.Context, _ *http.Client, _, _, _ string, _ time.Duration) (*GetUpdatesResponse, error) {
			n := calls.Add(1)
			if n == 1 {
				return &GetUpdatesResponse{Ret: SessionExpiredErrCode}, nil
			}
			// Block after the first success so the loop doesn't spin
			// infinitely in virtual time.
			<-ctx.Done()
			return nil, ctx.Err()
		}

		ctx, cancel := context.WithCancel(context.Background())
		startPollLoop(t, c, ctx)

		time.Sleep(11 * time.Minute)
		cancel()
		c.pollWg.Wait()

		if got := calls.Load(); got < 2 {
			t.Errorf("expected >= 2 calls, got %d", got)
		}
	})
}

func TestPollLoop_StaleSession_Pauses10Min(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newPollLoopConnector(t)
		var calls atomic.Int32
		c.getUpdatesFn = func(ctx context.Context, _ *http.Client, _, _, _ string, _ time.Duration) (*GetUpdatesResponse, error) {
			n := calls.Add(1)
			if n == 1 {
				return &GetUpdatesResponse{Ret: RateLimitErrCode, ErrMsg: "unknown error"}, nil
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}

		ctx, cancel := context.WithCancel(context.Background())
		startPollLoop(t, c, ctx)

		time.Sleep(11 * time.Minute)
		cancel()
		c.pollWg.Wait()

		if got := calls.Load(); got < 2 {
			t.Errorf("expected >= 2 calls, got %d", got)
		}
	})
}

func TestPollLoop_ErrorResponse_Backoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newPollLoopConnector(t)
		var calls atomic.Int32
		c.getUpdatesFn = func(_ context.Context, _ *http.Client, _, _, _ string, _ time.Duration) (*GetUpdatesResponse, error) {
			calls.Add(1)
			return &GetUpdatesResponse{Ret: -1, ErrMsg: "some error"}, nil
		}

		ctx, cancel := context.WithCancel(context.Background())
		startPollLoop(t, c, ctx)

		// Each call sleeps BackoffDelaySeconds (30s), so 70s yields 3 calls.
		time.Sleep(70 * time.Second)
		cancel()
		c.pollWg.Wait()

		if got := calls.Load(); got < 3 {
			t.Errorf("expected >= 3 calls (30s backoff each), got %d", got)
		}
	})
}

func TestPollLoop_CtxDoneDuringBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newPollLoopConnector(t)
		var calls atomic.Int32
		c.getUpdatesFn = func(_ context.Context, _ *http.Client, _, _, _ string, _ time.Duration) (*GetUpdatesResponse, error) {
			calls.Add(1)
			return nil, fmt.Errorf("refused")
		}

		ctx, cancel := context.WithCancel(context.Background())
		startPollLoop(t, c, ctx)

		time.Sleep(7 * time.Second)
		cancel()
		c.pollWg.Wait()

		if got := calls.Load(); got != 3 {
			t.Errorf("expected exactly 3 calls before backoff, got %d", got)
		}
	})
}

func TestPollLoop_Success_ProcessBatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newPollLoopConnector(t)
		c.queryFn = func(_ context.Context, _, _ string) {}
		c.queryWithContentFn = func(_ context.Context, _ []types.ContentBlock, _ string) {}

		var called int
		msgs := []Message{
			{FromUserID: "userA@im", MessageID: FlexString("m1"),
				ItemList: []Item{{Type: ItemText, TextItem: &TextItem{Text: "hello"}}}},
		}
		c.getUpdatesFn = func(ctx context.Context, _ *http.Client, _, _, _ string, _ time.Duration) (*GetUpdatesResponse, error) {
			called++
			if called == 1 {
				return &GetUpdatesResponse{Ret: 0, GetUpdatesBuf: "cursor", Msgs: msgs}, nil
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}

		ctx, cancel := context.WithCancel(context.Background())
		startPollLoop(t, c, ctx)

		select {
		case im := <-c.inboundCh:
			if im.userID != "userA@im" {
				t.Errorf("userID = %q, want userA@im", im.userID)
			}
			if im.text != "hello" {
				t.Errorf("text = %q, want hello", im.text)
			}
		case <-time.After(1 * time.Second):
			t.Fatal("message not enqueued")
		}

		cancel()
		c.pollWg.Wait()
	})
}

func TestPollLoop_LongPollingMs_UpdatesTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newPollLoopConnector(t)
		var mu sync.Mutex
		var timeouts []time.Duration
		c.getUpdatesFn = func(ctx context.Context, _ *http.Client, _, _, _ string, timeout time.Duration) (*GetUpdatesResponse, error) {
			mu.Lock()
			timeouts = append(timeouts, timeout)
			done := len(timeouts) >= 2
			mu.Unlock()
			if done {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return &GetUpdatesResponse{Ret: 0, LongPollingMs: 50000, GetUpdatesBuf: "ok"}, nil
		}

		ctx, cancel := context.WithCancel(context.Background())
		startPollLoop(t, c, ctx)

		time.Sleep(2 * time.Second)
		cancel()
		c.pollWg.Wait()

		mu.Lock()
		defer mu.Unlock()
		if len(timeouts) < 2 {
			t.Fatalf("expected >= 2 calls, got %d", len(timeouts))
		}
		if timeouts[0] != LongPollTimeoutMs*time.Millisecond {
			t.Errorf("first timeout = %v, want %v", timeouts[0], LongPollTimeoutMs*time.Millisecond)
		}
		if timeouts[1] != 50000*time.Millisecond {
			t.Errorf("second timeout = %v, want %v", timeouts[1], 50000*time.Millisecond)
		}
	})
}

func TestPollLoop_GetUpdatesBuf_UpdatesSyncBuf(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newPollLoopConnector(t)
		var called int
		c.getUpdatesFn = func(ctx context.Context, _ *http.Client, _, _, _ string, _ time.Duration) (*GetUpdatesResponse, error) {
			called++
			if called == 1 {
				return &GetUpdatesResponse{Ret: 0, GetUpdatesBuf: "new_cursor"}, nil
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}

		ctx, cancel := context.WithCancel(context.Background())
		startPollLoop(t, c, ctx)

		time.Sleep(1 * time.Second)
		cancel()
		c.pollWg.Wait()

		if called < 1 {
			t.Fatal("getUpdatesFn was never called")
		}
		c.stateMu.Lock()
		got := c.state.SyncBuf
		c.stateMu.Unlock()
		if got != "new_cursor" {
			t.Errorf("SyncBuf = %q, want new_cursor", got)
		}
	})
}
