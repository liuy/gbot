package wui

// /ws/stt bridge integration tests. The hook-using tests (sttWriterDoneHook /
// sttTerminalSendHook) must NOT call t.Parallel: they swap package-level
// seams, and sequential tests never overlap each other.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
)

// --- fake audio.cpp upstream ---------------------------------------------

type sttFakeUpstream struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// /v1/models knobs
	modelsStatus int
	modelsJSON   string
	modelsDelay  time.Duration
	// batch knob
	batchStatus int
	// live behavior: "ok" | "ok-bigline" | "stall" | "sse-error" | "400" | "readn-close" | "eof-death"
	liveMode     string
	live400Body  string // "400" mode body override (flat vs nested shape)
	liveRST      bool   // "eof-death": RST instead of graceful FIN
	sseErrorBody string
	readN        int
	closeCh      chan struct{}

	// capture
	liveCount   int
	liveQuery   url.Values
	liveAuth    string
	liveAccept  string
	modelsAuth  string
	liveTE      string
	pcmTotal    int
	multiModel  string
	multiPrompt string
	multiFile   []byte

	expectPCM    []byte // full byte-compare at EOF (nil = skip)
	progressThrs []int
	progress     chan struct{}
	exitOnce     sync.Once
	handlerExit  chan struct{}
}

// smallRcvListener pins every accepted connection's receive buffer at 16KB.
// With kernel defaults the fake can hold ~131KB unread, and the client's
// send buffer autotunes to megabytes — together the transport absorbs more
// than the endpointer's 60s audio budget, making backpressure untestable.
// Pinned on both sides (see pinSttLiveClient), absorption measures ~106KB
// (~103 frames): stall tests exceed it, write-error tests stay below it.
type smallRcvListener struct{ net.Listener }

func (l smallRcvListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(16 * 1024)
	}
	return c, nil
}

