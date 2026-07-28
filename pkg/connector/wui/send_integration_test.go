package wui

import (
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
// end: SendFile → sendWS → wsCh → wsWriter → activeWS.WriteMessage → client
// WS read. The connector struct is built by hand (no New(), no engine) so
// the test owns exactly the goroutines under test; wsWriter is started
// manually because the chain terminates at a real WS conn — without it the
// payload would sit in wsCh and never reach the client read end.
func TestWUI_SendFile_WSReceivesEvent(t *testing.T) {
	c := &WUIConnector{
		wsCh: make(chan []byte, 16),
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
	if err := os.WriteFile(tmpFile, []byte("fake-png-bytes"), 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}

	if err := c.SendFile(context.Background(), tmpFile, ""); err != nil {
		t.Fatalf("SendFile: %v", err)
	}

	if err := clientWS.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil { // REAL-TIME
		t.Fatalf("SetReadDeadline: %v", err)
	}
	msgType, data, err := clientWS.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if msgType != websocket.TextMessage {
		t.Fatalf("message type = %v, want TextMessage", msgType)
	}
	var got struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got.Type != "file" {
		t.Errorf("type = %q, want \"file\"", got.Type)
	}
	if got.Name != "test.png" {
		t.Errorf("name = %q, want test.png", got.Name)
	}
}
