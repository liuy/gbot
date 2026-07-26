package wui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/fileread"
	"github.com/liuy/gbot/pkg/types"
)

var chatUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

// RegisterChatWS mounts the chat WebSocket endpoint at /ws/chat on mux. The
// handler upgrades the connection, emits connect_status, then runs a readLoop
// (inbound: message / ask_response / stop / cancel_queued / history_request).
func RegisterChatWS(mux *http.ServeMux, c *WUIConnector) {
	mux.HandleFunc("/ws/chat", func(w http.ResponseWriter, r *http.Request) {
		ws, err := chatUpgrader.Upgrade(w, r, nil)
		if err != nil {
			http.Error(w, "upgrade failed", http.StatusInternalServerError)
			return
		}
		serveChatWS(ws, c)
	})
}

// serveChatWS drives one WS connection to completion with a unified
// takeover = swap WS + switchEngine. Deactivates old engine first (prevents
// live events from reaching new WS before metadata), swaps activeWS, then
// calls switchEngine which sends metadata (with embedded streamState snapshot).
// The previous WS is notified with a close frame (code 1000, reason
// "taken_over") so the client suppresses auto-reconnect — the client's
// onclose handler checks ev.code === 1000 && ev.reason === 'taken_over'.
func serveChatWS(ws *websocket.Conn, c *WUIConnector) {
	c.slotsMu.RLock()
	if oldSlot := c.slots[c.ActiveID()]; oldSlot != nil {
		oldSlot.active.Store(false)
	}
	c.slotsMu.RUnlock()

	if oldWS := c.activeWS.Swap(ws); oldWS != nil {
		// Send a close frame with code 1000 + reason "taken_over" so the
		// old client knows not to auto-reconnect (avoids ping-pong between
		// two clients). WriteControl is explicitly safe for concurrent use
		// with the wsWriter goroutine's WriteMessage calls — gorilla
		// documents this as the only concurrency-safe write method.
		closeFrame := websocket.FormatCloseMessage(1000, "taken_over")
		_ = oldWS.WriteControl(websocket.CloseMessage, closeFrame, time.Now().Add(time.Second))
		_ = oldWS.Close()
	}
	c.switchEngine(c.ActiveID())

	c.readLoop(ws)

	c.activeWS.CompareAndSwap(ws, nil)
	c.slotsMu.RLock()
	if slot := c.slots[c.ActiveID()]; slot != nil {
		slot.active.Store(false)
	}
	c.slotsMu.RUnlock()
	c.abortPendingAsksOnDisconnect()
	slog.Info("wui:disconnect")
}

