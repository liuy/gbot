package wechat

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/types"
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
// Sets activeUserID, dispatches EventConnectorUserMessage (so the TUI renders
// the user message via the shared Hub), calls the async engine query, then
// blocks on queryDone until Handle's EventQueryEnd closes it — preserving
// one-query-at-a-time semantics despite the async engine query.
func (c *WeChatConnector) handleInbound(ctx context.Context, msg inboundMessage) {
	slog.Info("wechat: process inbound", "user", safeID(msg.userID))

	c.activeUserID = msg.userID
	c.queryDone = make(chan struct{})
	done := c.queryDone
	c.lastFlush = time.Now()
	c.startTyping(ctx, msg.userID) // sets lastTypingRefresh

	// Build display text from attachment headers. Multiple documents get a
	// combined "[Documents: a, b]" header instead of per-file "[Document: ...]".
	var docNames []string
	var otherParts []string
	for _, cb := range msg.content {
		switch cb.Type {
		case types.ContentTypeImage:
			otherParts = append(otherParts, "[image]")
		case types.ContentTypeText:
			firstLine := cb.Text
			if idx := strings.IndexByte(cb.Text, '\n'); idx > 0 {
				firstLine = cb.Text[:idx]
			}
			if name, ok := extractDocName(firstLine); ok {
				docNames = append(docNames, name)
			} else {
				otherParts = append(otherParts, firstLine)
			}
		}
	}
	var displayText string
	if len(docNames) == 1 {
		displayText = "[Document: " + docNames[0] + "]"
	} else if len(docNames) > 1 {
		displayText = "[Documents: " + strings.Join(docNames, ", ") + "]"
	}
	if len(otherParts) > 0 {
		if displayText != "" {
			displayText += " "
		}
		displayText += strings.Join(otherParts, " ")
	}
	if displayText == "" {
		displayText = msg.text
	}
	c.hub.Dispatch(types.QueryEvent{
		Type: types.EventConnectorUserMessage,
		Message: &types.Message{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.NewTextBlock(displayText)},
		},
	})

	// Async — returns immediately. Results arrive via Handle() events.
	// engine may be nil in unit tests (queryFn is stubbed); guard the
	// SystemPrompt call so the test seam works without a real engine.
	sysPrompt := ""
	if c.engine != nil {
		sysPrompt = c.engine.SystemPrompt()
	}
	// When content blocks are present, bypass the text-only queryFn and dispatch
	// the assembled blocks via QueryWithContent (the image/document path).
	// Existing text-only tests stub queryFn and send no content, so they take
	// the else branch unchanged.
	if len(msg.content) > 0 && c.queryWithContentFn != nil {
		c.queryWithContentFn(ctx, msg.content, sysPrompt)
	} else {
		c.queryFn(ctx, msg.text, sysPrompt)
	}

	// Block until this query's EventQueryEnd closes queryDone. processLoop
	// reads the next inboundCh message only after this returns, preserving
	// one-query-at-a-time semantics despite the async engine query.
	select {
	case <-done:
	case <-ctx.Done():
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

// extractDocName parses "[Document: filename saved at /path]" → "filename".
func extractDocName(header string) (string, bool) {
	const prefix = "[Document: "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	rest := header[len(prefix):]
	if idx := strings.Index(rest, " saved at "); idx > 0 {
		return rest[:idx], true
	}
	return "", false
}

// isRetriable returns true for transient errors worth retrying: network
// failures (timeouts, connection refused). Rate limit is NOT retried —
// the flushBuffer cache logic handles it by retaining content for next flush.
func isRetriable(err error) bool {
	if errors.Is(err, ErrRateLimited) {
		return false
	}
	if errors.Is(err, ErrSessionExpired) {
		return false
	}
	return true
}
