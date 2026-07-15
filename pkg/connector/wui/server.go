package wui

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
func serveChatWS(ws *websocket.Conn, c *WUIConnector) {
	c.slotsMu.RLock()
	if oldSlot := c.slots[c.ActiveID()]; oldSlot != nil {
		oldSlot.active.Store(false)
	}
	c.slotsMu.RUnlock()

	c.activeWS.Store(ws)
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
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			slog.Info("wui:readLoop exit", "error", err)
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
		}
	}
}

// handleMessageInbound dispatches a user message to the active engine. If a
// query is already active, the message is enqueued via engine.EnqueueAttachment
// (same path as TUI's handleEnqueueMessage) — the engine drains it
// automatically after the current query finishes.
func (c *WUIConnector) handleMessageInbound(text string) {
	eng := c.activeEngine()
	if eng == nil {
		return
	}
	c.appendInputHistory(text)
	if eng.IsBusy() {
		attachUUID := uuid.NewString()
		eng.EnqueueAttachment(types.QueuedItem{
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
		c.sendWS(resp)
		return
	}
	go eng.Query(context.Background(), text, eng.SystemPrompt())
}

// handleAskResponse looks up a pending ask by id and writes the response to
// its ResponseCh. Permission asks carry decision; input asks carry text or
// aborted.
func (c *WUIConnector) handleAskResponse(id, decision, text string, aborted bool) {
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