// readLoop processes inbound JSON messages until the connection closes.
func (c *WUIConnector) readLoop(ws *websocket.Conn) {
	// Gorilla's default ReadLimit (64 MiB) would let a >50 MiB rogue single
	// binary frame into memory before our handleStart size check can reject
	// it; lower the limit explicitly so the read stops at the frame boundary.
	ws.SetReadLimit(maxAttachmentSize + 64*1024)
	// acc tracks the in-flight chunked-upload state for this WS connection.
	// Lives on readLoop's stack so disconnect naturally GC's it; no
	// connector-level cleanup is needed.
	acc := &attachmentAccumulator{}
	for {
		msgType, data, err := ws.ReadMessage()
		if err != nil {
			slog.Info("wui:readLoop exit", "error", err)
			return
		}
		// Binary frames are attachment chunk payloads — route them to the
		// accumulator before the JSON-type peek (binary bytes fail
		// json.Unmarshal and would otherwise be silently dropped).
		if msgType == websocket.BinaryMessage {
			if errMsg := acc.handleBinary(data); errMsg != "" {
				c.sendWS(buildError(errors.New(errMsg)))
			}
			continue
		}
		// Peek type first, then unmarshal into the right shape. Using a single
		// struct with duplicate json tags would silently drop fields.
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &head) != nil {
			continue
		}
		switch head.Type {
		case "message":
			var msg struct {
				Text        string              `json:"text"`
				Content     []inboundContent    `json:"content"`
				Attachments []inboundAttachment `json:"attachments"`
			}
			if json.Unmarshal(data, &msg) == nil {
				if len(msg.Attachments) > 0 {
					// Two-phase commit: user_message is the commit frame. All
					// attachment ids must already be in acc.saved (uploaded
					// beforehand via attachment_start/binary/end). A missing
					// id means the upload failed or never ran — reject the
					// whole message so partial state never reaches the
					// engine. An active upload (activeID != "") means the
					// user tried to commit mid-stream — also reject; the
					// frontend learns to wait for attachment_end.
					if acc.activeID != "" {
						c.sendWS(buildError(errors.New("attachment upload in progress; wait for current attachment to finish")))
						continue
					}
					contents, missing := acc.buildContents(msg.Attachments)
					if missing != "" {
						c.sendWS(buildError(fmt.Errorf("missing attachment uploads: %s", missing)))
						continue
					}
					text := msg.Text
					acc.reset()
					c.handleMessageInbound(text, contents)
					continue
				}
				// Legacy path: callers that pass content[] directly (existing
				// inbound_test.go fixtures). The new frontend never sends it.
				c.handleMessageInbound(msg.Text, msg.Content)
			}
		case "attachment_start":
			var msg attachmentStartMsg
			if json.Unmarshal(data, &msg) == nil {
				if errMsg := acc.handleStart(msg); errMsg != "" {
					c.sendWS(buildError(errors.New(errMsg)))
				}
			}
		case "attachment_end":
			var msg attachmentEndMsg
			if json.Unmarshal(data, &msg) == nil {
				if errMsg := acc.handleEnd(msg.ID, c.mediaCache); errMsg != "" {
					c.sendWS(buildError(errors.New(errMsg)))
				}
			}
		case "ask_response":
			var msg struct {
				ID       string `json:"id"`
				Decision string `json:"decision"`
				Text     string `json:"text"`
				Aborted  bool   `json:"aborted"`
				Timeout  bool   `json:"timeout"`
			}
			if json.Unmarshal(data, &msg) == nil {
				c.handleAskResponse(msg.ID, msg.Decision, msg.Text, msg.Aborted, msg.Timeout)
			}
		case "stop":
			c.handleStop()
		case "cancel_queued":
			var msg struct {
				UUIDs []string `json:"uuids"`
			}
			if json.Unmarshal(data, &msg) == nil {
				var removed []string
				eng := c.activeEngine()
				for _, id := range msg.UUIDs {
					if id != "" && eng != nil && eng.RemoveAttachment(id) {
						removed = append(removed, id)
					}
				}
				resp, _ := json.Marshal(struct {
					Type    string   `json:"type"`
					Removed []string `json:"removed"`
				}{Type: "cancel_result", Removed: removed})
				c.sendWS(resp)
			}
		case "history_request":
			var msg struct {
				Cursor string `json:"cursor"`
				Limit  int    `json:"limit"`
			}
			if json.Unmarshal(data, &msg) == nil {
				if histMsg := c.buildHistory(c.activeSlot(), msg.Cursor, msg.Limit); histMsg != nil {
					c.sendWS(histMsg)
				}
			}
		case "session_list_request":
			if payload := c.buildSessionList(); payload != nil {
				c.sendWS(payload)
			}
		case "session_switch":
			var msg struct {
				SessionID string `json:"sessionID"`
			}
			if json.Unmarshal(data, &msg) == nil && msg.SessionID != "" {
				c.handleSessionSwitch(msg.SessionID)
			}
		case "session_rename":
			var msg struct {
				SessionID string `json:"sessionID"`
				Title     string `json:"title"`
			}
			if json.Unmarshal(data, &msg) == nil {
				if eng := c.activeEngine(); eng != nil {
					_ = eng.UpdateSessionTitle(msg.SessionID, msg.Title)
				}
				if payload := c.buildSessionList(); payload != nil {
					c.sendWS(payload)
				}
			}
		case "session_new":
			c.handleSessionNew()
		case "model_switch":
			var msg struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			}
			if json.Unmarshal(data, &msg) == nil {
				c.handleModelSwitch(msg.Provider, msg.Model)
			}
		case "engine_switch":
			var msg struct {
				EngineID string `json:"engineID"`
			}
			if json.Unmarshal(data, &msg) == nil && msg.EngineID != "" {
				c.handleEngineSwitch(msg.EngineID)
			}
		case "engine_new":
			var msg struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(data, &msg) == nil {
				c.handleEngineNew(msg.Name)
			}
		case "context_request":
			c.handleContextRequest()
		case "quota_request":
			go c.buildQuota()
		}
	}
}