func newSttFakeUpstream(t *testing.T) *sttFakeUpstream {
	t.Helper()
	f := &sttFakeUpstream{
		t:           t,
		modelsJSON:  `{"data":[{"id":"stt-stream","mode":"streaming"},{"id":"stt","mode":"offline"}]}`,
		liveMode:    "ok",
		closeCh:     make(chan struct{}),
		progress:    make(chan struct{}, 32),
		handlerExit: make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", f.handleModels)
	mux.HandleFunc("/v1/audio/transcriptions/live", f.handleLive)
	mux.HandleFunc("/v1/audio/transcriptions", f.handleBatch)
	f.srv = httptest.NewUnstartedServer(mux)
	f.srv.Listener = smallRcvListener{f.srv.Listener}
	f.srv.Start()
	t.Cleanup(f.srv.Close)
	return f
}

func (f *sttFakeUpstream) set(liveMode string) {
	f.mu.Lock()
	f.liveMode = liveMode
	f.mu.Unlock()
}

func (f *sttFakeUpstream) setThresholds(ts ...int) {
	f.mu.Lock()
	f.progressThrs = ts
	f.mu.Unlock()
}

func (f *sttFakeUpstream) recordProgress(total int) {
	for {
		f.mu.Lock()
		if len(f.progressThrs) == 0 || total < f.progressThrs[0] {
			f.mu.Unlock()
			return
		}
		f.progressThrs = f.progressThrs[1:]
		f.mu.Unlock()
		select {
		case f.progress <- struct{}{}:
		default:
		}
	}
}

func (f *sttFakeUpstream) waitProgress(t *testing.T) {
	t.Helper()
	select {
	case <-f.progress:
	case <-time.After(30 * time.Second):
		t.Fatal("fake upstream progress signal never fired (batch not fully drained)")
	}
}

func (f *sttFakeUpstream) waitHandlerExit(t *testing.T) {
	t.Helper()
	select {
	case <-f.handlerExit:
	case <-time.After(30 * time.Second):
		t.Fatal("fake live handler never exited")
	}
}

func (f *sttFakeUpstream) handleModels(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.modelsAuth = r.Header.Get("Authorization")
	status, body, delay := f.modelsStatus, f.modelsJSON, f.modelsDelay
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	if status != 0 {
		// Error bodies still carry a VALID model list shape — pins that the
		// bridge keys off the status, not the body.
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func (f *sttFakeUpstream) handleLive(w http.ResponseWriter, r *http.Request) {
	defer f.exitOnce.Do(func() { close(f.handlerExit) })
	f.mu.Lock()
	f.liveCount++
	f.liveQuery = r.URL.Query()
	f.liveAuth = r.Header.Get("Authorization")
	f.liveAccept = r.Header.Get("Accept")
	f.liveTE = strings.Join(r.TransferEncoding, ",")
	mode := f.liveMode
	readN := f.readN
	rst := f.liveRST
	expect := f.expectPCM
	f.mu.Unlock()

	// net/http otherwise drains the unread request body before the first
	// response write (connection-reuse heuristic), which deadlocks against
	// a streaming upload until 256KB arrive. The real audio.cpp server is
	// C++ and streams both directions immediately.
	_ = http.NewResponseController(w).EnableFullDuplex()

	if mode == "400" {
		f.mu.Lock()
		body := f.live400Body
		f.mu.Unlock()
		if body == "" {
			body = `{"error":{"message":"live transcription requires a model configured with mode=streaming: stt","type":"invalid_request_error"}}`
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
		return
	}
	if f.liveTE != "chunked" {
		f.t.Errorf("live request Transfer-Encoding = %q, want chunked (io.Pipe body must be framed by net/http)", f.liveTE)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher := w.(http.Flusher)
	flusher.Flush()

	switch mode {
	case "stall":
		// Never read the body: the client's transport buffers fill and the
		// bridge's pcmCh backs up. Bounded wait — net/http only starts its
		// disconnect-detecting background read after the request body is
		// consumed, so a handler that never reads cannot observe the
		// client's abort and must time itself out.
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
		return
	case "sse-error":
		// Drain so the writer never sees a write error — the SSE error is
		// the single terminal source.
		go func() { _, _ = io.Copy(io.Discard, r.Body) }()
		body := f.sseErrorBody
		if body == "" {
			body = `{"type":"error","error":{"message":"model 'x' is busy: timed out after 2000 ms waiting for the inference lock"}}`
		}
		_, _ = io.WriteString(w, "data: "+body+"\n\n")
		flusher.Flush()
		return
	case "readn-close":
		buf := make([]byte, 32*1024)
		got := 0
		for got < readN {
			n, err := r.Body.Read(buf)
			got += n
			if err != nil {
				return
			}
		}
		f.recordProgress(got)
		<-f.closeCh
		hj, ok := w.(http.Hijacker)
		if !ok {
			f.t.Error("fake live handler cannot hijack")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			f.t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
		return
	case "eof-death":
		// Residual-window fake (Assumptions #13): drink the request body to
		// EOF — that is the bridge writer's pw.Close() after endpointing —
		// then die without writing a single SSE line. A plain return ends
		// the response gracefully (FIN); with liveRST, hijack + SO_LINGER(0)
		// close sends RST — the crash shape of the same window.
		buf := make([]byte, 32*1024)
		var total int
		for {
			n, err := r.Body.Read(buf)
			total += n
			f.recordProgress(total)
			if err != nil {
				break
			}
		}
		if !rst {
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			f.t.Error("fake live handler cannot hijack")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			f.t.Errorf("hijack: %v", err)
			return
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetLinger(0)
		}
		_ = conn.Close()
		return
	}

	// "ok-bigline": two oversized SSE data lines then the usual tail.
	if mode == "ok-bigline" {
		_, _ = io.WriteString(w, "data: {\"type\":\"transcript.text.delta\",\"delta\":\""+strings.Repeat("x", 200000)+"\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"transcript.text.delta\",\"delta\":\""+strings.Repeat("y", 1024*1023+100)+"\"}\n\n")
		_, _ = io.WriteString(w, `data: {"type":"transcript.text.done","text":"done"}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
		go func() { _, _ = io.Copy(io.Discard, r.Body) }()
		return
	}

	// "ok": incremental drain, first delta mid-stream, done after EOF.
	buf := make([]byte, 32*1024)
	var acc []byte
	delta1 := false
	for {
		n, err := r.Body.Read(buf)
		if n > 0 {
			acc = append(acc, buf[:n]...)
			f.recordProgress(len(acc))
		}
		if !delta1 && len(acc) >= 32768 {
			_, _ = io.WriteString(w, `data: {"type":"transcript.text.delta","delta":"你好。"}`+"\n\n")
			flusher.Flush()
			delta1 = true
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return // client aborted mid-stream (teardown path)
		}
	}
	f.mu.Lock()
	f.pcmTotal = len(acc)
	f.mu.Unlock()
	if expect != nil {
		if !bytes.Equal(acc, expect) {
			f.t.Errorf("fake upstream PCM mismatch: got %d bytes, want %d (double chunked framing or dropped/duplicated bytes)", len(acc), len(expect))
		}
	}
	// Robustness gauntlet: comment line, malformed JSON data line, [DONE]
	// marker early, a leading-space data line (SSE allows indented fields),
	// and one partial-replacement event — every ok-mode test must sail
	// through all of them.
	_, _ = io.WriteString(w, ": keepalive\n\n")
	_, _ = io.WriteString(w, "data: {not json\n\n")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	_, _ = io.WriteString(w, "  data: {\"type\":\"transcript.text.partial\",\"text\":\"临时\"}\n\n")
	_, _ = io.WriteString(w, `data: {"type":"transcript.text.delta","delta":" 世界。"}`+"\n\n")
	_, _ = io.WriteString(w, `data: {"type":"transcript.text.done","text":"你好。 世界。","timing":{"ttft_ms":100}}`+"\n\n")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func (f *sttFakeUpstream) handleBatch(w http.ResponseWriter, r *http.Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		f.t.Errorf("batch request not multipart: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var file []byte
	form := map[string]string{}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			f.t.Errorf("batch multipart part: %v", err)
			return
		}
		if part.FormName() == "file" {
			file, _ = io.ReadAll(part)
			continue
		}
		b, _ := io.ReadAll(part)
		form[part.FormName()] = string(b)
	}
	f.mu.Lock()
	f.multiModel = form["model"]
	f.multiPrompt = form["prompt"]
	f.multiFile = file
	status := f.batchStatus
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if status != 0 {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"batch boom"}}`))
		return
	}
	_, _ = w.Write([]byte(`{"text":"你好","timing":{"ttft_ms":50}}`))
}

func (f *sttFakeUpstream) got(t *testing.T) (liveCount int, q url.Values, auth string, pcmTotal int, multiModel, multiPrompt string, multiFile []byte) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.liveCount, f.liveQuery, f.liveAuth, f.pcmTotal, f.multiModel, f.multiPrompt, f.multiFile
}

func (f *sttFakeUpstream) gotExtras(t *testing.T) (accept, modelsAuth string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.liveAccept, f.modelsAuth
}

// --- bridge + client helpers ---------------------------------------------

// sttSettings builds a settings.json with one audio provider and stt.default.
// key == "" omits the keys array (keyless probe/write).
func sttSettings(upstreamURL, key, def string) string {
	keys := ""
	if key != "" {
		keys = fmt.Sprintf(`"keys":[%q],`, key)
	}
	return fmt.Sprintf(`{"providers":[{"name":"audiocpp","url":%q,"type":"audio",%s"models":{"stt-stream":{},"stt":{}}}],"stt":{"default":%q}}`,
		upstreamURL, keys, def)
}

// newSttBridge seeds an isolated HOME with the settings and mounts /ws/stt.
func newSttBridge(t *testing.T, settings string) *httptest.Server {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir()) // keep any project-local .gbot/settings.json out
	if err := os.MkdirAll(filepath.Join(home, ".gbot"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gbot", "settings.json"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterSTTRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func dialSttWS(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/stt", nil)
	if err != nil {
		t.Fatalf("dial /ws/stt: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func sttSendJSON(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatalf("send %s: %v", b, err)
	}
}

func sttSendBinaries(t *testing.T, c *websocket.Conn, blocks [][]byte) {
	t.Helper()
	for i, b := range blocks {
		if err := c.WriteMessage(websocket.BinaryMessage, b); err != nil {
			t.Fatalf("send binary block %d: %v", i, err)
		}
	}
}

// sttSendBinariesLoose ignores write errors — used when the bridge is
// expected to error/close mid-blast (stall test).
func sttSendBinariesLoose(c *websocket.Conn, blocks [][]byte) {
	for _, b := range blocks {
		if c.WriteMessage(websocket.BinaryMessage, b) != nil {
			return
		}
	}
}

func sttSendFrames(t *testing.T, c *websocket.Conn, pcm []byte) {
	t.Helper()
	sttSendBinaries(t, c, framesOf(pcm))
}

type sttReady struct {
	Type string `json:"type"`
	Mode string `json:"mode"`
}

func sttExpectReady(t *testing.T, c *websocket.Conn, wantMode string) {
	t.Helper()
	var r sttReady
	if err := json.Unmarshal(readWSMessage(t, c), &r); err != nil {
		t.Fatalf("ready frame: %v", err)
	}
	if r.Type != "ready" || r.Mode != wantMode {
		t.Fatalf("ready = %+v, want {ready %s}", r, wantMode)
	}
}

// sttClientLog is everything the client observed until the WS closed.
type sttClientLog struct {
	deltas   []string
	partials []string
	done     string
	errs     []string
	others   []string
	closeErr error
}

func sttReadUntilClose(t *testing.T, c *websocket.Conn) sttClientLog {
	t.Helper()
	var log sttClientLog
	for {
		_ = c.SetReadDeadline(time.Now().Add(30 * time.Second)) // REAL-TIME
		_, data, err := c.ReadMessage()
		if err != nil {
			log.closeErr = err
			return log
		}
		var m struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &m) != nil {
			log.others = append(log.others, string(data))
			continue
		}
		switch m.Type {
		case "delta":
			log.deltas = append(log.deltas, m.Text)
		case "partial":
			log.partials = append(log.partials, m.Text)
		case "done":
			log.done = m.Text
		case "error":
			log.errs = append(log.errs, m.Message)
		default:
			log.others = append(log.others, string(data))
		}
	}
}

// --- hook installers ------------------------------------------------------

type sttHookCounter struct {
	mu    sync.Mutex
	kinds []string
	msgs  []string
}

func (h *sttHookCounter) record(kind, msg string) {
	h.mu.Lock()
	h.kinds = append(h.kinds, kind)
	h.msgs = append(h.msgs, msg)
	h.mu.Unlock()
}

func (h *sttHookCounter) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.kinds)
}

func (h *sttHookCounter) countKind(kind string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, k := range h.kinds {
		if k == kind {
			n++
		}
	}
	return n
}

func installSttTerminalHook(t *testing.T) *sttHookCounter {
	t.Helper()
	h := &sttHookCounter{}
	old := sttTerminalSendHook
	sttTerminalSendHook = h.record
	t.Cleanup(func() { sttTerminalSendHook = old })
	return h
}

func installSttWriterDoneHook(t *testing.T) func() <-chan struct{} {
	t.Helper()
	var mu sync.Mutex
	var ch <-chan struct{}
	old := sttWriterDoneHook
	sttWriterDoneHook = func(c <-chan struct{}) {
		mu.Lock()
		ch = c
		mu.Unlock()
	}
	t.Cleanup(func() { sttWriterDoneHook = old })
	return func() <-chan struct{} {
		mu.Lock()
		defer mu.Unlock()
		return ch
	}
}

func waitSttWriterDone(t *testing.T, get func() <-chan struct{}) {
	t.Helper()
	ch := get()
	if ch == nil {
		t.Fatal("writerDone hook never fired")
	}
	select {
	case <-ch:
	case <-time.After(30 * time.Second):
		t.Fatal("writer goroutine did not exit (leak)")
	}
}

// --- core flow tests ------------------------------------------------------

// Core integration: WS → bridge endpointing → fake upstream finalize →
// done → close. 128 frames in two 64-frame batches with a fake-drain signal
// between (a >64-frame blast can spuriously fill pcmCh — see the batching
// discipline in the plan).
func TestSttWS_LiveAutoStop(t *testing.T) {
	f := newSttFakeUpstream(t)
	f.setThresholds(65536)
	expect := append(append(speechPCM(32*epdFrameSamples), silencePCM(32*epdFrameSamples)...), silencePCM(31*epdFrameSamples)...)
	f.mu.Lock()
	f.expectPCM = expect
	f.mu.Unlock()

	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
	ws := dialSttWS(t, srv)
	sttSendJSON(t, ws, map[string]any{"type": "start", "prompt": "gbot"})
	sttExpectReady(t, ws, "live")

	sttSendFrames(t, ws, append(speechPCM(32*epdFrameSamples), silencePCM(32*epdFrameSamples)...))
	f.waitProgress(t)
	sttSendFrames(t, ws, silencePCM(64*epdFrameSamples))

	log := sttReadUntilClose(t, ws)
	if len(log.deltas) != 2 || log.deltas[0] != "你好。" || log.deltas[1] != " 世界。" {
		t.Errorf("deltas = %q, want [你好。  世界。]", log.deltas)
	}
	if len(log.partials) != 1 || log.partials[0] != "临时" {
		t.Errorf("partials = %q, want [临时] (leading-space SSE field + partial translation)", log.partials)
	}
	if len(log.others) != 0 {
		t.Errorf("unrecognized frames = %q, want none (comment/malformed/[DONE] lines skipped silently)", log.others)
	}
	if log.done != "你好。 世界。" {
		t.Errorf("done = %q, want 你好。 世界。", log.done)
	}
	if len(log.errs) != 0 {
		t.Errorf("unexpected errors: %q", log.errs)
	}
	if !websocket.IsCloseError(log.closeErr, websocket.CloseNormalClosure) {
		t.Errorf("close error = %v, want normal closure after done", log.closeErr)
	}
	_, _, _, pcmTotal, _, _, _ := f.got(t)
	if pcmTotal != 97280 {
		t.Errorf("upstream received %d bytes, want 97280 (95 frames — the endpointing frame forwards, later frames drop)", pcmTotal)
	}
}

func TestSttWS_ManualStop(t *testing.T) {
	f := newSttFakeUpstream(t)
	f.mu.Lock()
	f.expectPCM = speechPCM(32 * epdFrameSamples)
	f.mu.Unlock()

	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
	ws := dialSttWS(t, srv)
	sttSendJSON(t, ws, map[string]any{"type": "start"})
	sttExpectReady(t, ws, "live")
	sttSendFrames(t, ws, speechPCM(32*epdFrameSamples))
	sttSendJSON(t, ws, map[string]any{"type": "stop"})

	log := sttReadUntilClose(t, ws)
	if log.done != "你好。 世界。" {
		t.Errorf("done = %q, want 你好。 世界。", log.done)
	}
	if len(log.errs) != 0 {
		t.Errorf("unexpected errors: %q", log.errs)
	}
	if !websocket.IsCloseError(log.closeErr, websocket.CloseNormalClosure) {
		t.Errorf("close error = %v, want normal closure", log.closeErr)
	}
	_, _, _, pcmTotal, _, _, _ := f.got(t)
	if pcmTotal != 32768 {
		t.Errorf("upstream received %d bytes, want 32768", pcmTotal)
	}
}

// Auto-stop fires, a manual stop arrives before done: stop ignored, exactly
// one done through the unified terminal exit.
func TestSttWS_StopAfterAutoStopIgnored(t *testing.T) {
	f := newSttFakeUpstream(t)
	f.setThresholds(65536)
	hook := installSttTerminalHook(t)

	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
	ws := dialSttWS(t, srv)
	sttSendJSON(t, ws, map[string]any{"type": "start"})
	sttExpectReady(t, ws, "live")

	sttSendFrames(t, ws, append(speechPCM(32*epdFrameSamples), silencePCM(32*epdFrameSamples)...))
	f.waitProgress(t)
	sttSendFrames(t, ws, silencePCM(64*epdFrameSamples))
	sttSendJSON(t, ws, map[string]any{"type": "stop"})

	log := sttReadUntilClose(t, ws)
	if len(log.errs) != 0 {
		t.Errorf("errors = %q, want none (stop after auto-stop is silently ignored)", log.errs)
	}
	if log.done != "你好。 世界。" {
		t.Errorf("done = %q, want 你好。 世界。", log.done)
	}
	if !websocket.IsCloseError(log.closeErr, websocket.CloseNormalClosure) {
		t.Errorf("close error = %v, want normal closure (a double-finalize panic would close abnormally)", log.closeErr)
	}
	if hook.countKind("done") != 1 {
		t.Errorf("terminal hook done count = %d, want exactly 1 (done must traverse the unified exit)", hook.countKind("done"))
	}
	if hook.count() != 1 {
		t.Errorf("terminal hook total count = %d, want 1", hook.count())
	}
	_, _, _, pcmTotal, _, _, _ := f.got(t)
	if pcmTotal != 97280 {
		t.Errorf("upstream received %d bytes, want 97280", pcmTotal)
	}
}

// Mis-tap: pure silence auto-closes at frame 63. First-batch progress
// threshold is pinned to 63 frames (64,512B) — the endpointing frame is the
// last one that ever reaches the fake, so a 64-frame threshold would never
// fire and the test would hang.
func TestSttWS_EmptyUtteranceAutoCloses(t *testing.T) {
	f := newSttFakeUpstream(t)
	f.setThresholds(64512)
	f.mu.Lock()
	f.expectPCM = silencePCM(63 * epdFrameSamples)
	f.mu.Unlock()

	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
	ws := dialSttWS(t, srv)
	sttSendJSON(t, ws, map[string]any{"type": "start"})
	sttExpectReady(t, ws, "live")

	sttSendFrames(t, ws, silencePCM(64*epdFrameSamples))
	f.waitProgress(t)
	sttSendFrames(t, ws, silencePCM(2*epdFrameSamples))

	log := sttReadUntilClose(t, ws)
	if log.done == "" {
		t.Errorf("no done received; errors = %q", log.errs)
	}
	if len(log.errs) != 0 {
		t.Errorf("errors = %q, want none", log.errs)
	}
	if !websocket.IsCloseError(log.closeErr, websocket.CloseNormalClosure) {
		t.Errorf("close error = %v, want normal closure", log.closeErr)
	}
	_, _, _, pcmTotal, _, _, _ := f.got(t)
	if pcmTotal != 64512 {
		t.Errorf("upstream received %d bytes, want 64512 (63 frames; frames 64+ dropped after endpointing)", pcmTotal)
	}
}

// 60s insurance on exact sample count through the bridge: 50 × 1.2s frames,
// finalize on frame 50 (960,000 samples).
func TestSttWS_MaxStreamInsurance(t *testing.T) {
	f := newSttFakeUpstream(t)
	var expect []byte
	var blocks [][]byte
	for i := 0; i < 25; i++ {
		sil := silencePCM(19200)
		ton := tonePCM(0.3, 19200)
		expect = append(append(expect, sil...), ton...)
		blocks = append(blocks, sil, ton)
	}
	f.mu.Lock()
	f.expectPCM = expect
	f.mu.Unlock()

	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
	ws := dialSttWS(t, srv)
	sttSendJSON(t, ws, map[string]any{"type": "start"})
	sttExpectReady(t, ws, "live")
	sttSendBinaries(t, ws, blocks)

	log := sttReadUntilClose(t, ws)
	if log.done == "" {
		t.Fatalf("no done; errors = %q", log.errs)
	}
	if len(log.errs) != 0 {
		t.Errorf("errors = %q, want none", log.errs)
	}
	if !websocket.IsCloseError(log.closeErr, websocket.CloseNormalClosure) {
		t.Errorf("close error = %v, want normal closure", log.closeErr)
	}
	_, _, _, pcmTotal, _, _, _ := f.got(t)
	if pcmTotal != 1920000 {
		t.Errorf("upstream received %d bytes, want 1920000 (exactly 60.0s)", pcmTotal)
	}
}

// Frames after the endpointing frame never reach the upstream.
func TestSttWS_FramesAfterFinalizeDropped(t *testing.T) {
	f := newSttFakeUpstream(t)
	f.setThresholds(65536)
	expect := append(append(speechPCM(32*epdFrameSamples), silencePCM(32*epdFrameSamples)...), silencePCM(31*epdFrameSamples)...)
	f.mu.Lock()
	f.expectPCM = expect
	f.mu.Unlock()

	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
	ws := dialSttWS(t, srv)
	sttSendJSON(t, ws, map[string]any{"type": "start"})
	sttExpectReady(t, ws, "live")

	sttSendFrames(t, ws, append(speechPCM(32*epdFrameSamples), silencePCM(32*epdFrameSamples)...))
	f.waitProgress(t)
	sttSendFrames(t, ws, silencePCM(64*epdFrameSamples))
	sttSendFrames(t, ws, silencePCM(10*epdFrameSamples))

	log := sttReadUntilClose(t, ws)
	if log.done == "" {
		t.Fatalf("no done; errors = %q", log.errs)
	}
	_, _, _, pcmTotal, _, _, _ := f.got(t)
	if pcmTotal != 97280 {
		t.Errorf("upstream received %d bytes, want 97280 (95×1024 — trigger frame forwarded, everything after dropped)", pcmTotal)
	}
}

// pinSttLiveClient swaps the bridge's live-request client for one whose
// socket send buffer is pinned at 32KB. Unpinned, Linux autotunes SO_SNDBUF
// up to tcp_wmem[2]=4MB — the transport then absorbs ~4MB (~4000 frames)
// before pw.Write blocks, which is past the endpointer's 60s audio budget
// (1875 frames) and the stall can never surface. Pinned, absorption
// measures ~163 frames (32KB sndbuf + 131KB default rcvbuf + 4KB bufio).
func pinSttLiveClient(t *testing.T) {
	t.Helper()
	old := sttLiveClient
	tr := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 5 * time.Second,
			Control: func(network, address string, c syscall.RawConn) error {
				return c.Control(func(fd uintptr) {
					_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, 32*1024)
				})
			},
		}).DialContext,
	}
	sttLiveClient = &http.Client{Transport: tr}
	t.Cleanup(func() { sttLiveClient = old })
}

