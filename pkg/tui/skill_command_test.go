package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/skills"
	taskpkg "github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/types"
)

// passthroughHandler is a Hub EventHandler that forwards events to a channel.
// Used in tests to inspect or replay events without blocking the Hub dispatcher.
type passthroughHandler struct {
	events chan hub.Event
}

func newPassthroughHandler() *passthroughHandler {
	return &passthroughHandler{events: make(chan hub.Event, 1024)}
}

func (h *passthroughHandler) Handle(evt hub.Event) {
	h.events <- evt
}

// TestHandleSubmitRepl_SkillCommand_Inline verifies that /skill-name is
// intercepted by LookupSkillCommand, engine.RunSkill is invoked, and the TUI
// state is correct (streaming active, exactly one user message rendered).
func TestHandleSubmitRepl_SkillCommand_Inline(t *testing.T) {
	t.Parallel()

	mp := &tuiMockProvider{}
	mp.responses = append(mp.responses, tuiMockResponse{
		events: textStreamEvents("test-model", "skill response"),
	})
	app := newTestApp(mp)
	app.width = 80
	app.height = 24

	RegisterSlashCommands(map[string]CommandDef{
		"test-skill": {Description: "test", HasArgs: true},
	})

	reg := skills.NewRegistry(t.TempDir())
	reg.RegisterBundledSkill(types.SkillCommand{
		Name:            "test-skill",
		Description:     "test",
		Type:            "prompt",
		Source:          types.SkillSourceBundled,
		LoadedFrom:      "bundled",
		IsUserInvocable: true,
		Content:         "Do the test thing.",
	})
	app.engine.SetSkillRegistry(reg)

	deps := engine.SharedDeps{
		WorkingDir: t.TempDir(),
		TaskList:   taskpkg.NewList(t.TempDir()),
		SkillReg:   reg,
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	app.engine.SetSharedDeps(&deps)

	app.handleSubmitRepl("/test-skill some args")

	if !app.repl.IsStreaming() {
		t.Fatal("should be streaming after /skill command")
	}

	userMsgCount := 0
	for _, msg := range app.repl.messages {
		if msg.Role == "user" {
			userMsgCount++
		}
	}
	if userMsgCount != 1 {
		t.Errorf("expected 1 user message in repl, got %d (double-add bug)", userMsgCount)
	}
}

func TestHandleSubmitRepl_SkillCommand_NotRegistered(t *testing.T) {
	t.Parallel()

	mp := &tuiMockProvider{}
	mp.responses = append(mp.responses, tuiMockResponse{
		events: textStreamEvents("test-model", "normal response"),
	})
	app := newTestApp(mp)
	app.width = 80

	app.handleSubmitRepl("/unknown-skill args")

	if !app.repl.IsStreaming() {
		t.Fatal("should be streaming after unknown slash command (falls through to Query)")
	}
}

// TestHandleSubmitRepl_SkillCommand_Fork_VisibleInMessages is a TUI-level
// end-to-end test for the "↑0 ↓0 tokens" bug. It feeds a fork skill through
// the TUI's event pipeline and verifies that the Skill virtual tool card and
// the sub-agent's text actually appear in repl.messages.
func TestHandleSubmitRepl_SkillCommand_Fork_VisibleInMessages(t *testing.T) {
	t.Parallel()

	mp := &tuiMockProvider{}
	// sub-agent response
	mp.responses = append(mp.responses, tuiMockResponse{
		events: textStreamEvents("test-model", "sub-agent review done"),
	})
	// main agent response
	mp.responses = append(mp.responses, tuiMockResponse{
		events: textStreamEvents("test-model", "main agent summary"),
	})

	app := newTestApp(mp)
	app.width = 80
	app.height = 24

	RegisterSlashCommands(map[string]CommandDef{
		"review": {Description: "review", HasArgs: true},
	})

	reg := skills.NewRegistry(t.TempDir())
	reg.RegisterBundledSkill(types.SkillCommand{
		Name:            "review",
		Description:     "review",
		Type:            "prompt",
		Context:         "fork",
		AgentType:       "General",
		IsUserInvocable: true,
		Source:          types.SkillSourceBundled,
		LoadedFrom:      "bundled",
		Content:         "Review the code.",
	})
	app.engine.SetSkillRegistry(reg)

	deps := engine.SharedDeps{
		WorkingDir: t.TempDir(),
		TaskList:   taskpkg.NewList(t.TempDir()),
		SkillReg:   reg,
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	app.engine.SetSharedDeps(&deps)

	app.handleSubmitRepl("/review")

	// TUI events flow through Hub → TUIHandler → appCh → App.Update.
	// Subscribe a tap to inspect Hub events, feed each through TUIHandler,
	// then drain appCh with App.Update — exactly what Bubbletea runtime does.
	tap := newPassthroughHandler()
	unsubscribe := app.hub.Subscribe(tap)
	defer unsubscribe()

	deadline := time.After(5 * time.Second)
	done := false
	for !done {
		select {
		case evt := <-tap.events:
			app.tuiHandler.Handle(evt)
			for {
				select {
				case msg := <-app.tuiHandler.appCh:
					app.Update(msg)
				default:
					goto drained
				}
			}
		drained:
			if evt.Type == types.EventQueryEnd && evt.Agent == nil {
				done = true
			}
		case <-deadline:
			t.Fatal("engine did not finish within 5s")
		}
	}
	if app.repl.IsStreaming() {
		t.Fatal("engine did not finish within 5s")
	}

	var skillCard *ToolCallView
	for i := len(app.repl.messages) - 1; i >= 0; i-- {
		for j := len(app.repl.messages[i].Blocks) - 1; j >= 0; j-- {
			blk := &app.repl.messages[i].Blocks[j]
			if blk.Type == BlockTool && blk.ToolCall.Name == "Skill" {
				skillCard = &blk.ToolCall
				break
			}
		}
		if skillCard != nil {
			break
		}
	}
	if skillCard == nil {
		t.Fatal("Skill virtual tool card not found in any message — fork skill did not render in TUI")
	}

	var cardText strings.Builder
	for _, blk := range skillCard.Blocks {
		if blk.Type == BlockText {
			cardText.WriteString(blk.Text)
		}
	}
	if !strings.Contains(cardText.String(), "sub-agent review done") {
		t.Errorf("Skill tool card text = %q, want it to contain 'sub-agent review done'", cardText.String())
	}
}

// TestHandleSubmitRepl_SkillCommand_StartsSpinnerTick verifies that the cmd
// returned by handleSubmitRepl for a /skill dispatch is a tea.Batch containing
// an initial spinnerTickMsg. Without it, the bottom progress line (elapsed
// timer, token counter, spinner animation) never updates during skill
// execution — spinnerTickMsg is self-perpetuating only after the first tick
// lands, so a missing initial tick freezes the whole progress line.
func TestHandleSubmitRepl_SkillCommand_StartsSpinnerTick(t *testing.T) {
	t.Parallel()

	mp := &tuiMockProvider{}
	mp.responses = append(mp.responses, tuiMockResponse{
		events: textStreamEvents("test-model", "skill response"),
	})
	app := newTestApp(mp)
	app.width = 80
	app.height = 24

	RegisterSlashCommands(map[string]CommandDef{
		"test-skill-tick": {Description: "test", HasArgs: true},
	})

	reg := skills.NewRegistry(t.TempDir())
	reg.RegisterBundledSkill(types.SkillCommand{
		Name:            "test-skill-tick",
		Description:     "test",
		Type:            "prompt",
		Source:          types.SkillSourceBundled,
		IsUserInvocable: true,
		Content:         "Do the thing.",
	})
	app.engine.SetSkillRegistry(reg)

	deps := engine.SharedDeps{
		WorkingDir: t.TempDir(),
		TaskList:   taskpkg.NewList(t.TempDir()),
		SkillReg:   reg,
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	app.engine.SetSharedDeps(&deps)

	cmd := app.handleSubmitRepl("/test-skill-tick args")
	if cmd == nil {
		t.Fatal("handleSubmitRepl returned nil cmd for /skill")
	}

	// Invoke the cmd to get the top-level msg. tea.Batch returns a BatchMsg,
	// which is `type BatchMsg []Cmd` — a slice of the batched Cmds.
	msg := cmd()
	if msg == nil {
		t.Fatal("cmd() returned nil msg")
	}
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("handleSubmitRepl /skill returned %T, expected tea.BatchMsg so an initial spinnerTickMsg fires alongside readEvents", msg)
	}

	// Execute each batched Cmd with a timeout; readEvents blocks, but
	// spinnerTickMsg returns immediately. We only need to find one tick.
	foundTick := false
	for _, sub := range batch {
		if sub == nil {
			continue
		}
		done := make(chan tea.Msg, 1)
		go func(c tea.Cmd) { done <- c() }(sub)
		select {
		case m := <-done:
			if _, ok := m.(spinnerTickMsg); ok {
				foundTick = true
			}
		case <-time.After(500 * time.Millisecond):
			// readEvents blocks on appCh; that's expected — skip it.
		}
	}
	// Unblock any lingering readEvents goroutines: close idleStop so the
	// readEvents select returns idleAbortedMsg immediately. Without this,
	// each batched readEvents cmd would leak a goroutine that blocks on
	// appCh until process exit.
	if app.idleStop != nil {
		close(app.idleStop)
		app.idleStop = nil
	}
	if !foundTick {
		t.Error("no spinnerTickMsg in batched cmd — progress line will freeze during /skill execution")
	}
}

func TestLookupSkillCommand_Basic(t *testing.T) {
	t.Parallel()

	RegisterSlashCommands(map[string]CommandDef{
		"my-skill": {Description: "test", HasArgs: true},
	})

	tests := []struct {
		input    string
		wantName string
		wantArgs string
		wantOk   bool
	}{
		{"/my-skill", "my-skill", "", true},
		{"/my-skill arg1 arg2", "my-skill", "arg1 arg2", true},
		{"/unknown", "", "", false},
		{"not a command", "", "", false},
		{"/clear", "", "", false},
	}

	for _, tt := range tests {
		name, args, ok := LookupSkillCommand(tt.input)
		if ok != tt.wantOk {
			t.Errorf("LookupSkillCommand(%q) ok = %v, want %v", tt.input, ok, tt.wantOk)
			continue
		}
		if ok {
			if name != tt.wantName {
				t.Errorf("LookupSkillCommand(%q) name = %q, want %q", tt.input, name, tt.wantName)
			}
			if args != tt.wantArgs {
				t.Errorf("LookupSkillCommand(%q) args = %q, want %q", tt.input, args, tt.wantArgs)
			}
		}
	}
}
