package skill

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/tool"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/types"
)

// Pin tests for the skill.go/budget.go mutation sweeps; assertions are exact
// so a single changed byte flips the test. Mutant line:mutator pairs are in
// the campaign ledger.

func pinReg(t *testing.T) *skills.Registry {
	t.Helper()
	reg := skills.NewRegistry(t.TempDir())
	reg.RegisterBundledSkill(types.SkillCommand{
		Name: "plain", Description: "Plain skill", Type: "prompt",
		Source: types.SkillSourceBundled, LoadedFrom: "bundled", IsUserInvocable: true,
		Content: "Body with ${SKILL_DIR}/helper.md reference.",
	})
	reg.RegisterBundledSkill(types.SkillCommand{
		Name: "subagent", Description: "New-context skill", Type: "prompt",
		Source: types.SkillSourceUser, LoadedFrom: "skills", Context: "new",
		Content: "Run in ${SKILL_DIR}.",
	})
	reg.RegisterBundledSkill(types.SkillCommand{
		Name: "forker", Description: "Fork skill", Type: "prompt",
		Source: types.SkillSourceUser, LoadedFrom: "skills", Context: "fork",
		Content: "Fork from ${SKILL_DIR}.",
	})
	reg.RegisterBundledSkill(types.SkillCommand{
		Name: "subargs", Description: "Takes arguments", Type: "prompt",
		Source: types.SkillSourceUser, LoadedFrom: "skills", Context: "new",
		Arguments: []types.SkillArgument{{Name: "1"}, {Name: "2"}},
		Content:   "Args: $1 and $2 in ${SKILL_DIR}.",
	})
	reg.RegisterBundledSkill(types.SkillCommand{
		Name: "fixedbody", Description: "No placeholders", Type: "prompt",
		Source: types.SkillSourceUser, LoadedFrom: "skills", Context: "new",
		AllowedTools: []string{"Bash"}, AgentType: "research", Model: "m2",
		Content: "Fixed body.",
	})
	reg.RegisterBundledSkill(types.SkillCommand{
		Name: "tooled", Description: "Has tools", Type: "prompt",
		Source: types.SkillSourceUser, LoadedFrom: "skills",
		AllowedTools: []string{"Bash"}, Content: "Uses tools.",
	})
	return reg
}

func pinCall(t *testing.T, reg *skills.Registry, agent *agenttool.AgentTool, raw string, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	t.Helper()
	return New(reg, agent).Call(context.Background(), json.RawMessage(raw), tctx)
}

