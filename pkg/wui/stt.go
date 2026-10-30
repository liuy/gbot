package wui

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/audio/stt"
	"github.com/liuy/gbot/pkg/config"
)

// sttUpgrader upgrades /ws/stt clients. No permessage-deflate: frames are
// binary PCM (low compression yield, saves CPU) — same choice as the VNC
// upgrader.
var sttUpgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

// sttWriterDoneHook / sttTerminalSendHook are test observation seams (nil in
// production). Both are read without a lock; hook-using tests must not call
// t.Parallel and must restore the nil value with defer.
//
//   - sttWriterDoneHook is called synchronously when a live utterance starts,
//     before the writer goroutine launches — a test holding the channel can
//     never miss its close.
//   - sttTerminalSendHook is called inside the terminal exit after the
//     terminal-flag check passes: it is the only reliable kill point for a
//     deleted flag check, because gorilla refuses WriteMessage after a close
//     frame (bytes never reach the client — the second send is invisible on
//     the wire).
var (
	sttWriterDoneHook   func(<-chan struct{})
	sttTerminalSendHook func(kind, msg string)
)

// sttPCMChFrames is the live upstream backpressure window: 64 frames ≈ 2s of
// audio. A full channel means the upstream stopped reading the request body
// for ≥2s — a stall, not something to wait out (readLoop must stay live to
// receive stop/teardown).
const sttPCMChFrames = 64

// sttLiveClient issues the live streaming request (no overall timeout — the
// connection ctx governs lifetime). Package-level as a test seam: kernel
// send-buffer autotuning (up to tcp_wmem[2]=4MB) otherwise absorbs
// megabytes before backpressure can engage, pushing saturation past the
// endpointer's 60s audio budget; stall tests pin the socket buffer here.
var sttLiveClient = &http.Client{}

// sttBatchLimit caps the batch-mode PCM buffer (aligned with the WS read
// limit; 60s × 32KB/s ≈ 1.9MB, so the cap is pure defense).
const sttBatchLimit = 20 << 20

// sttStallGrace is how long handleBinary waits on a full pcmCh before
// declaring an upstream stall: burst-filled queues must give the writer time
// to drain (and transport aborts time to win the terminal race), matching
// the "≈2s unconsumed" semantics of the 64-frame queue at speech rate.
const sttStallGrace = 500 * time.Millisecond

// sttReadLimit caps a single WS frame: one binary frame above the 20MB
// transcription budget is protocol abuse, not audio.
const sttReadLimit = 20<<20 + 1024

// RegisterSTTRoutes mounts the dictation WebSocket bridge at /ws/stt.
func RegisterSTTRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /ws/stt", func(w http.ResponseWriter, r *http.Request) {
		ws, err := sttUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serveSttWS(ws)
	})
}

// sttConn is one /ws/stt connection = one utterance (done/error are terminal
// and close the socket). All fields without a note are readLoop-only.
type sttConn struct {
	ws     *websocket.Conn
	sendMu sync.Mutex // serializes every ws.WriteMessage / terminal flag

	// terminal is the single dedup mechanism for done/error (first sender
	// wins). Guarded by sendMu.
	terminal bool

	active     bool // start accepted, utterance in progress
	finalizing bool // readLoop-side idempotency: race-frame drop, stop dedup
	prompt     string

	// mode probe cache (once per connection).
	modeLive   bool
	modeProbed bool

	epd endpointer

	// batch state
	pcmBuf []byte

	// live state — created on readLoop inside start handling before any
	// goroutine launches, so no readLoop access races their setup.
	pcmCh      chan []byte
	writerDone chan struct{}

	// upstream target resolved at start.
	baseURL string
	apiKey  string
	model   string

	ctx    context.Context
	cancel context.CancelFunc
}

