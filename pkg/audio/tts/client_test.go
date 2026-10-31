package tts

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSSE serves one SSE script per request: each element is a full `data:`
// line payload.
func fakeSSE(t *testing.T, script []string) (*httptest.Server, *int) {
	t.Helper()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept = %q, want text/event-stream", r.Header.Get("Accept"))
		}
		if r.URL.Path != "/audio/speech" {
			t.Errorf("path = %q, want /audio/speech", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range script {
			_, _ = w.Write([]byte("data: " + line + "\n\n"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func b64(pcm []byte) string { return base64.StdEncoding.EncodeToString(pcm) }

func TestStream_DeliversPCMInOrder(t *testing.T) {
	chunk1 := []byte{1, 2, 3, 4}
	chunk2 := []byte{5, 6, 7, 8}
	srv, _ := fakeSSE(t, []string{
		`{"type":"speech.audio.delta","audio":"` + b64(chunk1) + `"}`,
		`{"type":"speech.audio.delta","audio":"` + b64(chunk2) + `"}`,
		`{"type":"speech.audio.done","ttft_ms":50}`,
		`[DONE]`,
	})

	c := &Client{}
	var mu sync.Mutex
	var got [][]byte
	err := c.Stream(context.Background(), Request{
		BaseURL: srv.URL,
		Model:   "qwen3-tts-1.7b",
		Text:    "hi",
	}, func(pcm []byte) {
		mu.Lock()
		// Copy: the caller may reuse the buffer.
		cp := make([]byte, len(pcm))
		copy(cp, pcm)
		got = append(got, cp)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("chunks = %d, want 2 (%v)", len(got), got)
	}
	if string(got[0]) != string(chunk1) || string(got[1]) != string(chunk2) {
		t.Errorf("chunks = %v %v, want %v %v", got[0], got[1], chunk1, chunk2)
	}
}

func TestStream_SendsBodyParams(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		body = string(buf[:n])
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := &Client{}
	_ = c.Stream(context.Background(), Request{BaseURL: srv.URL, Model: "m", Text: "hi"}, func([]byte) {})

	for _, want := range []string{
		`"model":"m"`, `"input":"hi"`, `"response_format":"pcm"`,
		`"stream_format":"sse"`, `"seed":123`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("request body %q lacks %q", body, want)
		}
	}
	if strings.Contains(body, `"voice"`) {
		t.Errorf("request body %q must not carry a voice field (server default preset applies)", body)
	}
	// A configured voice rides the wire verbatim: the backend owns the
	// speaker catalog, the client never interprets the name.
	_ = c.Stream(context.Background(), Request{BaseURL: srv.URL, Model: "m", Text: "hi", Voice: "Serena"}, func([]byte) {})
	if !strings.Contains(body, `"voice":"Serena"`) {
		t.Errorf("request body %q lacks the configured voice", body)
	}
}

func TestStream_ErrorEventFails(t *testing.T) {
	srv, _ := fakeSSE(t, []string{
		`{"type":"error","error":{"message":"boom"}}`,
	})
	c := &Client{}
	err := c.Stream(context.Background(), Request{BaseURL: srv.URL, Model: "m", Text: "hi"}, func([]byte) {})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Stream error = %v, want boom", err)
	}
}

func TestStream_HTTPErrorFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"model overloaded"}}`))
	}))
	defer srv.Close()

	c := &Client{}
	err := c.Stream(context.Background(), Request{BaseURL: srv.URL, Model: "m", Text: "hi"}, func([]byte) {})
	if err == nil || !strings.Contains(err.Error(), "model overloaded") {
		t.Fatalf("Stream error = %v, want model overloaded", err)
	}
}

// Stream must honor ctx cancel even when the server never finishes the stream.
func TestStream_ContextCancel(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})

	c := &Client{}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.Stream(ctx, Request{BaseURL: srv.URL, Model: "m", Text: "hi"}, func([]byte) {})
	}()
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Stream with cancelled ctx must return an error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled in chain", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stream did not return after ctx cancel")
	}
}

func TestSynthesize_CollectsAllPCM(t *testing.T) {
	chunk := []byte{9, 9, 9, 9}
	srv, _ := fakeSSE(t, []string{
		`{"type":"speech.audio.delta","audio":"` + b64(chunk) + `"}`,
		`{"type":"speech.audio.delta","audio":"` + b64(chunk) + `"}`,
		`{"type":"speech.audio.done"}`,
		`[DONE]`,
	})

	c := &Client{}
	pcm, err := c.Synthesize(context.Background(), Request{BaseURL: srv.URL, Model: "m", Text: "hi"})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(pcm) != 2*len(chunk) {
		t.Errorf("len(pcm) = %d, want %d", len(pcm), 2*len(chunk))
	}
	for i, b := range pcm {
		if b != 9 {
			t.Errorf("pcm[%d] = %d, want 9", i, b)
		}
	}
}

// SampleRate pins the wire contract: audio.cpp speech PCM is s16le mono 24kHz.
func TestSampleRate(t *testing.T) {
	if SampleRate != 24000 {
		t.Errorf("SampleRate = %d, want 24000", SampleRate)
	}
}

// --- mutate-killers: header/wire contract exactness ---

func TestStream_HeadersAndAuth(t *testing.T) {
	var gotAccept, gotCT, gotAuth string
	var gotAuthSet bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		gotCT = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		gotAuthSet = gotAuth != ""
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := &Client{}
	_ = c.Stream(context.Background(), Request{BaseURL: srv.URL, Model: "m", Text: "hi", APIKey: "sk-x"}, func([]byte) {})
	if gotAccept != "text/event-stream" {
		t.Errorf("Accept = %q, want text/event-stream", gotAccept)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}
	if gotAuth != "Bearer sk-x" {
		t.Errorf("Authorization = %q, want Bearer sk-x", gotAuth)
	}

	_ = c.Stream(context.Background(), Request{BaseURL: srv.URL, Model: "m", Text: "hi"}, func([]byte) {})
	if gotAuthSet {
		t.Error("Authorization sent with empty APIKey, want absent")
	}
}

func TestStream_SeedOverride(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		body = string(buf[:n])
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := &Client{}
	_ = c.Stream(context.Background(), Request{BaseURL: srv.URL, Model: "m", Text: "hi", Seed: 7}, func([]byte) {})
	if !strings.Contains(body, `"seed":7`) {
		t.Errorf("body %q lacks seed:7 (explicit seed must pass through)", body)
	}
}

// [DONE] alone ends the stream successfully (done event is not required on
// the wire); malformed and non-data lines are skipped.
func TestStream_DoneMarkerWithoutDoneEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": keepalive\n\n"))
		_, _ = w.Write([]byte("data: {not json\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"speech.audio.delta\",\"audio\":\"AAE=\"}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := &Client{}
	var pcm []byte
	err := c.Stream(context.Background(), Request{BaseURL: srv.URL, Model: "m", Text: "hi"}, func(p []byte) { pcm = append(pcm, p...) })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if !bytes.Equal(pcm, []byte{0x00, 0x01}) {
		t.Errorf("pcm = %v, want 00 01 (base64 AAE=)", pcm)
	}
}

// After speech.audio.done, trailing garbage on the wire must be ignored.
func TestStream_TrailingGarbageAfterDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"speech.audio.done\"}\n\n"))
		_, _ = w.Write([]byte("data: {bad json\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"error\",\"error\":{\"message\":\"late\"}}\n\n"))
	}))
	defer srv.Close()

	c := &Client{}
	if err := c.Stream(context.Background(), Request{BaseURL: srv.URL, Model: "m", Text: "hi"}, func([]byte) {}); err != nil {
		t.Fatalf("Stream after done with trailing garbage: %v (done is terminal)", err)
	}
}

func TestStream_InvalidBase64Fails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"speech.audio.delta\",\"audio\":\"!!!not-base64!!!\"}\n\n"))
	}))
	defer srv.Close()

	c := &Client{}
	err := c.Stream(context.Background(), Request{BaseURL: srv.URL, Model: "m", Text: "hi"}, func([]byte) {})
	if err == nil || !strings.Contains(err.Error(), "tts: decode audio delta") {
		t.Fatalf("err = %v, want tts: decode audio delta", err)
	}
}

// Error event without a message falls back to the generic wording.
func TestStream_ErrorEventDefaultMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"error\"}\n\n"))
	}))
	defer srv.Close()

	c := &Client{}
	err := c.Stream(context.Background(), Request{BaseURL: srv.URL, Model: "m", Text: "hi"}, func([]byte) {})
	if err == nil || !strings.Contains(err.Error(), "upstream error") {
		t.Fatalf("err = %v, want the default upstream error wording", err)
	}
}

// Non-2xx error extraction: flat message shape, then the bare status line.
func TestStream_HTTPErrorFallbacks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"message":"gateway sad"}`))
	}))
	defer srv.Close()
	c := &Client{}
	err := c.Stream(context.Background(), Request{BaseURL: srv.URL, Model: "m", Text: "hi"}, func([]byte) {})
	if err == nil || !strings.Contains(err.Error(), "gateway sad") {
		t.Fatalf("flat-message error = %v, want gateway sad", err)
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv2.Close()
	err = c.Stream(context.Background(), Request{BaseURL: srv2.URL, Model: "m", Text: "hi"}, func([]byte) {})
	if err == nil || !strings.Contains(err.Error(), "speech HTTP 500") {
		t.Fatalf("status-line error = %v, want speech HTTP 500", err)
	}
}

