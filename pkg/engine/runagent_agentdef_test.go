package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"

	agenttool "github.com/liuy/gbot/pkg/tool/agent"
)

// The tests in this file exercise the RunAgent paths driven by agent
// definition fields no built-in definition carries: inline MCP servers
// (mcpServers frontmatter), preloaded skills, and a pinned model. Each test
// injects its definition under a unique agent type, so the process-global
// override never reaches the built-in types the other RunAgent tests spawn.

// logAgentAttr matches a captured slog record by its agent attribute, so a
// record another parallel test logged with the same message cannot satisfy
// the assertion.
func logAgentAttr(agent string) func(map[string]any) bool {
	return func(attrs map[string]any) bool { return attrs["agent"] == agent }
}

// logAgentErrAttr additionally requires the record's error attribute to
// carry errPart, pinning that the warned error is the connector's own.
func logAgentErrAttr(agent, errPart string) func(map[string]any) bool {
	return func(attrs map[string]any) bool {
		return attrs["agent"] == agent && strings.Contains(fmt.Sprint(attrs["error"]), errPart)
	}
}

// injectAgentDef publishes def under its own AgentType for the duration of
// the test; the cleanup removes just that key so parallel tests injecting
// under other types keep theirs.
func injectAgentDef(t *testing.T, def *types.AgentDefinition) {
	t.Helper()
	agenttool.SetDefinitionOverrideForTest(map[string]*types.AgentDefinition{def.AgentType: def})
	t.Cleanup(func() {
		agenttool.SetDefinitionOverrideForTest(map[string]*types.AgentDefinition{def.AgentType: nil})
	})
}

func seamDef(agentType string, mutate func(*types.AgentDefinition)) *types.AgentDefinition {
	def := &types.AgentDefinition{
		AgentType:    agentType,
		WhenToUse:    "seam probe agent",
		SystemPrompt: func() string { return "You are the " + agentType + " agent." },
		Tools:        []string{"*"},
	}
	if mutate != nil {
		mutate(def)
	}
	return def
}