// Upstream stops reading: pcmCh fills → exactly one stalled error, writer
// exits, and the terminal flag is the only dedup (hook count 1 — a deleted
// flag check lets the cancel-released writer reach the hook a second time).
//
// 600 speech frames ≈ 19.2s of audio (< the 60s insurance, which would
// preempt the stall via finalize) and ~3.7× the pinned-buffer absorption
// (~163 frames) — pcmCh provably fills. Speech content matters: a constant
// tone would endpoint into finalize and never stall.
func TestSttWS_UpstreamStallErrors(t *testing.T) {
	f := newSttFakeUpstream(t)
	f.set("stall")
	pinSttLiveClient(t)
	hook := installSttTerminalHook(t)
	writerDone := installSttWriterDoneHook(t)

	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
	ws := dialSttWS(t, srv)
	sttSendJSON(t, ws, map[string]any{"type": "start"})
	sttExpectReady(t, ws, "live")

	sttSendBinariesLoose(ws, framesOf(speechPCM(600*epdFrameSamples)))

	log := sttReadUntilClose(t, ws)
	if len(log.errs) != 1 || !strings.Contains(log.errs[0], "stalled") {
		t.Fatalf("errors = %q, want exactly one containing 'stalled'", log.errs)
	}
	if log.done != "" {
		t.Errorf("done = %q, want none on stall", log.done)
	}
	waitSttWriterDone(t, writerDone)
	if n := hook.count(); n != 1 {
		t.Errorf("terminal hook count = %d, want 1 (the terminal flag must suppress the writer's second error; kind=%v)", n, hook.kinds)
	}
	if hook.countKind("error") != 1 {
		t.Errorf("terminal hook error count = %d, want 1", hook.countKind("error"))
	}
}

