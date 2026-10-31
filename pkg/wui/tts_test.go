package wui

// /api/tts endpoint integration tests: one sentence in (text/plain), raw PCM
// out (s16le@24k mono, chunked), through a real httptest mux and a fake
// audio.cpp SSE upstream whose wire shape was verified live
// (speech.audio.delta base64 "audio" field, speech.audio.done, [DONE]).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
)

// --- fake upstream (audio.cpp speech endpoint only) ------------------------

type ttsFake struct {
	t *testing.T

	mu          sync.Mutex
	speechCalls int
	speechBody  []string // request bodies in call order
	speechAuth  []string // Authorization header per call
	voicesQuery string   // last /audio/voices query
	voicesAuth  string   // last /audio/voices Authorization
	voicesFail  bool     // make /audio/voices answer 502

	// holdFirstSpeech: the FIRST /audio/speech call flushes SSE headers then
	// blocks until its request ctx dies (client-cancel test). firstCanceled
	// closes when that abort is observed.
	holdFirstSpeech bool
	firstCanceled   chan struct{}

	// stallAfterFirstDelta: the first speech call emits ONE audio delta,
	// flushes, then parks on releaseFirst until the test closes it (the
	// streaming test proves the handler forwards chunk 1 without waiting for
	// the response to finish).
	stallAfterFirstDelta bool
	releaseFirst         chan struct{}

	// firstSpeechFails: the first speech call answers an SSE error event
	// before any delta (synthesis failure before any PCM).
	firstSpeechFails bool

	// firstSpeechPartialThenError: the first speech call emits ONE audio
	// delta then an error event (headers already committed — truncated
	// stream).
	firstSpeechPartialThenError bool

	// firstSpeechZeroPCM: the first speech call emits done + [DONE] with no
	// audio delta at all (zero-PCM success).
	firstSpeechZeroPCM bool
}

func (f *ttsFake) countSpeech() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.speechCalls
}

func (f *ttsFake) speechBodies() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.speechBody...)
}

func (f *ttsFake) speechAuths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.speechAuth...)
}

func (f *ttsFake) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/audio/speech", f.handleSpeech)
	mux.HandleFunc("GET /v1/audio/voices", f.handleVoices)
	return mux
}

