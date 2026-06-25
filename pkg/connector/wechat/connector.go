package wechat

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/utils"
)

// inboundMessage represents a message from WeChat to be processed by the engine.
type inboundMessage struct {
	userID string
	text   string
}

// WeChatConnector manages the WeChat connection lifecycle.
// One WeChat account = one connector = one engine = one session.
type WeChatConnector struct {
	engine *engine.Engine
	hub    *hub.Hub
	client *http.Client

	state   *State
	stateMu sync.Mutex

	pollCancel context.CancelFunc
	pollWg     sync.WaitGroup

	inboundCh chan inboundMessage

	ctxTokens sync.Map

	dedup *dedupSet

	// Typing indicator support.
	typingCache *typingTicketCache
	typingAPI   typingAPI

	// Override in tests to inject errors / record sends.
	sendToUserFn func(ctx context.Context, userID, text string) error
	sendMsgFn    func(ctx context.Context, client *http.Client, baseURL, token,
		fromUser, toUser, text, contextToken, clientID string) error
	queryFn func(ctx context.Context, userMessage, systemPrompt string)

	activeUserID string // set in handleInbound, cleared in QueryEnd
	thinkingSecs float64
	searchCount  int
	fileCount    int
	cmdCount     int
	agentCount   int
	textBuffer   strings.Builder
	toolNames    map[string]string // toolUseID→name: ToolEnd only carries ToolUseID, so we capture name at ToolStart
	lastFlush    time.Time

	// processLoop is serial only if handleInbound blocks until the query
	// finishes. Without this, async Query() would let two queries overlap.
	queryDone chan struct{}
}

// New creates a WeChatConnector. h must be the same Hub the TUI handler
// subscribes to — the connector subscribes itself so it receives the
// engine's event stream alongside the TUI.
func New(eng *engine.Engine, h *hub.Hub) *WeChatConnector {
	c := &WeChatConnector{
		engine:      eng,
		hub:         h,
		client:      &http.Client{},
		inboundCh:   make(chan inboundMessage, 100),
		dedup:       newDedupSet(MessageDedupTTLSeconds),
		typingCache: newTypingTicketCache(typingTTL),
	}
	c.sendToUserFn = c.sendToUser
	c.sendMsgFn = SendMessage
	c.queryFn = func(ctx context.Context, userMessage, _ string) {
		eng.Query(ctx, userMessage, eng.SystemPrompt())
	}
	h.Subscribe(c)
	return c
}

// Start begins the poll and processing loops.
func (c *WeChatConnector) Start(ctx context.Context) error {
	state, err := LoadState()
	if err != nil {
		return fmt.Errorf("wechat: load state: %w", err)
	}
	if state == nil {
		return nil
	}
	c.state = state
	c.restoreContextTokens()

	c.typingAPI = &iLinkTypingAPI{
		client:  c.client,
		baseURL: c.state.BaseURL,
		token:   c.state.Token,
	}

	ctx, cancel := context.WithCancel(ctx)
	c.pollCancel = cancel

	c.pollWg.Add(2)
	go c.pollLoop(ctx)
	go c.processLoop(ctx)

	slog.Info("wechat: connector started", "account_id", c.state.AccountID)
	return nil
}

// Stop gracefully stops the connector.
func (c *WeChatConnector) Stop() {
	if c.pollCancel != nil {
		c.pollCancel()
	}
	c.pollWg.Wait()
	c.SaveState()
	slog.Info("wechat: connector stopped")
}

func (c *WeChatConnector) Handle(event hub.Event) {
	switch event.Type {

	case types.EventConnectorUserMessage:
		return

	case types.EventQueryStart:
		c.lastFlush = time.Now()
		c.resetStatCounters()

	case types.EventThinkingEnd:
		if event.Thinking != nil {
			secs := event.Thinking.Duration.Seconds()
			if secs < 0.1 {
				secs = 0.1
			}
			c.thinkingSecs += secs
		}

	case types.EventToolStart:
		if event.ToolUse != nil {
			if c.toolNames == nil {
				c.toolNames = make(map[string]string)
			}
			c.toolNames[event.ToolUse.ID] = event.ToolUse.Name
		}

	case types.EventToolEnd:
		if event.ToolResult != nil {
			name := c.toolNames[event.ToolResult.ToolUseID]
			switch name {
			case "Web":
				c.searchCount++
			case "Read", "Grep", "Glob", "Edit", "Write", "Lsp":
				c.fileCount++
			case "Bash":
				c.cmdCount++
			case "Agent":
				c.agentCount++
			}
			delete(c.toolNames, event.ToolResult.ToolUseID)
		}

	case types.EventTextDelta:
		c.textBuffer.WriteString(event.Text)

	case types.EventTextEnd:
		if c.textBuffer.Len() > 0 {
			c.textBuffer.WriteString("\n\n")
		}
		if c.activeUserID != "" && !c.lastFlush.IsZero() &&
			time.Since(c.lastFlush) >= 5*time.Second {
			c.flushBuffer()
		}

	case types.EventQueryEnd:
		// Engine delivers ALL failures as EventQueryEnd{Error} — never EventError.
		if event.Error != nil && c.activeUserID != "" {
			if err := c.sendToUserFn(context.Background(), c.activeUserID, fmt.Sprintf("⚠️ Error: %v", event.Error)); err != nil {
				slog.Error("wechat: send error message failed", "user", safeID(c.activeUserID), "error", err)
			}
		}
		// Force flush regardless of 5s window.
		c.flushBuffer()
		userID := c.activeUserID
		if userID != "" {
			c.stopTyping(context.Background(), userID)
		}
		c.activeUserID = ""
		c.toolNames = nil
		slog.Info("wechat: query done", "user", safeID(userID), "error", event.Error)
		if c.queryDone != nil {
			close(c.queryDone)
			c.queryDone = nil
		}
	}
}