func TestSttWS_LivePromptAndParams(t *testing.T) {
	for _, tc := range []struct {
		name      string
		key       string
		prompt    string
		wantAuth  string
		wantPrmpt bool
	}{
		{"keyless without prompt", "", "", "", false},
		{"keyless with prompt", "", "gbot", "", true},
		{"keyed", "sk-1", "gbot", "Bearer sk-1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSttFakeUpstream(t)
			srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", tc.key, "audiocpp/stt-stream"))
			ws := dialSttWS(t, srv)
			start := map[string]any{"type": "start"}
			if tc.prompt != "" {
				start["prompt"] = tc.prompt
			}
			sttSendJSON(t, ws, start)
			sttExpectReady(t, ws, "live")
			sttSendFrames(t, ws, speechPCM(4*epdFrameSamples))
			sttSendJSON(t, ws, map[string]any{"type": "stop"})
			log := sttReadUntilClose(t, ws)
			if log.done == "" {
				t.Fatalf("no done; errors = %q", log.errs)
			}

			_, q, auth, _, _, _, _ := f.got(t)
			if got := q.Get("model"); got != "stt-stream" {
				t.Errorf("query model = %q, want stt-stream", got)
			}
			for k, want := range map[string]string{
				"sample_rate":     "16000",
				"channels":        "1",
				"sample_format":   "s16le",
				"busy_timeout_ms": "5000",
			} {
				if got := q.Get(k); got != want {
					t.Errorf("query %s = %q, want %q", k, got, want)
				}
			}
			_, hasPrompt := q["prompt"]
			if hasPrompt != tc.wantPrmpt {
				t.Errorf("query prompt present = %v (value %q), want %v", hasPrompt, q.Get("prompt"), tc.wantPrmpt)
			}
			if hasPrompt && q.Get("prompt") != tc.prompt {
				t.Errorf("query prompt = %q, want %q", q.Get("prompt"), tc.prompt)
			}
			if auth != tc.wantAuth {
				t.Errorf("Authorization = %q, want %q", auth, tc.wantAuth)
			}
		})
	}
}

// Offline model entry → batch mode: PCM buffered, endpointing transcribes,
// /live never touched.
func TestSttWS_BatchFallbackAutoStop(t *testing.T) {
	f := newSttFakeUpstream(t)
	expect := append(append(speechPCM(32*epdFrameSamples), silencePCM(32*epdFrameSamples)...), silencePCM(31*epdFrameSamples)...)

	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt"))
	ws := dialSttWS(t, srv)
	sttSendJSON(t, ws, map[string]any{"type": "start", "prompt": "gbot"})
	sttExpectReady(t, ws, "batch")

	// Batch mode has no pcmCh — no batching discipline needed.
	sttSendFrames(t, ws, append(speechPCM(32*epdFrameSamples), silencePCM(96*epdFrameSamples)...))

	log := sttReadUntilClose(t, ws)
	if log.done != "你好" {
		t.Fatalf("done = %q, want 你好; errors = %q", log.done, log.errs)
	}
	if !websocket.IsCloseError(log.closeErr, websocket.CloseNormalClosure) {
		t.Errorf("close error = %v, want normal closure", log.closeErr)
	}
	liveCount, _, _, _, model, prompt, file := f.got(t)
	if liveCount != 0 {
		t.Errorf("live requests = %d, want 0 in batch mode", liveCount)
	}
	if model != "stt" {
		t.Errorf("multipart model = %q, want stt", model)
	}
	if prompt != "gbot" {
		t.Errorf("multipart prompt = %q, want gbot", prompt)
	}
	if len(file) != 44+len(expect) {
		t.Fatalf("multipart file = %d bytes, want %d (44-byte WAV header + PCM)", len(file), 44+len(expect))
	}
	if string(file[:4]) != "RIFF" || string(file[8:12]) != "WAVE" {
		t.Errorf("WAV magic = %q %q, want RIFF WAVE", file[:4], file[8:12])
	}
	// Header field pins: 16k mono s16le — the exact contract the upstream
	// query params promise for the live path must hold for batch too.
	rate := int(file[24]) | int(file[25])<<8 | int(file[26])<<16 | int(file[27])<<24
	channels := int(file[22]) | int(file[23])<<8
	bits := int(file[34]) | int(file[35])<<8
	block := int(file[32]) | int(file[33])<<8
	if rate != 16000 || channels != 1 || bits != 16 || block != 2 {
		t.Errorf("WAV header = rate %d / channels %d / bits %d / blockAlign %d, want 16000/1/16/2", rate, channels, bits, block)
	}
	if !bytes.Equal(file[44:], expect) {
		t.Error("multipart PCM payload differs from the forwarded frames")
	}
}

// Probe failure (models 500) → batch fallback, full batch flow.
func TestSttWS_ProbeFailureFallsBackToBatch(t *testing.T) {
	f := newSttFakeUpstream(t)
	f.mu.Lock()
	f.modelsStatus = 500
	f.mu.Unlock()

	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
	ws := dialSttWS(t, srv)
	sttSendJSON(t, ws, map[string]any{"type": "start"})
	sttExpectReady(t, ws, "batch")
	sttSendFrames(t, ws, append(speechPCM(32*epdFrameSamples), silencePCM(96*epdFrameSamples)...))

	log := sttReadUntilClose(t, ws)
	if log.done != "你好" {
		t.Fatalf("done = %q; errors = %q", log.done, log.errs)
	}
	liveCount, _, _, _, _, _, _ := f.got(t)
	if liveCount != 0 {
		t.Errorf("live requests = %d, want 0", liveCount)
	}
}

// Pre-stream 400: the bridge surfaces the upstream JSON error message and
// closes; no ready frame is ever sent.
func TestSttWS_Upstream400(t *testing.T) {
	f := newSttFakeUpstream(t)
	f.set("400")

	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
	ws := dialSttWS(t, srv)
	sttSendJSON(t, ws, map[string]any{"type": "start"})

	log := sttReadUntilClose(t, ws)
	if len(log.errs) != 1 || !strings.Contains(log.errs[0], "mode=streaming") {
		t.Fatalf("errors = %q, want one containing the upstream message", log.errs)
	}
	if log.done != "" {
		t.Errorf("done = %q, want none", log.done)
	}
}

