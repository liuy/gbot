package wechat

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/fileread"
	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/utils"
)

type inboundMessage struct {
	userID  string
	text    string               // caption / voice transcription, used for TUI render + stat headers
	content []types.ContentBlock // text + optional image block; nil → text-only fallback
}

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

	// nil when cache init failed; downloadMedia nil-checks.
	mediaCache *media.Store

	// Typing indicator support.
	typingCache *typingTicketCache
	typingAPI   typingAPI

	// Override in tests to inject errors / record sends.
	sendToUserFn func(ctx context.Context, userID, text string) error
	sendMsgFn    func(ctx context.Context, client *http.Client, baseURL, token,
		fromUser, toUser, text, contextToken, clientID string) error
	queryFn            func(ctx context.Context, userMessage, systemPrompt string)
	queryWithContentFn func(ctx context.Context, content []types.ContentBlock, systemPrompt string)

	activeUserID      string // set in handleInbound, cleared in QueryEnd
	thinkingSecs      float64
	searchCount       int
	fileCount         int
	cmdCount          int
	agentCount        int
	textBuffer        strings.Builder
	toolNames         map[string]string // toolUseID→name: ToolEnd only carries ToolUseID, so we capture name at ToolStart
	lastFlush         time.Time
	lastTypingRefresh time.Time

	// processLoop is serial only if handleInbound blocks until the query
	// finishes. Without this, async Query() would let two queries overlap.
	queryDone chan struct{}
}

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
	c.queryWithContentFn = func(ctx context.Context, content []types.ContentBlock, _ string) {
		eng.QueryWithContent(ctx, content, eng.SystemPrompt())
	}
	// Init failure is non-fatal; log so the operator knows media is disabled.
	if store, err := media.New(); err != nil {
		slog.Warn("wechat: media cache init failed, media attachments disabled", "error", err)
		c.mediaCache = nil
	} else {
		c.mediaCache = store
	}
	h.Subscribe(c)
	return c
}

func (c *WeChatConnector) MediaCache() *media.Store { return c.mediaCache }

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
	// Refresh typing indicator if >5s since last refresh. iLink's typing
	// pulse fades after a few seconds; periodic events keep it alive.
	if c.activeUserID != "" && c.typingAPI != nil &&
		!c.lastTypingRefresh.IsZero() &&
		time.Since(c.lastTypingRefresh) >= 5*time.Second {
		c.startTyping(context.Background(), c.activeUserID)
	}

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

func hasMedia(items []Item) bool {
	for _, item := range items {
		switch item.Type {
		case ItemImage, ItemVideo, ItemFile, ItemVoice:
			return true
		}
	}
	return false
}

func hasFileItem(items []Item) bool {
	for _, item := range items {
		if item.Type == ItemFile {
			return true
		}
	}
	return false
}

// reports the two retrievable-bytes signals: a full URL or encrypt query param.
func downloadableMedia(m *MediaRef) bool {
	if m == nil {
		return false
	}
	return m.FullURL != "" || m.EncryptQueryParam != ""
}

// Priority IMAGE > VIDEO > FILE > VOICE matches openclaw process-message.ts:124-138.
func pickMediaItem(items []Item) *Item {
	// IMAGE
	for i := range items {
		it := &items[i]
		if it.Type == ItemImage && it.ImageItem != nil && downloadableMedia(it.ImageItem.Media) {
			return it
		}
	}
	// VIDEO
	for i := range items {
		it := &items[i]
		if it.Type == ItemVideo && it.VideoItem != nil && downloadableMedia(it.VideoItem.Media) {
			return it
		}
	}
	// FILE
	for i := range items {
		it := &items[i]
		if it.Type == ItemFile && it.FileItem != nil && downloadableMedia(it.FileItem.Media) {
			return it
		}
	}
	// VOICE — only when there is no transcription text (otherwise the text path
	// already carries the content; audio transcoding is out of scope).
	for i := range items {
		it := &items[i]
		if it.Type == ItemVoice && it.VoiceItem != nil && it.VoiceItem.Text == "" &&
			downloadableMedia(it.VoiceItem.Media) {
			return it
		}
	}
	return nil
}