func serveSttWS(ws *websocket.Conn) {
	ws.SetReadLimit(sttReadLimit)
	c := &sttConn{ws: ws}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	defer c.cancel()
	for {
		mt, data, err := ws.ReadMessage()
		if err != nil {
			// Teardown: cancel aborts the live request; the writer exits via
			// its ctx.Done branch (nobody closes pcmCh on this path).
			return
		}
		if mt == websocket.BinaryMessage {
			c.handleBinary(data)
			continue
		}
		var msg struct {
			Type   string `json:"type"`
			Prompt string `json:"prompt"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "start":
			c.handleStart(msg.Prompt)
		case "stop":
			c.handleStop()
		}
	}
}

// handleStart resolves the STT provider, probes the mode (live vs batch), and
// launches the upstream request. Runs on readLoop.
func (c *sttConn) handleStart(prompt string) {
	if c.active {
		// Non-terminal refusal: the in-flight utterance is unaffected.
		c.sendJSON(struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}{Type: "error", Message: "utterance in progress"})
		return
	}
	cfg, err := config.Load()
	if err != nil {
		c.failConn("stt: config load failed: " + err.Error())
		return
	}
	p, model, err := cfg.ResolveSTT()
	if err != nil {
		c.failConn("stt: " + err.Error())
		return
	}
	c.baseURL = strings.TrimRight(p.URL, "/")
	c.apiKey = p.ResolveKey()
	c.model = model
	c.prompt = prompt
	c.active = true
	c.finalizing = false
	c.epd = endpointer{}

	live := c.probeLiveMode()
	if !live {
		c.sendJSON(struct {
			Type string `json:"type"`
			Mode string `json:"mode"`
		}{Type: "ready", Mode: "batch"})
		return
	}

	// Live: pipe body (unknown length → chunked framing by net/http — raw
	// PCM bytes only, never hand-written chunk headers). pcmCh/writerDone
	// are created here on readLoop; the writer goroutine only launches after
	// a 2xx, but the hook fires now so tests cannot miss the exit.
	pr, pw := io.Pipe()
	c.pcmCh = make(chan []byte, sttPCMChFrames)
	c.writerDone = make(chan struct{})
	if sttWriterDoneHook != nil {
		sttWriterDoneHook(c.writerDone)
	}
	q := url.Values{}
	q.Set("model", c.model)
	q.Set("sample_rate", "16000")
	q.Set("channels", "1")
	q.Set("sample_format", "s16le")
	if c.prompt != "" {
		q.Set("prompt", c.prompt)
	}
	q.Set("busy_timeout_ms", "5000")
	req, err := http.NewRequestWithContext(c.ctx, http.MethodPost,
		c.baseURL+"/audio/transcriptions/live?"+q.Encode(), pr)
	if err != nil {
		c.failConn("stt: upstream request: " + err.Error())
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	go func() {
		resp, err := sttLiveClient.Do(req)
		if err != nil {
			c.failConn("stt: upstream dial failed: " + err.Error())
			return
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			c.failConn("stt: " + sttUpstreamError(resp.StatusCode, body))
			return
		}
		go c.pcmWriter(pw)
		c.sendJSON(struct {
			Type string `json:"type"`
			Mode string `json:"mode"`
		}{Type: "ready", Mode: "live"})
		c.sseReader(resp)
	}()
}

// probeLiveMode GETs {base}/models once per connection and reports whether
// the configured model entry declares mode "streaming". Any probe failure
// falls back to batch — batch works for both entry modes.
func (c *sttConn) probeLiveMode() bool {
	if c.modeProbed {
		return c.modeLive
	}
	c.modeProbed = true
	c.modeLive = false
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return false
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var list struct {
		Data []struct {
			ID   string `json:"id"`
			Mode string `json:"mode"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&list) != nil {
		return false
	}
	for _, m := range list.Data {
		if m.ID == c.model && m.Mode == "streaming" {
			c.modeLive = true
			break
		}
	}
	return c.modeLive
}

// pcmWriter drains pcmCh into the upstream request body. select on ctx.Done:
// teardown and SSE-error paths never close pcmCh, so a for-range writer would
// block on the channel receive forever (goroutine leak).
func (c *sttConn) pcmWriter(pw *io.PipeWriter) {
	defer close(c.writerDone)
	for {
		select {
		case p, ok := <-c.pcmCh:
			if !ok {
				_ = pw.Close()
				return
			}
			if _, err := pw.Write(p); err != nil {
				// No finalizing check (r9): the terminal flag alone dedups.
				// This also covers graceful-finalize + upstream death — the
				// flag is still unset, so exactly one error reaches the
				// client instead of nobody finalizing.
				c.failConn("stt: upstream write failed")
				return
			}
		case <-c.ctx.Done():
			return
		}
	}
}

// sseReader translates the upstream SSE stream into D3 messages. When the
// stream drains or errors with no done/error ever arriving, the residual
// window below fires one error: otherwise nobody is left to finalize (the
// writer already closed the pipe and exited) and the client hangs on an
// open socket until the page is refreshed.
func (c *sttConn) sseReader(resp *http.Response) {
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "[DONE]" {
			continue
		}
		var ev struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
			Text  string `json:"text"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(payload), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "transcript.text.delta":
			c.sendJSON(struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{Type: "delta", Text: ev.Delta})
		case "transcript.text.partial":
			c.sendJSON(struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{Type: "partial", Text: ev.Text})
		case "transcript.text.done":
			c.terminalSend("done", ev.Text, sttMustJSON(struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{Type: "done", Text: ev.Text}))
		case "error":
			msg := "upstream error"
			if ev.Error != nil && ev.Error.Message != "" {
				msg = ev.Error.Message
			}
			c.failConn(msg)
		}
	}
	// Residual-window fallback (Assumptions #13): the stream ended without
	// done/error. Both exits count — drained (graceful FIN) and read error
	// (crash RST) are the same window. Two gates keep this exact:
	//   - ctx cancelled → teardown or stall cancelled — an active close, not
	//     upstream death.
	//   - writerDone not closed → the writer still owns finalizing: a
	//     transport abort wakes its pw.Write and this reader at once, so
	//     firing here would turn the error message into a sendMu coin flip.
	// writerDone closes (with the ctx alive) only via the writer's finalize
	// drain — pw.Close() as its last act — strictly before the pipe EOF can
	// reach the upstream, so in the true window the ordering is guaranteed.
	// failConn's terminal-flag check keeps the normal path a no-op: done/error
	// always precede stream end on the wire and this reader consumes in order.
	if c.ctx.Err() == nil {
		select {
		case <-c.writerDone:
			c.failConn("stt: upstream stream ended without a final event")
		default:
		}
	}
}

// handleBinary forwards one PCM frame. Runs on readLoop (naturally serial).
func (c *sttConn) handleBinary(pcm []byte) {
	if !c.active || c.finalizing {
		// Race frames after the endpointer fired / after terminal — dropped.
		return
	}
	if c.pcmCh != nil {
		select {
		case c.pcmCh <- pcm:
		default:
			// A full queue means ≥64 frames ≈ 2s of audio unconsumed — but
			// a burst fills it in milliseconds while the writer is merely
			// paused on transport backpressure. Give the writer a grace
			// window: transport-level deaths (abort → write failed) declare
			// themselves well inside it, so only a queue that is still full
			// after the grace is a genuine stall.
			select {
			case c.pcmCh <- pcm:
			case <-time.After(sttStallGrace):
				// Order is a defensive invariant: flag, close, error, cancel.
				c.finalizing = true
				close(c.pcmCh)
				c.failConn("stt: upstream audio stalled")
				c.cancel()
				return
			case <-c.ctx.Done():
				// Terminal was reported elsewhere (e.g. the writer's write
				// failure): frames no longer matter.
				return
			}
		}
	} else {
		// Batch: append to the utterance buffer.
		if len(c.pcmBuf)+len(pcm) > sttBatchLimit {
			c.failConn("stt: utterance too large")
			return
		}
		c.pcmBuf = append(c.pcmBuf, pcm...)
	}
	if c.epd.observe(pcm) {
		c.finalizeUtterance()
	}
}

// handleStop honors a manual stop. Runs on readLoop.
func (c *sttConn) handleStop() {
	if !c.active || c.finalizing {
		return // stop after auto-stop is silently ignored
	}
	c.finalizeUtterance()
}

// finalizeUtterance is the single entry for endpointing and manual stop.
func (c *sttConn) finalizeUtterance() {
	c.finalizing = true
	if c.pcmCh != nil {
		// Writer drains then closes the pipe → terminal chunk → upstream
		// done arrives on the SSE stream.
		close(c.pcmCh)
		return
	}
	// Batch: transcribe the buffered utterance now.
	audio := stt.EncodeWavPCM16(c.pcmBuf, 16000, 1)
	c.pcmBuf = nil
	baseURL, apiKey, model, prompt := c.baseURL, c.apiKey, c.model, c.prompt
	go func() {
		text, err := (&stt.Client{}).Transcribe(c.ctx, stt.Request{
			BaseURL: baseURL, Model: model, APIKey: apiKey, Prompt: prompt, Audio: audio,
		})
		if err != nil {
			c.failConn("stt: transcription failed: " + err.Error())
			return
		}
		c.terminalSend("done", text, sttMustJSON(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{Type: "done", Text: text}))
	}()
}

// sendJSON writes one non-terminal frame under sendMu.
func (c *sttConn) sendJSON(v any) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	_ = c.ws.WriteMessage(websocket.TextMessage, sttMustJSON(v))
}

// failConn is the terminal error exit.
func (c *sttConn) failConn(msg string) {
	c.terminalSend("error", msg, sttMustJSON(struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}{Type: "error", Message: msg}))
}

// terminalSend is the ONE exit every done/error share: under sendMu the
// terminal flag is checked, set, observed by the hook, then the message and a
// normal close frame are written. First sender wins; later senders return
// without writing.
func (c *sttConn) terminalSend(kind, msg string, payload []byte) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.terminal {
		return
	}
	c.terminal = true
	if sttTerminalSendHook != nil {
		sttTerminalSendHook(kind, msg)
	}
	_ = c.ws.WriteMessage(websocket.TextMessage, payload)
	_ = c.ws.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(time.Second))
}

// sttUpstreamError extracts the message from a non-2xx live response
// (nested {"error":{"message"}} → flat {"message"} → status line).
func sttUpstreamError(status int, body []byte) string {
	var nested struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &nested) == nil {
		if nested.Error.Message != "" {
			return nested.Error.Message
		}
		if nested.Message != "" {
			return nested.Message
		}
	}
	return "upstream HTTP " + strconv.Itoa(status)
}

func sttMustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"type":"error","message":"encode failed"}`)
	}
	return b
}