// Flat {"message": ...} 400 body: the second upstream error shape surfaces
// its message too, not just the nested error.message form.
func TestSttWS_Upstream400FlatBody(t *testing.T) {
	f := newSttFakeUpstream(t)
	f.set("400")
	f.mu.Lock()
	f.live400Body = `{"message":"insufficient quota"}`
	f.mu.Unlock()

	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
	ws := dialSttWS(t, srv)
	sttSendJSON(t, ws, map[string]any{"type": "start"})

	log := sttReadUntilClose(t, ws)
	if len(log.errs) != 1 || log.errs[0] != "stt: insufficient quota" {
		t.Fatalf("errors = %q, want exactly the flat-body message", log.errs)
	}
	if log.done != "" {
		t.Errorf("done = %q, want none", log.done)
	}
}

// SSE error event: single terminal source (fake drains the body so the
// writer never errors), exactly one error through the unified exit, no leak.
func TestSttWS_UpstreamSSEError(t *testing.T) {
	f := newSttFakeUpstream(t)
	f.set("sse-error")
	hook := installSttTerminalHook(t)
	writerDone := installSttWriterDoneHook(t)

	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
	ws := dialSttWS(t, srv)
	sttSendJSON(t, ws, map[string]any{"type": "start"})
	sttExpectReady(t, ws, "live")

	log := sttReadUntilClose(t, ws)
	if len(log.errs) != 1 || !strings.Contains(log.errs[0], "busy") {
		t.Fatalf("errors = %q, want exactly one containing 'busy'", log.errs)
	}
	if log.done != "" {
		t.Errorf("done = %q, want none", log.done)
	}
	waitSttWriterDone(t, writerDone)
	if hook.countKind("error") != 1 || hook.count() != 1 {
		t.Errorf("terminal hook = %d total / %d error, want 1/1", hook.count(), hook.countKind("error"))
	}
}

// Upstream hard-dies mid-stream: the writer's own pw.Write error (not a
// readLoop stall) finalizes the connection. With both socket buffers
// pinned, absorption measures ~103 frames: of the 130 frames sent the fake
// consumes 16, the transport holds ~87 more, so the writer is blocked
// mid-write with pcmCh holding ~26 — below its 64 capacity, so the stall
// path cannot fire (the message-content assertion makes that visible).
func TestSttWS_UpstreamWriteError(t *testing.T) {
	f := newSttFakeUpstream(t)
	f.set("readn-close")
	f.mu.Lock()
	f.readN = 16 * 1024
	f.mu.Unlock()
	f.setThresholds(16384)
	pinSttLiveClient(t)
	hook := installSttTerminalHook(t)
	writerDone := installSttWriterDoneHook(t)

	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
	ws := dialSttWS(t, srv)
	sttSendJSON(t, ws, map[string]any{"type": "start"})
	sttExpectReady(t, ws, "live")

	sttSendFrames(t, ws, speechPCM(130*epdFrameSamples))
	f.waitProgress(t)
	close(f.closeCh) // fake hijacks and hard-closes the raw conn

	// The in-flight pw.Write returns with an error (transport aborts the
	// request body) → the writer's failConn is the single terminal source.
	log := sttReadUntilClose(t, ws)
	if len(log.errs) != 1 {
		t.Fatalf("errors = %q, want exactly one", log.errs)
	}
	if !strings.Contains(log.errs[0], "write failed") {
		t.Errorf("error message = %q, want it to contain 'write failed'", log.errs[0])
	}
	if strings.Contains(log.errs[0], "stalled") {
		t.Errorf("error message = %q must NOT contain 'stalled' (that would mean readLoop's stall path raced in)", log.errs[0])
	}
	if log.done != "" {
		t.Errorf("done = %q, want none", log.done)
	}
	waitSttWriterDone(t, writerDone)
	if hook.countKind("error") != 1 || hook.count() != 1 {
		t.Errorf("terminal hook = %d total / %d error, want 1/1", hook.count(), hook.countKind("error"))
	}
}

// Residual window (Assumptions #13): endpointing fired, the writer closed
// the pipe and exited, then the upstream died without a single SSE event —
// graceful FIN and crash RST are the same window. The SSE reader fallback
// must deliver exactly one error; before it existed nobody was left to
// finalize and the client hung on an open socket until a page refresh.
func TestSttWS_UpstreamDeathAfterFinalize(t *testing.T) {
	for _, tc := range []struct {
		name string
		rst  bool
	}{
		{"graceful fin", false},
		{"crash rst", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSttFakeUpstream(t)
			f.set("eof-death")
			f.mu.Lock()
			f.liveRST = tc.rst
			f.mu.Unlock()
			f.setThresholds(65536)
			hook := installSttTerminalHook(t)
			writerDone := installSttWriterDoneHook(t)

			srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
			ws := dialSttWS(t, srv)
			sttSendJSON(t, ws, map[string]any{"type": "start"})
			sttExpectReady(t, ws, "live")

			// 32 speech + 32 silence; the fake drinks the first 64 frames.
			sttSendFrames(t, ws, append(speechPCM(32*epdFrameSamples), silencePCM(32*epdFrameSamples)...))
			f.waitProgress(t)
			// Endpointing fires mid-batch, pcmCh closes, the writer drains
			// and pw.Close()s — the fake then sees body EOF and dies having
			// written no SSE line at all.
			sttSendFrames(t, ws, silencePCM(64*epdFrameSamples))

			log := sttReadUntilClose(t, ws)
			if len(log.errs) != 1 || log.errs[0] != "stt: upstream stream ended without a final event" {
				t.Fatalf("errors = %q, want exactly one fallback error", log.errs)
			}
			if log.done != "" {
				t.Errorf("done = %q, want none", log.done)
			}
			if !websocket.IsCloseError(log.closeErr, websocket.CloseNormalClosure) {
				t.Errorf("close error = %v, want normal closure after the fallback error", log.closeErr)
			}
			waitSttWriterDone(t, writerDone)
			if hook.countKind("error") != 1 || hook.count() != 1 {
				t.Errorf("terminal hook = %d total / %d error, want 1/1 (no double send)", hook.count(), hook.countKind("error"))
			}
		})
	}
}

// --- sseReader residual-window fallback, driven directly -------------------

// sttRSTBody is a response body whose every read fails — the crash-RST
// shape of an upstream death.
type sttRSTBody struct{}

func (sttRSTBody) Read([]byte) (int, error) {
	return 0, errors.New("read tcp: connection reset by peer")
}

// sttServerPair upgrades a throwaway WS pair: the server side drives sttConn
// methods directly, the client side observes what was (not) sent.
func sttServerPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	srvCh := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := sttUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		srvCh <- ws
	}))
	t.Cleanup(srv.Close)
	cl, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/", nil)
	if err != nil {
		t.Fatalf("dial pair: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	var s *websocket.Conn
	select {
	case s = <-srvCh:
	case <-time.After(30 * time.Second):
		t.Fatal("server side of the pair never upgraded")
	}
	return s, cl
}

// sttAssertSilent proves nothing was sent after a synchronous sseReader
// return: a marker written afterwards must be the first frame the client
// sees (WS is ordered — anything the reader sent would precede it, and a
// close frame would refuse the marker write outright).
func sttAssertSilent(t *testing.T, srv, cl *websocket.Conn) {
	t.Helper()
	if err := srv.WriteMessage(websocket.TextMessage, []byte(`{"type":"pair-marker"}`)); err != nil {
		t.Fatalf("marker write: %v (a close frame already went out — the reader was not silent)", err)
	}
	if data := readWSMessage(t, cl); string(data) != `{"type":"pair-marker"}` {
		t.Fatalf("first frame = %s, want the marker (the reader must send nothing)", data)
	}
}

// sttExpectFallbackError asserts the fallback's exact error frame and the
// normal close frame behind it.
func sttExpectFallbackError(t *testing.T, cl *websocket.Conn) {
	t.Helper()
	data := readWSMessage(t, cl)
	var m struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("fallback frame not JSON: %s", data)
	}
	if m.Type != "error" || m.Message != "stt: upstream stream ended without a final event" {
		t.Fatalf("fallback frame = %+v, want the exact residual-window error", m)
	}
	_ = cl.SetReadDeadline(time.Now().Add(30 * time.Second)) // REAL-TIME
	if _, _, err := cl.ReadMessage(); !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
		t.Fatalf("post-error read = %v, want the normal close frame", err)
	}
}

