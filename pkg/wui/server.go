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
	"uuid"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/tool/fileread"
	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/utils"
)

// EnableCompression negotiates permessage-deflate: chat frames are JSON
// (8-15x compressible) and remote clients can sit on ~1Mbps tailscale links
// where an uncompressed ~1MB takeover metadata frame costs ~16 seconds.
var chatUpgrader = websocket.Upgrader{
	CheckOrigin:       func(*http.Request) bool { return true },
	EnableCompression: true,
}

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
		// Level 1 (BestSpeed): streaming frames are latency-sensitive and
		// the CPU cost stays negligible while JSON still compresses ~10x.
		_ = ws.SetCompressionLevel(1)
		serveChatWS(ws, c)
	})
}

// channelSink adapts one wui WS connection era to transport.ClientSink. It
// captures the era's outbound queue at creation: after a takeover the old
// queue is closed and later pushes drop there — stale-era channel frames can
// never leak into the new client's stream.
type channelSink struct {
	q    *outQueue
	done <-chan struct{}
}

func (s channelSink) SendBinary(frame []byte) {
	s.q.push(wsMsg{data: frame, isBinary: true}, s.done)
}

func (s channelSink) SendJSON(payload []byte) {
	s.q.push(wsMsg{data: payload, isBinary: false}, s.done)
}

// serveChatWS drives one WS connection to completion with a unified
// takeover = swap WS + switchEngine. Deactivates old engine first (prevents
// live events from reaching new WS before metadata), installs a fresh
// outbound era (so the writer never writes old-era frames to the new conn
// and pushers parked on the flood are released), swaps activeWS, then calls
// switchEngine which sends metadata (with embedded streamState snapshot).
// The previous WS is notified with a close frame (code 1000, reason
// "taken_over") so the client suppresses auto-reconnect — the client's
// onclose handler checks ev.code === 1000 && ev.reason === 'taken_over'.
func serveChatWS(ws *websocket.Conn, c *WUIConnector) {
	c.slotsMu.RLock()
	if oldSlot := c.slots[c.ActiveID()]; oldSlot != nil {
		oldSlot.active.Store(false)
	}
	c.slotsMu.RUnlock()

	// Swap the era BEFORE the conn: between the two swaps the new queue is
	// empty, so the writer has nothing to misdirect. A batch the writer
	// already TOOK from the old queue is covered by wsWriter's per-frame
	// check, which pairs with THIS swap order by loading the conn BEFORE
	// the era check — observing the new conn implies the outQ swap already
	// happened, so the check that follows breaks.
	// sendMetadata's pristine check reuses the fresh queue.
	oldQ := c.outQ.Swap(newOutQueue(outQueueCapacity))
	oldQ.close() // release any pusher parked on the flood

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
	// Transport channels follow the same single-client rule: the new
	// connection's sink replaces the old one and the old client's channels
	// unbind (providers keep running headless). Without a hub there is
	// nothing to bind, so no sink is stored (channel_open degrades to an
	// error) — storing one would be a dead write nothing reads.
	sink := &channelSink{q: c.outQ.Load(), done: c.done}
	if c.transport != nil {
		if old := c.activeSink.Swap(sink); old != nil {
			c.transport.UnbindClient(old)
		}
	}
	c.switchEngine(c.ActiveID())

	// This handler's era: serveChatWS just installed a fresh queue. A newer
	// era at cleanup time means a takeover superseded this connection.
	myEra := c.outQ.Load()

	c.readLoop(ws, sink)

	// Era-guard the disconnect deactivation: only when no newer era was
	// installed was this the last active connection. A stale readLoop exiting
	// after a newer takeover must not deactivate the slot the new session's
	// sendMetadata just activated — otherwise the old connection's exit kills
	// the new session's live streaming until the next takeover. (Comparing
	// activeWS alone is not enough: the writer's failed-write CAS can nil it
	// for either connection.)
	// Accepted delta: a genuine disconnect after a mid-connection session/model
	// switch (newer era) skips deactivation — benign, switchEngine re-deactivates on the next dial.
	if myEra == c.outQ.Load() {
		c.slotsMu.RLock()
		if slot := c.slots[c.ActiveID()]; slot != nil {
			slot.active.Store(false)
		}
		c.slotsMu.RUnlock()
	}
	// Unbind everything this connection's sink ever bound — unconditional
	// and pointer-scoped. A CAS here misses the straggler case: a
	// channel_open this readLoop dispatched AFTER the takeover Swap bound
	// the superseded sink again, and skipping the unbind would leave the
	// channel "already open" for the new client until the provider
	// reconnects. UnbindClient only touches bindings whose sink is exactly
	// this one, so the new client's channels are never affected.
	if c.transport != nil {
		c.transport.UnbindClient(sink)
	}
	c.activeWS.CompareAndSwap(ws, nil)
	c.abortPendingAsksOnDisconnect()
	slog.Info("wui:disconnect")
}