// TestRunAgent_AgentDefMCPToolCallable drives the full agent-MCP chain: the
// injected definition carries one inline server spec, the connector wires a
// registry-backed MCP tool, and the sub-agent must actually call it — the
// echoed text can only appear in a later request if the tool survived the
// merge into the filtered set and stayed executable. Also pins the hygiene
// around the connect: the connector runs exactly once with the prefixed agent
// id and the raw spec, a live context, and its cleanup runs after the
// sub-agent finished.
func TestRunAgent_AgentDefMCPToolCallable(t *testing.T) {
	t.Parallel()

	cs := setupInMemoryEchoServer(t)
	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	t.Cleanup(func() { _ = registry.Close() })
	registry.SetConnectionForTest("test-srv", cs)
	echoDT := mcp.DiscoveredTool{Name: "mcp__test-srv__echo", OriginalName: "echo", ServerName: "test-srv"}
	registry.SetToolsForTest([]mcp.DiscoveredTool{echoDT})

	// Only sharedDeps carries the registry: the parent's own tool set must
	// stay free of MCP tools so the sub-agent can only reach the echo tool
	// through the agent-MCP merge under test.
	deps := runAgentDeps(t)
	deps.McpReg = registry

	injectAgentDef(t, seamDef("McpProbe", func(d *types.AgentDefinition) {
		d.McpServersRaw = []json.RawMessage{json.RawMessage(`"test-srv"`)}
	}))

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test", "call_probe_echo", "mcp__test-srv__echo", `{"text":"SEAM-ECHO-OK"}`), nil)
	mp.addResponse(textStreamEvents("test", "probe done"), nil)

	var connectCalls, cleanupCalls, cleanupSawProviderCalls int
	var gotAgentID string
	var gotSpecs []string
	var ctxNil bool

	eng := New(&Params{
		Provider:      mp,
		Model:         "test",
		Logger:        silentLogger(),
		ToolsProvider: func() map[string]tool.Tool { return runAgentTools() },
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	logs := captureSlog(t)
	res, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:    "call the echo tool",
		AgentType: "McpProbe",
		McpConnect: func(ctx context.Context, agentID string, raw []json.RawMessage) (*agenttool.McpConnectResult, error) {
			connectCalls++
			if ctx == nil {
				ctxNil = true
			}
			gotAgentID = agentID
			for _, r := range raw {
				gotSpecs = append(gotSpecs, string(r))
			}
			return &agenttool.McpConnectResult{
				// Registry-backed tool, mirroring the production connector
				// wiring: the merged tool must remain callable, not just
				// listed.
				Tools: map[string]tool.Tool{echoDT.Name: NewMCPTool(echoDT, registry)},
				Cleanup: func() error {
					cleanupCalls++
					cleanupSawProviderCalls = mp.callCount()
					return nil
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if res == nil || !strings.Contains(res.Content, "probe done") {
		t.Errorf("result = %+v, want the scripted reply", res)
	}

	if connectCalls != 1 {
		t.Errorf("agent MCP connector calls = %d, want 1 (definition declares exactly one inline server)", connectCalls)
	}
	if gotAgentID != "agent-McpProbe" {
		t.Errorf("connector agentID = %q, want %q", gotAgentID, "agent-McpProbe")
	}
	if len(gotSpecs) != 1 || gotSpecs[0] != `"test-srv"` {
		t.Errorf("connector raw specs = %q, want exactly one spec %q", gotSpecs, `"test-srv"`)
	}
	if ctxNil {
		t.Error("connector received a nil context; the spawn context must be forwarded")
	}
	if !mp.anyRequestContains("SEAM-ECHO-OK") {
		t.Error("no request carries the echoed tool output — the sub-agent never executed the merged MCP tool")
	}
	if !strings.Contains(string(mp.lastRequest.System), "mcp__test-srv__echo") {
		t.Errorf("sub-agent system prompt lacks the merged tool name; Enabled tools: %.400s", mp.lastRequest.System)
	}
	if cleanupCalls != 1 {
		t.Errorf("cleanup calls = %d, want 1 (deferred cleanup must run when RunAgent returns)", cleanupCalls)
	}
	if cleanupCalls == 1 && cleanupSawProviderCalls < 1 {
		t.Errorf("cleanup ran before the sub-agent's first provider call (provider calls seen: %d); the defer must cover the whole sub-agent run", cleanupSawProviderCalls)
	}
	if call, ok := logs.find("agent MCP connect failed", logAgentAttr("McpProbe")); ok {
		t.Errorf("connect-failed Warn on a successful connect: %+v", call)
	}
	if call, ok := logs.find("agent MCP cleanup failed", logAgentAttr("McpProbe")); ok {
		t.Errorf("cleanup-failed Warn on a nil cleanup error: %+v", call)
	}
}

// TestRunAgent_AgentDefMCPConnectErrorWarnsOnly pins that a failing connector
// degrades to a warning: the sub-agent still runs on its normal tools and the
// caller gets the scripted result, but the failure is logged with the agent
// identity and the connector's error.
func TestRunAgent_AgentDefMCPConnectErrorWarnsOnly(t *testing.T) {
	t.Parallel()

	deps := runAgentDeps(t)
	injectAgentDef(t, seamDef("McpProbeFail", func(d *types.AgentDefinition) {
		d.McpServersRaw = []json.RawMessage{json.RawMessage(`"dead-srv"`)}
	}))

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "degraded but done"), nil)

	eng := New(&Params{
		Provider:      mp,
		Model:         "test",
		Logger:        silentLogger(),
		ToolsProvider: func() map[string]tool.Tool { return runAgentTools() },
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	logs := captureSlog(t)
	res, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:    "do the thing",
		AgentType: "McpProbeFail",
		McpConnect: func(context.Context, string, []json.RawMessage) (*agenttool.McpConnectResult, error) {
			return nil, errors.New("connector exploded")
		},
	})
	if err != nil {
		t.Fatalf("connect failure must not fail the spawn: %v", err)
	}
	if res == nil || !strings.Contains(res.Content, "degraded but done") {
		t.Errorf("result = %+v, want the scripted reply", res)
	}
	call, ok := logs.find("agent MCP connect failed", logAgentErrAttr("McpProbeFail", "connector exploded"))
	if !ok {
		t.Error("no connect-failed Warn attributed to the agent carrying its error; the failure would be silent")
	} else if call.level.String() != "WARN" {
		t.Errorf("connect-failure record level = %s, want WARN", call.level)
	}
}

// TestRunAgent_AgentDefMCPCleanupErrorWarnsOnly pins the deferred-cleanup
// contract: cleanup runs exactly once when RunAgent returns, and a cleanup
// error is warned with the agent identity, never propagated to the caller.
func TestRunAgent_AgentDefMCPCleanupErrorWarnsOnly(t *testing.T) {
	t.Parallel()

	deps := runAgentDeps(t)
	injectAgentDef(t, seamDef("McpProbeDirty", func(d *types.AgentDefinition) {
		d.McpServersRaw = []json.RawMessage{json.RawMessage(`"dirty-srv"`)}
	}))

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "dirty but done"), nil)

	var cleanupCalls int
	eng := New(&Params{
		Provider:      mp,
		Model:         "test",
		Logger:        silentLogger(),
		ToolsProvider: func() map[string]tool.Tool { return runAgentTools() },
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	logs := captureSlog(t)
	res, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:    "do the thing",
		AgentType: "McpProbeDirty",
		McpConnect: func(context.Context, string, []json.RawMessage) (*agenttool.McpConnectResult, error) {
			return &agenttool.McpConnectResult{
				Tools: map[string]tool.Tool{},
				Cleanup: func() error {
					cleanupCalls++
					return errors.New("cleanup exploded")
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("cleanup failure must not fail the spawn: %v", err)
	}
	if res == nil || !strings.Contains(res.Content, "dirty but done") {
		t.Errorf("result = %+v, want the scripted reply", res)
	}
	if cleanupCalls != 1 {
		t.Errorf("cleanup calls = %d, want 1", cleanupCalls)
	}
	if _, ok := logs.find("agent MCP cleanup failed", logAgentErrAttr("McpProbeDirty", "cleanup exploded")); !ok {
		t.Error("no cleanup-failed Warn attributed to the agent carrying its error; the failure would be silent")
	}
}

// TestRunAgent_AgentDefMCPWithoutConnector pins that a definition declaring
// inline servers spawns fine when no connector is wired: the agent-MCP block
// must be skipped rather than call through a nil connector.
func TestRunAgent_AgentDefMCPWithoutConnector(t *testing.T) {
	t.Parallel()

	deps := runAgentDeps(t)
	injectAgentDef(t, seamDef("McpProbeBare", func(d *types.AgentDefinition) {
		d.McpServersRaw = []json.RawMessage{json.RawMessage(`"unwired-srv"`)}
	}))

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "bare done"), nil)

	eng := New(&Params{
		Provider:      mp,
		Model:         "test",
		Logger:        silentLogger(),
		ToolsProvider: func() map[string]tool.Tool { return runAgentTools() },
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	res, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:    "do the thing",
		AgentType: "McpProbeBare",
	})
	if err != nil {
		t.Fatalf("RunAgent without a connector must succeed: %v", err)
	}
	if res == nil || !strings.Contains(res.Content, "bare done") {
		t.Errorf("result = %+v, want the scripted reply", res)
	}
}

// TestRunAgent_AgentDefSkillsPreloaded pins the skill-preload path: a
// definition listing one skill gets that skill's command metadata and content
// injected as a user message ahead of the prompt, exactly once.
func TestRunAgent_AgentDefSkillsPreloaded(t *testing.T) {
	t.Parallel()

	const sentinel = "PROBE-SKILL-SENTINEL-3f9d"
	reg := skills.NewRegistry(t.TempDir())
	reg.RegisterBundledSkill(types.SkillCommand{Name: "probe-skill", Content: sentinel + " skill instructions"})

	deps := runAgentDeps(t)
	deps.SkillReg = reg
	injectAgentDef(t, seamDef("SkillProbe", func(d *types.AgentDefinition) {
		d.Skills = []string{"probe-skill"}
	}))

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "skill probe done"), nil)

	eng := New(&Params{
		Provider:      mp,
		Model:         "test",
		Logger:        silentLogger(),
		ToolsProvider: func() map[string]tool.Tool { return runAgentTools() },
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	res, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:    "use the skill",
		AgentType: "SkillProbe",
	})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if res == nil || !strings.Contains(res.Content, "skill probe done") {
		t.Errorf("result = %+v, want the scripted reply", res)
	}
	if mp.lastRequest == nil {
		t.Fatal("sub-engine never sent a request")
	}
	flat := flattenText(mp.lastRequest.Messages)
	if got := strings.Count(flat, sentinel); got != 1 {
		t.Errorf("skill sentinel occurrences in request = %d, want 1\nrequest: %s", got, truncate(flat, 1200))
	}
	if !strings.Contains(flat, "<command-message>probe-skill</command-message>") {
		t.Errorf("request lacks the skill command metadata message\nrequest: %s", truncate(flat, 1200))
	}
}

// TestRunAgent_AgentDefSkillsWithoutRegistry pins the guard's other operand:
// a definition listing skills but no skill registry available must skip the
// preload and still spawn successfully — the sub-agent just starts without
// the skill messages.
func TestRunAgent_AgentDefSkillsWithoutRegistry(t *testing.T) {
	t.Parallel()

	deps := runAgentDeps(t)
	deps.SkillReg = nil
	injectAgentDef(t, seamDef("SkillProbeBare", func(d *types.AgentDefinition) {
		d.Skills = []string{"probe-skill"}
	}))

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test", "bare skill done"), nil)

	eng := New(&Params{
		Provider:      mp,
		Model:         "test",
		Logger:        silentLogger(),
		ToolsProvider: func() map[string]tool.Tool { return runAgentTools() },
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)

	res, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:    "use the skill",
		AgentType: "SkillProbeBare",
	})
	if err != nil {
		t.Fatalf("spawn with skills but no registry must succeed: %v", err)
	}
	if res == nil || !strings.Contains(res.Content, "bare skill done") {
		t.Errorf("result = %+v, want the scripted reply", res)
	}
	if mp.lastRequest == nil {
		t.Fatal("sub-engine never sent a request")
	}
	if flat := flattenText(mp.lastRequest.Messages); strings.Contains(flat, "<command-message>") {
		t.Errorf("request carries a skill message though no registry was wired:\n%s", truncate(flat, 1200))
	}
}