// Every gate of the fallback is load-bearing; each subtest pins one.
// writerDone closed + ctx alive + flag unset is exactly the residual
// window — writerDone only closes via the writer's exit, and with the ctx
// alive that exit is the finalize drain (pw.Close) finishing.
func TestSttSSEReader_ResidualFallback(t *testing.T) {
	newConn := func(t *testing.T, ws *websocket.Conn, cancelCtx, terminal bool) *sttConn {
		t.Helper()
		c := &sttConn{ws: ws, terminal: terminal}
		c.ctx, c.cancel = context.WithCancel(context.Background())
		if cancelCtx {
			c.cancel()
		}
		c.writerDone = make(chan struct{})
		close(c.writerDone) // the writer already closed the pipe and exited
		return c
	}

	t.Run("EOF after writer exit sends one error", func(t *testing.T) {
		hook := installSttTerminalHook(t)
		srv, cl := sttServerPair(t)
		newConn(t, srv, false, false).sseReader(&http.Response{
			Body: io.NopCloser(strings.NewReader("")),
		})
		sttExpectFallbackError(t, cl)
		if hook.count() != 1 || hook.countKind("error") != 1 {
			t.Fatalf("terminal hook = %d total / %d error, want 1/1", hook.count(), hook.countKind("error"))
		}
	})
	t.Run("read error after writer exit sends one error", func(t *testing.T) {
		hook := installSttTerminalHook(t)
		srv, cl := sttServerPair(t)
		newConn(t, srv, false, false).sseReader(&http.Response{
			Body: io.NopCloser(sttRSTBody{}),
		})
		sttExpectFallbackError(t, cl)
		if hook.count() != 1 || hook.countKind("error") != 1 {
			t.Fatalf("terminal hook = %d total / %d error, want 1/1", hook.count(), hook.countKind("error"))
		}
	})
	t.Run("ctx cancel stays silent", func(t *testing.T) {
		hook := installSttTerminalHook(t)
		srv, cl := sttServerPair(t)
		newConn(t, srv, true, false).sseReader(&http.Response{
			Body: io.NopCloser(strings.NewReader("")),
		})
		sttAssertSilent(t, srv, cl)
		if hook.count() != 0 {
			t.Fatalf("terminal hook fired %dx on a ctx-cancel exit, want 0 (teardown/stall must not error)", hook.count())
		}
	})
	t.Run("terminal already set is a no-op", func(t *testing.T) {
		hook := installSttTerminalHook(t)
		srv, cl := sttServerPair(t)
		newConn(t, srv, false, true).sseReader(&http.Response{
			Body: io.NopCloser(strings.NewReader("")),
		})
		sttAssertSilent(t, srv, cl)
		if hook.count() != 0 {
			t.Fatalf("terminal hook fired %dx after done already went out, want 0 (no double send)", hook.count())
		}
	})
	t.Run("writer still running stays silent", func(t *testing.T) {
		hook := installSttTerminalHook(t)
		srv, cl := sttServerPair(t)
		c := newConn(t, srv, false, false)
		c.writerDone = make(chan struct{}) // writer alive: its write-error path owns finalizing
		c.sseReader(&http.Response{
			Body: io.NopCloser(strings.NewReader("")),
		})
		sttAssertSilent(t, srv, cl)
		if hook.count() != 0 {
			t.Fatalf("terminal hook fired %dx while the writer was still running, want 0 (message ownership: write-failed path)", hook.count())
		}
	})
}

// start while an utterance is in progress: non-terminal refusal, connection
// and utterance survive (stop still finalizes).
func TestSttWS_StartWhileActive(t *testing.T) {
	f := newSttFakeUpstream(t)
	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
	ws := dialSttWS(t, srv)
	sttSendJSON(t, ws, map[string]any{"type": "start"})
	sttExpectReady(t, ws, "live")

	sttSendJSON(t, ws, map[string]any{"type": "start"})
	var second struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(readWSMessage(t, ws), &second); err != nil {
		t.Fatalf("second start reply: %v", err)
	}
	if second.Type != "error" || !strings.Contains(second.Message, "in progress") {
		t.Fatalf("second start reply = %+v, want error 'utterance in progress'", second)
	}

	// Non-terminal: the utterance still finalizes through stop.
	sttSendJSON(t, ws, map[string]any{"type": "stop"})
	log := sttReadUntilClose(t, ws)
	if log.done != "你好。 世界。" {
		t.Errorf("done = %q, want 你好。 世界。 (refusal must not kill the utterance); errs = %q", log.done, log.errs)
	}
}

func TestSttWS_BatchOverflow(t *testing.T) {
	// The 20MB pcmBuf limit is unreachable through the WS (the endpointer's
	// 60s insurance finalizes at ~1.9MB); the observable bound is the WS
	// read limit — an oversized single frame kills the connection.
	t.Run("giant frame disconnects", func(t *testing.T) {
		f := newSttFakeUpstream(t)
		srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt"))
		ws := dialSttWS(t, srv)
		sttSendJSON(t, ws, map[string]any{"type": "start"})
		sttExpectReady(t, ws, "batch")

		// Above the WS read limit: gorilla refuses the frame and the bridge
		// stops reading — the client write is parked in its socket until
		// the close propagates, so it must not run on the test goroutine.
		go func() {
			_ = ws.WriteMessage(websocket.BinaryMessage, make([]byte, sttReadLimit+1))
		}()
		_ = ws.SetReadDeadline(time.Now().Add(30 * time.Second)) // REAL-TIME
		if _, _, err := ws.ReadMessage(); err == nil {
			t.Fatal("connection should be dead after an oversized frame")
		}
	})
}

// Closing the WS mid-utterance cancels the upstream request (fake handler
// exits) and leaks no writer goroutine.
func TestSttWS_TeardownCancelsUpstream(t *testing.T) {
	f := newSttFakeUpstream(t)
	writerDone := installSttWriterDoneHook(t)

	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
	ws := dialSttWS(t, srv)
	sttSendJSON(t, ws, map[string]any{"type": "start"})
	sttExpectReady(t, ws, "live")
	sttSendFrames(t, ws, speechPCM(8*epdFrameSamples))

	if err := ws.Close(); err != nil {
		t.Fatalf("client close: %v", err)
	}
	f.waitHandlerExit(t)
	waitSttWriterDone(t, writerDone)
}

// --- settings-layer tests -------------------------------------------------

const sttSettingsFixture = `{
  "model": {"default": "zhipu/glm-5.3"},
  "providers": [
    {"name": "zhipu", "url": "https://open.bigmodel.cn/api/paas/v4", "keys": ["k"], "models": {"glm-5.3": {}}},
    {"name": "audiocpp", "url": "http://127.0.0.1:8082/v1", "type": "audio", "models": {"stt-stream": {}, "stt": {}}}
  ],
  "stt": {"default": "audiocpp/stt-stream"}
}`

func TestSttDefaultPut(t *testing.T) {
	srv := newSettingsServer(t)
	seedSettings(t, sttSettingsFixture)

	resp, err := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/stt", strings.NewReader(`{"provider":"audiocpp","model":"stt-stream"}`))
	if err != nil {
		t.Fatal(err)
	}
	httpResp, err := http.DefaultClient.Do(resp)
	if err != nil {
		t.Fatal(err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", httpResp.StatusCode)
	}
	raw, err := os.ReadFile(filepath.Join(homeDir(t), ".gbot", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		STT map[string]string `json:"stt"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.STT["default"] != "audiocpp/stt-stream" {
		t.Errorf("stt.default = %q, want audiocpp/stt-stream", doc.STT["default"])
	}

	for _, tc := range []struct {
		name string
		body string
	}{
		{"non-audio provider", `{"provider":"zhipu","model":"glm-5.3"}`},
		{"unknown provider", `{"provider":"nope","model":"stt"}`},
		{"unknown model", `{"provider":"audiocpp","model":"nope"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/stt", strings.NewReader(tc.body))
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Body.Close()
			if r.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", r.StatusCode)
			}
		})
	}
}

func homeDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func TestValidateProviders_AudioAccepted(t *testing.T) {
	p := config.Provider{Name: "a", URL: "http://127.0.0.1:8082/v1", Type: config.ProviderTypeAudio}
	p.Models.Set("stt-stream", config.ModelConfig{})
	if msg := validateProviders([]config.Provider{p}); msg != "" {
		t.Errorf("validateProviders(audio) = %q, want accepted", msg)
	}
}

func TestProbeProvider_AudioKeyless(t *testing.T) {
	probed := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case probed <- r.URL.Path + "|" + r.Header.Get("Authorization"):
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	p := &config.Provider{Name: "a", URL: srv.URL, Type: config.ProviderTypeAudio}
	latency, err := probeProvider(context.Background(), p)
	if err != nil {
		t.Fatalf("probeProvider keyless audio: %v", err)
	}
	if latency < 0 {
		t.Errorf("latency = %d, want >= 0", latency)
	}
	select {
	case got := <-probed:
		if got != "/models|" {
			t.Errorf("probe hit %q, want /models with no Authorization", got)
		}
	default:
		t.Error("probe made no request")
	}
}