// Exact error wording (errorf-wrap prefixes are part of the contract).
func TestStream_ErrorWording(t *testing.T) {
	srv, _ := fakeSSE(t, []string{`{"type":"error","error":{"message":"boom"}}`})
	c := &Client{}
	err := c.Stream(context.Background(), Request{BaseURL: srv.URL, Model: "m", Text: "hi"}, func([]byte) {})
	if err == nil || err.Error() != "tts: boom" {
		t.Fatalf("err = %q, want tts: boom", err)
	}
}

func TestVoices_RelaysEngineCatalog(t *testing.T) {
	var gotPath, gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotAuth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"voices":["Aiden","Serena","Ryan"]}`))
	}))
	defer srv.Close()
	c := &Client{}
	voices, err := c.Voices(context.Background(), Request{BaseURL: srv.URL + "/v1", Model: "qwen3-tts-1.7b", APIKey: "k"})
	if err != nil {
		t.Fatalf("Voices: %v", err)
	}
	if len(voices) != 3 || voices[0] != "Aiden" || voices[2] != "Ryan" {
		t.Errorf("voices = %v, want [Aiden Serena Ryan]", voices)
	}
	if gotPath != "/v1/audio/voices" {
		t.Errorf("path = %q, want /v1/audio/voices", gotPath)
	}
	if gotQuery != "model=qwen3-tts-1.7b" {
		t.Errorf("query = %q, want model=qwen3-tts-1.7b", gotQuery)
	}
	if gotAuth != "Bearer k" {
		t.Errorf("authorization = %q, want Bearer k", gotAuth)
	}
}

func TestVoices_HTTPErrorFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()
	c := &Client{}
	if _, err := c.Voices(context.Background(), Request{BaseURL: srv.URL, Model: "m"}); err == nil {
		t.Fatal("Voices must fail on a non-200 upstream")
	}
}
