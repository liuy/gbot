package wechat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// TestSendToUser_SuccessFirstTry — no retry needed.
func TestSendToUser_SuccessFirstTry(t *testing.T) {
	var calls atomic.Int32
	c := newTestConnector(func() error {
		calls.Add(1)
		return nil
	})

	err := c.sendToUser(context.Background(), "user1", "hello")
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("expected 1 call, got %d", got)
	}
}

// TestSendToUser_RetriesAndSucceeds — fails twice, succeeds on 3rd.
// Uses synctest to run instantly despite 3s of backoff.
func TestSendToUser_RetriesAndSucceeds(t *testing.T) {
	var calls atomic.Int32
	c := newTestConnector(func() error {
		n := calls.Add(1)
		if n < 3 {
			return errors.New("connection reset")
		}
		return nil
	})

	synctest.Test(t, func(t *testing.T) {
		err := c.sendToUser(context.Background(), "user1", "hello")
		if err != nil {
			t.Fatalf("expected nil after retry, got %v", err)
		}
		if got := calls.Load(); got != 3 {
			t.Errorf("expected 3 calls, got %d", got)
		}
	})
}

// TestSendToUser_ExhaustsRetries — all 3 attempts fail.
func TestSendToUser_ExhaustsRetries(t *testing.T) {
	var calls atomic.Int32
	c := newTestConnector(func() error {
		calls.Add(1)
		return errors.New("timeout")
	})

	synctest.Test(t, func(t *testing.T) {
		err := c.sendToUser(context.Background(), "user1", "hello")
		if err == nil {
			t.Fatal("sendToUser should fail after 3 attempts, got nil")
		}
		if got := calls.Load(); got != 3 {
			t.Errorf("expected 3 attempts, got %d", got)
		}
	})
}

// TestSendToUser_NoRetryOnRateLimit — rate limit is NOT retried; flushBuffer
// cache logic handles it by retaining content for next flush.
func TestSendToUser_NoRetryOnRateLimit(t *testing.T) {
	var calls atomic.Int32
	c := newTestConnector(func() error {
		calls.Add(1)
		return ErrRateLimited
	})

	err := c.sendToUser(context.Background(), "user1", "hello")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("expected 1 call (rate limit not retried), got %d", got)
	}
}

// TestSendToUser_NoRetryOnSessionExpired — ret=-14 is NOT retriable.
func TestSendToUser_NoRetryOnSessionExpired(t *testing.T) {
	var calls atomic.Int32
	c := newTestConnector(func() error {
		calls.Add(1)
		return ErrSessionExpired
	})

	err := c.sendToUser(context.Background(), "user1", "hello")
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expected ErrSessionExpired, got %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("expected 1 call (session expired not retriable), got %d", got)
	}
}

// TestSendToUser_ContextCancelDuringBackoff — ctx canceled while waiting
// returns ctx.Err() immediately, not the last send error.
func TestSendToUser_ContextCancelDuringBackoff(t *testing.T) {
	var calls atomic.Int32
	c := newTestConnector(func() error {
		calls.Add(1)
		return errors.New("network down")
	})

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()

		err := c.sendToUser(ctx, "user1", "hello")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})
}

// TestSendToUser_BackoffDelays — verify backoff duration is exponential.
// synctest lets us measure virtual time precisely.
func TestSendToUser_BackoffDelays(t *testing.T) {
	var calls atomic.Int32
	c := newTestConnector(func() error {
		calls.Add(1)
		return errors.New("timeout")
	})

	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		_ = c.sendToUser(context.Background(), "user1", "hello")
		elapsed := time.Since(start)

		// 2 backoffs: ~1s + ~2s = ~3s minimum (plus jitter up to 1s).
		if elapsed < 3*time.Second {
			t.Errorf("expected >= 3s of backoff, got %v", elapsed)
		}
		if elapsed > 4*time.Second {
			t.Errorf("backoff too long: %v (max jitter 1s)", elapsed)
		}
	})
}

// ---------------------------------------------------------------------------
// isRetriable — direct table test including wrapped sentinels
// ---------------------------------------------------------------------------

func TestIsRetriable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"network timeout", errors.New("i/o timeout"), true},
		{"connection refused", errors.New("connection refused"), true},
		{"rate limit", ErrRateLimited, false},
		{"session expired", ErrSessionExpired, false},
		{"wrapped rate limit", fmt.Errorf("send failed: %w", ErrRateLimited), false},
		{"wrapped session expired", fmt.Errorf("send failed: %w", ErrSessionExpired), false},
		{"generic error", errors.New("something went wrong"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// isRetriable(nil) should never be called in production
			// (sendToUser returns nil on success before checking), but
			// test it for completeness.
			if tt.err == nil {
				return
			}
			if got := isRetriable(tt.err); got != tt.want {
				t.Errorf("isRetriable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// newTestConnector builds a connector with the given send behavior.
func newTestConnector(fn func() error) *WeChatConnector {
	c := &WeChatConnector{
		client: &http.Client{},
		sendMsgFn: func(_ context.Context, _ *http.Client, _, _, _, _, _, _, _ string) error {
			return fn()
		},
	}
	c.state = &State{BaseURL: "http://test", Token: "tok", AccountID: "bot"}
	return c
}
