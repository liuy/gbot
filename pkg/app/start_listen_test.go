package app

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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

// pinSharedCaches redirects the build/cache/config dirs that Start's async
// LSP discovery (gopls + go-list children) writes into for seconds after a
// test ends — racing TempDir RemoveAll on the isolated HOME ("unlinkat
// .../.cache/go-build: directory not empty"). These dirs are content-addressed
// and meant to be shared, so pin them to the real user cache; the config dir
// (gopls telemetry queue) goes to a scratch dir under it — stable, never
// RemoveAll'd, and the user's real ~/.config stays untouched.
func pinSharedCaches(t *testing.T) {
	t.Helper()
	if cache, err := os.UserCacheDir(); err == nil {
		t.Setenv("GOCACHE", filepath.Join(cache, "go-build"))
		t.Setenv("XDG_CACHE_HOME", cache)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(cache, "gbot-test-config"))
	}
}

// minimalHome isolates the daemon state (PID guard, memory store, config)
// from the real HOME and provides one offline provider so config resolution
// succeeds without any network access.
func minimalHome(t *testing.T) string {
	t.Helper()
	pinSharedCaches(t)
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
		_, err := Start(Options{DaemonMode: true, NoTUI: true})
		startErr <- err
	}()

	base := "http://127.0.0.1:" + port
	timeout := time.After(5 * time.Second)
	for {
		resp, err := http.Get(base + "/")
		if err == nil {
			if cerr := resp.Body.Close(); cerr != nil {
				t.Logf("close probe body: %v", cerr)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("first response after port open: status %d, want 200 (listener opened before routes mounted?)", resp.StatusCode)
			}
			break
		}
		select {
		case <-timeout:
			t.Fatalf("port never accepted connections: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
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

	if _, err := Start(Options{DaemonMode: true, NoTUI: true}); err == nil {
		t.Fatal("expected Start to fail on occupied port")
	} else if !strings.Contains(err.Error(), "ws server") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// sttHome is a minimal home whose settings carry an audio provider and
// stt.default — the config frame must then advertise stt:true and the
// dictation endpoint must be mounted.
func sttHome(t *testing.T) string {
	t.Helper()
	pinSharedCaches(t)
	home := t.TempDir()
	settings := `{"providers":[
		{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}},
		{"name":"audiocpp","type":"audio","url":"http://127.0.0.1:1","models":{"stt-stream":{}}}],
		"model":{"default":"t/m"},"stt":{"default":"audiocpp/stt-stream"}}`
	if err := os.MkdirAll(filepath.Join(home, ".gbot"), 0o755); err != nil {
		t.Fatalf("mkdir .gbot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gbot", "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	return home
}

// The wui boot wires the dictation surface: /ws/stt upgrades, and the chat
// metadata's config advertises stt:true only when SetSTTDefault ran.
func TestStart_MountsSttSurface(t *testing.T) {
	t.Setenv("HOME", sttHome(t))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	t.Setenv("GBOT_WS_ADDR", "")
	port := freeTCPPort(t)
	startInTempProject(t)
	inst, err := Start(Options{WSPort: port})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdownInstance(t, inst)
	waitForHTTP(t, "http://127.0.0.1:"+port, 5*time.Second)

	ws, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:"+port+"/ws/stt", nil)
	if err != nil {
		t.Fatalf("dial /ws/stt: %v (route must be mounted)", err)
	}
	defer ws.Close()

	// The chat WS metadata carries config.stt — the mic visibility flag.
	chat, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:"+port+"/ws/chat", nil)
	if err != nil {
		t.Fatalf("dial /ws/chat: %v", err)
	}
	defer chat.Close()
	deadline := time.Now().Add(10 * time.Second) // REAL-TIME
	for {
		_ = chat.SetReadDeadline(deadline)
		_, data, err := chat.ReadMessage()
		if err != nil {
			t.Fatalf("read metadata: %v", err)
		}
		if !strings.Contains(string(data), `"connect"`) {
			continue
		}
		if !strings.Contains(string(data), `"stt":true`) {
			t.Fatalf("metadata config lacks stt:true: %s", data)
		}
		return
	}
}