// Documents are parsed inline via fileread; zero-value block on failure degrades to text-only.
func (c *WeChatConnector) downloadMedia(ctx context.Context, items []Item) types.ContentBlock {
	if c.mediaCache == nil {
		return types.ContentBlock{}
	}
	item := pickMediaItem(items)
	if item == nil {
		return types.ContentBlock{}
	}

	switch item.Type {
	case ItemImage:
		return c.downloadImage(ctx, item.ImageItem)
	case ItemFile:
		return c.downloadFile(ctx, item.FileItem)
	case ItemVideo:
		// Video is rarely useful to an LLM and unsupported here; log and fall
		// through to text-only.
		slog.Info("wechat: video media download not supported, skipping", "type", "video")
		return types.ContentBlock{}
	case ItemVoice:
		// Voice audio transcoding (SILK→WAV) is out of scope. A voice WITH a
		// transcription was already skipped by pickMediaItem; a voice WITHOUT
		// one has nothing to surface, so degrade to text-only.
		slog.Info("wechat: voice media download not supported, skipping", "type", "voice")
		return types.ContentBlock{}
	}
	return types.ContentBlock{}
}

func (c *WeChatConnector) downloadImage(ctx context.Context, img *MediaItemHolder) types.ContentBlock {
	if img == nil || img.Media == nil {
		return types.ContentBlock{}
	}
	data, err := c.fetchMediaBytes(ctx, img.Media)
	if err != nil {
		slog.Warn("wechat: image download failed, falling back to text-only", "error", err)
		return types.ContentBlock{}
	}
	mime := media.SniffImageMime(data)
	if mime == "" {
		// The image content block can only carry image/* media; if the bytes
		// are not a recognized image format we cannot send an image block.
		slog.Warn("wechat: downloaded media is not a recognized image format, skipping", "len", len(data))
		return types.ContentBlock{}
	}
	ext := media.ExtFromMime(mime)
	path, err := c.mediaCache.Save(media.CategoryImage, data, ext)
	if err != nil {
		slog.Warn("wechat: image cache save failed, falling back to text-only", "error", err)
		return types.ContentBlock{}
	}
	return types.NewFileImageBlock(mime, path)
}

func (c *WeChatConnector) downloadFile(ctx context.Context, f *FileItem) types.ContentBlock {
	if f == nil || f.Media == nil {
		return types.ContentBlock{}
	}
	data, err := c.fetchMediaBytes(ctx, f.Media)
	if err != nil {
		slog.Warn("wechat: file download failed, falling back to text-only", "error", err)
		return types.ContentBlock{}
	}
	// Preserve the original filename extension — round-tripping through
	// MimeFromExt→ExtFromMime loses types like .pptx/.docx that are in the
	// forward map but not the reverse map, collapsing them to .bin.
	ext := filepath.Ext(f.FileName)
	if ext == "" {
		ext = ".bin"
	}
	path, err := c.mediaCache.Save(media.CategoryDocument, data, ext)
	if err != nil {
		slog.Warn("wechat: file cache save failed, falling back to text-only", "error", err)
		return types.ContentBlock{}
	}
	name := f.FileName
	if name == "" {
		name = filepath.Base(path)
	}

	// Parse the document inline so the LLM gets content, not a path to chase.
	input, _ := json.Marshal(fileread.Input{FilePath: path})
	result, err := fileread.Execute(ctx, input, &tool.ToolUseContext{UncappedOutput: true})
	if err != nil || result == nil {
		slog.Warn("wechat: document parse failed, sending path as fallback", "file", name, "error", err)
		return types.NewTextBlock(fmt.Sprintf("[Document attachment: %s saved at %s]", name, path))
	}
	// ToolResult.Data is `any` — extract text content from TextOutput.
	content := ""
	if out, ok := result.Data.(fileread.TextOutput); ok {
		content = out.Content
	} else if s, ok := result.Data.(string); ok {
		content = s
	}
	if content == "" {
		slog.Warn("wechat: document parse returned empty content, sending path as fallback", "file", name)
		return types.NewTextBlock(fmt.Sprintf("[Document attachment: %s saved at %s]", name, path))
	}
	slog.Info("wechat: document parsed inline", "file", name, "contentLen", len(content))
	return types.NewTextBlock(fmt.Sprintf("[Document: %s saved at %s]\n%s", name, path, content))
}

func (c *WeChatConnector) fetchMediaBytes(ctx context.Context, ref *MediaRef) ([]byte, error) {
	if ref.AesKey != "" {
		return media.DownloadAndDecrypt(ctx, c.client, ref.FullURL, ref.AesKey)
	}
	return media.DownloadPlain(ctx, c.client, ref.FullURL)
}

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
