package wechat

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
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

		resp, err := c.getUpdatesFn(ctx, c.client, c.state.BaseURL,
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

		// Voice with transcription is text-only; voice without transcription
		// can't be processed (SILK decode out of scope), so treat both as text.
		hasVoiceOnly := hasVoiceItem(msg.ItemList) && !hasNonVoiceMedia(msg.ItemList)
		if c.mediaCache != nil && hasMedia(msg.ItemList) && !hasVoiceOnly {
			if block := c.downloadMedia(ctx, msg.ItemList); block.Type != "" {
				if mergedUserID != "" && mergedUserID != msg.FromUserID {
					flush()
				}
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

// enqueue routes an inbound message. When the engine is mid-query, the
// message attaches to the running query via EnqueueAttachment (the engine
// auto-drains and runs the next turn when it next goes idle). When idle, it
// enters the serial inboundCh for the normal Query path. Content blocks
// (images/documents) are carried verbatim through the attachment queue.
func (c *WeChatConnector) enqueue(userID, text string, content []types.ContentBlock) {
	if c.isBusyFn != nil && c.isBusyFn() {
		value := text
		if len(content) > 0 {
			value = attachmentValue(text, content)
		}
		c.engine.EnqueueAttachment(types.QueuedItem{
			Value:     value,
			Content:   content,
			Mode:      types.ItemModePrompt,
			Priority:  types.PriorityNext,
			Origin:    &types.MessageOrigin{Kind: types.OriginHuman},
			UUID:      uuid.NewString(),
			Timestamp: time.Now(),
		})
		return
	}
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

// attachmentValue serializes a message into the string Value carried by a
// QueuedItem. Text-only messages pass through. Document text blocks are
// concatenated. Image blocks degrade to "[image]" because the Value string is
// what the LLM sees in the Attachment.Prompt metadata; the actual image block
// is delivered via Content so the degradation only affects the metadata label.
func attachmentValue(text string, content []types.ContentBlock) string {
	if len(content) == 0 {
		return text
	}
	var parts []string
	for _, cb := range content {
		switch cb.Type {
		case types.ContentTypeText:
			if cb.Text != "" {
				parts = append(parts, cb.Text)
			}
		case types.ContentTypeImage:
			parts = append(parts, "[image]")
		}
	}
	joined := strings.Join(parts, "\n\n")
	if joined == "" {
		return text
	}
	return joined
}
