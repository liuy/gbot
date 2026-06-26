package wechat

import (
	"context"
	"fmt"
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

		c.processBatch(ctx, resp.Msgs)
	}
}

// isStaleSession checks if the response indicates a stale session.
func isStaleSession(ret, errcode int, errmsg string) bool {
	if ret != RateLimitErrCode && errcode != RateLimitErrCode {
		return false
	}
	return errmsg == "unknown error"
}

// processBatch processes all messages from one GetUpdates response as a group.
// Media messages from the same user are merged into a single inboundMessage so
// the engine processes them in one query. Text-only messages are enqueued
// individually (they represent distinct user turns).
func (c *WeChatConnector) processBatch(ctx context.Context, msgs []Message) {
	var mediaBlocks []types.ContentBlock
	var mergedUserID string
	var mergedToken string

	flush := func() {
		if len(mediaBlocks) == 0 {
			return
		}
		if mergedToken != "" {
			c.setStateContextToken(mergedUserID, mergedToken)
		}
		prompt := mediaPrompt(len(mediaBlocks))
		content := append(mediaBlocks, types.NewTextBlock(prompt))
		c.enqueue(mergedUserID, prompt, content)
		mediaBlocks = nil
		mergedUserID = ""
		mergedToken = ""
	}

	for _, msg := range msgs {
		if msg.FromUserID == c.state.AccountID {
			continue
		}
		if !c.dedup.Add(string(msg.MessageID)) {
			continue
		}

		if c.mediaCache != nil && hasMedia(msg.ItemList) {
			if block := c.downloadMedia(ctx, msg.ItemList); block.Type != "" {
				if mergedUserID == "" {
					mergedUserID = msg.FromUserID
				}
				if msg.ContextToken != "" {
					mergedToken = msg.ContextToken
				}
				mediaBlocks = append(mediaBlocks, block)
			}
			continue
		}

		// Text-only message: flush any pending media first, then enqueue text.
		flush()

		text := extractText(msg.ItemList)
		if text == "" {
			continue
		}
		if msg.ContextToken != "" {
			c.setStateContextToken(msg.FromUserID, msg.ContextToken)
		}
		c.enqueue(msg.FromUserID, text, nil)
	}

	flush()
}

// mediaPrompt returns the default instruction sent to the LLM alongside a
// batch of media blocks. Works for any mix of documents and images.
func mediaPrompt(count int) string {
	return fmt.Sprintf("The user sent %d attachment(s). Tell the user what you received with a one-sentence summary for each, then ask what they want to do, in the user's language.", count)
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
