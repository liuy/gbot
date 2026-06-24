package wechat

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
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

	// Override in tests to inject errors / record sends.
	sendToUserFn func(ctx context.Context, userID, text string) error
	querySyncFn  func(ctx context.Context, userMsg, sysPrompt string) *engine.QueryResult
}

// New creates a WeChatConnector from loaded state.
func New(eng *engine.Engine) *WeChatConnector {
	h := hub.NewHub()
	c := &WeChatConnector{
		engine:    eng,
		hub:       h,
		client:    &http.Client{},
		inboundCh: make(chan inboundMessage, 100),
		dedup:     newDedupSet(MessageDedupTTLSeconds),
	}
	c.sendToUserFn = c.sendToUser
	c.querySyncFn = func(ctx context.Context, userMsg, _ string) *engine.QueryResult {
		r := eng.QuerySync(ctx, userMsg, eng.SystemPrompt())
		return &r
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

	c.engine.SetDispatcher(c.hub)

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

// Handle receives engine events from Hub.Dispatch (engine goroutine).
func (c *WeChatConnector) Handle(event hub.Event) {
	// For now, events are handled via QuerySync result extraction in queue.go.
	// Hub subscription reserved for future streaming support.
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

// Send delivers a message to a specific WeChat user.
func (c *WeChatConnector) Send(userID, text string) error {
	ctxToken := c.getContextToken(userID)
	clientID := fmt.Sprintf("gbot-weixin-%s", uuid.New().String())
	return SendMessage(context.Background(), c.client,
		c.state.BaseURL, c.state.Token, c.state.AccountID, userID, text, ctxToken, clientID)
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