// handleMessageInbound dispatches a user message (text + optional content
// blocks) to the active engine. Content blocks reference files saved by the
// WS chunked-upload path (or the legacy content[] field for tests); each is
// converted to a types.ContentBlock before reaching the engine. If the
// engine is busy, the assembled blocks are enqueued as a single QueuedItem
// (Content field overrides Value).
//
// Dispatch decision ordering (critical for correctness):
//  1. c.appendInputHistory(text) — synchronous, fast
//  2. busy := eng.IsBusy()       — synchronous (atomic read of engine state)
//  3. go func() { assemble blocks; enqueue | Query* }()  — async
//
// IsBusy MUST be read synchronously, BEFORE the goroutine starts. If it is
// read inside the goroutine, two rapid messages each spawn a goroutine that
// waits through a 10-30s PDF parse and both observe IsBusy()==false (because
// neither has called Query yet) — then both call QueryWithContent and the
// engine's Query/QueryWithContent (pkg/engine/engine.go) overwrites
// e.activeCancel without guarding against an in-flight query, corrupting
// engine state. Reading busy up-front on the readLoop closes the race:
// inbound frames are processed serially by readLoop, so the second
// invocation's `busy := eng.IsBusy()` read happens AFTER the first
// goroutine has already been launched; the first goroutine's `eng.Query*`
// call lands microseconds later (block assembly is trivial for text-only
// messages), flipping `IsBusy()` to true before the second readLoop
// iteration reaches its read.
//
// History-append stays synchronous because input-history tests
// (input_history_test.go) assert on history state immediately after the
// call. Everything else (block assembly, fileread.Execute, engine dispatch)
// runs in a goroutine so a slow 50MB PDF parse cannot block readLoop from
// draining concurrent stop / ask_response / cancel_queued frames.
func (c *WUIConnector) handleMessageInbound(text string, content []inboundContent) {
	eng := c.activeEngine()
	if eng == nil {
		return
	}
	if text != "" {
		c.appendInputHistory(text)
	}
	// Read busy SYNCHRONOUSLY — see the doc comment for the race this prevents.
	busy := eng.IsBusy()
	go func() {
		ctx := context.Background()
		blocks := c.assembleContentBlocks(ctx, text, content)
		if len(blocks) == 0 {
			// All attachments were rejected AND there was no text. Surface a
			// WS error so the frontend isn't left waiting for an engine
			// response that will never arrive.
			if len(content) > 0 {
				c.sendWS(buildError(errors.New("attachments rejected")))
			}
			return
		}
		if busy {
			attachUUID := uuid.NewString()
			eng.EnqueueAttachment(types.QueuedItem{
				Value:     text,
				Content:   blocks,
				Mode:      types.ItemModePrompt,
				UUID:      attachUUID,
				Priority:  types.PriorityNext,
				Origin:    &types.MessageOrigin{Kind: types.OriginHuman},
				Timestamp: time.Now(),
			})
			resp, _ := json.Marshal(struct {
				Type string `json:"type"`
				UUID string `json:"uuid"`
			}{Type: "queued", UUID: attachUUID})
			c.sendWS(resp)
			return
		}
		if len(content) == 0 {
			eng.Query(ctx, text, eng.SystemPrompt())
			return
		}
		eng.QueryWithContent(ctx, blocks, eng.SystemPrompt())
	}()
}

// assembleContentBlocks converts text + inbound content items into the
// []types.ContentBlock shape the engine consumes. Text is the first block
// (when non-empty); image items are decoded + resized + base64-encoded via
// fileread and become a base64 image ContentBlock; document items are parsed
// inline via fileread and become a [Document: ...] text block. Unrecognized
// image bytes are logged and skipped — partial
// degradation is preferred over aborting the entire turn.
func (c *WUIConnector) assembleContentBlocks(ctx context.Context, text string, content []inboundContent) []types.ContentBlock {
	var blocks []types.ContentBlock
	if text != "" {
		blocks = append(blocks, types.NewTextBlock(text))
	}
	for _, item := range content {
		if c.mediaCache == nil {
			slog.Warn("wui: media cache not configured, skipping attachment")
			continue
		}
		switch item.Type {
		case "image":
			mime := item.Source.Mime
			if mime == "" {
				// Re-sniff the first 512 bytes — SniffImageMime only needs
				// the first ~12 bytes, so reading the whole 50MB image just
				// to sniff was wasteful. The result guards against passing
				// unrecognizable bytes into fileread's full decode attempt.
				f, err := os.Open(item.Source.Path)
				if err != nil {
					slog.Warn("wui: open image failed", "path", item.Source.Path, "error", err)
					continue
				}
				head, _ := io.ReadAll(io.LimitReader(f, 512))
				_ = f.Close()
				mime = media.SniffImageMime(head)
				if mime == "" {
					slog.Warn("wui: image bytes not a recognized format", "path", item.Source.Path)
					continue
				}
			}
			// fileread owns decode + resize + base64 encode — the single
			// shared path with the wechat connector. MediaType is derived
			// from the decoded image (more accurate than the sniffed mime).
			if block, ok := fileread.ReadAsImageBlock(ctx, item.Source.Path); ok {
				blocks = append(blocks, block)
			} else {
				slog.Warn("wui: image resize/encode failed, skipping", "path", item.Source.Path)
			}
		case "document":
			name := item.Source.Name
			if name == "" {
				name = filepath.Base(item.Source.Path)
			}
			blocks = append(blocks, c.parseDocument(ctx, item.Source.Path, name))
		default:
			slog.Warn("wui: unknown content type, skipping", "type", item.Type)
		}
	}
	return blocks
}

