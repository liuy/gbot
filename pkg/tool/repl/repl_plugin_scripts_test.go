package repl

import (
	"context"
	"strings"
	"testing"
)

func TestReplScriptPreload(t *testing.T) {
	SetReplScripts([]ReplScript{
		{Name: "t:harness.js", Source: "globalThis.browser = { ping: () => 42 };"},
	})
	defer SetReplScripts(nil)
	s := newTestSession(t)
	output, _, err := s.Execute(context.Background(), `console.log(String(browser.ping()))`, "", nil, 10000, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := strings.TrimSpace(output); got != "42" {
		t.Errorf("browser.ping() output = %q, want 42", output)
	}
}

// Scripts are per-session: state agents hang off the harness object (SKILL.md
// teaches globalThis state) must survive every Execute in the same session.
func TestReplScript_EvaluatedOncePerSession(t *testing.T) {
	SetReplScripts([]ReplScript{
		{Name: "t:once.js", Source: "globalThis.browser = { state: [] };"},
	})
	defer SetReplScripts(nil)
	s := newTestSession(t)
	ctx := context.Background()

	out1, _, err := s.Execute(ctx, `browser.state.push("first"); console.log(String(browser.state.length))`, "", nil, 10000, nil)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if got := strings.TrimSpace(out1); got != "1" {
		t.Errorf("after first Execute browser.state.length = %q, want 1", out1)
	}

	out2, _, err := s.Execute(ctx, `browser.state.push("second"); console.log(String(browser.state.length))`, "", nil, 10000, nil)
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	// A re-evaluation would replace browser and the length would reset to 1.
	if got := strings.TrimSpace(out2); got != "2" {
		t.Errorf("after second Execute browser.state.length = %q, want 2 — harness state must survive across Executes", out2)
	}
}

func TestReplScript_BrokenSkipped(t *testing.T) {
	SetReplScripts([]ReplScript{
		{Name: "t:broken.js", Source: "this is not } javascript"},
		{Name: "t:ok.js", Source: "globalThis.__marker = 'alive';"},
	})
	defer SetReplScripts(nil)
	s := newTestSession(t)
	output, _, err := s.Execute(context.Background(), `console.log(__marker)`, "", nil, 10000, nil)
	if err != nil {
		t.Fatalf("Execute after broken script: %v", err)
	}
	if got := strings.TrimSpace(output); got != "alive" {
		t.Errorf("__marker output = %q, want alive", output)
	}
}

// The exported getter must mirror what was set and hand out a copy — the
// app-side wiring pin reads it from outside the package.
func TestReplScripts_GetterRoundTrip(t *testing.T) {
	SetReplScripts([]ReplScript{{Name: "t:get.js", Source: "x"}})
	defer SetReplScripts(nil)
	got := ReplScripts()
	if len(got) != 1 || got[0].Name != "t:get.js" || got[0].Source != "x" {
		t.Fatalf("ReplScripts() = %+v, want the one script that was set", got)
	}
	got[0].Source = "clobbered"
	if again := ReplScripts(); len(again) != 1 || again[0].Source != "x" {
		t.Errorf("ReplScripts() handed out the live slice: %+v, want the stored source", again)
	}
	SetReplScripts(nil)
	if again := ReplScripts(); len(again) != 0 {
		t.Errorf("ReplScripts() after clearing = %+v, want empty", again)
	}
}

func TestReplScript_CanReadAllTools(t *testing.T) {
	SetReplScripts([]ReplScript{
		{Name: "t:h.js", Source: "globalThis.browser = { toolCount: () => ALL_TOOLS.length };"},
	})
	defer SetReplScripts(nil)
	s := newTestSession(t)
	output, _, err := s.Execute(context.Background(), `console.log(String(browser.toolCount() >= 0))`, "", nil, 10000, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output, "true") {
		t.Errorf("harness cannot see ALL_TOOLS, output = %q", output)
	}
}