// TestRunAgent_AgentDefModelRoutesSubProvider pins that a definition-pinned
// "provider/model" routes the sub-agent exactly like the caller-supplied
// override does: the target provider serves the request with the bare model
// name and the parent provider is never consulted.
func TestRunAgent_AgentDefModelRoutesSubProvider(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Providers: []config.Provider{{Name: "zhipu"}}}
	cfg.Providers[0].Models.Set("glm-x", config.ModelConfig{})
	target := &mockProvider{}
	target.addResponse(textStreamEvents("test", "routed"), nil)
	d := runAgentDeps(t)
	d.Cfg, d.Providers = cfg, config.ProviderMap{"zhipu": target}

	injectAgentDef(t, seamDef("ModelProbe", func(def *types.AgentDefinition) {
		def.Model = "zhipu/glm-x"
	}))

	parent := &mockProvider{} // no scripted responses: any parent call is a bug
	eng := New(&Params{Provider: parent, Model: "parent-model", Logger: silentLogger()})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&d)

	res, err := eng.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:    "p",
		AgentType: "ModelProbe",
	})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if res == nil || !strings.Contains(res.Content, "routed") {
		t.Errorf("result = %+v, want the scripted reply", res)
	}
	if calls := allRequestsLen(parent); calls != 0 {
		t.Errorf("parent provider got %d requests, want 0 (definition-pinned model must switch provider)", calls)
	}
	if target.lastRequest == nil {
		t.Fatal("target provider never received a request; the definition's model fell back to inherit")
	}
	if target.lastRequest.Model != "glm-x" {
		t.Errorf("target request model = %q, want bare name %q", target.lastRequest.Model, "glm-x")
	}
}