func TestBuildConfig_SttFlag(t *testing.T) {
	audio := &config.Provider{Name: "audiocpp", URL: "http://127.0.0.1:8082/v1", Type: config.ProviderTypeAudio}
	audio.Models.Set("stt-stream", config.ModelConfig{})
	c := newTestConnectorWithConfig(t, hub.NewHub(), buildTestProviders(), map[string]*config.Provider{
		"zhipu":    buildTestProviderConfigs()["zhipu"],
		"audiocpp": audio,
	})
	c.mock().providerFn = func() llm.Provider { return &stubLLMProvider{name: "zhipu"} }
	c.mock().modelFn = func() string { return "glm-5.2" }

	c.SetSTTDefault("audiocpp/stt-stream")
	payload := c.buildConfig(c.activeSlotTest(t))
	if !strings.Contains(string(payload), `"stt":true`) {
		t.Errorf("config frame with STT default = %s, want stt:true", payload)
	}

	c.SetSTTDefault("")
	payload = c.buildConfig(c.activeSlotTest(t))
	if strings.Contains(string(payload), `"stt"`) {
		t.Errorf("config frame without STT default = %s, want the stt field omitted", payload)
	}
}

func TestGetProviders_SttPayload(t *testing.T) {
	srv := newSettingsServer(t)
	seedSettings(t, sttSettingsFixture)

	var got struct {
		Stt *struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"stt"`
	}
	resp := getJSON(t, srv.URL+"/api/settings/providers", &got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got.Stt == nil {
		t.Fatal("stt payload missing — configured default must surface")
	}
	if got.Stt.Provider != "audiocpp" || got.Stt.Model != "stt-stream" {
		t.Errorf("stt = %s/%s, want audiocpp/stt-stream", got.Stt.Provider, got.Stt.Model)
	}
}

// SSE scanner buffer: events larger than the initial bufio window must
// still parse (the 1MB cap is the real limit). Two lines: one ~200KB (kills
// buffer-shrink and division mutants), one at cap+100 (kills a one-KB-shy
// cap) — both must deliver their deltas verbatim.
func TestSttWS_LargeSSELines(t *testing.T) {
	f := newSttFakeUpstream(t)
	f.set("ok-bigline")
	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
	ws := dialSttWS(t, srv)
	sttSendJSON(t, ws, map[string]any{"type": "start"})
	sttExpectReady(t, ws, "live")
	sttSendFrames(t, ws, speechPCM(2*epdFrameSamples))
	sttSendJSON(t, ws, map[string]any{"type": "stop"})

	log := sttReadUntilClose(t, ws)
	if len(log.deltas) != 2 {
		t.Fatalf("deltas = %d, want 2 (both large lines must parse)", len(log.deltas))
	}
	if len(log.deltas[0]) != 200000 || len(log.deltas[1]) != 1024*1023+100 {
		t.Errorf("delta lengths = %d, %d, want 200000 and %d", len(log.deltas[0]), len(log.deltas[1]), 1024*1023+100)
	}
	if log.done == "" {
		t.Errorf("no done after large lines; errors = %q", log.errs)
	}
}

// Frames that arrive before any start must vanish silently — no stall
// error, no crash; the connection still accepts a start afterwards.
func TestSttWS_FramesBeforeStartDropped(t *testing.T) {
	f := newSttFakeUpstream(t)
	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
	ws := dialSttWS(t, srv)
	sttSendBinaries(t, ws, framesOf(silencePCM(4*epdFrameSamples)))

	sttSendJSON(t, ws, map[string]any{"type": "start"})
	sttExpectReady(t, ws, "live")
	sttSendFrames(t, ws, speechPCM(2*epdFrameSamples))
	sttSendJSON(t, ws, map[string]any{"type": "stop"})
	log := sttReadUntilClose(t, ws)
	if len(log.errs) != 0 {
		t.Fatalf("errors = %q, want none (pre-start frames are drops, not stalls)", log.errs)
	}
	if log.done == "" {
		t.Fatal("no done — connection must stay usable after pre-start frames")
	}
	_, _, _, pcmTotal, _, _, _ := f.got(t)
	if pcmTotal != 2*1024 {
		t.Errorf("upstream received %d bytes, want 2048 (only the post-start frames)", pcmTotal)
	}
}

// A malformed text frame is dropped, not fatal: the read loop keeps serving
// the same connection (text-frame mirror of FramesBeforeStartDropped).
func TestSttWS_MalformedJSONFrameDropped(t *testing.T) {
	f := newSttFakeUpstream(t)
	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
	ws := dialSttWS(t, srv)
	if err := ws.WriteMessage(websocket.TextMessage, []byte("{not json")); err != nil {
		t.Fatalf("send malformed frame: %v", err)
	}

	sttSendJSON(t, ws, map[string]any{"type": "start"})
	sttExpectReady(t, ws, "live")
	sttSendFrames(t, ws, speechPCM(2*epdFrameSamples))
	sttSendJSON(t, ws, map[string]any{"type": "stop"})
	log := sttReadUntilClose(t, ws)
	if len(log.errs) != 0 {
		t.Fatalf("errors = %q, want none (malformed frames are drops)", log.errs)
	}
	if log.done == "" {
		t.Fatal("no done — connection must stay usable after a malformed frame")
	}
	_, _, _, pcmTotal, _, _, _ := f.got(t)
	if pcmTotal != 2*1024 {
		t.Errorf("upstream received %d bytes, want 2048", pcmTotal)
	}
}

// start failures surface as terminal error frames with the cause in the
// message: missing stt.default and unreadable settings.json.
func TestSttWS_StartResolveFailures(t *testing.T) {
	f := newSttFakeUpstream(t)
	srv := newSttBridge(t, `{"providers":[{"name":"audiocpp","url":`+fmt.Sprintf("%q", f.srv.URL+"/v1")+`,"type":"audio","models":{"stt-stream":{}}}]}`)
	ws := dialSttWS(t, srv)
	sttSendJSON(t, ws, map[string]any{"type": "start"})
	log := sttReadUntilClose(t, ws)
	if len(log.errs) != 1 || !strings.Contains(log.errs[0], "no default configured") {
		t.Fatalf("errors = %q, want the missing-default message", log.errs)
	}
}

func TestSttWS_StartConfigLoadFailure(t *testing.T) {
	srv := newSttBridge(t, `{not json`)
	ws := dialSttWS(t, srv)
	sttSendJSON(t, ws, map[string]any{"type": "start"})
	log := sttReadUntilClose(t, ws)
	if len(log.errs) != 1 || !strings.Contains(log.errs[0], "config load failed") {
		t.Fatalf("errors = %q, want a config-load failure message", log.errs)
	}
}

// Batch transcription failure surfaces as a terminal error.
func TestSttWS_BatchTranscribeFailure(t *testing.T) {
	f := newSttFakeUpstream(t)
	f.mu.Lock()
	f.batchStatus = http.StatusInternalServerError
	f.mu.Unlock()
	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt"))
	ws := dialSttWS(t, srv)
	sttSendJSON(t, ws, map[string]any{"type": "start"})
	sttExpectReady(t, ws, "batch")
	sttSendFrames(t, ws, append(speechPCM(32*epdFrameSamples), silencePCM(96*epdFrameSamples)...))
	log := sttReadUntilClose(t, ws)
	if len(log.errs) != 1 || !strings.Contains(log.errs[0], "transcription failed") {
		t.Fatalf("errors = %q, want one containing 'transcription failed'", log.errs)
	}
	if log.done != "" {
		t.Errorf("done = %q, want none on failure", log.done)
	}
}

// Probe behavior pins: malformed provider URL, non-200 models with a valid
// body, and the exact 5s probe timeout boundary from both sides.
func TestSttWS_ProbeBranches(t *testing.T) {
	t.Run("invalid provider URL falls back to batch", func(t *testing.T) {
		srv := newSttBridge(t, sttSettings("http://127.0.0.1:1\\x7f", "", "audiocpp/stt-stream"))
		ws := dialSttWS(t, srv)
		sttSendJSON(t, ws, map[string]any{"type": "start"})
		sttExpectReady(t, ws, "batch")
		_ = ws.Close()
	})
	t.Run("non-200 models with valid body falls back to batch", func(t *testing.T) {
		f := newSttFakeUpstream(t)
		f.mu.Lock()
		f.modelsStatus = http.StatusUnauthorized
		f.mu.Unlock()
		srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
		ws := dialSttWS(t, srv)
		sttSendJSON(t, ws, map[string]any{"type": "start"})
		sttExpectReady(t, ws, "batch")
		_ = ws.Close()
	})
	t.Run("models answering just inside 5s still goes live", func(t *testing.T) {
		f := newSttFakeUpstream(t)
		f.mu.Lock()
		f.modelsDelay = 4500 * time.Millisecond
		f.mu.Unlock()
		srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
		ws := dialSttWS(t, srv)
		sttSendJSON(t, ws, map[string]any{"type": "start"})
		sttExpectReady(t, ws, "live")
		_ = ws.Close()
	})
	t.Run("models answering just past 5s falls back to batch", func(t *testing.T) {
		f := newSttFakeUpstream(t)
		f.mu.Lock()
		f.modelsDelay = 5500 * time.Millisecond
		f.mu.Unlock()
		srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
		ws := dialSttWS(t, srv)
		sttSendJSON(t, ws, map[string]any{"type": "start"})
		sttExpectReady(t, ws, "batch")
		_ = ws.Close()
	})
	t.Run("probe carries the key as Bearer, omits it when keyless", func(t *testing.T) {
		f := newSttFakeUpstream(t)
		srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "sk-9", "audiocpp/stt-stream"))
		ws := dialSttWS(t, srv)
		sttSendJSON(t, ws, map[string]any{"type": "start"})
		sttExpectReady(t, ws, "live")
		_, modelsAuth := f.gotExtras(t)
		if modelsAuth != "Bearer sk-9" {
			t.Errorf("probe Authorization = %q, want Bearer sk-9", modelsAuth)
		}
		_ = ws.Close()

		f2 := newSttFakeUpstream(t)
		srv2 := newSttBridge(t, sttSettings(f2.srv.URL+"/v1", "", "audiocpp/stt-stream"))
		ws2 := dialSttWS(t, srv2)
		sttSendJSON(t, ws2, map[string]any{"type": "start"})
		sttExpectReady(t, ws2, "live")
		_, modelsAuth2 := f2.gotExtras(t)
		if modelsAuth2 != "" {
			t.Errorf("keyless probe Authorization = %q, want empty", modelsAuth2)
		}
		_ = ws2.Close()
	})
}

// Live request headers: Accept pins the SSE expectation.
func TestSttWS_LiveAcceptHeader(t *testing.T) {
	f := newSttFakeUpstream(t)
	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
	ws := dialSttWS(t, srv)
	sttSendJSON(t, ws, map[string]any{"type": "start"})
	sttExpectReady(t, ws, "live")
	sttSendFrames(t, ws, speechPCM(2*epdFrameSamples))
	sttSendJSON(t, ws, map[string]any{"type": "stop"})
	log := sttReadUntilClose(t, ws)
	if log.done == "" {
		t.Fatalf("no done; errors = %q", log.errs)
	}
	accept, _ := f.gotExtras(t)
	if accept != "text/event-stream" {
		t.Errorf("live Accept = %q, want text/event-stream", accept)
	}
}

// SSE error events with a missing or empty error object must not crash the
// reader and must fall back to the generic message.
func TestSttWS_UpstreamSSEErrorNoObject(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"no error object", `{"type":"error"}`, "upstream error"},
		{"empty message", `{"type":"error","error":{"message":""}}`, "upstream error"},
		{"with message", `{"type":"error","error":{"message":"boom text"}}`, "boom text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSttFakeUpstream(t)
			f.mu.Lock()
			f.liveMode = "sse-error"
			f.sseErrorBody = tc.body
			f.mu.Unlock()
			srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt-stream"))
			ws := dialSttWS(t, srv)
			sttSendJSON(t, ws, map[string]any{"type": "start"})
			sttExpectReady(t, ws, "live")
			log := sttReadUntilClose(t, ws)
			if len(log.errs) != 1 || log.errs[0] != tc.want {
				t.Fatalf("errors = %q, want exactly [%s]", log.errs, tc.want)
			}
		})
	}
}

// PUT /api/settings/stt contract pins: exact ok body, malformed-body 400,
// and internal-failure 500 (read-only HOME makes the save fail).
func TestSttDefaultPutContract(t *testing.T) {
	t.Run("ok body is exactly {\"ok\":true}", func(t *testing.T) {
		srv := newSettingsServer(t)
		seedSettings(t, sttSettingsFixture)
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/stt", strings.NewReader(`{"provider":"audiocpp","model":"stt"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if string(body) != `{"ok":true}` {
			t.Errorf("body = %s, want exactly {\"ok\":true}", body)
		}
	})
	t.Run("malformed body is a 400", func(t *testing.T) {
		srv := newSettingsServer(t)
		seedSettings(t, sttSettingsFixture)
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/stt", strings.NewReader(`{nope`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "invalid body") {
			t.Errorf("body = %s, want the invalid-body message (not a save-path error)", body)
		}
	})
	t.Run("save failure is a 500", func(t *testing.T) {
		srv := newSettingsServer(t)
		seedSettings(t, sttSettingsFixture)
		// Read-only config dir: validation passes (file exists) but every
		// write path fails.
		home := homeDir(t)
		if err := os.Chmod(filepath.Join(home, ".gbot"), 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(home, ".gbot"), 0o755) })
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/stt", strings.NewReader(`{"provider":"audiocpp","model":"stt"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", resp.StatusCode)
		}
	})
}

