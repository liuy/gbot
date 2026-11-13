package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/types"
)

// startStreamingForTest reproduces the side-effect sequence of a real submit
// (repl.go:1646-1648) so View renders the live progress line.
func startStreamingForTest(app *App) {
	app.width, app.height = 80, 24 // View() needs a non-zero viewport
	app.repl.StartQuery()
	app.status.SetStreaming(true)
	app.spinner.Start()
	app.repl.AppendTextItem()
}

// progressLineVisible reports whether View renders the live streaming
// progress line. app.go:1436 renders it as "<spinner> (<elapsed> · ↑N ↓M tokens…)".
// Four markers on one line are required because no pair is unique: the committed
// stats block (repl.go:792) shares "↑N ↓M tokens" but never a parenthesised
// segment, the /context line (command_context.go:263) has " (" + "tokens" but no
// arrows, and tool token counts (components.go:949/1213) are "↑N ↓M" without the
// word "tokens". A tool output line carrying all four markers would be a false
// positive; no test fixture renders that shape.
func progressLineVisible(app *App) bool {
	for line := range strings.SplitSeq(app.View(), "\n") {
		if strings.Contains(line, "↑") && strings.Contains(line, "↓") &&
			strings.Contains(line, "tokens") && strings.Contains(line, " (") {
			return true
		}
	}
	return false
}

// pumpOne runs one readEvents cmd and feeds the msg it produces to Update,
// returning the msg and the cmd Update hands back (the next pump).
func pumpOne(t *testing.T, app *App, cmd tea.Cmd) (tea.Msg, tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("no cmd to pump — the event pump was already dead before this step")
	}
	msg := cmd() // blocks on appCh
	model, next := app.Update(msg)
	got, ok := model.(*App)
	if !ok || got != app {
		t.Fatalf("Update(%T) returned a different App model — the pump chain lost it", msg)
	}
	return msg, next
}

func TestApp_TurnEndForwarded_PumpSurvivesThenQueryEndFinishesStream(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	startStreamingForTest(app)
	if !progressLineVisible(app) {
		t.Fatal("setup: progress line not rendered before the messages — the disappearance assertion below would be vacuous")
	}

	// The engine's real dispatch order: turnEnd immediately precedes queryEnd.
	app.tuiHandler.appCh <- turnEndMsg{}
	app.tuiHandler.appCh <- queryEndMsg{}

	msg, next := pumpOne(t, app, app.readEvents())
	if _, ok := msg.(turnEndMsg); !ok {
		t.Fatalf("first pump msg = %T, want turnEndMsg", msg)
	}
	if next == nil {
		t.Fatalf("Update(%T) returned nil cmd — event pump died, queryEndMsg unreachable (stuck streaming)", msg)
	}

	msg, _ = pumpOne(t, app, next)
	if _, ok := msg.(queryEndMsg); !ok {
		t.Fatalf("second pump msg = %T, want queryEndMsg", msg)
	}
	if app.repl.IsStreaming() {
		t.Error("streaming still true after main queryEndMsg — FinishStream never ran")
	}
	if progressLineVisible(app) {
		t.Error("progress line still in View after queryEndMsg — the user-visible exit never happened")
	}
}

func TestApp_EngineLoop_TurnEndThenQueryEnd_PumpSurvives(t *testing.T) {
	t.Parallel()
	mp := &tuiMockProvider{}
	mp.responses = append(mp.responses, tuiMockResponse{events: textStreamEvents("test-model", "hi")})
	app := newTestApp(mp)
	startStreamingForTest(app)

	// Query runs synchronously; the hub dispatches on this same goroutine, so
	// every event of the query is already queued in appCh when it returns.
	app.engine.Query(context.Background(), "test", "sys")

	cmd := app.readEvents()
	sawQueryEnd := false
	for range 64 {
		msg, next := pumpOne(t, app, cmd)
		if next == nil {
			t.Fatalf("pump died on %T mid-engine-loop — every handler must re-arm readEvents", msg)
		}
		if qe, ok := msg.(queryEndMsg); ok && qe.Agent == nil {
			sawQueryEnd = true
			break
		}
		cmd = next
	}
	if !sawQueryEnd {
		t.Fatal("engine loop never delivered a main-engine queryEndMsg to Update")
	}
	if app.repl.IsStreaming() {
		t.Error("streaming still true after the engine's queryEndMsg")
	}
	if progressLineVisible(app) {
		t.Error("progress line still in View after the engine's queryEndMsg")
	}
}

