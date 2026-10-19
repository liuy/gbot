package plugins

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/liuy/gbot/pkg/transport"
)

type testSink struct {
	mu     sync.Mutex
	frames []string
	jsons  []string
}

func (s *testSink) SendBinary(frame []byte) {
	s.mu.Lock()
	s.frames = append(s.frames, string(frame))
	s.mu.Unlock()
}

func (s *testSink) SendJSON(payload []byte) {
	s.mu.Lock()
	s.jsons = append(s.jsons, string(payload))
	s.mu.Unlock()
}

func (s *testSink) waitFrames(n int, d time.Duration) []string {
	deadline := time.Now().Add(d)     // REAL-TIME: cross-goroutine goja/hub delivery
	for time.Now().Before(deadline) { // REAL-TIME
		s.mu.Lock()
		got := append([]string(nil), s.frames...)
		s.mu.Unlock()
		if len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond) // REAL-TIME
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.frames...)
}

func writeMod(t *testing.T, body string) ProtocolModule {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "protocol.js")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return ProtocolModule{Plugin: "plug", RootPath: dir, Path: path}
}

type hostEnv struct {
	runner *ProtocolRunner
	hub    *transport.Hub
	sink   *testSink
	id     uint8
}

// loadWith runs the module against a fresh hub and opens one client channel.
func loadWith(t *testing.T, body string) *hostEnv {
	t.Helper()
	mod := writeMod(t, body)
	hub := transport.New()
	runner, err := LoadProtocol(mod, hub)
	if err != nil {
		hub.Close()
		t.Fatal(err)
	}
	sink := &testSink{}
	id, err := hub.OpenChannel(sink, "plug")
	if err != nil {
		runner.Close()
		hub.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		runner.Close()
		hub.Close()
	})
	return &hostEnv{runner: runner, hub: hub, sink: sink, id: id}
}

func (e *hostEnv) send(payload string) {
	e.hub.ChannelSend(e.id, []byte(payload))
}

func (e *hostEnv) want(t *testing.T, want string) {
	t.Helper()
	frames := e.sink.waitFrames(1, 3*time.Second)
	if len(frames) != 1 {
		t.Fatalf("frames = %q, want exactly 1", frames)
	}
	f := frames[0]
	if len(f) < 1 || f[0] != e.id {
		t.Fatalf("frame %q must carry channel id %d", f, e.id)
	}
	if got := f[1:]; got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}

func TestHostChannelRoundTrip(t *testing.T) {
	e := loadWith(t, `
var ch = host.channel()
ch.onMessage(function(m) { ch.send('back:' + m) })
`)
	e.send("ping")
	e.want(t, "back:ping")
}

func TestHostFetchResolvesStatusAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/info" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"ws":"ws://x"}`))
	}))
	defer srv.Close()
	e := loadWith(t, `
var ch = host.channel('plug')
ch.onMessage(function(m) {
  host.fetch(m).then(function(r) { ch.send(r.status + '|' + r.body) },
                     function(e) { ch.send('reject:' + e) })
})
`)
	e.send(srv.URL + "/info")
	e.want(t, `200|{"ws":"ws://x"}`)
}

func TestHostFetchNon200Resolves(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()
	e := loadWith(t, `
var ch = host.channel('plug')
ch.onMessage(function(m) {
  host.fetch(m).then(function(r) { ch.send(r.status + '|' + r.body) },
                     function(e) { ch.send('reject:' + e) })
})
`)
	e.send(srv.URL + "/missing")
	e.want(t, "404|nope")
}

func TestHostFetchRejectsOnRefused(t *testing.T) {
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + dead.Addr().String()
	_ = dead.Close()
	e := loadWith(t, `
var ch = host.channel('plug')
ch.onMessage(function(m) {
  host.fetch(m).then(function(r) { ch.send(r.status) },
                     function(e) { ch.send('rejected') })
})
`)
	e.send(url)
	e.want(t, "rejected")
}

func TestHostDialWSPump(t *testing.T) {
	up := websocket.Upgrader{}
	wsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			_ = c.WriteMessage(mt, []byte("ws:"+string(data)))
		}
	}))
	defer wsSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(wsSrv.URL, "http")

	e := loadWith(t, `
var ch = host.channel('plug')
var conn = null
ch.onMessage(function(m) {
  if (conn) { conn.send(m); return }
  host.dial('`+wsURL+`').then(function(c) {
    conn = c
    c.onMessage(function(f) { ch.send(f) })
    c.send(m)
  }, function(e) { ch.send('dial-fail') })
})
`)
	e.send("hi")
	e.want(t, "ws:hi")
}

func TestHostDialTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		line, _ := bufio.NewReader(c).ReadString('\n')
		_, _ = c.Write([]byte("tcp:" + strings.TrimSuffix(line, "\n") + "\n"))
	}()
	e := loadWith(t, `
var ch = host.channel('plug')
var conn = null
ch.onMessage(function(m) {
  if (conn) { conn.send(m); return }
  host.dial('tcp://`+ln.Addr().String()+`').then(function(c) {
    conn = c
    c.onMessage(function(f) { ch.send(f) })
    c.send(m)
  }, function(e) { ch.send('dial-fail') })
})
`)
	e.send("hi")
	e.want(t, "tcp:hi")
}

func TestHostDialRejectsBadScheme(t *testing.T) {
	e := loadWith(t, `
var ch = host.channel('plug')
ch.onMessage(function(m) {
  host.dial(m).then(function(c) { ch.send('ok') }, function(e) { ch.send('bad-scheme') })
})
`)
	e.send("ftp://127.0.0.1:1")
	e.want(t, "bad-scheme")
}

func TestHostDialRejectsRefused(t *testing.T) {
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := dead.Addr().String()
	_ = dead.Close()
	e := loadWith(t, `
var ch = host.channel('plug')
ch.onMessage(function(m) {
  host.dial(m).then(function(c) { ch.send('ok') }, function(e) { ch.send('refused') })
})
`)
	e.send("tcp://" + addr)
	e.want(t, "refused")
}

func TestHostConnCloseCallback(t *testing.T) {
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = c.Close() // drop immediately after handshake
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	e := loadWith(t, `
var ch = host.channel('plug')
ch.onMessage(function(m) {
  host.dial('`+wsURL+`').then(function(c) {
    c.onClose(function() { ch.send('closed') })
  }, function(e) { ch.send('dial-fail') })
})
`)
	e.send("go")
	e.want(t, "closed")
}

func TestHostOnCloseAfterConnCloseReplays(t *testing.T) {
	// onClose registered on an already-closed conn must fire immediately
	// (the replay branch), not silently drop the event.
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	e := loadWith(t, `
var ch = host.channel('plug')
ch.onMessage(function(m) {
  host.dial('`+wsURL+`').then(function(c) {
    c.close()
    c.onClose(function() { ch.send('replayed') })
  })
})
`)
	e.send("go")
	e.want(t, "replayed")
}

func TestHostDialWSUpgradeFailureCarriesStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such endpoint", http.StatusNotFound)
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	e := loadWith(t, `
var ch = host.channel('plug')
ch.onMessage(function(m) {
  host.dial(m).then(function(c) { ch.send('ok') },
                    function(e) { ch.send(String(e)) })
})
`)
	e.send(wsURL)
	frames := e.sink.waitFrames(1, 3*time.Second)
	if len(frames) != 1 {
		t.Fatalf("frames = %q, want exactly 1", frames)
	}
	if msg := frames[0][1:]; !strings.Contains(msg, "status 404") {
		t.Fatalf("upgrade-failure reject %q must carry the HTTP status", msg)
	}
}

func TestHostConnSendAfterCloseThrows(t *testing.T) {
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	e := loadWith(t, `
var ch = host.channel('plug')
ch.onMessage(function(m) {
  host.dial('`+wsURL+`').then(function(c) {
    c.close()
    try { c.send('x'); ch.send('no-throw') }
    catch (err) { ch.send('threw:' + (err instanceof TypeError)) }
  })
})
`)
	e.send("go")
	e.want(t, "threw:true")
}

func TestProtocolModuleSyntaxErrorFailsLoad(t *testing.T) {
	mod := writeMod(t, `var ch = host.channel('plug'`) // unclosed paren
	hub := transport.New()
	defer hub.Close()
	if _, err := LoadProtocol(mod, hub); err == nil {
		t.Fatal("syntax error must fail the load")
	} else if !strings.Contains(err.Error(), "protocol plug") {
		t.Fatalf("error = %v, want it to name the plugin", err)
	}
	if hub.Connected("plug") {
		t.Fatal("failed load must not leave a provider registered")
	}
}

func TestProtocolCloseUnwiresHub(t *testing.T) {
	e := loadWith(t, `var ch = host.channel('plug')`)
	if !e.hub.Connected("plug") {
		t.Fatal("provider must be connected after load")
	}
	e.runner.Close()
	if e.hub.Connected("plug") {
		t.Fatal("Close must unregister the provider")
	}
}

func TestProtocolMissingFileFailsLoad(t *testing.T) {
	dir := t.TempDir()
	mod := ProtocolModule{Plugin: "ghost", RootPath: dir, Path: filepath.Join(dir, "nope.js")}
	hub := transport.New()
	defer hub.Close()
	_, err := LoadProtocol(mod, hub)
	if err == nil {
		t.Fatal("missing module file must fail the load")
	}
	if !strings.Contains(err.Error(), "protocol ghost:") {
		t.Fatalf("error %q must name the plugin with the protocol prefix", err)
	}
}

type captureHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (c *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (c *captureHandler) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.msgs = append(c.msgs, r.Message)
	c.mu.Unlock()
	return nil
}
func (c *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *captureHandler) WithGroup(string) slog.Handler      { return c }

func TestHostLogJoinsArgs(t *testing.T) {
	capH := &captureHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(capH))
	defer slog.SetDefault(old)
	e := loadWith(t, `
var ch = host.channel('plug')
host.log('hello', 42, true)
ch.onMessage(function(m) { ch.send(m) })
`)
	e.send("x")
	e.want(t, "x")
	capH.mu.Lock()
	defer capH.mu.Unlock()
	var found bool
	for _, m := range capH.msgs {
		if m == "protocol: hello 42 true" {
			found = true
		}
	}
	if !found {
		t.Fatalf("log output not joined as expected: %q", capH.msgs)
	}
}

func TestHostFetchTruncatesLargeBody(t *testing.T) {
	big := strings.Repeat("a", (1<<20)+100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, big)
	}))
	defer srv.Close()
	e := loadWith(t, `
var ch = host.channel('plug')
ch.onMessage(function(m) {
  host.fetch(m).then(function(r) { ch.send(String(r.body.length)) })
})
`)
	e.send(srv.URL + "/big")
	e.want(t, "1048576")
}

func TestHostChannelNameDefaultAndExplicit(t *testing.T) {
	e := loadWith(t, `
var a = host.channel()
var b = host.channel('custom')
a.onMessage(function(m) { a.send(a.name + '|' + b.name) })
`)
	e.send("names")
	e.want(t, "plug|custom")
}

func TestHostLogRuns(t *testing.T) {
	// log() must not throw inside the loop; the echo proves the module kept
	// running after the log call.
	e := loadWith(t, `
var ch = host.channel('plug')
host.log('hello', 42)
ch.onMessage(function(m) { ch.send(m) })
`)
	e.send("still-alive")
	e.want(t, "still-alive")
}

func TestProtocolCloseDropsDialedConns(t *testing.T) {
	up := websocket.Upgrader{}
	accepted := make(chan struct{})
	closed := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		close(accepted)
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				once.Do(func() { close(closed) })
				return
			}
		}
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	e := loadWith(t, `
var ch = host.channel('plug')
ch.onMessage(function(m) {
  host.dial('`+wsURL+`').then(function(c) { c.send('hi'); ch.send('dialed') })
})
`)
	e.send("go")
	e.want(t, "dialed")
	<-accepted
	e.runner.Close()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("runner.Close must close dialed connections")
	}
}

func TestHubCloseDropsDialedConns(t *testing.T) {
	up := websocket.Upgrader{}
	accepted := make(chan struct{})
	closed := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		close(accepted)
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				once.Do(func() { close(closed) })
				return
			}
		}
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	e := loadWith(t, `
var ch = host.channel('plug')
ch.onMessage(function(m) {
  host.dial('`+wsURL+`').then(function(c) { c.send('hi'); ch.send('dialed') })
})
`)
	e.send("go")
	e.want(t, "dialed")
	<-accepted
	e.hub.Close() // onGone path: stop loop + close conns
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("hub close must drop dialed conns (onGone)")
	}
}

func TestHostWSBinaryFrameCloses(t *testing.T) {
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = c.WriteMessage(websocket.BinaryMessage, []byte{1, 2, 3})
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	e := loadWith(t, `
var ch = host.channel('plug')
ch.onMessage(function(m) {
  host.dial('`+wsURL+`').then(function(c) {
    c.onMessage(function(f) { ch.send('text:' + f) })
    c.onClose(function() { ch.send('binary-killed') })
  })
})
`)
	e.send("go")
	e.want(t, "binary-killed")
}

func TestHostTCPSplitLines(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = c.Write([]byte("ab"))      // split across writes
		time.Sleep(50 * time.Millisecond) // REAL-TIME: force two Read hits mid-line
		_, _ = c.Write([]byte("c\nd\n"))
	}()
	e := loadWith(t, `
var ch = host.channel('plug')
ch.onMessage(function(m) {
  host.dial('tcp://`+ln.Addr().String()+`').then(function(c) {
    c.onMessage(function(f) { ch.send('line:' + f) })
  })
})
`)
	e.send("go")
	frames := e.sink.waitFrames(2, 3*time.Second)
	if len(frames) != 2 {
		t.Fatalf("frames = %q, want [abc, d]", frames)
	}
	if frames[0][1:] != "line:abc" || frames[1][1:] != "line:d" {
		t.Fatalf("split-line reassembly wrong: %q", frames)
	}
}
