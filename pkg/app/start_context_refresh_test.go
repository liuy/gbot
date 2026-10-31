package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
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
	// Port 0 on purpose: nothing here dials the ws address — Start only binds it —
	// so naming a probed-free port would only open the window in which another
	// process takes that port between the probe and Start's bind.
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:0")

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

	// The same closure builds the system prompt, so pin that it reached the engine
	// built by the factory: an empty or mis-argumented prompt is silent otherwise,
	// and the sentinel checks above only prove the file half arrived.
	if sp := eng.DumpAPIRequest().SystemPrompt; !strings.Contains(sp, "workspace="+project) {
		t.Errorf("engine system prompt lacks workspace=%s — buildSystemPrompt was built with the wrong arguments; got: %.200s", project, sp)
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

// sysPromptCaptureProvider records each Stream request's system prompt and
// answers with a minimal one-block text response, so a sub-agent spawn can be
// observed end to end without a live provider. The request's system field is
// raw JSON (string or block array), so the capture keeps the raw bytes and the
// assertions substring-match the block heading.
type sysPromptCaptureProvider struct {
	mu      sync.Mutex
	systems []string
}

func (p *sysPromptCaptureProvider) Name() string { return "sys-capture" }

func (p *sysPromptCaptureProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return nil, fmt.Errorf("not implemented")
}

func (p *sysPromptCaptureProvider) Stream(_ context.Context, req *llm.Request) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	p.systems = append(p.systems, string(req.System))
	p.mu.Unlock()
	ch := make(chan llm.StreamEvent, 6)
	go func() {
		ch <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{}}
		ch <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText, Text: ""}}
		ch <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "OK"}}
		ch <- llm.StreamEvent{Type: "content_block_stop", Index: 0}
		ch <- llm.StreamEvent{Type: "message_delta", Usage: &types.Usage{InputTokens: 10, OutputTokens: 1}, DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}}
		ch <- llm.StreamEvent{Type: "message_stop"}
		close(ch)
	}()
	return ch, nil
}

// TestEngineFactory_SubAgentPromptExcludesVoiceBlock pins the 10-31 ruling's
// other half: the voice block rides in the USER-facing engines' prompts, and a
// regular sub-agent spawned from such an engine must NOT inherit it — the
// sub-agent's prompt is its own agent definition, and wrapping every spawn
// would tax each one ~400 tokens for guidance it cannot act on. The parent's
// wrapped prompt is asserted first so the contrast cannot pass vacuously.
func TestEngineFactory_SubAgentPromptExcludesVoiceBlock(t *testing.T) {
	t.Setenv("HOME", minimalHome(t))
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:0")

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

	inst, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	eng, _, err := inst.EngineFactory("voice-sub", "voice-sub", "t", "m")
	if err != nil {
		t.Fatalf("engine factory: %v", err)
	}
	t.Cleanup(func() { eng.Close() })

	if sp := eng.SystemPrompt(); !strings.Contains(sp, "# Voice Interaction") {
		t.Fatalf("parent engine prompt lacks the voice block — the contrast below would pass vacuously; got: %.200s", sp)
	}

	capture := &sysPromptCaptureProvider{}
	eng.SetProvider(capture)
	if _, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:    "say OK",
		AgentType: "General",
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	capture.mu.Lock()
	defer capture.mu.Unlock()
	if len(capture.systems) == 0 {
		t.Fatal("the sub-agent made no provider request — the exclusion assertion would pass vacuously")
	}
	for i, sys := range capture.systems {
		if !strings.Contains(sys, "You are an agent for") {
			t.Errorf("sub-agent request %d does not carry the General agent definition prompt — the capture observed the wrong request; got: %.200s", i, sys)
		}
		if strings.Contains(sys, "# Voice Interaction") || strings.Contains(sys, "# Speech Output") {
			t.Errorf("sub-agent request %d system prompt carries the voice block; got: %.200s", i, sys)
		}
	}
}

// TestEngineFactory_BasePromptAlwaysCarriesVoiceBlock pins the 10-28 ruling:
// the voice block is an unconditional resident of the user base system prompt
// — every assembly path (factory construction, a brand-new never-voiced
// session, the compaction-time refresher) must carry it with zero flags and
// zero refreshes. The model gates the voice behavior on the reminder markers,
// not on this block's presence.
func TestEngineFactory_BasePromptAlwaysCarriesVoiceBlock(t *testing.T) {
	t.Setenv("HOME", minimalHome(t))
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:0")

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

	inst, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	eng, _, err := inst.EngineFactory("voice-base", "voice-base", "t", "m")
	if err != nil {
		t.Fatalf("engine factory: %v", err)
	}
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(inst.Store, project)

	sections := []string{
		"# Voice Interaction",
		"## Language", "## Reasoning", "## Preambles", "## Verbosity",
		"## Unclear Audio", "## Tools",
		"# Speech Output",
	}
	assertBlock := func(stage string) {
		t.Helper()
		sp := eng.SystemPrompt()
		for _, s := range sections {
			if !strings.Contains(sp, s) {
				t.Errorf("%s: system prompt lacks %q (block must be unconditionally resident); got: %.200s", stage, s, sp)
			}
		}
	}

	// Fresh engine, never-voiced session: resident from construction.
	assertBlock("fresh engine")

	// A brand-new never-voiced session: every request still carries it.
	if err := eng.NewSession(project, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	assertBlock("new session")

	// The compaction-time refresher rebuilds the prompt: the block must
	// survive that path too, or the first compact would strip it.
	eng.SetCompactor(stubCompactor{}, engine.AutoCompactConfig{})
	if _, err := eng.ManualCompact(context.Background(), types.Message{}, ""); err != nil {
		t.Fatalf("ManualCompact: %v", err)
	}
	assertBlock("after compaction")
}

// TestEngineFactory_WeChatPromptExcludesVoiceBlock pins the 10-31 ruling's
// restore half: persisted engines are re-created through the factory on every
// startup, and the wechat connector's engines ("wechat-<account>") are text
// channels — the voice block's [voice]-marker gating never applies there, so
// the wrap must skip them. Without this, a TUI-mode restored wechat engine
// queried with the stored wrapped prompt (the daemon-mode bare reset in
// wechat.go only runs under daemonMode).
func TestEngineFactory_WeChatPromptExcludesVoiceBlock(t *testing.T) {
	t.Setenv("HOME", minimalHome(t))
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:0")
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
	inst, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	eng, _, err := inst.EngineFactory("wechat-wxid_123", "wechat-wxid_123", "t", "m")
	if err != nil {
		t.Fatalf("engine factory: %v", err)
	}
	t.Cleanup(func() { eng.Close() })
	if sp := eng.SystemPrompt(); strings.Contains(sp, "# Voice Interaction") || strings.Contains(sp, "# Speech Output") {
		t.Fatalf("restored wechat engine prompt carries the voice block: %.200s", sp)
	}
	if sp := eng.SystemPrompt(); len(sp) == 0 {
		t.Fatal("wechat engine prompt is empty — the factory failed to build a base prompt")
	}
}
