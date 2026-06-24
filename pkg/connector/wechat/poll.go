package wechat

import (
	"context"
	"log/slog"
	"time"
)

// pollLoop is the long-poll loop for inbound messages.
func (c *WeChatConnector) pollLoop(ctx context.Context) {
	defer c.pollWg.Done()

	syncBuf := c.state.SyncBuf
	timeout := LongPollTimeoutMs
	consecutiveFailures := 0

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		resp, err := GetUpdates(ctx, c.client, c.state.BaseURL,
			c.state.Token, syncBuf, time.Duration(timeout)*time.Millisecond)
		if err != nil {
			consecutiveFailures++
			slog.Warn("wechat: poll error",
				"error", err,
				"failures", consecutiveFailures,
				"max", MaxConsecutiveFailures)

			if consecutiveFailures >= MaxConsecutiveFailures {
				slog.Warn("wechat: backoff after consecutive failures")
				select {
				case <-ctx.Done():
					return
				case <-time.After(BackoffDelaySeconds * time.Second):
				}
				consecutiveFailures = 0
			} else {
				select {
				case <-ctx.Done():
					return
				case <-time.After(RetryDelaySeconds * time.Second):
				}
			}
			continue
		}

		// Check for session expired / stale session
		if (resp.Ret == SessionExpiredErrCode || resp.ErrCode == SessionExpiredErrCode) ||
			isStaleSession(resp.Ret, resp.ErrCode, resp.ErrMsg) {
			slog.Error("wechat: session expired, pausing for 10 minutes")
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Minute):
			}
			consecutiveFailures = 0
			continue
		}

		if resp.Ret != 0 || resp.ErrCode != 0 {
			consecutiveFailures++
			slog.Warn("wechat: poll error response",
				"ret", resp.Ret, "errcode", resp.ErrCode,
				"errmsg", resp.ErrMsg,
				"failures", consecutiveFailures)

			if consecutiveFailures >= MaxConsecutiveFailures {
				consecutiveFailures = 0
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(BackoffDelaySeconds * time.Second):
			}
			continue
		}

		consecutiveFailures = 0

		if resp.LongPollingMs > 0 {
			timeout = resp.LongPollingMs
		}

		if resp.GetUpdatesBuf != "" {
			syncBuf = resp.GetUpdatesBuf
			c.stateMu.Lock()
			c.state.SyncBuf = syncBuf
			c.stateMu.Unlock()
			c.SaveState()
		}

		for _, msg := range resp.Msgs {
			c.processInbound(msg)
		}
	}
}

// isStaleSession checks if the response indicates a stale session.
func isStaleSession(ret, errcode int, errmsg string) bool {
	if ret != RateLimitErrCode && errcode != RateLimitErrCode {
		return false
	}
	return errmsg == "unknown error"
}

// processInbound filters and enqueues an inbound message.
func (c *WeChatConnector) processInbound(msg Message) {
	// Filter self-messages
	if msg.FromUserID == c.state.AccountID {
		return
	}

	// Dedup by message_id
	if !c.dedup.Add(string(msg.MessageID)) {
		return
	}

	// Dedup by message_id only — content dedup blocks legitimate repeat messages
	text := extractText(msg.ItemList)

	if text == "" && !hasMedia(msg.ItemList) {
		return
	}

	// Save context_token for reply
	if msg.ContextToken != "" {
		c.setStateContextToken(msg.FromUserID, msg.ContextToken)
	}

	// Enqueue for processing
	c.enqueue(msg.FromUserID, text, msg.ItemList)
}

// enqueue sends an inbound message to the serial processing loop.
func (c *WeChatConnector) enqueue(userID, text string, _ []Item) {
	msg := inboundMessage{
		userID: userID,
		text:   text,
	}
	select {
	case c.inboundCh <- msg:
	default:
		slog.Warn("wechat: inbound channel full, dropping message", "user", safeID(userID))
	}
}
