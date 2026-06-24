package wechat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"
)

// processLoop is the serial message processing loop.
// One goroutine reads from inboundCh and processes messages one at a time,
// ensuring serial execution order for the engine.
func (c *WeChatConnector) processLoop(ctx context.Context) {
	defer c.pollWg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-c.inboundCh:
			c.handleInbound(ctx, msg)
		}
	}
}

// handleInbound processes a single inbound message through the engine.
func (c *WeChatConnector) handleInbound(ctx context.Context, msg inboundMessage) {
	slog.Info("wechat: process inbound", "user", safeID(msg.userID))

	c.startTyping(ctx, msg.userID)
	defer c.stopTyping(ctx, msg.userID)

	result := c.querySyncFn(ctx, msg.text, "")

	if result.Error != nil {
		slog.Warn("wechat: query error", "user", safeID(msg.userID), "error", result.Error)
		if err := c.sendToUserFn(ctx, msg.userID, fmt.Sprintf("⚠️ Error: %v", result.Error)); err != nil {
			slog.Error("wechat: send error message failed", "user", safeID(msg.userID), "error", err)
		}
		return
	}

	reply := result.Reply
	slog.Info("wechat: query done", "user", safeID(msg.userID),
		"msgCount", len(result.Messages), "replyLen", len(reply))
	if reply != "" {
		for _, chunk := range splitForWeChat(formatMessage(reply)) {
			if err := c.sendToUserFn(ctx, msg.userID, chunk); err != nil {
				slog.Error("wechat: send reply failed", "user", safeID(msg.userID), "error", err)
				return
			}
		}
	}
}

// sendToUser sends text to a WeChat user via iLink sendmessage.
// Retries up to 3 times with exponential backoff + jitter, but ONLY for
// retriable errors (network failures and rate limit). Other errors
// (session expired, invalid request) are returned immediately.
func (c *WeChatConnector) sendToUser(ctx context.Context, userID, text string) error {
	contextToken := c.getContextToken(userID)
	var lastErr error
	for attempt := range 3 {
		err := c.sendMsgFn(ctx, c.client, c.state.BaseURL, c.state.Token,
			c.state.AccountID, userID, text, contextToken, "")
		if err == nil {
			return nil
		}
		lastErr = err
		if !isRetriable(err) {
			return err
		}
		if attempt < 2 {
			backoff := time.Duration(1<<attempt) * time.Second // 1s, 2s
			jitter := time.Duration(rand.IntN(500)) * time.Millisecond
			slog.Warn("wechat: send retry", "user", safeID(userID),
				"attempt", attempt+1, "backoff", backoff+jitter, "error", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff + jitter):
			}
		}
	}
	return lastErr
}

// isRetriable returns true for transient errors worth retrying: network
// failures (timeouts, connection refused) and rate limit. Stale-session
// (ret=-2 with "unknown error", mapped to ErrSessionExpired) and real
// session expiry (ret=-14) are not retriable — retrying won't help.
func isRetriable(err error) bool {
	if errors.Is(err, ErrRateLimited) {
		return true
	}
	// ErrSessionExpired and typed API errors are not retriable.
	if errors.Is(err, ErrSessionExpired) {
		return false
	}
	// Network errors (timeouts, connection failures) don't wrap a sentinel,
	// so we treat them as retriable by default — they're the most common
	// transient failures. iLink ret/errcode errors are sentinel-typed and
	// caught above.
	return true
}