// readLoop processes inbound JSON messages until the connection closes.
func (c *WUIConnector) readLoop(ws *websocket.Conn, sink *channelSink) {
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
		// Binary frames are [1-byte channel id][payload]. 0x00 is the
		// legacy attachment-chunk lane; any other byte routes the rest of
		// the frame to the transport hub for that channel.
		if msgType == websocket.BinaryMessage {
			if len(data) == 0 {
				continue
			}
			if data[0] == 0x00 {
				if errMsg := acc.handleBinary(data[1:]); errMsg != "" {
					c.sendWS(buildError(errors.New(errMsg)))
				}
			} else if c.transport != nil {
				c.transport.ChannelSend(data[0], data[1:])
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
					// attachment ids must already be staged — normally in
					// acc.saved (uploaded beforehand via
					// attachment_start/binary/end); cancel-restored ids resolve
					// through the connector-level map in resolveContents. A
					// missing id means the upload failed or never ran — reject
					// the whole message so partial state never reaches the
					// engine. An active upload (activeID != "") means the
					// user tried to commit mid-stream — also reject; the
					// frontend learns to wait for attachment_end.
					if acc.activeID != "" {
						c.sendWS(buildError(errors.New("attachment upload in progress; wait for current attachment to finish")))
						continue
					}
					contents, missing := c.resolveContents(acc, msg.Attachments)
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
				var restored []queuedMsgJSON
				// Single locked fetch — separate activeSlot/activeEngine
				// reads could straddle an engine switch.
				slot := c.activeSlot()
				if slot != nil && slot.engine != nil {
					eng := slot.engine
					// Bump FIRST so in-flight parse goroutines that capture
					// an older epoch restore instead of enqueueing.
					slot.cancelEpoch.Add(1)
					want := make(map[string]bool, len(msg.UUIDs))
					for _, id := range msg.UUIDs {
						want[id] = true
					}
					for _, item := range eng.PendingAttachments() {
						if item.Mode != types.ItemModePrompt || item.IsMeta {
							continue
						}
						// Empty uuid list pops everything pending — the
						// client cancels before the server-assigned uuids
						// arrive, and those items must not leak into the
						// next turn.
						if len(want) > 0 && !want[item.UUID] {
							continue
						}
						if eng.RemoveAttachment(item.UUID) {
							removed = append(removed, item.UUID)
							restored = append(restored, c.buildRestoredMsg(item))
						}
					}
				}
				resp, _ := json.Marshal(struct {
					Type     string          `json:"type"`
					Removed  []string        `json:"removed"`
					Restored []queuedMsgJSON `json:"restored,omitempty"`
				}{Type: "cancel_result", Removed: removed, Restored: restored})
				c.sendWS(resp)
			}
		case "restore_attachments":
			c.handleRestoreAttachments()
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
		case "thinking_switch":
			var msg struct {
				Effort string `json:"effort"`
			}
			if json.Unmarshal(data, &msg) == nil {
				c.handleThinkingSwitch(msg.Effort)
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
		case "job_stop":
			var msg struct {
				JobID string `json:"jobID"`
			}
			if json.Unmarshal(data, &msg) == nil && msg.JobID != "" {
				c.handleJobStop(msg.JobID)
			}
		case "context_request":
			c.handleContextRequest()
		case "compact_request":
			go c.handleCompactRequest()
		case "quota_request":
			go c.buildQuota()
		case "channel_open":
			var msg struct {
				Channel string `json:"channel"`
			}
			if json.Unmarshal(data, &msg) == nil && msg.Channel != "" {
				c.handleChannelOpen(sink, msg.Channel)
			}
		case "channel_close":
			var msg struct {
				ChannelID uint8  `json:"channelId"`
				Channel   string `json:"channel"`
			}
			if json.Unmarshal(data, &msg) == nil && c.transport != nil {
				if msg.ChannelID != 0 {
					c.transport.CloseChannel(msg.ChannelID)
				} else if msg.Channel != "" {
					// Closed before the server assigned an id (card closed
					// mid-connecting): name is the only handle the client has.
					c.transport.CloseChannelByName(msg.Channel)
				}
			}
		}
	}
}

