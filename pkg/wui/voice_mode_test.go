package wui

import (
	"testing"
	"time"

	ctxbuild "github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/types"
)

// Voice-marker contract (10-28 final ruling): voice mode as a session state
// is retired. Each spoken message carries the wire-transient voice flag; the
// connector prefixes ctxbuild.VoiceMsgMarker onto the text and dispatches a
// plain Query — the stored message rides the prefix, so compact cannot lose
// the marking and no engine surface exists beyond Query.

// waitQueries blocks until the mock holds n Query calls. Inbound frames
// arrive on the readLoop goroutine and the message dispatch spawns its own
// goroutine, so assertions need this handshake before reading mock state.
func waitQueries(t *testing.T, m *mockEngine, n int) {
	t.Helper()
	if !waitFor(2*time.Second, func() bool {
		m.mu.RLock()
		defer m.mu.RUnlock()
		return len(m.queryCalls) == n
	}) {
		m.mu.RLock()
		defer m.mu.RUnlock()
		t.Fatalf("Query calls stuck at %d, want %d (calls=%+v)", len(m.queryCalls), n, m.queryCalls)
	}
}

// A voice message (voice:true) rides the plain Query path with the marker
// prefix baked into the text — no reminders, no attachment-queue rides.
func TestVoiceMessage_CarriesMarkerPrefix(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)
	defer ws.Close()

	sendJSON(t, ws, map[string]any{"type": "message", "text": "你好，语音第一句", "voice": true})
	m := c.mock()
	waitQueries(t, m, 1)

	m.mu.RLock()
	defer m.mu.RUnlock()
	if got, want := m.queryCalls[0].userMessage, ctxbuild.VoiceMsgMarker+"你好，语音第一句"; got != want {
		t.Errorf("voice message text = %q, want marker-prefixed %q", got, want)
	}
	if n := len(m.enqueueCalls); n != 0 {
		t.Errorf("attachment enqueues after voice message = %d, want 0 (plain Query path only)", n)
	}
}

// Every voice final carries the prefix, not just the first of a session —
// the message, not a mode, is the unit of marking. Sequential waits: back-
// to-back dispatch goroutines give no cross-message ordering guarantee.
func TestVoiceMessage_EveryFinalCarriesPrefix(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)
	defer ws.Close()

	m := c.mock()
	for i, text := range []string{"第一句", "第二句"} {
		sendJSON(t, ws, map[string]any{"type": "message", "text": text, "voice": true})
		waitQueries(t, m, i+1)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	for i, text := range []string{"第一句", "第二句"} {
		if got, want := m.queryCalls[i].userMessage, ctxbuild.VoiceMsgMarker+text; got != want {
			t.Errorf("voice message %d text = %q, want %q", i+1, got, want)
		}
	}
}

// A message without the voice flag lands byte-identical to the typed path:
// zero prefix.
func TestVoiceMessage_PlainMessageNoPrefix(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)
	defer ws.Close()

	sendJSON(t, ws, map[string]any{"type": "message", "text": "hello"})
	m := c.mock()
	waitQueries(t, m, 1)

	m.mu.RLock()
	defer m.mu.RUnlock()
	if got := m.queryCalls[0].userMessage; got != "hello" {
		t.Errorf("plain message text = %q, want %q (no marker)", got, "hello")
	}
}

// voice_mode frames are retired with the mode: on/off frames from older
// clients are inert noise, with zero enqueues and zero queries, and a later
// message flows through untouched.
func TestVoiceModeFrame_RetiredInert(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)
	defer ws.Close()

	sendJSON(t, ws, map[string]any{"type": "voice_mode", "on": true})
	sendJSON(t, ws, map[string]any{"type": "voice_mode", "on": false})
	sendJSON(t, ws, map[string]any{"type": "message", "text": "after retirement"})
	m := c.mock()
	waitQueries(t, m, 1)

	m.mu.RLock()
	defer m.mu.RUnlock()
	if got := m.queryCalls[0].userMessage; got != "after retirement" {
		t.Errorf("message after voice_mode frames = %q, want the plain text", got)
	}
	if n := len(m.enqueueCalls); n != 0 {
		t.Errorf("attachment enqueues after voice_mode frames = %d, want 0 (frames are inert)", n)
	}
}

// Busy engine: the voice message queues as one ordinary prompt item with the
// marker already inside Value — the same single-item ride as a typed message.
func TestVoiceMessage_WhileBusyQueuesMarkedPrompt(t *testing.T) {
	c := newTestConnector(t)
	m := c.mock()
	m.mu.Lock()
	m.isBusyFn = func() bool { return true }
	m.mu.Unlock()
	ws := dialAndStore(t, c)
	defer ws.Close()

	sendJSON(t, ws, map[string]any{"type": "message", "text": "忙时插话", "voice": true})
	if !waitFor(2*time.Second, func() bool {
		m.mu.RLock()
		defer m.mu.RUnlock()
		return len(m.enqueueCalls) == 1
	}) {
		m.mu.RLock()
		defer m.mu.RUnlock()
		t.Fatalf("busy voice message never queued (enqueueCalls=%+v)", m.enqueueCalls)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	item := m.enqueueCalls[0]
	if item.Mode != types.ItemModePrompt {
		t.Errorf("queued item mode = %q, want prompt (no reminder rides the queue)", item.Mode)
	}
	if item.IsMeta {
		t.Error("queued voice message must not be meta")
	}
	if got, want := item.Value, ctxbuild.VoiceMsgMarker+"忙时插话"; got != want {
		t.Errorf("queued item Value = %q, want marker-prefixed %q", got, want)
	}
	if len(m.queryCalls) != 0 {
		t.Errorf("Query calls while busy = %d, want 0", len(m.queryCalls))
	}
}
