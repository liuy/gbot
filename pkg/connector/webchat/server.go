package webchat

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/types"
)

var chatUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

// RegisterChatWS mounts the chat WebSocket endpoint at /ws/chat on mux. The
// handler upgrades the connection, emits connect_status, then runs a readLoop
// (inbound: message / ask_response / stop / cancel_queued / history_request).
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

// serveChatWS drives one WS connection to completion with a 3-step atomic
// takeover under writeMu: (1) invalidate old connection, (2) push
// connect_status + history + streamBuf replay, (3) activate new
// connection. The readLoop blocks until the client disconnects.
func serveChatWS(ws *websocket.Conn, c *WebChatConnector) {
	// Pre-construct connect_status outside the lock (reads engine state).
	connectMsg := c.buildConnectStatusMessage()

	// Pre-compute history snapshot before writeMu (constraint: no engine
	// state access under the lock).
	histMsg := c.buildHistoryMessage("", 10)

	// Pre-compute config before writeMu (reads engine + provider state).
	configMsg := c.buildConfigMessage()

	// Entire takeover sequence under writeMu: invalidate old, push frames,
	// replay buffer, then activate new conn. The engine goroutine's Handle
	// blocks on writeMu during this window — it cannot race with the replay
	// or with buildHistoryMessage's snapshot of engine.Messages().
	c.writeMu.Lock()
	slog.Info("webchat:takeover", "hasHistory", histMsg != nil, "bufFrames", len(c.streamBuf))
	c.activeWS.Store(nil) // 1. old conn invalidated

	// 2. connect_status
	_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second)) // REAL-TIME
	_ = ws.WriteMessage(websocket.TextMessage, connectMsg)

	// 3. history page (committed messages only; in-flight not yet committed)
	if histMsg != nil {
		_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second)) // REAL-TIME
		_ = ws.WriteMessage(websocket.TextMessage, histMsg)
	}

	// config frame — model list + current selection so the frontend can
	// populate the model picker immediately on connect.
	_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second)) // REAL-TIME
	_ = ws.WriteMessage(websocket.TextMessage, configMsg)

	// 4. replay current turn buffer — in-flight deltas that are NOT in
	//    engine.Messages() yet (text_delta, thinking_delta, tool_start,
	//    tool_output_delta, etc., accumulated since the last turn_end).
	//    The buffer is under writeMu so Handle's appends are serialized.
	replayed := 0
	for _, payload := range c.streamBuf {
		_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second)) // REAL-TIME
		if err := ws.WriteMessage(websocket.TextMessage, payload); err != nil {
			slog.Warn("webchat:takeover replay write failed", "frame", replayed, "error", err)
			break
		}
		replayed++
	}
	if replayed > 0 {
		slog.Info("webchat:takeover replay", "frames", replayed)
	}

	// task list — current committed disk state, pushed AFTER replay so the
	// client has historical context first, then the latest task snapshot.
	// Direct write (not from streamBuf) because buildTaskListMessage
	// reads live state, which may be newer than a task_list frame buffered
	// earlier in this same turn.
	if taskMsg := c.buildTaskListMessage(); taskMsg != nil {
		_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second)) // REAL-TIME
		_ = ws.WriteMessage(websocket.TextMessage, taskMsg)
	}

	c.activeWS.Store(ws) // 6. new connection becomes the sink
	c.writeMu.Unlock()
	slog.Info("webchat:takeover complete")

	// Heartbeat: ping every 30s to keep the connection alive through idle
	// proxies and detect dead clients. Shares writeMu to avoid concurrent
	// writes (gorilla constraint).
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				c.writeMu.Lock()
				if cur := c.activeWS.Load(); cur == ws {
					_ = cur.SetWriteDeadline(time.Now().Add(5 * time.Second))
					_ = cur.WriteMessage(websocket.PingMessage, nil)
				}
				c.writeMu.Unlock()
			}
		}
	}()

	// readLoop owns the read side and dispatches inbound (query/ask/stop/
	// cancel_queued/history_request). Blocks until read error (client gone).
	c.readLoop(ws)
	close(done)

	// Connection gone: clear activeWS only if it still points at us (a newer
	// takeover may have already swapped in its own ws — don't clobber it).
	c.clearActiveIfCurrent(ws)
	c.abortPendingAsksOnDisconnect()
	slog.Info("webchat:disconnect")
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
				_ = c.writeDirect(resp)
			}
		case "history_request":
			var msg struct {
				Cursor string `json:"cursor"`
				Limit  int    `json:"limit"`
			}
			if json.Unmarshal(data, &msg) == nil {
				if histMsg := c.buildHistoryMessage(msg.Cursor, msg.Limit); histMsg != nil {
					_ = c.writeDirect(histMsg)
				}
			}
		case "session_list_request":
			if payload := c.buildSessionListMessage(); payload != nil {
				_ = c.writeDirect(payload)
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
				_ = c.engine.UpdateSessionTitle(msg.SessionID, msg.Title)
				if payload := c.buildSessionListMessage(); payload != nil {
					_ = c.writeDirect(payload)
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
		}
	}
}

// handleMessageInbound dispatches a user message to the engine. If a query
// is already active, the message is enqueued via engine.EnqueueAttachment
// (same path as TUI's handleEnqueueMessage) — the engine drains it
// automatically after the current query finishes.
func (c *WebChatConnector) handleMessageInbound(text string) {
	c.appendInputHistory(text)
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
		_ = c.writeDirect(resp)
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

// cleanupConn aborts any pending asks (so the engine doesn't deadlock waiting
// on a disconnected client) and clears activeWS. Does NOT abort the active
// query — a brief disconnect (e.g. mobile browser backgrounding) should not
// interrupt the LLM. The query continues; results land in history and are
// visible on reconnect. Called from Stop (which has no specific ws to clear).
func (c *WebChatConnector) cleanupConn() {
	c.writeMu.Lock()
	c.activeWS.Store(nil)
	c.writeMu.Unlock()
	c.abortPendingAsksOnDisconnect()
}

// clearActiveIfCurrent atomically clears activeWS only if it still equals ws.
// Prevents a stale readLoop-exit from clobbering a newer takeover's connection.
func (c *WebChatConnector) clearActiveIfCurrent(ws *websocket.Conn) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.activeWS.Load() == ws {
		c.activeWS.Store(nil)
	}
}

// abortPendingAsksOnDisconnect aborts pending asks on disconnect. Does NOT
// abort the active query (engine keeps running; results land in history on
// reconnect).
func (c *WebChatConnector) abortPendingAsksOnDisconnect() {
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
	slog.Debug("webchat: asks aborted on disconnect", "count", len(asks))
}