// handleAskResponse looks up a pending ask by id and writes the response to
// its ResponseCh. Permission asks carry decision; input asks carry text or
// aborted (with timeout flag distinguishing countdown expiry from user cancel).
func (c *WUIConnector) handleAskResponse(id, decision, text string, aborted bool, timeout bool) {
	c.pendingMu.Lock()
	ask := c.pendingAsks[id]
	delete(c.pendingAsks, id)
	c.pendingMu.Unlock()
	if ask == nil || ask.ResponseCh == nil {
		return
	}
	var resp types.AskResponse
	if ask.Kind == types.AskPermission {
		resp = types.AskResponse{Decision: types.UserDecision(decision)}
	} else {
		resp = types.AskResponse{Text: text, Aborted: aborted, Timeout: timeout}
	}
	select {
	case ask.ResponseCh <- resp:
	default:
	}
}

// handleStop aborts the active query. Calls engine.Abort directly —
// same path as TUI's ESC handler. This cancels the engine's internal
// activeCancel which propagates to the LLM stream and all tool contexts.
func (c *WUIConnector) handleStop() {
	if eng := c.activeEngine(); eng != nil {
		eng.Abort()
	}
}

// cleanupConn aborts any pending asks (so the engine doesn't deadlock waiting
// on a disconnected client) and clears activeWS. Does NOT abort the active
// query — a brief disconnect (e.g. mobile browser backgrounding) should not
// interrupt the LLM. The query continues; results land in history and are
// visible on reconnect. Called from Stop (which has no specific ws to clear).
func (c *WUIConnector) cleanupConn() {
	c.activeWS.Store(nil)
	c.abortPendingAsksOnDisconnect()
}

// abortPendingAsksOnDisconnect aborts pending asks on disconnect. Does NOT
// abort the active query (engine keeps running; results land in history on
// reconnect).
func (c *WUIConnector) abortPendingAsksOnDisconnect() {
	c.pendingMu.Lock()
	asks := c.pendingAsks
	c.pendingAsks = make(map[string]*types.AskEvent)
	c.pendingMu.Unlock()
	for _, ask := range asks {
		if ask.ResponseCh != nil {
			select {
			case ask.ResponseCh <- types.AskResponse{Aborted: true}:
			default:
			}
		}
	}
	slog.Debug("wui: asks aborted on disconnect", "count", len(asks))
}

// parseDocument reads the file at path via fileread.Execute and returns a
// text content block shaped like wechat's downloadFile: a [Document: name
// saved at path] header line followed by the parsed content. On any failure
// (parse error, empty content, unsupported format) the block degrades to
// just the header line — never an error to the caller, because document
// attachment should not abort the entire user turn.
//
// Mirrors wechat connector's downloadFile logic 1:1 (line numbers shift
// across revisions; locate by function name).
func (c *WUIConnector) parseDocument(ctx context.Context, path, name string) types.ContentBlock {
	input, _ := json.Marshal(fileread.Input{FilePath: path})
	result, err := fileread.Execute(ctx, input, &tool.ToolUseContext{UncappedOutput: true})
	if err != nil || result == nil {
		slog.Warn("wui: document parse failed, sending path as fallback", "file", name, "error", err)
		return types.NewTextBlock(fmt.Sprintf("[Document: %s saved at %s]", name, path))
	}
	content := ""
	if out, ok := result.Data.(fileread.TextOutput); ok {
		content = out.Content
	} else if s, ok := result.Data.(string); ok {
		content = s
	}
	if content == "" {
		slog.Warn("wui: document parse returned empty content, sending path as fallback", "file", name)
		return types.NewTextBlock(fmt.Sprintf("[Document: %s saved at %s]", name, path))
	}
	slog.Info("wui: document parsed inline", "file", name, "contentLen", len(content))
	return types.NewTextBlock(fmt.Sprintf("[Document: %s saved at %s]\n%s", name, path, content))
}
