package wechat

import (
	"context"
	"log/slog"
	"time"

	"github.com/liuy/gbot/pkg/types"
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
			c.processInbound(ctx, msg)
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
func (c *WeChatConnector) processInbound(ctx context.Context, msg Message) {
	// Filter self-messages
	if msg.FromUserID == c.state.AccountID {
		return
	}

	// Dedup by message_id
	if !c.dedup.Add(string(msg.MessageID)) {
		return
	}

	// text carries the caption / voice transcription, independent of media
	// download — a message with BOTH an image and a caption produces an image
	// block followed by the caption text block.
	text := extractText(msg.ItemList)

	// When there's a document but no caption, add a default prompt so the LLM
	// has an instruction. Images don't need this — the LLM can see them directly.
	if text == "" && hasFileItem(msg.ItemList) {
		text = "Tell the user you received a document about [one-sentence summary], then ask what the user wants to do with it, in the user's language."
	}

	var content []types.ContentBlock
	if c.mediaCache != nil && hasMedia(msg.ItemList) {
		if block := c.downloadMedia(ctx, msg.ItemList); block.Type != "" {
			content = append(content, block)
			if text != "" {
				content = append(content, types.NewTextBlock(text))
			}
		}
	}

	// Drop messages with neither text nor a usable media block.
	if text == "" && len(content) == 0 {
		return
	}

	// Save context_token for reply
	if msg.ContextToken != "" {
		c.setStateContextToken(msg.FromUserID, msg.ContextToken)
	}

	c.enqueue(msg.FromUserID, text, content)
}

// enqueue sends an inbound message to the serial processing loop.
func (c *WeChatConnector) enqueue(userID, text string, content []types.ContentBlock) {
	msg := inboundMessage{
		userID:  userID,
		text:    text,
		content: content,
	}
	select {
	case c.inboundCh <- msg:
	default:
		slog.Warn("wechat: inbound channel full, dropping message", "user", safeID(userID))
	}
}