// probeProvider's audio branch: malformed URL errors with zero latency;
// the key rides as Bearer when present.
func TestProbeProvider_AudioBranches(t *testing.T) {
	t.Run("malformed URL errors", func(t *testing.T) {
		p := &config.Provider{Name: "a", URL: "http://127.0.0.1:1\x7f", Type: config.ProviderTypeAudio}
		latency, err := probeProvider(context.Background(), p)
		if err == nil {
			t.Fatal("probeProvider with malformed URL must error")
		}
		if latency != 0 {
			t.Errorf("latency = %d, want 0 on failure", latency)
		}
	})
	t.Run("keyed probe sends Bearer", func(t *testing.T) {
		var auth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)
		p := &config.Provider{Name: "a", URL: srv.URL, Type: config.ProviderTypeAudio, Keys: []string{"sk-2"}}
		if _, err := probeProvider(context.Background(), p); err != nil {
			t.Fatalf("probeProvider: %v", err)
		}
		if auth != "Bearer sk-2" {
			t.Errorf("probe Authorization = %q, want Bearer sk-2", auth)
		}
	})
}

// Batch-mode twin of the pre-start drop test: in batch mode a leaked
// pre-start frame would land in pcmBuf and ride into the transcription, so
// the byte-count pin catches it.
func TestSttWS_FramesBeforeStartDroppedBatch(t *testing.T) {
	f := newSttFakeUpstream(t)
	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt"))
	ws := dialSttWS(t, srv)
	sttSendBinaries(t, ws, framesOf(silencePCM(4*epdFrameSamples)))

	sttSendJSON(t, ws, map[string]any{"type": "start"})
	sttExpectReady(t, ws, "batch")
	sttSendFrames(t, ws, speechPCM(2*epdFrameSamples))
	sttSendJSON(t, ws, map[string]any{"type": "stop"})
	log := sttReadUntilClose(t, ws)
	if log.done != "你好" {
		t.Fatalf("done = %q; errors = %q", log.done, log.errs)
	}
	_, _, _, _, _, _, file := f.got(t)
	// 2 frames of speech + nothing else — the 4 pre-start frames must not
	// ride along (44-byte WAV header + 2048 PCM).
	if len(file) != 44+2*1024 {
		t.Errorf("transcribed file = %d bytes, want %d (pre-start frames leaked into the buffer)", len(file), 44+2*1024)
	}
}

// The upgrader accepts any Origin (same-origin deployment posture, same as
// the chat/VNC upgraders); gorilla's default would 403 a foreign Origin.
func TestSttWS_AcceptsForeignOrigin(t *testing.T) {
	f := newSttFakeUpstream(t)
	srv := newSttBridge(t, sttSettings(f.srv.URL+"/v1", "", "audiocpp/stt"))
	hdr := http.Header{}
	hdr.Set("Origin", "http://foreign.example")
	ws, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/stt", hdr)
	if err != nil {
		t.Fatalf("dial with foreign Origin: %v (status %v)", err, respStatus(resp))
	}
	defer ws.Close()
	sttSendJSON(t, ws, map[string]any{"type": "start"})
	sttExpectReady(t, ws, "batch")
}

func respStatus(resp *http.Response) any {
	if resp == nil {
		return "nil"
	}
	return resp.StatusCode
}

// A plain GET without upgrade headers is refused by the upgrader itself
// (gorilla writes the 400); the handler must return, never enter the WS
// loop with a nil conn.
func TestSttWS_PlainGetRejected(t *testing.T) {
	srv := newSttBridge(t, sttSettings("http://127.0.0.1:1/v1", "", "audiocpp/stt"))
	resp, err := http.Get(srv.URL + "/ws/stt")
	if err != nil {
		t.Fatalf("plain GET: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read refusal body: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if string(body) != "Bad Request\n" {
		t.Errorf("body = %q, want gorilla's handshake refusal", body)
	}
}