// handleChannelOpen binds a transport channel to this connection's sink and
// reports the assigned id, or explains why not.
func (c *WUIConnector) handleChannelOpen(sink *channelSink, channel string) {
	if c.transport == nil {
		c.sendChannelError(channel, "transport unavailable")
		return
	}
	// A straggler open from a superseded connection era would bind a dead
	// sink and notify the wrong client; the hub-side reject races the
	// takeover Swap, so gate it here where the ordering is visible. A nil
	// sink (transport attached after this connection started) passes —
	// there is no era to be superseded by.
	if cur := c.activeSink.Load(); cur != nil && cur != sink {
		c.sendChannelError(channel, "connection superseded")
		return
	}
	id, err := c.transport.OpenChannel(sink, channel)
	if err != nil {
		c.sendChannelError(channel, err.Error())
		return
	}
	c.finishChannelOpen(sink, channel, id)
}

// finishChannelOpen completes a channel open. A bind that a concurrent
// takeover superseded in the window between handleChannelOpen's gate and the
// bind landing is torn down at once — the new client must never observe
// "already open" caused by a bind it superseded. Every other outcome gets
// the channel_opened reply.
func (c *WUIConnector) finishChannelOpen(sink *channelSink, channel string, id uint8) {
	if cur := c.activeSink.Load(); cur != nil && cur != sink {
		c.transport.CloseChannel(id)
		c.sendChannelError(channel, "connection superseded")
		return
	}
	resp, _ := json.Marshal(struct {
		Type      string `json:"type"`
		Channel   string `json:"channel"`
		ChannelID uint8  `json:"channelId"`
	}{Type: "channel_opened", Channel: channel, ChannelID: id})
	c.sendWS(resp)
}

func (c *WUIConnector) sendChannelError(channel, reason string) {
	resp, _ := json.Marshal(struct {
		Type    string `json:"type"`
		Channel string `json:"channel"`
		Error   string `json:"error"`
	}{Type: "channel_error", Channel: channel, Error: reason})
	c.sendWS(resp)
}

// assembleGate, when non-nil, runs at the top of the assemble goroutine so
// tests can hold a message mid-parse and bump the cancel epoch before the
// enqueue decision. nil in production.
var assembleGate func()

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
	slot := c.activeSlot()
	if slot == nil || slot.engine == nil {
		return
	}
	eng := slot.engine
	if text != "" {
		c.appendInputHistory(text)
	}
	// A new commit supersedes any pending rewind stash: the user moved on,
	// so a later restore must not resurrect the aborted prompt's chips.
	c.restoreMu.Lock()
	c.rewindStash = nil
	c.restoreMu.Unlock()
	// Read busy SYNCHRONOUSLY — see the doc comment for the race this prevents.
	busy := eng.IsBusy()
	// Captured on the readLoop BEFORE the parse goroutine starts: if the
	// user cancels the queue while a document is still parsing, the epoch
	// bumps and the assembled item is restored to the client instead of
	// enqueued (the client has already cleared its queue bubbles).
	epoch := slot.cancelEpoch.Load()
	go func() {
		// Test gate for the mid-parse cancel window: tests block here to
		// hold the goroutine between epoch capture and the enqueue compare.
		// nil in production.
		if assembleGate != nil {
			assembleGate()
		}
		ctx := engine.WithSource(context.Background(), "wui")
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
			if slot.cancelEpoch.Load() != epoch {
				// Cancelled mid-parse: the client already cleared its queue
				// bubbles and will never match a late 'queued' uuid stamp.
				// Restore the assembled message directly, same payload shape
				// as a synchronous pop.
				restored := c.buildRestoredMsg(types.QueuedItem{
					Value: text, Content: blocks, Mode: types.ItemModePrompt,
				})
				resp, _ := json.Marshal(struct {
					Type     string          `json:"type"`
					Restored []queuedMsgJSON `json:"restored"`
				}{Type: "cancel_result", Restored: []queuedMsgJSON{restored}})
				c.sendWS(resp)
				return
			}
			attachUUID := uuid.New().String()
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
// fileread and become a base64 image ContentBlock; document items become a
// document REFERENCE block (name/path/mime/size) that the engine expands to
// parsed text at LLM-request time. Unrecognized image bytes are logged and
// skipped — partial degradation is preferred over aborting the entire turn.
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
			// Parse once HERE (like the old inline flow) so the disk cache
			// is warm for every later expansion and the block can carry an
			// exact-ish token estimate; the stored message keeps only the
			// reference. Parse failure leaves estTokens 0 → Size/4 fallback.
			est := 0
			if md, ok := media.ParseDocument(ctx, item.Source.Path); ok {
				est = utils.EstimateTokens(md)
			}
			blocks = append(blocks, types.NewDocumentBlock(name, item.Source.Path, item.Source.Mime, item.Source.Size, est))
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
	slog.Info("wui:handle_stop_enter")
	if eng := c.activeEngine(); eng != nil {
		slog.Info("wui:handle_stop_calling_abort")
		eng.Abort()
		slog.Info("wui:handle_stop_abort_returned")
	} else {
		slog.Info("wui:handle_stop_no_active_engine")
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
