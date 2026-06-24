package wechat

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

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
func (c *WeChatConnector) handleInbound(ctx context.Context, msg inboundMessage) {
	slog.Info("wechat: process inbound", "user", safeID(msg.userID))

	result := c.querySyncFn(ctx, msg.text, "")

	if result.Error != nil {
		slog.Warn("wechat: query error", "user", safeID(msg.userID), "error", result.Error)
		c.sendToUserFn(ctx, msg.userID, fmt.Sprintf("⚠️ Error: %v", result.Error))
		return
	}

	reply := extractAssistantReply(result.Messages)
	slog.Info("wechat: query done", "user", safeID(msg.userID),
		"msgCount", len(result.Messages), "replyLen", len(reply))
	if reply != "" {
		formatted := formatMessage(reply)
		if err := c.sendToUserFn(ctx, msg.userID, formatted); err != nil {
			slog.Error("wechat: send reply failed", "user", safeID(msg.userID), "error", err)
		}
	}
}

// extractAssistantReply returns the last message's text if it's from the assistant.
func extractAssistantReply(msgs []types.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	last := msgs[len(msgs)-1]
	if last.Role != types.RoleAssistant {
		return ""
	}
	var sb strings.Builder
	for _, block := range last.Content {
		if block.Type == types.ContentTypeText {
			sb.WriteString(block.Text)
		}
	}
	return sb.String()
}

// sendToUser sends text to a WeChat user via iLink sendmessage.
func (c *WeChatConnector) sendToUser(ctx context.Context, userID, text string) error {
	contextToken := c.getContextToken(userID)
	return SendMessage(ctx, c.client, c.state.BaseURL, c.state.Token,
		c.state.AccountID, userID, text, contextToken, "")
}
