package wui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestWUI_SendFile_WSReceivesEvent verifies the full outbound chain end to
// end: SendFile → sendWS/sendBinaryChunk → wsCh → wsWriter →
// activeWS.WriteMessage → client WS read. The connector struct is built by
// hand (no New(), no engine) so the test owns exactly the goroutines under
// test; wsWriter is started manually because the chain terminates at a real
// WS conn — without it the payload would sit in wsCh and never reach the
// client read end.
func TestWUI_SendFile_WSReceivesEvent(t *testing.T) {
	c := &WUIConnector{
		wsCh: make(chan wsMsg, 16),
		done: make(chan struct{}),
	}
	go c.wsWriter()
	t.Cleanup(func() { close(c.done) })

	// Real WS pair via httptest: client dials, the server-side conn is
	// stored in activeWS so wsWriter writes reach the client read end.
	srvWSCh := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		srvWSCh <- ws
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	clientWS, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = clientWS.Close() })
	srvWS := <-srvWSCh
	t.Cleanup(func() { _ = srvWS.Close() })
	c.activeWS.Store(srvWS)

	tmpFile := filepath.Join(t.TempDir(), "test.png")
	const fileBody = "fake-png-bytes"
	if err := os.WriteFile(tmpFile, []byte(fileBody), 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}

	if err := clientWS.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil { // REAL-TIME
		t.Fatalf("SetReadDeadline: %v", err)
	}

	if err := c.SendFile(context.Background(), tmpFile, ""); err != nil {
		t.Fatalf("SendFile: %v", err)
	}

	// Frame 1: file_start text frame.
	msgType, data, err := clientWS.ReadMessage()
	if err != nil {
		t.Fatalf("read file_start: %v", err)
	}
	if msgType != websocket.TextMessage {
		t.Fatalf("frame 1 type = %v, want TextMessage", msgType)
	}
	var start fileStartMsg
	if err := json.Unmarshal(data, &start); err != nil {
		t.Fatalf("unmarshal file_start: %v", err)
	}
	if start.Type != "file_start" {
		t.Errorf("start type = %q, want file_start", start.Type)
	}
	if start.Name != "test.png" {
		t.Errorf("start name = %q, want test.png", start.Name)
	}
	if start.Mime != "image/png" {
		t.Errorf("start mime = %q, want image/png", start.Mime)
	}

	// Frame 2: binary chunk.
	msgType, data, err = clientWS.ReadMessage()
	if err != nil {
		t.Fatalf("read binary chunk: %v", err)
	}
	if msgType != websocket.BinaryMessage {
		t.Fatalf("frame 2 type = %v, want BinaryMessage", msgType)
	}
	if !bytes.Equal(data, []byte(fileBody)) {
		t.Errorf("binary data = %q, want %q", string(data), fileBody)
	}

	// Frame 3: file_end text frame.
	msgType, data, err = clientWS.ReadMessage()
	if err != nil {
		t.Fatalf("read file_end: %v", err)
	}
	if msgType != websocket.TextMessage {
		t.Fatalf("frame 3 type = %v, want TextMessage", msgType)
	}
	var end fileEndMsg
	if err := json.Unmarshal(data, &end); err != nil {
		t.Fatalf("unmarshal file_end: %v", err)
	}
	if end.Type != "file_end" {
		t.Errorf("end type = %q, want file_end", end.Type)
	}
	if end.Name != "test.png" {
		t.Errorf("end name = %q, want test.png", end.Name)
	}
}