func TestPinSkill_InlineMessagesAndOutput(t *testing.T) {
	t.Parallel()
	reg := pinReg(t)
	res, err := pinCall(t, reg, nil, `{"skill":"plain","args":"x y"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, ok := res.Data.(skillOutput)
	if !ok {
		t.Fatalf("data = %T", res.Data)
	}
	if !out.Success || out.CommandName != "plain" || out.Status != "inline" {
		t.Errorf("inline output = %+v", out)
	}
	if len(res.NewMessages) != 2 {
		t.Fatalf("inline messages = %d, want metadata + content", len(res.NewMessages))
	}
	meta, content := res.NewMessages[0], res.NewMessages[1]
	if meta.Flags != types.FlagMeta || content.Flags != types.FlagMeta {
		t.Errorf("message flags = %v/%v, want meta on both", meta.Flags, content.Flags)
	}
	if len(meta.Content) != 1 || meta.Content[0].Type != "text" {
		t.Fatalf("metadata block = %+v", meta.Content)
	}
	if !strings.Contains(meta.Content[0].Text, "<command-name>/plain</command-name>") {
		t.Errorf("metadata = %q, want command-name tag", meta.Content[0].Text)
	}
	if !strings.Contains(meta.Content[0].Text, "<command-args>x y</command-args>") {
		t.Errorf("metadata = %q, want command-args", meta.Content[0].Text)
	}
	if len(content.Content) != 1 || content.Content[0].Type != "text" {
		t.Fatalf("content block = %+v", content.Content)
	}
	if !strings.Contains(content.Content[0].Text, "Body with ") || !strings.Contains(content.Content[0].Text, "/helper.md reference.") {
		t.Errorf("content = %q, want substituted body", content.Content[0].Text)
	}
	if strings.Contains(content.Content[0].Text, "${SKILL_DIR}") {
		t.Errorf("content = %q, ${SKILL_DIR} not substituted", content.Content[0].Text)
	}

	// A skill with allowed-tools attaches a third permissions message.
	tooled, err := pinCall(t, reg, nil, `{"skill":"tooled"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tooled.NewMessages) != 3 {
		t.Fatalf("tooled messages = %d, want permissions message", len(tooled.NewMessages))
	}
	perms := tooled.NewMessages[2]
	if perms.Flags != types.FlagMeta {
		t.Errorf("permissions flags = %v", perms.Flags)
	}
	if !strings.Contains(perms.Content[0].Text, "<allowed-tools>Bash</allowed-tools>") {
		t.Errorf("permissions = %q", perms.Content[0].Text)
	}
	if len(perms.Content) != 1 || perms.Content[0].Type != "text" {
		t.Errorf("permissions block = %+v, want single text block", perms.Content)
	}
}

func TestPinSkill_InvalidInputWraps(t *testing.T) {
	t.Parallel()
	reg := pinReg(t)
	_, err := pinCall(t, reg, nil, `{"skill": 1}`, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "skill tool: invalid input:") {
		t.Fatalf("invalid input err = %v, want prefix", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("invalid input err not wrapped (%%w): %v", err)
	}
}

func TestPinSkill_NewSkillFlow(t *testing.T) {
	t.Parallel()
	reg := pinReg(t)
	var gotCtx context.Context
	var gotOpts agenttool.AgentOpts
	agent := agenttool.New()
	agent.SetEngine(&mockSubEngine{runFn: func(ctx context.Context, opts agenttool.AgentOpts) (*types.SubQueryResult, error) {
		gotCtx = ctx
		gotOpts = opts
		if inv := reg.GetInvokedSkillsForAgent("skill-new:subagent"); len(inv) != 1 {
			t.Errorf("invoked skills during run = %d, want 1 (cleared only after)", len(inv))
		}
		return &types.SubQueryResult{Content: "sub done"}, nil
	}})
	res, err := pinCall(t, reg, agent, `{"skill":"subagent"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotCtx == nil {
		t.Error("RunAgent received nil context")
	}
	// Bundled skills have no source dir, so ${SKILL_DIR} substitutes to "".
	if gotOpts.Prompt != "Run in ." {
		t.Errorf("RunAgent prompt = %q, want substituted content %q", gotOpts.Prompt, "Run in .")
	}
	if len(gotOpts.AllowedTools) != 0 || gotOpts.AgentType != "" || gotOpts.Model != "" {
		t.Errorf("RunAgent opts passthrough = %+v", gotOpts)
	}
	out, _ := res.Data.(skillOutput)
	if !out.Success || out.CommandName != "subagent" || out.Status != "forked" || out.AgentID != "skill-new:subagent" || out.Result != "sub done" {
		t.Errorf("new output = %+v", out)
	}
	// The sub-agent's invoked-skill bookkeeping is cleared after the run.
	if inv := reg.GetInvokedSkillsForAgent("skill-new:subagent"); len(inv) != 0 {
		t.Errorf("invoked skills after run = %d, want cleared", len(inv))
	}

	// No engine wired: explicit error, not a panic.
	_, err = pinCall(t, reg, agenttool.New(), `{"skill":"subagent"}`, nil)
	if err == nil || !strings.Contains(err.Error(), "no sub-agent engine") {
		t.Fatalf("nil-engine err = %v, want engine error", err)
	}
}

func TestPinSkill_SubagentArgSubstitution(t *testing.T) {
	t.Parallel()
	reg := pinReg(t)
	var gotPrompt string
	agent := agenttool.New()
	agent.SetEngine(&mockSubEngine{runFn: func(ctx context.Context, opts agenttool.AgentOpts) (*types.SubQueryResult, error) {
		gotPrompt = opts.Prompt
		return &types.SubQueryResult{Content: "ok"}, nil
	}})
	if _, err := pinCall(t, reg, agent, `{"skill":"subargs","args":"first second"}`, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotPrompt, "Args: first and second in ") {
		t.Errorf("substituted prompt = %q, want positional args substituted", gotPrompt)
	}
	if strings.Contains(gotPrompt, "$1") || strings.Contains(gotPrompt, "$2") || strings.Contains(gotPrompt, "${SKILL_DIR}") {
		t.Errorf("substituted prompt retains placeholders: %q", gotPrompt)
	}

	// Content without placeholders appends the raw arguments; command
	// properties pass through to the sub-agent opts.
	var fixed string
	var fixedOpts agenttool.AgentOpts
	agent2 := agenttool.New()
	agent2.SetMcpConnect(func(ctx context.Context, agentID string, rawSpecs []json.RawMessage) (*agenttool.McpConnectResult, error) {
		return &agenttool.McpConnectResult{}, nil
	})
	agent2.SetEngine(&mockSubEngine{runFn: func(ctx context.Context, opts agenttool.AgentOpts) (*types.SubQueryResult, error) {
		fixed = opts.Prompt
		fixedOpts = opts
		return &types.SubQueryResult{Content: "ok"}, nil
	}})
	if _, err := pinCall(t, reg, agent2, `{"skill":"fixedbody","args":"extra input"}`, &tool.ToolUseContext{ToolUseID: "tu-9"}); err != nil {
		t.Fatal(err)
	}
	if len(fixedOpts.AllowedTools) != 1 || fixedOpts.AllowedTools[0] != "Bash" || fixedOpts.AgentType != "research" || fixedOpts.Model != "m2" || fixedOpts.ParentToolUseID != "tu-9" {
		t.Errorf("fixedbody opts passthrough = %+v", fixedOpts)
	}
	if fixedOpts.McpConnect == nil {
		t.Error("McpConnect not passed through to sub-agent opts")
	}
	if fixed != "Fixed body.\n\nARGUMENTS: extra input" {
		t.Errorf("no-placeholder prompt = %q, want arguments appended", fixed)
	}
}

func TestPinSkill_ForkSkillFlow(t *testing.T) {
	t.Parallel()
	reg := pinReg(t)
	var gotOpts agenttool.AgentOpts
	agent := agenttool.New()
	agent.SetNotifyFn(func(string) {}, func() string { return "system prompt" })
	agent.SetMcpConnect(func(ctx context.Context, agentID string, rawSpecs []json.RawMessage) (*agenttool.McpConnectResult, error) {
		return &agenttool.McpConnectResult{}, nil
	})
	agent.SetEngine(&mockSubEngine{runFn: func(ctx context.Context, opts agenttool.AgentOpts) (*types.SubQueryResult, error) {
		if ctx == nil {
			t.Error("RunAgent received nil context")
		}
		gotOpts = opts
		if inv := reg.GetInvokedSkillsForAgent("skill-fork:forker"); len(inv) != 1 {
			t.Errorf("fork invoked skills during run = %d, want 1", len(inv))
		}
		return &types.SubQueryResult{Content: "fork done"}, nil
	}})
	tctx := &tool.ToolUseContext{ToolUseID: "tu-1", Messages: []types.Message{}}
	res, err := pinCall(t, reg, agent, `{"skill":"forker"}`, tctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotOpts.ForkMessages) == 0 {
		t.Error("RunAgent got no fork messages")
	}
	lastFork := gotOpts.ForkMessages[len(gotOpts.ForkMessages)-1]
	if len(lastFork.Content) == 0 || strings.Contains(lastFork.Content[0].Text, "${SKILL_DIR}") {
		t.Errorf("fork prompt not substituted: %+v", lastFork.Content)
	}
	if gotOpts.McpConnect == nil {
		t.Error("fork McpConnect not passed through")
	}
	if gotOpts.SystemPrompt == "" {
		t.Error("RunAgent got empty system prompt")
	}
	if gotOpts.ParentToolUseID != "tu-1" {
		t.Errorf("ParentToolUseID = %q, want tu-1", gotOpts.ParentToolUseID)
	}
	if len(gotOpts.AllowedTools) != 0 {
		t.Errorf("fork AllowedTools = %v, want empty", gotOpts.AllowedTools)
	}
	if gotOpts.AgentType != "" || gotOpts.Model != "" {
		t.Errorf("fork opts passthrough = %+v", gotOpts)
	}
	out, _ := res.Data.(skillOutput)
	if !out.Success || out.CommandName != "forker" || out.Status != "forked" || out.AgentID != "skill-fork:forker" || out.Result != "fork done" {
		t.Errorf("fork output = %+v", out)
	}

	// Arguments without placeholders in the skill body are appended to the
	// fork prompt verbatim.
	var argOpts agenttool.AgentOpts
	agent.SetEngine(&mockSubEngine{runFn: func(ctx context.Context, opts agenttool.AgentOpts) (*types.SubQueryResult, error) {
		argOpts = opts
		return &types.SubQueryResult{Content: "fork done"}, nil
	}})
	if _, err := pinCall(t, reg, agent, `{"skill":"forker","args":"zzz"}`, tctx); err != nil {
		t.Fatal(err)
	}
	lastArg := argOpts.ForkMessages[len(argOpts.ForkMessages)-1]
	if !strings.Contains(lastArg.Content[0].Text, "ARGUMENTS: zzz") {
		t.Errorf("fork prompt with args = %q, want ARGUMENTS appended", lastArg.Content[0].Text)
	}

	// Guard errors instead of panicking on unwired dependencies, each with
	// its own message (a removed guard must surface as a panic or a
	// different message, not silently pass).
	if _, err := pinCall(t, reg, agenttool.New(), `{"skill":"forker"}`, tctx); err == nil || err.Error() != "skill: \"forker\" has context=fork but no sub-agent engine" {
		t.Fatalf("nil-engine fork err = %v, want engine guard message", err)
	}
	engineOnly := agenttool.New()
	engineOnly.SetEngine(&mockSubEngine{})
	if _, err := pinCall(t, reg, engineOnly, `{"skill":"forker"}`, tctx); err == nil || err.Error() != "skill: \"forker\" has context=fork but SysPromptFn not wired" {
		t.Fatalf("no-sysprompt fork err = %v, want SysPromptFn guard message", err)
	}
	if _, err := pinCall(t, reg, agent, `{"skill":"forker"}`, nil); err == nil || err.Error() != "skill: \"forker\" has context=fork but no tool-use context (parent messages unavailable)" {
		t.Fatalf("nil-tctx fork err = %v, want tctx guard message", err)
	}
}

func TestPinSkill_WireAndRender(t *testing.T) {
	t.Parallel()
	reg := pinReg(t)
	tl := New(reg, nil)

	if !strings.Contains(tl.Prompt(), "Execute a skill within the main conversation") {
		t.Errorf("Prompt() = %.50q, want the skill tool prompt", tl.Prompt())
	}
	if tl.MaxResultSize() != 100000 {
		t.Errorf("MaxResultSize() = %d, want 100000", tl.MaxResultSize())
	}
	if tl.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe = true, want false")
	}
	if !tl.IsReadOnly(nil) {
		t.Error("IsReadOnly = false, want true")
	}

	wb := tl.(tool.ToolWithWireBlocks)
	// Non-skill payloads fall back to JSON text, as a single block.
	blocks := wb.FormatWireBlocks(42)
	if len(blocks) != 1 || blocks[0].Text != "42" {
		t.Errorf("non-skill wire blocks = %+v, want single JSON text block", blocks)
	}
	// Inline status announces the launch.
	blocks = wb.FormatWireBlocks(skillOutput{CommandName: "plain", Status: "inline"})
	if len(blocks) != 1 || blocks[0].Text != "Launching skill: plain" {
		t.Errorf("inline wire = %+v", blocks)
	}
	// Forked status returns the result body.
	blocks = wb.FormatWireBlocks(skillOutput{CommandName: "plain", Status: "forked", Result: "done body"})
	if len(blocks) != 1 || !strings.Contains(blocks[0].Text, "done body") {
		t.Errorf("forked wire = %+v", blocks)
	}

	// TUI render counts tools and plurals correctly.
	if got := tl.RenderResult(skillOutput{CommandName: "x", AllowedTools: []string{"A"}}); got != "Successfully loaded skill · 1 tool allowed" {
		t.Errorf("render one tool = %q", got)
	}
	if got := tl.RenderResult(skillOutput{CommandName: "x", AllowedTools: []string{"A", "B"}, Model: "sonnet"}); got != "Successfully loaded skill · 2 tools allowed · sonnet" {
		t.Errorf("render tools+model = %q", got)
	}
	if got := tl.RenderResult(skillOutput{CommandName: "x", Status: "forked"}); got != "Done" {
		t.Errorf("render forked = %q", got)
	}
}

func TestPinSkill_PermissionDecision(t *testing.T) {
	t.Parallel()
	reg := pinReg(t)
	tl := New(reg, nil)
	// Safe skill auto-allows; tooled skill asks.
	if _, ok := tl.CheckPermissions(json.RawMessage(`{"skill":"plain"}`), nil).(types.PermissionAllowDecision); !ok {
		t.Error("safe skill not auto-allowed")
	}
	ask, ok := tl.CheckPermissions(json.RawMessage(`{"skill":"tooled"}`), nil).(types.PermissionAskDecision)
	if !ok {
		t.Fatalf("tooled skill decision = %#v, want ask", ask)
	}
	if ask.Message != "Execute skill: tooled" {
		t.Errorf("ask message = %q", ask.Message)
	}
	// Unknown skill asks too (no auto-allow for nil command).
	if _, ok := tl.CheckPermissions(json.RawMessage(`{"skill":"missing"}`), nil).(types.PermissionAskDecision); !ok {
		t.Error("unknown skill decision not ask")
	}
}

// ---------------------------------------------------------------------------
// budget.go — exact numeric fixtures
// ---------------------------------------------------------------------------

func TestPinSkill_BudgetEnvOverride(t *testing.T) {
	// No t.Parallel: t.Setenv.
	t.Setenv("SLASH_COMMAND_TOOL_CHAR_BUDGET", "1")
	// Budget of 1 forces the names-only path for a non-bundled skill.
	got := BuildSkillListing([]types.SkillCommand{
		{Name: "a", Description: strings.Repeat("d", 30), Source: types.SkillSourceUser},
	}, 100000)
	if got != "- a" {
		t.Errorf("budget=1 listing = %q, want names-only", got)
	}
}

func TestPinSkill_BudgetSingleToken(t *testing.T) {
	t.Parallel()
	// A 1-token window yields a near-zero budget (names-only), not the
	// 8000-char default.
	got := BuildSkillListing([]types.SkillCommand{
		{Name: "a", Description: strings.Repeat("d", 30), Source: types.SkillSourceUser},
	}, 1)
	if got != "- a" {
		t.Errorf("1-token listing = %q, want names-only", got)
	}
}

func TestPinSkill_DescTruncation(t *testing.T) {
	t.Parallel()
	// Exactly at the cap: no truncation.
	at := strings.Repeat("d", MaxListingDescChars)
	if got := getCommandDescription(types.SkillCommand{Description: at}); got != at {
		t.Errorf("desc at cap truncated to %d runes", len([]rune(got)))
	}
	// Above the cap: exactly MaxListingDescChars runes ending with ellipsis.
	over := strings.Repeat("d", MaxListingDescChars+10)
	got := getCommandDescription(types.SkillCommand{Description: over})
	if runeCount(got) != MaxListingDescChars {
		t.Errorf("truncated desc len = %d runes, want %d", runeCount(got), MaxListingDescChars)
	}
	if !strings.HasSuffix(got, "\u2026") {
		t.Errorf("truncated desc lacks ellipsis: %q", got[len(got)-5:])
	}
	// WhenToUse is appended before truncation applies.
	got = getCommandDescription(types.SkillCommand{Description: "base", WhenToUse: strings.Repeat("w", MaxListingDescChars)})
	if !strings.HasPrefix(got, "base - ") {
		t.Errorf("whenToUse desc = %q", got[:20])
	}
}

func TestPinSkill_BudgetNewlineCounting(t *testing.T) {
	// No t.Parallel: t.Setenv.
	// Single 15-char entry with budget exactly 15: the newline surcharge
	// applies only after the first entry (i > 0), so it still fits.
	t.Setenv("SLASH_COMMAND_TOOL_CHAR_BUDGET", "15")
	desc := strings.Repeat("d", 10)
	got := BuildSkillListing([]types.SkillCommand{
		{Name: "a", Description: desc, Source: types.SkillSourceUser},
	}, 1)
	want := "- a: " + desc
	if got != want {
		t.Errorf("exact-budget listing = %q, want full entry", got)
	}
}

func TestPinSkill_BudgetRestOverhead(t *testing.T) {
	// No t.Parallel: t.Setenv.
	// Two rest entries, budget 50: overhead 5+5+1 = 11, avail 39, maxDesc
	// 19 -> names-only. Dropping the separator overhead gives avail 40,
	// maxDesc 20 -> truncation path. The outputs differ, pinning the +1.
	t.Setenv("SLASH_COMMAND_TOOL_CHAR_BUDGET", "50")
	desc := strings.Repeat("d", 25)
	got := BuildSkillListing([]types.SkillCommand{
		{Name: "a", Description: desc, Source: types.SkillSourceUser},
		{Name: "b", Description: desc, Source: types.SkillSourceUser},
	}, 1)
	if got != "- a\n- b" {
		t.Errorf("budget-50 listing = %q, want names-only", got)
	}
}

func TestPinSkill_BudgetTruncateAtMaxDesc(t *testing.T) {
	// No t.Parallel: t.Setenv.
	// Two rest entries, budget 89: overhead 5+5+1=11, avail 78, maxDesc 39.
	// The 43-rune desc forces phase 2; the 39-rune desc sits exactly at
	// maxDesc and must NOT be truncated (strict >).
	t.Setenv("SLASH_COMMAND_TOOL_CHAR_BUDGET", "89")
	atMax := strings.Repeat("x", 38) + "Z"
	over := strings.Repeat("y", 43)
	got := BuildSkillListing([]types.SkillCommand{
		{Name: "a", Description: atMax, Source: types.SkillSourceUser},
		{Name: "b", Description: over, Source: types.SkillSourceUser},
	}, 1)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("listing lines = %d, want 2 (%q)", len(lines), got)
	}
	if lines[0] != "- a: "+atMax {
		t.Errorf("at-max desc line = %q, want untruncated", lines[0])
	}
	if !strings.HasSuffix(lines[1], "\u2026") || runeCount(lines[1]) != 5+39 {
		t.Errorf("over-max desc line = %q, want truncated to 39 runes", lines[1])
	}
}

func runeCount(s string) int { return len([]rune(s)) }
