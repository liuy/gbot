package app

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The upload route must mount on the daemon's ws mux next to the chat WS. With
// the RegisterUploadRoute call gone the mux answers 404 for POST /api/upload
// and every WebView upload fails with no server-side signal.
func TestStart_UploadRouteMountedOnWSMux(t *testing.T) {
	t.Setenv("HOME", minimalHome(t))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	t.Setenv("GBOT_WS_ADDR", "")
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	startInTempProject(t)
	inst, err := Start(Options{DaemonMode: true, NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdownInstance(t, inst)
	base := "http://127.0.0.1:" + port
	waitForHTTP(t, base, 5*time.Second)

	resp, err := http.Post(base+"/api/upload", "application/octet-stream", strings.NewReader("not multipart"))
	if err != nil {
		t.Fatalf("POST /api/upload: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("POST /api/upload status = %d (%s), want 415 — the route must be mounted on the ws mux", resp.StatusCode, body)
	}
	if want := `{"error":"expected multipart/form-data"}`; string(body) != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}