func TestApp_TurnEnd_PausesRateMeter(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.TokenRate().Add("hello world", 10)
	if _, ok := app.repl.TokenRate().Rate(); !ok {
		t.Fatal("setup: rate meter has no reading after Add — the pause assertion below would be vacuous")
	}

	app.tuiHandler.appCh <- turnEndMsg{}
	pumpOne(t, app, app.readEvents())

	if _, ok := app.repl.TokenRate().Rate(); ok {
		t.Error("turnEndMsg left the rate meter running — the handler never reached TokenRate().Pause()")
	}
}

func TestApp_AutoTurn_TurnEndKeepsStreamingPumpAlive(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	startStreamingForTest(app)
	if !progressLineVisible(app) {
		t.Fatal("setup: progress line not rendered before the messages")
	}

	// One query, two turns: the turn boundary must not end the stream.
	app.tuiHandler.appCh <- turnEndMsg{}
	app.tuiHandler.appCh <- turnStartMsg{}
	app.tuiHandler.appCh <- textDeltaMsg{Text: "second turn"}
	app.tuiHandler.appCh <- queryEndMsg{}

	msg, next := pumpOne(t, app, app.readEvents())
	if next == nil {
		t.Fatalf("Update(%T) returned nil cmd — the pump died at the turn boundary of a multi-turn query", msg)
	}
	if !app.repl.IsStreaming() {
		t.Errorf("turnEndMsg ended the query stream — turn end is not query end (msg %T)", msg)
	}
	if !progressLineVisible(app) {
		t.Error("progress line disappeared at turnEndMsg — an auto-turn must keep streaming visible")
	}

	msg, next = pumpOne(t, app, next)
	if _, ok := msg.(turnStartMsg); !ok {
		t.Fatalf("msg = %T, want turnStartMsg", msg)
	}
	msg, next = pumpOne(t, app, next)
	if _, ok := msg.(textDeltaMsg); !ok {
		t.Fatalf("msg = %T, want textDeltaMsg", msg)
	}
	msg, _ = pumpOne(t, app, next)
	if _, ok := msg.(queryEndMsg); !ok {
		t.Fatalf("msg = %T, want queryEndMsg", msg)
	}
	if app.repl.IsStreaming() {
		t.Error("streaming still true after queryEndMsg")
	}
	if progressLineVisible(app) {
		t.Error("progress line still in View after queryEndMsg")
	}
}

func TestApp_SubAgentQueryEnd_DoesNotFinishMainStream(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	startStreamingForTest(app)
	if handled, _ := app.updateRepl(toolStartMsg{ID: "agent-run-1", Name: "Agent", Summary: "explore"}); !handled {
		t.Fatal("setup: updateRepl did not handle toolStartMsg")
	}

	app.tuiHandler.appCh <- queryEndMsg{Agent: &types.AgentMeta{
		ParentToolUseID: "agent-run-1",
		AgentType:       "Explore",
		Depth:           0,
	}}
	msg, next := pumpOne(t, app, app.readEvents())
	if next == nil {
		t.Fatalf("Update(%T) returned nil cmd — event pump died", msg)
	}
	if !app.repl.IsStreaming() {
		t.Error("sub-agent queryEndMsg finished the main stream — the m.Agent != nil guard must stay")
	}
	tv := app.repl.FindToolView("agent-run-1")
	if tv == nil {
		t.Fatal("parent Agent tool card not found")
	}
	if !tv.Done {
		t.Error("parent Agent tool card not Done after the sub-agent's queryEndMsg")
	}
}
