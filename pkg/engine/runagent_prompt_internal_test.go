package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/tool"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/tool/bash"
)

// subAgentSystemPrompt runs one General sub-agent through RunAgent and returns
// the system prompt of the single request the sub-engine sent.
//
// The prompt is read off the wire rather than off the sub-engine because
// RunAgent hands it to RunForkedQuery as a parameter and never exposes it: a
// field read would also pass if the enhancement were applied somewhere the
// sub-engine never uses.
func subAgentSystemPrompt(t *testing.T, workingDir, suppliedPrompt string) string {
	t.Helper()
	var systems []string
	mp := &testProvider{onStream: func(req *llm.Request) {
		var b strings.Builder
		for _, sb := range req.SystemBlocks {
			b.WriteString(sb.Text)
		}
		systems = append(systems, b.String())
	}}
	mp.responses = []testResponse{{events: subTextEvents("test", "sub-agent done")}}

	deps := SharedDeps{
		WorkingDir: workingDir,
		SkillReg:   skills.NewRegistry(t.TempDir()),
		Hooks:      hooks.NewHooks(hooks.HooksConfig{}, &hooks.CommandExecutor{}),
	}
	bt := bash.New(nil)
	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		WorkingDir: workingDir,
		// A non-empty parent tool set: the agent definition is wildcard, so
		// the enhanced prompt's tool list mirrors exactly these tools.
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{bt.Name(): bt}
		},
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	if _, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:       "do the thing",
		AgentType:    "General",
		SystemPrompt: suppliedPrompt,
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if len(systems) != 1 {
		t.Fatalf("sub-engine requests = %d, want 1", len(systems))
	}
	return systems[0]
}

// TestRunAgent_NoSuppliedPrompt_EnhancesSubEnginePrompt pins the sub-engine
// construction path's prompt assembly: with no pre-built prompt, RunAgent must
// hand the sub-agent the enhanced prompt (agent base prompt + notes + tool
// list + env block), not the bare agent prompt. Deleting the
// EnhanceSystemPrompt call leaves the sub-agent with no environment block and
// no tool list, and nothing else in the pipeline adds them back.
func TestRunAgent_NoSuppliedPrompt_EnhancesSubEnginePrompt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	system := subAgentSystemPrompt(t, dir, "")

	def, err := agenttool.GetAgentDefinition("General")
	if err != nil {
		t.Fatalf("GetAgentDefinition(General): %v", err)
	}
	// The enhancement appends to the agent's own prompt; a sub-agent that got
	// only the env block would be just as broken as one that got only the base.
	if base := def.SystemPrompt(); !strings.HasPrefix(system, base) {
		t.Errorf("sub-engine prompt does not start with the General agent prompt, got: %.200q", system)
	}
	for _, marker := range []string{
		// Env block, anchored on this test's own dir so it cannot come from
		// any other engine's prompt.
		"Working directory: " + dir,
		"Is directory a git repo:",
		// Tool list rendered from the agent-filtered tool set.
		"Enabled tools:\n- Bash\n",
		// Notes appended to every agent prompt.
		"share file paths (always absolute, never relative)",
	} {
		if !strings.Contains(system, marker) {
			t.Errorf("sub-engine prompt missing %q", marker)
		}
	}
}

// TestRunAgent_SuppliedPrompt_NotEnhanced is the other half of the branch: a
// caller that already built the prompt (session memory, fork, skill fork) must
// get it verbatim. Enhancing it would double the env block and rewrite the
// prompt-cache prefix of every forked query.
func TestRunAgent_SuppliedPrompt_NotEnhanced(t *testing.T) {
	t.Parallel()
	const supplied = "PRE-BUILT-SUB-PROMPT-6b04"
	system := subAgentSystemPrompt(t, t.TempDir(), supplied)
	if system != supplied {
		t.Errorf("sub-engine prompt = %.200q, want exactly %q", system, supplied)
	}
}
