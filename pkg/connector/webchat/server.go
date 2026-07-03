package webchat

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/types"
)

var chatUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

// RegisterChatWS mounts the chat WebSocket endpoint at /ws/chat on mux. The
// handler upgrades the connection, emits connect_status, then runs a readLoop
// (inbound: message / ask_response / stop) and a writeLoop (outbound:
// events streamed from the connector's channel).
func RegisterChatWS(mux *http.ServeMux, c *WebChatConnector) {
	mux.HandleFunc("/ws/chat", func(w http.ResponseWriter, r *http.Request) {
		ws, err := chatUpgrader.Upgrade(w, r, nil)
		if err != nil {
			http.Error(w, "upgrade failed", http.StatusInternalServerError)
			return
		}
		serveChatWS(ws, c)
	})
}

// serveChatWS drives one WS connection to completion.
func serveChatWS(ws *websocket.Conn, c *WebChatConnector) {
	// Single-client model: a new connection replaces any prior one. The prior
	// writeLoop notices activeWS no longer points at it and exits.
	c.activeWS.Store(ws)

	// connect_status — sent synchronously before the loops start so the client
	// always sees it first.
	connectMsg, _ := json.Marshal(struct {
		Type      string `json:"type"`
		Connected bool   `json:"connected"`
	}{Type: "connect_status", Connected: true})
	_ = ws.WriteMessage(websocket.TextMessage, connectMsg)

	// Send conversation history so the client renders the prior transcript
	// on reconnect / page refresh — same as TUI's SwitchSession path. Only
	// the latest page is sent; the client requests older pages via
	// history_request once mounted (load-more on scroll-up).
	if histMsg := c.buildHistoryMessage("", 10); histMsg != nil {
		_ = ws.WriteMessage(websocket.TextMessage, histMsg)
	}

	done := make(chan struct{})
	var once sync.Once
	closeDone := func() { once.Do(func() { close(done) }) }

	// readLoop owns the connection's read side and drives query/ask/stop
	// dispatch. It exits on read error (client disconnect) and signals done.
	go func() {
		defer closeDone()
		c.readLoop(ws)
	}()

	// writeLoop owns the write side, draining msgCh. It exits when done is
	// closed (reader stopped) or on write error.
	go func() {
		defer closeDone()
		c.writeLoop(ws, done)
	}()

	// Block until both loops finish, then clean up engine-facing state.
	<-done
	c.cleanupConn()
}

// readLoop processes inbound JSON messages until the connection closes.
func (c *WebChatConnector) readLoop(ws *websocket.Conn) {
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			return
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
				Text string `json:"text"`
			}
			if json.Unmarshal(data, &msg) == nil {
				c.handleMessageInbound(msg.Text)
			}
		case "ask_response":
			var msg struct {
				ID       string `json:"id"`
				Decision string `json:"decision"`
				Text     string `json:"text"`
				Aborted  bool   `json:"aborted"`
			}
			if json.Unmarshal(data, &msg) == nil {
				c.handleAskResponse(msg.ID, msg.Decision, msg.Text, msg.Aborted)
			}
		case "stop":
			c.handleStop()
		case "cancel_queued":
			var msg struct {
				UUIDs []string `json:"uuids"`
			}
			if json.Unmarshal(data, &msg) == nil {
				var removed []string
				for _, id := range msg.UUIDs {
					if id != "" && c.engine.RemoveAttachment(id) {
						removed = append(removed, id)
					}
				}
				resp, _ := json.Marshal(struct {
					Type    string   `json:"type"`
					Removed []string `json:"removed"`
				}{Type: "cancel_result", Removed: removed})
				c.msgCh <- resp
			}
		case "history_request":
			// Client requests an older page of history. Route the response
			// through msgCh (NOT a direct ws.WriteMessage) because gorilla
			// websocket does not support concurrent writers — writeLoop owns
			// the write side. Blocking send matches Handle's contract; the
			// 1024 buffer makes a stall practically unreachable.
			var msg struct {
				Cursor string `json:"cursor"`
				Limit  int    `json:"limit"`
			}
			if json.Unmarshal(data, &msg) == nil {
				if histMsg := c.buildHistoryMessage(msg.Cursor, msg.Limit); histMsg != nil {
					c.msgCh <- histMsg
				}
			}
		}
	}
}

// handleMessageInbound dispatches a user message to the engine. If a query
// is already active, the message is enqueued via engine.EnqueueAttachment
// (same path as TUI's handleEnqueueMessage) — the engine drains it
// automatically after the current query finishes.
func (c *WebChatConnector) handleMessageInbound(text string) {
	if c.engine.IsBusy() {
		attachUUID := uuid.NewString()
		c.engine.EnqueueAttachment(types.QueuedItem{
			Value:     text,
			Mode:      types.ItemModePrompt,
			UUID:      attachUUID,
			Priority:  types.PriorityNext,
			Origin:    &types.MessageOrigin{Kind: types.OriginHuman},
			Timestamp: time.Now(),
		})
		// Send queued UUID back to client so it can cancel later.
		resp, _ := json.Marshal(struct {
			Type string `json:"type"`
			UUID string `json:"uuid"`
		}{Type: "queued", UUID: attachUUID})
		c.msgCh <- resp
		return
	}
	go c.engine.Query(context.Background(), text, c.engine.SystemPrompt())
}

// handleAskResponse looks up a pending ask by id and writes the response to
// its ResponseCh. Permission asks carry decision; input asks carry text or
// aborted.
func (c *WebChatConnector) handleAskResponse(id, decision, text string, aborted bool) {
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
		resp = types.AskResponse{Text: text, Aborted: aborted}
	}
	select {
	case ask.ResponseCh <- resp:
	default:
	}
}

// handleStop aborts the active query. Calls engine.Abort directly —
// same path as TUI's ESC handler. This cancels the engine's internal
// activeCancel which propagates to the LLM stream and all tool contexts.
func (c *WebChatConnector) handleStop() {
	c.engine.Abort()
}

// writeLoop drains msgCh, writing each message to the WS connection. Exits
// when done is closed, the channel is closed, or on write error.
func (c *WebChatConnector) writeLoop(ws *websocket.Conn, done <-chan struct{}) {
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()
	for {
		select {
		case <-done:
			return
		case msg, ok := <-c.msgCh:
			if !ok {
				return
			}
			if err := ws.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-pingTicker.C:
			if err := ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// cleanupConn aborts any pending asks (so the engine doesn't deadlock waiting
// on a disconnected client), cancels the active query, and clears activeWS.
// Mirrors TUI's deny-on-disconnect behavior (pkg/tui/handler.go:122).
func (c *WebChatConnector) cleanupConn() {
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
	c.handleStop()
	c.activeWS.Store(nil)
	// Drain msgCh so a blocked Handle (if any) is released.
	c.drainMsgCh()
	slog.Debug("webchat: connection cleaned up", "pending_asks", len(asks))
}

// drainMsgCh empties msgCh without blocking so a producer that raced
// ahead of cleanup is not wedged.
func (c *WebChatConnector) drainMsgCh() {
	for {
		select {
		case <-c.msgCh:
		default:
			return
		}
	}
}
