package wui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// metadataConnectEnv is the subset of the connect sub-payload the wuiHash
// assertions read.
type metadataConnectEnv struct {
	WuiHash string `json:"wuiHash"`
}

// serveMetaForTest stands up the standard /ws/chat test server so the
// wuiHash tests can drive real WS connections through serveChatWS.
func serveMetaForTest(t *testing.T, c *WUIConnector) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var handlerWG sync.WaitGroup
	mux.HandleFunc("/ws/chat", func(w http.ResponseWriter, r *http.Request) {
		handlerWG.Add(1)
		defer handlerWG.Done()
		ws, err := chatUpgrader.Upgrade(w, r, nil)
		if err != nil {
			http.Error(w, "upgrade failed", http.StatusInternalServerError)
			return
		}
		serveChatWS(ws, c)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.Close()
		handlerWG.Wait()
	})
	return srv
}

// readConnectWuiHash dials a fresh WS connection, consumes the composite
// metadata frame, and returns the connect sub-payload's wuiHash.
func readConnectWuiHash(t *testing.T, url string) string {
	t.Helper()
	ws := dialChatWS(t, url)
	defer ws.Close()
	meta := readMetadata(t, ws)
	var env metadataConnectEnv
	if err := json.Unmarshal(meta.Connect, &env); err != nil {
		t.Fatalf("unmarshal connect payload: %v", err)
	}
	return env.WuiHash
}

func TestMetadata_ConnectCarriesWuiHash(t *testing.T) {
	c := newTestConnector(t)
	c.mock().messagesFn = func() []types.Message { return nil }

	srv := serveMetaForTest(t, c)
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/chat"

	got := readConnectWuiHash(t, url)
	if got == "" {
		t.Fatal("connect.wuiHash is empty, want sha256 of the embedded index.html")
	}
	sum := sha256.Sum256(indexHTML)
	if want := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("connect.wuiHash = %q, want %q (sha256 of the uncompressed embedded index.html)", got, want)
	}

	// The client compares the hash across reconnects, so every connection
	// of the same binary must observe the identical value.
	second := readConnectWuiHash(t, url)
	if second != got {
		t.Fatalf("second connection wuiHash = %q, want %q (stable across connections)", second, got)
	}
}
