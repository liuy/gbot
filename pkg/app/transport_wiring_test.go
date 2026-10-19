package app

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/plugins"
	"github.com/liuy/gbot/pkg/transport"
)

// writeProtocolModule lays out one plugin dir with a plugin.json declaring a
// protocol module plus the module file itself.
func writeProtocolModule(t *testing.T, name, body string) plugins.ProtocolModule {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plugin.json"),
		[]byte(`{"name":"`+name+`","protocol":"./protocol.js"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "protocol.js"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return plugins.ProtocolModule{Plugin: name, RootPath: root, Path: filepath.Join(root, "protocol.js")}
}

func TestWireTransportLoadsProtocolModules(t *testing.T) {
	mod := writeProtocolModule(t, "browser", `var ch = host.channel('browser'); ch.onMessage(function(m){ ch.send(m) })`)
	hub, runners := wireTransport(&plugins.LoadedPlugins{Protocols: []plugins.ProtocolModule{mod}})
	t.Cleanup(func() {
		for _, r := range runners {
			r.Close()
		}
		hub.Close()
	})
	if len(runners) != 1 {
		t.Fatalf("runners = %d, want 1", len(runners))
	}
	if !hub.Connected("browser") {
		t.Fatal("loaded module must register its provider")
	}
}

func TestWireTransportBadModuleDegradesPerPlugin(t *testing.T) {
	good := writeProtocolModule(t, "good", `host.log('up')`)
	bad := writeProtocolModule(t, "bad", `throw new Error('boom')`)
	missing := plugins.ProtocolModule{Plugin: "missing", RootPath: t.TempDir(), Path: filepath.Join(t.TempDir(), "nope.js")}
	hub, runners := wireTransport(&plugins.LoadedPlugins{Protocols: []plugins.ProtocolModule{good, bad, missing}})
	t.Cleanup(func() {
		for _, r := range runners {
			r.Close()
		}
		hub.Close()
	})
	if len(runners) != 1 {
		t.Fatalf("runners = %d, want only the good module", len(runners))
	}
	if !hub.Connected("good") || hub.Connected("bad") || hub.Connected("missing") {
		t.Fatal("only the good plugin may be wired")
	}
}

func TestWireTransportNilLoaded(t *testing.T) {
	hub, runners := wireTransport(nil)
	defer hub.Close()
	if len(runners) != 0 {
		t.Fatalf("runners = %d, want 0", len(runners))
	}
	if hub.Connected("anything") {
		t.Fatal("empty hub must report nothing connected")
	}
}

func TestLoadProtocolChannelEcho(t *testing.T) {
	// The module echoes every client frame back with a prefix: pins the
	// full host bridge (channel object, frame callback into the loop,
	// Deliver back into the hub).
	mod := writeProtocolModule(t, "echo", `
var ch = host.channel('echo')
ch.onMessage(function(m) { ch.send('echo:' + m) })
`)
	hub := transport.New()
	defer hub.Close()
	runner, err := plugins.LoadProtocol(mod, hub)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()

	sink := &wiringSink{}
	id, err := hub.OpenChannel(sink, "echo")
	if err != nil {
		t.Fatal(err)
	}
	hub.ChannelSend(id, []byte(`ping`))
	var frames [][]byte
	deadline := time.Now().Add(2 * time.Second) // REAL-TIME: goja loop delivery is cross-goroutine
	for time.Now().Before(deadline) {           // REAL-TIME
		if frames = sink.snapshot(); len(frames) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond) // REAL-TIME
	}
	if len(frames) != 1 {
		t.Fatalf("echo frames = %d, want 1", len(frames))
	}
	f := frames[0]
	if f[0] != id || string(f[1:]) != "echo:ping" {
		t.Fatalf("echo frame wrong: id=%d payload=%q", f[0], f[1:])
	}
}

type wiringSink struct {
	mu     sync.Mutex
	frames [][]byte
}

func (w *wiringSink) SendBinary(frame []byte) {
	cp := make([]byte, len(frame))
	copy(cp, frame)
	w.mu.Lock()
	w.frames = append(w.frames, cp)
	w.mu.Unlock()
}

func (w *wiringSink) snapshot() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([][]byte(nil), w.frames...)
}

func (w *wiringSink) SendJSON([]byte) {}

func TestWireTransportTwoModules(t *testing.T) {
	m1 := writeProtocolModule(t, "one", `var ch = host.channel('one')`)
	m2 := writeProtocolModule(t, "two", `var ch = host.channel('two')`)
	hub, runners := wireTransport(&plugins.LoadedPlugins{Protocols: []plugins.ProtocolModule{m1, m2}})
	defer func() {
		for _, r := range runners {
			r.Close()
		}
		hub.Close()
	}()
	if len(runners) != 2 {
		t.Fatalf("runners = %d, want 2", len(runners))
	}
	if !hub.Connected("one") || !hub.Connected("two") {
		t.Fatal("both modules must be wired")
	}
}

func TestWireTransportBadModuleFirstStillWiresRest(t *testing.T) {
	// Pins the `continue` (not `break`) in the load loop: a failing module
	// must not truncate the remaining ones.
	bad := writeProtocolModule(t, "bad", `throw new Error('boom')`)
	good := writeProtocolModule(t, "good", `var ch = host.channel('good')`)
	hub, runners := wireTransport(&plugins.LoadedPlugins{Protocols: []plugins.ProtocolModule{bad, good}})
	defer func() {
		for _, r := range runners {
			r.Close()
		}
		hub.Close()
	}()
	if len(runners) != 1 {
		t.Fatalf("runners = %d, want 1 (bad skipped, good wired)", len(runners))
	}
	if !hub.Connected("good") {
		t.Fatal("good module must stay wired after a bad one")
	}
}