func (f *ttsFake) handleVoices(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.voicesQuery = r.URL.RawQuery
	f.voicesAuth = r.Header.Get("Authorization")
	f.mu.Unlock()
	if f.voicesFail {
		http.Error(w, "engine boom", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"voices":["Aiden","Serena","Ryan"]}`))
}

var voiceFakePCM = [][]byte{
	{1, 2, 3, 4},
	{5, 6, 7, 8},
}

func (f *ttsFake) handleSpeech(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.speechCalls++
	call := f.speechCalls
	hold := f.holdFirstSpeech && call == 1
	failFirst := f.firstSpeechFails && call == 1
	partialFirst := f.firstSpeechPartialThenError && call == 1
	zeroFirst := f.firstSpeechZeroPCM && call == 1
	stallFirst := f.stallAfterFirstDelta && call == 1
	body, _ := io.ReadAll(r.Body)
	f.speechBody = append(f.speechBody, string(body))
	f.speechAuth = append(f.speechAuth, r.Header.Get("Authorization"))
	f.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher := w.(http.Flusher)
	flusher.Flush()

	writeDelta := func(chunk []byte) {
		_, _ = io.WriteString(w, `data: {"type":"speech.audio.delta","audio":"`+base64.StdEncoding.EncodeToString(chunk)+`"}`+"\n\n")
		flusher.Flush()
	}
	writeError := func() {
		_, _ = io.WriteString(w, `data: {"type":"error","error":{"message":"engine boom"}}`+"\n\n")
		flusher.Flush()
	}
	if failFirst {
		writeError()
		return
	}
	if partialFirst {
		writeDelta(voiceFakePCM[0])
		writeError()
		return
	}
	if zeroFirst {
		_, _ = io.WriteString(w, `data: {"type":"speech.audio.done","ttft_ms":42}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}
	if stallFirst {
		writeDelta(voiceFakePCM[0])
		<-f.releaseFirst
		writeDelta(voiceFakePCM[1])
		_, _ = io.WriteString(w, `data: {"type":"speech.audio.done","ttft_ms":42}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}
	if hold {
		<-r.Context().Done()
		if f.firstCanceled != nil {
			select {
			case <-f.firstCanceled:
			default:
				close(f.firstCanceled)
			}
		}
		return
	}

	for _, chunk := range voiceFakePCM {
		writeDelta(chunk)
	}
	_, _ = io.WriteString(w, `data: {"type":"speech.audio.done","ttft_ms":42}`+"\n\n")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// --- settings + mux helpers -------------------------------------------------

func ttsSettings(upstreamURL string, withTTS bool) string {
	tts := ""
	if withTTS {
		tts = `,"tts":{"default":"audiocpp/qwen3-tts-1.7b"}`
	}
	return fmt.Sprintf(`{"providers":[{"name":"audiocpp","url":%q,"type":"audio","keys":["k"],"models":{"asr-stream":{},"qwen3-tts-1.7b":{}}}],"stt":{"default":"audiocpp/asr-stream"}%s}`,
		upstreamURL, tts)
}

func newTTSBridge(t *testing.T, settings string) *httptest.Server {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".gbot"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gbot", "settings.json"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterTTSRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// postTTS issues one POST /api/tts against the bridge.
func postTTS(t *testing.T, srv *httptest.Server, text string) *http.Response {
	t.Helper()
	return postBytes(t, srv, []byte(text))
}

// postBytes issues one POST /api/tts with a raw body (the bound tests need
// byte-exact sizes).
func postBytes(t *testing.T, srv *httptest.Server, body []byte) *http.Response {
	t.Helper()
	resp, err := http.Post(srv.URL+"/api/tts", "text/plain", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// ttsErrorBody decodes the {"error":...} envelope of an error response.
func ttsErrorBody(t *testing.T, body io.Reader) string {
	t.Helper()
	var env struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(body).Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	return env.Error
}

// newFakeUpstream mounts the fake audio.cpp speech endpoint.
func newFakeUpstream(t *testing.T, f *ttsFake) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(f.handler())
	t.Cleanup(up.Close)
	return up
}

// --- the tests ----------------------------------------------------------------

// Hot path: one sentence in, the full PCM out, and the upstream request is
// resolved server-side (the client carries no backend knowledge).
func TestTTSHTTP_Stream(t *testing.T) {
	f := &ttsFake{t: t}
	up := newFakeUpstream(t, f)
	srv := newTTSBridge(t, ttsSettings(up.URL+"/v1", true))

	resp := postTTS(t, srv, "hi")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "audio/L16;rate=24000;channels=1" {
		t.Errorf("content type = %q, want audio/L16;rate=24000;channels=1", ct)
	}
	pcm, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(pcm) != "\x01\x02\x03\x04\x05\x06\x07\x08" {
		t.Errorf("body = %v (%d bytes), want the two fake chunks concatenated", pcm, len(pcm))
	}
	bodies := f.speechBodies()
	if len(bodies) != 1 {
		t.Fatalf("speech calls = %d, want 1", len(bodies))
	}
	for _, want := range []string{`"input":"hi"`, `"model":"qwen3-tts-1.7b"`} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("speech body missing %s: %s", want, bodies[0])
		}
	}
	// The provider key resolves server-side; the upstream sees it as a bearer.
	auths := f.speechAuths()
	if len(auths) != 1 || auths[0] != "Bearer k" {
		t.Errorf("authorization = %v, want [Bearer k] (server-side key resolution)", auths)
	}
	// No tts.voice in settings: the request must omit the field so the
	// engine's own default preset applies.
	if strings.Contains(bodies[0], "voice") {
		t.Errorf("speech body should omit voice when unconfigured: %s", bodies[0])
	}
}

func TestTTSHTTP_VoiceFromSettings(t *testing.T) {
	f := &ttsFake{t: t}
	up := newFakeUpstream(t, f)
	srv := newTTSBridge(t, strings.Replace(ttsSettings(up.URL+"/v1", true),
		`"tts":{"default":"audiocpp/qwen3-tts-1.7b"}`,
		`"tts":{"default":"audiocpp/qwen3-tts-1.7b","voice":"Serena"}`, 1))
	resp := postTTS(t, srv, "hi")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	bodies := f.speechBodies()
	if len(bodies) != 1 {
		t.Fatalf("speech calls = %d, want 1", len(bodies))
	}
	if !strings.Contains(bodies[0], `"voice":"Serena"`) {
		t.Errorf("speech body missing configured voice: %s", bodies[0])
	}
}

func TestTTSHTTP_EmptyBody(t *testing.T) {
	f := &ttsFake{t: t}
	up := newFakeUpstream(t, f)
	srv := newTTSBridge(t, ttsSettings(up.URL+"/v1", true))

	resp := postTTS(t, srv, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if msg := ttsErrorBody(t, resp.Body); !strings.Contains(msg, "empty body") {
		t.Errorf("error = %q, want the empty-body diagnosis", msg)
	}
	if calls := f.countSpeech(); calls != 0 {
		t.Errorf("speech calls = %d, want 0 (rejected before any synthesis)", calls)
	}
}

// The bound is pinned by literals (a ttsBodyLimit-relative body would track
// the constant under mutation): 1 MiB + 1 bytes is rejected, exactly 1 MiB
// still synthesizes.
func TestTTSHTTP_BodyTooLarge(t *testing.T) {
	f := &ttsFake{t: t}
	up := newFakeUpstream(t, f)
	srv := newTTSBridge(t, ttsSettings(up.URL+"/v1", true))

	resp, err := http.Post(srv.URL+"/api/tts", "text/plain", bytes.NewReader(make([]byte, 1<<20+1)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
	if msg := ttsErrorBody(t, resp.Body); !strings.Contains(msg, "too large") {
		t.Errorf("error = %q, want the too-large diagnosis", msg)
	}
	if calls := f.countSpeech(); calls != 0 {
		t.Errorf("speech calls = %d, want 0 (rejected before any synthesis)", calls)
	}

	// Exactly at the bound passes (the limit is inclusive).
	okResp := postBytes(t, srv, make([]byte, 1<<20))
	if okResp.StatusCode != http.StatusOK {
		t.Fatalf("status at the bound = %d, want 200", okResp.StatusCode)
	}
	pcm, err := io.ReadAll(okResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(pcm) != "\x01\x02\x03\x04\x05\x06\x07\x08" {
		t.Errorf("body at the bound = %v (%d bytes), want the full fake PCM", pcm, len(pcm))
	}
}

// errReader always fails: the client-aborted-upload shape, driven straight
// into the handler (a real HTTP client cannot produce a mid-body read error).
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("upload died") }

func TestTTSHTTP_BodyReadError(t *testing.T) {
	f := &ttsFake{t: t}
	up := newFakeUpstream(t, f)
	newTTSBridge(t, ttsSettings(up.URL+"/v1", true))

	w := httptest.NewRecorder()
	handleTTS(w, httptest.NewRequest(http.MethodPost, "/api/tts", errReader{}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if !strings.Contains(env.Error, "read body") || !strings.Contains(env.Error, "upload died") {
		t.Errorf("error = %q, want the read-body diagnosis carrying the cause", env.Error)
	}
	if calls := f.countSpeech(); calls != 0 {
		t.Errorf("speech calls = %d, want 0", calls)
	}
}

// No tts default configured: 503 BEFORE streaming anything.
func TestTTSHTTP_NoDefault(t *testing.T) {
	f := &ttsFake{t: t}
	up := newFakeUpstream(t, f)
	srv := newTTSBridge(t, ttsSettings(up.URL+"/v1", false))

	resp := postTTS(t, srv, "hi")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if msg := ttsErrorBody(t, resp.Body); !strings.Contains(msg, "tts") {
		t.Errorf("error = %q, want a tts diagnosis", msg)
	}
	if calls := f.countSpeech(); calls != 0 {
		t.Errorf("speech calls = %d, want 0 (errors before streaming; nothing half-open)", calls)
	}
}

// Malformed settings.json: 500, still a real HTTP error — not a dead stream.
func TestTTSHTTP_ConfigLoadError(t *testing.T) {
	f := &ttsFake{t: t}
	newFakeUpstream(t, f)
	srv := newTTSBridge(t, `{bad json`)

	resp := postTTS(t, srv, "hi")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	if msg := ttsErrorBody(t, resp.Body); !strings.Contains(msg, "config") {
		t.Errorf("error = %q, want the config-load diagnosis", msg)
	}
	if calls := f.countSpeech(); calls != 0 {
		t.Errorf("speech calls = %d, want 0", calls)
	}
}

// Synthesis error before any PCM: a real 502, not a half-open 200 stream.
func TestTTSHTTP_SynthesisErrorBeforePCM(t *testing.T) {
	f := &ttsFake{t: t, firstSpeechFails: true}
	up := newFakeUpstream(t, f)
	srv := newTTSBridge(t, ttsSettings(up.URL+"/v1", true))

	resp := postTTS(t, srv, "hi")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	if msg := ttsErrorBody(t, resp.Body); !strings.Contains(msg, "engine boom") {
		t.Errorf("error = %q, want the upstream failure text", msg)
	}
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "audio") {
		t.Errorf("content type = %q, want no audio headers on a pre-PCM failure", ct)
	}
}

// Error AFTER the first PCM chunk: headers were already committed (200,
// audio content type) and the stream simply ends short — the deferred-header
// contract's other half. The connection must close cleanly, not hang.
func TestTTSHTTP_SynthesisErrorAfterPCM(t *testing.T) {
	f := &ttsFake{t: t, firstSpeechPartialThenError: true}
	up := newFakeUpstream(t, f)
	srv := newTTSBridge(t, ttsSettings(up.URL+"/v1", true))

	resp := postTTS(t, srv, "hi")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (committed at the first chunk)", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "audio/L16;rate=24000;channels=1" {
		t.Errorf("content type = %q, want audio/L16;rate=24000;channels=1", ct)
	}
	pcm, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read truncated body: %v (must close cleanly, not hang)", err)
	}
	if !bytes.Equal(pcm, voiceFakePCM[0]) {
		t.Errorf("body = %v, want exactly the first chunk (truncated stream)", pcm)
	}
}

// Zero-PCM synthesis is a valid empty audio response: 200 + audio headers +
// empty body (uniform response contract).
func TestTTSHTTP_ZeroPCM(t *testing.T) {
	f := &ttsFake{t: t, firstSpeechZeroPCM: true}
	up := newFakeUpstream(t, f)
	srv := newTTSBridge(t, ttsSettings(up.URL+"/v1", true))

	resp := postTTS(t, srv, "hi")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "audio/L16;rate=24000;channels=1" {
		t.Errorf("content type = %q, want audio/L16;rate=24000;channels=1", ct)
	}
	pcm, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(pcm) != 0 {
		t.Errorf("body = %v (%d bytes), want 0", pcm, len(pcm))
	}
}

// Client abort must propagate all the way to the engine request: cancel the
// POST's context while the upstream parks — the fake observes its request
// ctx die and the client never sees headers (no PCM arrived).
func TestTTSHTTP_ClientCancel(t *testing.T) {
	f := &ttsFake{t: t, holdFirstSpeech: true, firstCanceled: make(chan struct{})}
	up := newFakeUpstream(t, f)
	srv := newTTSBridge(t, ttsSettings(up.URL+"/v1", true))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/tts", strings.NewReader("hold"))
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		resp *http.Response
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		ch <- result{resp: resp, err: err}
	}()
	time.Sleep(100 * time.Millisecond) // REAL-TIME: let the held request start
	cancel()

	select {
	case <-f.firstCanceled:
	case <-time.After(2 * time.Second): // REAL-TIME
		t.Fatal("client cancel never reached the engine request")
	}
	select {
	case got := <-ch:
		if got.err == nil {
			t.Fatal("client Do succeeded — headers must not arrive before any PCM")
		}
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("Do error = %v, want context.Canceled", got.err)
		}
	case <-time.After(2 * time.Second): // REAL-TIME
		t.Fatal("client Do never returned after the cancel")
	}
	if got := f.countSpeech(); got != 1 {
		t.Errorf("speech calls = %d, want exactly 1", got)
	}
}

// Streaming, not buffering: with chunk 2 withheld upstream, chunk 1 must
// already be readable client-side — the handler flushes per chunk instead of
// letting the response buffer drain at handler return.
func TestTTSHTTP_ChunksStream(t *testing.T) {
	f := &ttsFake{t: t, stallAfterFirstDelta: true, releaseFirst: make(chan struct{})}
	up := newFakeUpstream(t, f)
	srv := newTTSBridge(t, ttsSettings(up.URL+"/v1", true))

	released := false
	release := func() {
		if !released {
			released = true
			close(f.releaseFirst)
		}
	}
	t.Cleanup(release)

	firstCh := make(chan []byte, 1)
	restCh := make(chan []byte, 1)
	go func() {
		resp, err := http.Post(srv.URL+"/api/tts", "text/plain", strings.NewReader("stream"))
		if err != nil {
			firstCh <- nil
			return
		}
		defer resp.Body.Close()
		first := make([]byte, 4)
		if _, err := io.ReadFull(resp.Body, first); err != nil {
			firstCh <- nil
			return
		}
		firstCh <- first
		rest, err := io.ReadAll(resp.Body)
		if err != nil {
			restCh <- nil
			return
		}
		restCh <- rest
	}()
	select {
	case first := <-firstCh:
		if first == nil {
			t.Fatal("the streaming POST failed before its first chunk")
		}
		if !bytes.Equal(first, voiceFakePCM[0]) {
			t.Errorf("first chunk = %v, want %v", first, voiceFakePCM[0])
		}
	case <-time.After(2 * time.Second): // REAL-TIME
		t.Fatal("first chunk never arrived — chunks must flush as they are synthesized, not at response end")
	}
	release()
	select {
	case rest := <-restCh:
		if rest == nil {
			t.Fatal("reading the released stream failed")
		}
		if !bytes.Equal(rest, voiceFakePCM[1]) {
			t.Errorf("remaining bytes = %v, want %v", rest, voiceFakePCM[1])
		}
	case <-time.After(2 * time.Second): // REAL-TIME
		t.Fatal("the released stream never finished")
	}
}

// noFlushWriter hides the Flusher interface and counts WriteHeader calls: a
// superfluous second WriteHeader is a silent no-op on the wire, so only a
// counting writer can pin the one-header contract.
type noFlushWriter struct {
	header      http.Header
	status      int
	headerCalls int
	body        bytes.Buffer
}

func (w *noFlushWriter) Header() http.Header { return w.header }
func (w *noFlushWriter) WriteHeader(code int) {
	w.status = code
	w.headerCalls++
}
func (w *noFlushWriter) Write(p []byte) (int, error) { return w.body.Write(p) }

func TestTTSHTTP_NoFlusher(t *testing.T) {
	f := &ttsFake{t: t}
	up := newFakeUpstream(t, f)
	newTTSBridge(t, ttsSettings(up.URL+"/v1", true))

	w := &noFlushWriter{header: http.Header{}}
	handleTTS(w, httptest.NewRequest(http.MethodPost, "/api/tts", strings.NewReader("hi")))
	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.status)
	}
	if w.headerCalls != 1 {
		t.Errorf("WriteHeader calls = %d, want exactly 1 (the zero-PCM fallback must not re-commit on a PCM response)", w.headerCalls)
	}
	if ct := w.header.Get("Content-Type"); ct != "audio/L16;rate=24000;channels=1" {
		t.Errorf("content type = %q, want audio/L16;rate=24000;channels=1", ct)
	}
	if !bytes.Equal(w.body.Bytes(), []byte("\x01\x02\x03\x04\x05\x06\x07\x08")) {
		t.Errorf("body = %v, want the full PCM (flush-less writes lose nothing)", w.body.Bytes())
	}
}

// The zero-PCM branch must WRITE its 200 explicitly: through a real server
// an unwritten status is masked by the implicit 200 on handler return, so
// this runs the handler directly against the counting writer.
func TestTTSHTTP_ZeroPCMWritesStatus(t *testing.T) {
	f := &ttsFake{t: t, firstSpeechZeroPCM: true}
	up := newFakeUpstream(t, f)
	newTTSBridge(t, ttsSettings(up.URL+"/v1", true))

	w := &noFlushWriter{header: http.Header{}}
	handleTTS(w, httptest.NewRequest(http.MethodPost, "/api/tts", strings.NewReader("hi")))
	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want the explicitly written 200", w.status)
	}
	if w.headerCalls != 1 {
		t.Errorf("WriteHeader calls = %d, want exactly 1", w.headerCalls)
	}
	if w.body.Len() != 0 {
		t.Errorf("body = %v, want empty", w.body.Bytes())
	}
	if ct := w.header.Get("Content-Type"); ct != "audio/L16;rate=24000;channels=1" {
		t.Errorf("content type = %q, want audio/L16;rate=24000;channels=1", ct)
	}
}

// The 405 below is what the BARE test mux returns; in production the
// GET / SPA catch-all (pkg/wui/assets.go) would answer GET /api/tts with
// the index HTML (200, harmless). This test pins route registration, NOT
// production routing, and that production behavior is not a bug to "fix".
func TestTTSHTTP_MethodGuard(t *testing.T) {
	srv := newTTSBridge(t, ttsSettings("http://127.0.0.1:1/v1", true))

	resp, err := http.Get(srv.URL + "/api/tts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

// buildConfig must carry a tts flag mirroring stt: present and true when the
// startup-cached default resolves on an audio provider, omitted otherwise.
func TestBuildConfig_TtsFlag(t *testing.T) {
	audio := &config.Provider{Name: "audiocpp", URL: "http://127.0.0.1:8082/v1", Type: config.ProviderTypeAudio}
	audio.Models.Set("asr-stream", config.ModelConfig{})
	audio.Models.Set("qwen3-tts-1.7b", config.ModelConfig{})
	c := newTestConnectorWithConfig(t, hub.NewHub(), buildTestProviders(), map[string]*config.Provider{
		"zhipu":    buildTestProviderConfigs()["zhipu"],
		"audiocpp": audio,
	})
	c.mock().providerFn = func() llm.Provider { return &stubLLMProvider{name: "zhipu"} }
	c.mock().modelFn = func() string { return "glm-5.2" }

	c.SetTTSDefault("audiocpp/qwen3-tts-1.7b")
	payload := c.buildConfig(c.activeSlotTest(t))
	if !strings.Contains(string(payload), `"tts":true`) {
		t.Errorf("config frame with TTS default = %s, want tts:true", payload)
	}

	c.SetTTSDefault("")
	payload = c.buildConfig(c.activeSlotTest(t))
	if strings.Contains(string(payload), `"tts"`) {
		t.Errorf("config frame without TTS default = %s, want the tts field omitted", payload)
	}

	// A default pointing at a model the audio provider lacks must not flip
	// the flag on.
	c.SetTTSDefault("audiocpp/no-such-tts")
	payload = c.buildConfig(c.activeSlotTest(t))
	if strings.Contains(string(payload), `"tts":true`) {
		t.Errorf("config frame with unresolvable TTS default = %s, want tts omitted", payload)
	}
}

// --- TTS settings endpoints (voice-ui P4.1; no voice field — 10-28 cut) ---

func TestTTSDefaultPut(t *testing.T) {
	srv := newSettingsServer(t)
	seedSettings(t, sttSettingsFixture)

	resp, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/tts", strings.NewReader(`{"provider":"audiocpp","model":"stt"}`))
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
		TTS map[string]string `json:"tts"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.TTS["default"] != "audiocpp/stt" {
		t.Errorf("tts.default = %q, want audiocpp/stt", doc.TTS["default"])
	}
	if doc.STT["default"] != "audiocpp/stt-stream" {
		t.Errorf("stt.default = %q, want preserved audiocpp/stt-stream", doc.STT["default"])
	}

	// Response body of the success path is {"ok":true}.
	var okBody map[string]bool
	if err := json.NewDecoder(httpResp.Body).Decode(&okBody); err != nil || !okBody["ok"] {
		t.Errorf("success body = %v (err %v), want {ok:true}", okBody, err)
	}

	// Non-audio provider → 400.
	r2, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/tts", strings.NewReader(`{"provider":"zhipu","model":"glm-5.3"}`))
	h2, err := http.DefaultClient.Do(r2)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Body.Close()
	if h2.StatusCode != http.StatusBadRequest {
		t.Errorf("non-audio status = %d, want 400", h2.StatusCode)
	}

	// Unknown model → 400.
	r3, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/tts", strings.NewReader(`{"provider":"audiocpp","model":"nope"}`))
	h3, err := http.DefaultClient.Do(r3)
	if err != nil {
		t.Fatal(err)
	}
	defer h3.Body.Close()
	if h3.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown model status = %d, want 400", h3.StatusCode)
	}
}

func TestGetProviders_TtsPayload(t *testing.T) {
	srv := newSettingsServer(t)
	fixture := sttSettingsFixture[:len(sttSettingsFixture)-1] + `,"tts":{"default":"audiocpp/stt"}}`
	seedSettings(t, fixture)

	resp, err := http.NewRequest(http.MethodGet, srv.URL+"/api/settings/providers", nil)
	if err != nil {
		t.Fatal(err)
	}
	httpResp, err := http.DefaultClient.Do(resp)
	if err != nil {
		t.Fatal(err)
	}
	defer httpResp.Body.Close()
	var got struct {
		Tts *struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"tts"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Tts == nil {
		t.Fatal("payload.tts missing, want the resolved tts default")
	}
	if got.Tts.Provider != "audiocpp" || got.Tts.Model != "stt" {
		t.Errorf("tts = %+v, want {audiocpp stt}", got.Tts)
	}
}

// Settings endpoint: malformed body is a 400 (validation surface).
func TestTTSDefaultPut_InvalidBody(t *testing.T) {
	srv := newSettingsServer(t)
	seedSettings(t, sttSettingsFixture)
	resp, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/tts", strings.NewReader(`{bad`))
	httpResp, err := http.DefaultClient.Do(resp)
	if err != nil {
		t.Fatal(err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid body status = %d, want 400", httpResp.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&body); err != nil || !strings.Contains(body.Error, "invalid body") {
		t.Errorf("invalid body error = %q (err %v), want the invalid-body wording", body.Error, err)
	}
}

func TestTTSVoices_ProxiesEngineCatalog(t *testing.T) {
	f := &ttsFake{t: t}
	up := newFakeUpstream(t, f)
	srv := newTTSBridge(t, ttsSettings(up.URL+"/v1", true))
	resp, err := http.Get(srv.URL + "/api/tts/voices")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var payload struct {
		Voices []string `json:"voices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Voices) != 3 || payload.Voices[1] != "Serena" {
		t.Errorf("voices = %v, want [Aiden Serena Ryan]", payload.Voices)
	}
	f.mu.Lock()
	q, auth := f.voicesQuery, f.voicesAuth
	f.mu.Unlock()
	if q != "model=qwen3-tts-1.7b" {
		t.Errorf("upstream query = %q, want model=qwen3-tts-1.7b", q)
	}
	if auth != "Bearer k" {
		t.Errorf("upstream authorization = %q, want Bearer k", auth)
	}
}

func TestTTSVoices_ColdStartIsEmpty(t *testing.T) {
	srv := newTTSBridge(t, strings.Replace(ttsSettings("http://127.0.0.1:1/v1", true), `"tts":{"default":"audiocpp/qwen3-tts-1.7b"}`, `"tts":{}`, 1))
	resp, err := http.Get(srv.URL + "/api/tts/voices")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (unconfigured is a normal state)", resp.StatusCode)
	}
	var payload struct {
		Voices []string `json:"voices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Voices) != 0 {
		t.Errorf("voices = %v, want empty", payload.Voices)
	}
}

func TestTTSVoices_UpstreamErrorIsBadGateway(t *testing.T) {
	f := &ttsFake{t: t, voicesFail: true}
	up := newFakeUpstream(t, f)
	srv := newTTSBridge(t, ttsSettings(up.URL+"/v1", true))
	resp, err := http.Get(srv.URL + "/api/tts/voices")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}

func TestTTSDefaultPutVoiceRoundTrip(t *testing.T) {
	srv := newSettingsServer(t)
	seedSettings(t, sttSettingsFixture)
	put, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/tts",
		strings.NewReader(`{"provider":"audiocpp","model":"stt","voice":"Serena"}`))
	httpResp, err := http.DefaultClient.Do(put)
	if err != nil {
		t.Fatal(err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", httpResp.StatusCode)
	}
	raw, _ := os.ReadFile(filepath.Join(homeDir(t), ".gbot", "settings.json"))
	var doc struct {
		TTS map[string]string `json:"tts"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.TTS["voice"] != "Serena" {
		t.Errorf("tts.voice = %q, want Serena", doc.TTS["voice"])
	}
	// GET /api/settings/providers echoes the configured voice back for the
	// picker's current value.
	get, err := http.Get(srv.URL + "/api/settings/providers")
	if err != nil {
		t.Fatal(err)
	}
	defer get.Body.Close()
	var payload struct {
		Tts *struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
			Voice    string `json:"voice"`
		} `json:"tts"`
	}
	if err := json.NewDecoder(get.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Tts == nil || payload.Tts.Voice != "Serena" {
		t.Errorf("providers payload tts = %+v, want voice Serena", payload.Tts)
	}
}
