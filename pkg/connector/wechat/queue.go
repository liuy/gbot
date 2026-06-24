package wechat

import (
	"context"
	"fmt"
	"log/slog"
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
func (c *WeChatConnector) sendToUser(ctx context.Context, userID, text string) error {
	contextToken := c.getContextToken(userID)
	return SendMessage(ctx, c.client, c.state.BaseURL, c.state.Token,
		c.state.AccountID, userID, text, contextToken, "")
}
