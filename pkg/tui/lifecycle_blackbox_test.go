package tui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
)

// sseSSEBody is the Anthropic wire response for one turn, byte-for-byte the
// shape pkg/llm's parser expects (pkg/llm/anthropic_test.go:723 is the twin).
// The engine derives its turnEnd/queryEnd ordering from this real stream.
var sseSSEBody = []string{
	"event: message_start\n" + `data: {"message":{"id":"msg_1","type":"message","role":"assistant","model":"test-model","usage":{"input_tokens":10}}}` + "\n\n",
	"event: content_block_start\n" + `data: {"index":0,"content_block":{"type":"text","text":""}}` + "\n\n",
	"event: content_block_delta\n" + `data: {"index":0,"delta":{"type":"text_delta","text":"Hi there"}}` + "\n\n",
	"event: content_block_stop\n" + `data: {"index":0}` + "\n\n",
	"event: message_delta\n" + `data: {"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}` + "\n\n",
	"event: message_stop\n" + `data: {}` + "\n\n",
}

// newSSETestApp assembles a real App over a real Anthropic client talking to an
// httptest SSE upstream — no mock provider in the reply path.
func newSSETestApp(t *testing.T) *App {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer does not support flushing")
			return
		}
		for _, data := range sseSSEBody {
			if _, err := fmt.Fprint(w, data); err != nil {
				t.Errorf("write SSE event: %v", err)
				return
			}
			flusher.Flush()
		}
	}))
	t.Cleanup(server.Close)

	provider := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Timeout: 5 * time.Second,
	})

	h := hub.NewHub()
	eng := engine.New(&engine.Params{
		Provider:   provider,
		Model:      "test-model",
		Dispatcher: h,
	})
	at := agenttool.New()
	at.SetRegistry(agenttool.NewAgentRegistry())
	eng.SetToolRefs(engine.ToolRefs{Agent: at})

	app := NewApp(eng, "test system prompt", h)
	app.history = NewHistory("") // in-memory only, no file I/O
	app.width, app.height = 80, 24
	// Releases the idle readEvents goroutine re-armed after queryEnd
	// (repl.go:1756 selects on idleStop) so the test leaves no goroutine behind.
	t.Cleanup(func() {
		if app.idleStop != nil {
			close(app.idleStop)
		}
	})
	return app
}

// driveUntilIdle mirrors the bubbletea runtime: run each cmd concurrently, feed
// its msg back into Update, run the cmd Update returns, until idle() holds or
// the deadline passes. Update itself only ever runs on the caller's goroutine,
// exactly as the real runtime serialises it.
func driveUntilIdle(t *testing.T, app *App, start tea.Cmd, idle func() bool, timeout time.Duration) {
	t.Helper()
	msgCh := make(chan tea.Msg, 256)
	var last []string // recent msg types, for the timeout diagnostic
	var launch func(tea.Cmd)
	launch = func(c tea.Cmd) {
		if c == nil {
			return
		}
		go func() {
			switch m := c().(type) {
			case tea.BatchMsg:
				for _, sub := range m {
					launch(sub)
				}
			case nil:
			default:
				msgCh <- m
			}
		}()
	}
	launch(start)
	deadline := time.Now().Add(timeout) // REAL-TIME: live SSE round trip plus engine loop, no virtual clock here
	for !idle() {
		if time.Now().After(deadline) { // REAL-TIME: wall-clock bound on the deadline above
			t.Fatalf("app did not reach idle within %v; recent msgs: %v — stuck streaming, event pump dead?", timeout, last)
		}
		select {
		case msg := <-msgCh:
			last = append(last, fmt.Sprintf("%T", msg))
			if len(last) > 8 {
				last = last[1:]
			}
			_, cmd := app.Update(msg)
			launch(cmd)
		case <-time.After(10 * time.Millisecond):
			// The poll gap only re-checks idle(); it is not a wait for completion.
		}
	}
}

func assistantReplyContains(app *App, want string) bool {
	for _, m := range app.repl.Messages() {
		if m.Role != "assistant" {
			continue
		}
		for _, b := range m.Blocks {
			if b.Type == BlockText && strings.Contains(b.Text, want) {
				return true
			}
		}
	}
	return false
}

// TestApp_Lifecycle_SubmitReplyReachesIdle is the black-box lifecycle gate:
// submit a message, the reply renders, the App returns to idle and accepts the
// next submit. Any break in the forwarding chain shows up as "never idle".
func TestApp_Lifecycle_SubmitReplyReachesIdle(t *testing.T) {
	app := newSSETestApp(t)

	app.input.SetValue("hello")
	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("submit produced no cmd — the query never started")
	}
	if !app.repl.IsStreaming() {
		t.Fatal("setup: app not streaming right after submit")
	}
	if !progressLineVisible(app) {
		t.Fatal("setup: progress line not rendered after submit — the disappearance assertion below would be vacuous")
	}

	idle := func() bool { return !app.repl.IsStreaming() }
	driveUntilIdle(t, app, cmd, idle, 5*time.Second)

	if app.repl.IsStreaming() {
		t.Error("streaming still true at idle")
	}
	if progressLineVisible(app) {
		t.Error("progress line still in View at idle — the user never saw the reply finish")
	}
	if !assistantReplyContains(app, "Hi there") {
		t.Error("assistant reply text \"Hi there\" never rendered into the message list")
	}

	// Idle means the App takes the next submit on the normal path: a stuck
	// streaming app would route it to handleEnqueueMessage, which returns nil.
	app.input.SetValue("again")
	_, cmd2 := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd2 == nil {
		t.Fatal("second submit produced no cmd — the app is stuck in streaming")
	}
	if !app.repl.IsStreaming() {
		t.Fatal("second submit did not start a new query")
	}
	driveUntilIdle(t, app, cmd2, idle, 5*time.Second)
	if app.repl.IsStreaming() {
		t.Error("streaming still true at idle after the second reply")
	}
	if progressLineVisible(app) {
		t.Error("progress line still in View at idle after the second reply")
	}
}
