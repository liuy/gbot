package app

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func freeTCPPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	return port
}

// minimalHome isolates the daemon state (PID guard, memory store, config)
// from the real HOME and provides one offline provider so config resolution
// succeeds without any network access.
func minimalHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	settings := `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}],"model":{"default":"t/m"}}`
	if err := os.MkdirAll(filepath.Join(home, ".gbot"), 0o755); err != nil {
		t.Fatalf("mkdir .gbot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gbot", "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	return home
}

func startInTempProject(t *testing.T) {
	t.Helper()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
}

// The WS listener must open only after all routes mount, so the FIRST
// successful HTTP exchange a client observes must serve the SPA. The test
// runs Start in a goroutine and races it: under an early-listen regression
// the listen-before-mount window (~0.3-1s of engine/LSP init) yields 404s
// from the empty mux, which fail the poll immediately.
func TestStart_PortOpenImpliesRoutesReady(t *testing.T) {
	t.Setenv("HOME", minimalHome(t))
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	startInTempProject(t)

	startErr := make(chan error, 1)
	go func() {
		_, err := Start(Options{DaemonMode: true})
		startErr <- err
	}()

	base := "http://127.0.0.1:" + port
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(base + "/")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("first response after port open: status %d, want 200 (listener opened before routes mounted?)", resp.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("port never accepted connections: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case err := <-startErr:
		if err != nil {
			t.Fatalf("Start failed: %v", err)
		}
	default:
	}
}

// A port conflict fails Start before user-visible side effects (WeChat
// connectors, hooks, dream timer) run — pinned by the preflight bind at the
// old early-listen site.
func TestStart_FailFastOnOccupiedPort(t *testing.T) {
	t.Setenv("HOME", minimalHome(t))
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	startInTempProject(t)

	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer ln.Close()

	if _, err := Start(Options{DaemonMode: true}); err == nil {
		t.Fatal("expected Start to fail on occupied port")
	} else if !strings.Contains(err.Error(), "ws server") {
		t.Fatalf("unexpected error: %v", err)
	}
}