// flushBuffer sends accumulated stat + text as a single message, then resets.
// If sending fails (e.g. rate limited), content stays in buffer for next flush.
func (c *WeChatConnector) flushBuffer() {
	if c.activeUserID == "" {
		c.resetStatCounters()
		c.textBuffer.Reset()
		return
	}
	text := strings.TrimRight(c.textBuffer.String(), "\n")
	header := c.buildStatHeader()
	if header == "" && text == "" {
		return
	}
	body := text
	if header != "" {
		if text != "" {
			body = header + "\n\n" + text
		} else {
			body = header
		}
	}
	if err := c.sendWeChatReplyErr(context.Background(), c.activeUserID, body); err != nil {
		// Rate limited or send failed — keep stats and text in buffer for next flush.
		// lastFlush unchanged: next flush window counts from the last *successful* flush.
		slog.Warn("wechat: flush failed, retaining buffer", "user", safeID(c.activeUserID), "error", err)
		return
	}
	c.lastFlush = time.Now()
	c.resetStatCounters()
	c.textBuffer.Reset()
}

func (c *WeChatConnector) buildStatHeader() string {
	var parts []string
	if c.thinkingSecs > 0 {
		parts = append(parts, "思考 "+utils.FormatDuration(time.Duration(c.thinkingSecs*float64(time.Second))))
	}
	if c.searchCount > 0 {
		parts = append(parts, fmt.Sprintf("搜索 %d次", c.searchCount))
	}
	if c.fileCount > 0 {
		parts = append(parts, fmt.Sprintf("文件 %d次", c.fileCount))
	}
	if c.cmdCount > 0 {
		parts = append(parts, fmt.Sprintf("命令 %d次", c.cmdCount))
	}
	if c.agentCount > 0 {
		parts = append(parts, fmt.Sprintf("代理 %d次", c.agentCount))
	}
	if len(parts) == 0 {
		return ""
	}
	return "⬡ " + strings.Join(parts, " · ")
}

func (c *WeChatConnector) resetStatCounters() {
	c.thinkingSecs = 0
	c.searchCount = 0
	c.fileCount = 0
	c.cmdCount = 0
	c.agentCount = 0
}

func (c *WeChatConnector) sendWeChatReply(ctx context.Context, userID, text string) {
	if err := c.sendWeChatReplyErr(ctx, userID, text); err != nil {
		slog.Error("wechat: send reply failed", "user", safeID(userID), "error", err)
	}
}

func (c *WeChatConnector) sendWeChatReplyErr(ctx context.Context, userID, text string) error {
	formatted := formatMessage(text)
	if formatted == "" {
		return nil
	}
	for _, chunk := range splitForWeChat(formatted) {
		if err := c.sendToUserFn(ctx, userID, chunk); err != nil {
			return err
		}
	}
	return nil
}

// SaveState persists the connector state to disk.
func (c *WeChatConnector) SaveState() {
	if c.state == nil {
		return
	}
	_ = SaveState(c.state)
}

// getContextToken returns the context_token for a specific WeChat user.
func (c *WeChatConnector) getContextToken(userID string) string {
	val, ok := c.ctxTokens.Load(userID)
	if !ok {
		return ""
	}
	return val.(string)
}

// setStateContextToken stores the context_token for a WeChat user.
func (c *WeChatConnector) setStateContextToken(userID, token string) {
	c.ctxTokens.Store(userID, token)
}

// restoreContextTokens loads saved context tokens from state.
func (c *WeChatConnector) restoreContextTokens() {
	if c.state == nil {
		return
	}
	for k, v := range c.state.ContextTokens {
		if k == "_self_user_id" {
			continue
		}
		c.ctxTokens.Store(k, v)
	}
}

// Send delivers a message to a specific WeChat user, with retry.
func (c *WeChatConnector) Send(userID, text string) error {
	return c.sendToUserFn(context.Background(), userID, text)
}

// safeID truncates an ID for safe logging.
func safeID(value string) string {
	if value == "" {
		return "?"
	}
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}

// hasMedia checks if an item list contains media items.
func hasMedia(items []Item) bool {
	for _, item := range items {
		switch item.Type {
		case ItemImage, ItemVideo, ItemFile, ItemVoice:
			return true
		}
	}
	return false
}

// extractText iterates ItemList and returns the first text item's content,
// handling RefMsg (quoted/forwarded messages) and voice transcriptions.
func extractText(items []Item) string {
	for _, item := range items {
		if item.Type == ItemText && item.TextItem != nil {
			text := item.TextItem.Text
			if item.RefMsg != nil {
				ref := item.RefMsg
				if ref.MessageItem != nil {
					refType := ref.MessageItem.Type
					if refType == ItemImage || refType == ItemVideo || refType == ItemFile || refType == ItemVoice {
						prefix := "[引用媒体]"
						if ref.Title != "" {
							prefix = fmt.Sprintf("[引用媒体: %s]", ref.Title)
						}
						return prefix + "\n" + text
					}
					var parts []string
					if ref.Title != "" {
						parts = append(parts, ref.Title)
					}
					refText := extractText([]Item{*ref.MessageItem})
					if refText != "" {
						parts = append(parts, refText)
					}
					if len(parts) > 0 {
						return fmt.Sprintf("[引用: %s]\n%s", strings.Join(parts, " | "), text)
					}
				}
			}
			return text
		}
	}
	// Check for voice items with transcribed text
	for _, item := range items {
		if item.Type == ItemVoice && item.VoiceItem != nil && item.VoiceItem.Text != "" {
			return item.VoiceItem.Text
		}
	}
	return ""
}
