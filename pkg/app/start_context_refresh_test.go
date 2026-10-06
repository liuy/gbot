package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// stubCompactor replaces the AutoCompactor the engine auto-creates at
// construction: that one summarizes through the configured provider, which in
// this test is an unreachable address, so ManualCompact would abort before it
// ever reaches the context refresh.
type stubCompactor struct{}

func (stubCompactor) Compact(_ context.Context, messages []types.Message) (*short.CompactResult, error) {
	return &short.CompactResult{Messages: messages}, nil
}

// dumpedUserContext flattens the message half of a request dump, which is
// where the CLAUDE.md context is prepended.
func dumpedUserContext(d *engine.APIRequestDump) string {
	var b strings.Builder
	for _, m := range d.Messages {
		for _, cb := range m.Content {
			b.WriteString(cb.Text)
		}
	}
	return b.String()
}

// TestEngineFactory_WiresContextRefresher pins the refresher wiring of the
// production engine factory. Every engine in the process — including each one
// restored from meta.json — is built by that one closure, so dropping the
// SetContextRefresher call leaves all of them serving the CLAUDE.md and memory
// files as they were at startup for the rest of the session, with no error
// anywhere. The engine-side behaviour is covered in pkg/engine; this is the
// only assertion that the callback actually reaches an engine built by app.
//
// The refresher is an unexported engine field, so the proof is behavioural:
// DumpAPIRequest assembles what callLLM would send (system prompt plus the
// prepended context) without a live provider, which matters here because the
// factory's engines hold the config's provider, not a mock.
func TestEngineFactory_WiresContextRefresher(t *testing.T) {
	t.Setenv("HOME", minimalHome(t))
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+freeTCPPort(t))

	project := t.TempDir()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldCwd); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})

	const (
		sentinelA = "APP-FACTORY-CTX-A-5c91"
		sentinelB = "APP-FACTORY-CTX-B-7e38"
	)
	claudeMd := filepath.Join(project, "CLAUDE.md")
	if err := os.WriteFile(claudeMd, []byte("# project rules\n"+sentinelA), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}

	inst, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	eng, _, err := inst.EngineFactory("ctx-refresher", "ctx-refresher", "t", "m")
	if err != nil {
		t.Fatalf("engine factory: %v", err)
	}
	t.Cleanup(func() { eng.Close() })

	// Baseline: the engine starts on the startup file, so the sentinel that
	// shows up later can only have arrived through a refresh.
	if got := dumpedUserContext(eng.DumpAPIRequest()); !strings.Contains(got, sentinelA) {
		t.Fatalf("fresh engine's request lacks the startup CLAUDE.md sentinel %s, got: %.300s", sentinelA, got)
	}

	if err := os.WriteFile(claudeMd, []byte("# project rules\n"+sentinelB), 0o644); err != nil {
		t.Fatalf("rewrite CLAUDE.md: %v", err)
	}
	eng.SetCompactor(stubCompactor{}, engine.AutoCompactConfig{})
	if _, err := eng.ManualCompact(context.Background(), types.Message{}, ""); err != nil {
		t.Fatalf("ManualCompact: %v", err)
	}

	got := dumpedUserContext(eng.DumpAPIRequest())
	if !strings.Contains(got, sentinelB) {
		t.Errorf("request after compaction lacks the edited CLAUDE.md sentinel %s — the factory wired no context refresher; got: %.300s", sentinelB, got)
	}
	if strings.Contains(got, sentinelA) {
		t.Errorf("request after compaction still carries the startup sentinel %s; got: %.300s", sentinelA, got)
	}
}
