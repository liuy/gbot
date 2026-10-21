package wui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// The chat WS must negotiate permessage-deflate: chat frames are JSON
// (8-15x compressible) and remote clients can sit on ~1Mbps tailscale links
// where an uncompressed ~1MB takeover metadata frame costs ~16 seconds.
func TestChatWS_NegotiatesPermessageDeflate(t *testing.T) {
	c := newTestConnector(t)
	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dialer := &websocket.Dialer{EnableCompression: true}
	_, resp, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat", nil)
	if err != nil {
		t.Fatalf("dial with compression: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	ext := resp.Header.Get("Sec-Websocket-Extensions")
	if !strings.Contains(ext, "permessage-deflate") {
		t.Fatalf("Sec-Websocket-Extensions = %q, want it to contain permessage-deflate (server must offer compression)", ext)
	}
}

// A compressing client must still receive intact frames: compression is
// transport-level and must not corrupt payloads.
func TestChatWS_CompressedRoundTripIntact(t *testing.T) {
	c := newTestConnector(t)
	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dialer := &websocket.Dialer{EnableCompression: true}
	ws, resp, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat", nil)
	if err != nil {
		t.Fatalf("dial with compression: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	t.Cleanup(func() { _ = resp.Body.Close() })

	// The dial's takeover handshake pushes a metadata frame that races the
	// probe below; a frame sent before the handshake completes is dropped by
	// the era swap. Drain the metadata first so the probe lands on the live
	// connection deterministically.
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second)) // REAL-TIME — handshake timing, not logic
	if _, _, err := ws.ReadMessage(); err != nil {
		t.Fatalf("read metadata frame: %v", err)
	}

	// Drive one write from the server side and read it back: the connector
	// sends an error frame when the engine is unknown, which exercises the
	// compressed write path without needing a live engine.
	c.sendWS(buildError(errors.New("compression round-trip probe")))
	for {
		_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second)) // REAL-TIME — handshake timing, not logic
		_, raw, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("read compressed frame: %v", err)
		}
		if strings.Contains(string(raw), "compression round-trip probe") {
			break
		}
	}
}
