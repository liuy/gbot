package skill

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/tool"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/types"
)

func xs(n int) string { return strings.Repeat("x", n) }

// Phase 1 keeps full descriptions when the total is exactly the budget.
func TestBuildSkillListing_ExactlyAtBudgetKeepsFull(t *testing.T) {
	t.Setenv("SLASH_COMMAND_TOOL_CHAR_BUDGET", "13")
	got := BuildSkillListing([]types.SkillCommand{
		{Name: "a", Description: "A"},
		{Name: "b", Description: "B"},
	}, 0)
	if got != "- a: A\n- b: B" {
		t.Errorf("listing = %q, want %q (one newline between two 6-rune lines)", got, "- a: A\n- b: B")
	}
}

// Bundled entries contribute their full width (plus newline) to the budget.
func TestBuildSkillListing_BundledWidthAccounting(t *testing.T) {
	t.Setenv("SLASH_COMMAND_TOOL_CHAR_BUDGET", "70")
	got := BuildSkillListing([]types.SkillCommand{
		{Name: "b1", Description: xs(10), Source: types.SkillSourceBundled},
		{Name: "b2", Description: xs(20), Source: types.SkillSourceBundled},
		{Name: "r", Description: xs(50), Source: types.SkillSourceUser},
	}, 0)
	want := "- b1: " + xs(10) + "\n- b2: " + xs(20) + "\n- r: " + xs(20) + "…"
	if got != want {
		t.Errorf("listing =\n%q\nwant\n%q", got, want)
	}
}

// Per-name overhead ("- name: " plus separators) is summed over the rest set.
func TestBuildSkillListing_RestNameOverheadAccounting(t *testing.T) {
	t.Setenv("SLASH_COMMAND_TOOL_CHAR_BUDGET", "61")
	got := BuildSkillListing([]types.SkillCommand{
		{Name: "aa", Description: xs(50), Source: types.SkillSourceUser},
		{Name: "bbbb", Description: xs(50), Source: types.SkillSourceUser},
	}, 0)
	want := "- aa: " + xs(22) + "…\n- bbbb: " + xs(22) + "…"
	if got != want {
		t.Errorf("listing =\n%q\nwant\n%q", got, want)
	}
}

// maxDescLen of exactly MinDescLength still truncates; one less goes names-only.
func TestBuildSkillListing_MinDescLengthBoundary(t *testing.T) {
	skillsIn := []types.SkillCommand{{Name: "r", Description: xs(50), Source: types.SkillSourceUser}}

	t.Setenv("SLASH_COMMAND_TOOL_CHAR_BUDGET", "25") // maxDescLen = 20
	got := BuildSkillListing(skillsIn, 0)
	if want := "- r: " + xs(19) + "…"; got != want {
		t.Errorf("maxDescLen 20 listing = %q, want %q", got, want)
	}

	t.Setenv("SLASH_COMMAND_TOOL_CHAR_BUDGET", "24") // maxDescLen = 19
	got = BuildSkillListing(skillsIn, 0)
	if want := "- r"; got != want {
		t.Errorf("maxDescLen 19 listing = %q, want %q", got, want)
	}
}

// One rune over budget drops the descriptions entirely.
func TestBuildSkillListing_OneOverBudgetGoesNamesOnly(t *testing.T) {
	t.Setenv("SLASH_COMMAND_TOOL_CHAR_BUDGET", "12")
	got := BuildSkillListing([]types.SkillCommand{
		{Name: "a", Description: "A"},
		{Name: "b", Description: "B"},
	}, 0)
	if got != "- a\n- b" {
		t.Errorf("listing = %q, want %q (13 runes of full text over a 12 budget)", got, "- a\n- b")
	}
}

func TestFormatCommandPermissions_Matrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		tools []string
		model string
		want  string
	}{
		{nil, "", ""},
		{[]string{}, "", ""},
		{[]string{"Read"}, "", "<command-permissions>\n  <allowed-tools>Read</allowed-tools>\n</command-permissions>"},
		{nil, "zhipu/glm-5.3-flash", "<command-permissions>\n  <model>zhipu/glm-5.3-flash</model>\n</command-permissions>"},
		{[]string{"Read", "Grep"}, "zhipu/glm-5.3-flash", "<command-permissions>\n  <allowed-tools>Read,Grep</allowed-tools>\n  <model>zhipu/glm-5.3-flash</model>\n</command-permissions>"},
	}
	for _, c := range cases {
		got := formatCommandPermissions(c.tools, c.model)
		if got != c.want {
			t.Errorf("formatCommandPermissions(%v, %q) = %q, want %q", c.tools, c.model, got, c.want)
		}
	}
}

func TestFormatCommandLoadingMetadata_LoadedFrom(t *testing.T) {
	t.Parallel()
	skillShape := "<command-message>n</command-message>\n<command-name>n</command-name>\n<skill-format>true</skill-format>"
	slashShape := "<command-message>n</command-message>\n<command-name>/n</command-name>"
	for _, from := range []string{"skills", "plugin", "mcp"} {
		cmd := &types.SkillCommand{Name: "n", LoadedFrom: from}
		if got := formatCommandLoadingMetadata(cmd, "ignored"); got != skillShape {
			t.Errorf("LoadedFrom %q = %q, want the skill-format shape", from, got)
		}
	}
	for _, from := range []string{"bundled", ""} {
		cmd := &types.SkillCommand{Name: "n", LoadedFrom: from}
		if got := formatCommandLoadingMetadata(cmd, ""); got != slashShape {
			t.Errorf("LoadedFrom %q = %q, want the slash shape", from, got)
		}
	}
	cmd := &types.SkillCommand{Name: "n", LoadedFrom: "bundled"}
	withArgs := slashShape + "\n<command-args>go</command-args>"
	if got := formatCommandLoadingMetadata(cmd, "go"); got != withArgs {
		t.Errorf("args fallback = %q, want %q", got, withArgs)
	}

	// A user-invocable skill takes the slash format even when LoadedFrom
	// would otherwise select the skill-format shape.
	invocable := &types.SkillCommand{Name: "n", LoadedFrom: "skills", IsUserInvocable: true}
	if got := formatCommandLoadingMetadata(invocable, ""); got != slashShape {
		t.Errorf("user-invocable metadata = %q, want the slash shape", got)
	}
}

func skillCmd(name string, tools []string, model string) *types.SkillCommand {
	return &types.SkillCommand{
		Name: name, Description: "d", Type: "prompt",
		Source: types.SkillSourceUser, LoadedFrom: "skills",
		IsUserInvocable: true, AllowedTools: tools, Model: model,
		Content: "body",
	}
}

// The permissions message exists iff the skill declares tools or a model.
func TestExecuteInlineSkill_PermissionsMessageShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		tools []string
		model string
		msgs  int
		perms string
	}{
		{"tools only", []string{"Read"}, "", 3, "<command-permissions>\n  <allowed-tools>Read</allowed-tools>\n</command-permissions>"},
		{"model only", nil, "zhipu/glm-5.3-flash", 3, "<command-permissions>\n  <model>zhipu/glm-5.3-flash</model>\n</command-permissions>"},
		{"neither", nil, "", 2, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reg := skills.NewRegistry(t.TempDir())
			res, err := executeInlineSkill(skillCmd("s", c.tools, c.model), "s", "", reg)
			if err != nil {
				t.Fatalf("executeInlineSkill: %v", err)
			}
			if len(res.NewMessages) != c.msgs {
				t.Fatalf("messages = %d, want %d", len(res.NewMessages), c.msgs)
			}
			if c.msgs == 3 {
				if got := res.NewMessages[2].Content[0].Text; got != c.perms {
					t.Errorf("permissions message = %q, want %q", got, c.perms)
				}
			}
		})
	}
}

func depsWith(engine *mockSubEngine) *agenttool.SubagentDeps {
	return &agenttool.SubagentDeps{Engine: engine, SysPromptFn: func() string { return "sys" }}
}

// A forked/new skill with no engine output falls back to the fixed sentence.
func TestExecuteForkAndNew_ResultText(t *testing.T) {
	reg := skills.NewRegistry(t.TempDir())
	tctx := &tool.ToolUseContext{ToolUseID: "tu-1", Messages: []types.Message{}}

	for _, tc := range []struct {
		name string
		run  func(ctx context.Context, opts agenttool.AgentOpts) (*types.SubQueryResult, error)
		want string
	}{
		{"content used", func(context.Context, agenttool.AgentOpts) (*types.SubQueryResult, error) {
			return &types.SubQueryResult{Content: "done"}, nil
		}, "done"},
		{"empty content falls back", func(context.Context, agenttool.AgentOpts) (*types.SubQueryResult, error) {
			return &types.SubQueryResult{Content: ""}, nil
		}, "Skill execution completed"},
		{"nil result falls back", func(context.Context, agenttool.AgentOpts) (*types.SubQueryResult, error) {
			return nil, nil
		}, "Skill execution completed"},
	} {
		res, err := executeForkSkill(context.Background(), skillCmd("f", nil, ""), "f", "", reg,
			depsWith(&mockSubEngine{runFn: tc.run}), tctx)
		if err != nil {
			t.Fatalf("fork %s: %v", tc.name, err)
		}
		if got := res.Data.(skillOutput).Result; got != tc.want {
			t.Errorf("fork result (%s) = %q, want %q", tc.name, got, tc.want)
		}

		res, err = executeNewSkill(context.Background(), skillCmd("n", nil, ""), "n", "", reg,
			depsWith(&mockSubEngine{runFn: tc.run}), tctx)
		if err != nil {
			t.Fatalf("new %s: %v", tc.name, err)
		}
		if got := res.Data.(skillOutput).Result; got != tc.want {
			t.Errorf("new result (%s) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Engine failures stay attributable through the wrap chain.
func TestExecuteForkAndNew_ErrorWrapping(t *testing.T) {
	reg := skills.NewRegistry(t.TempDir())
	tctx := &tool.ToolUseContext{ToolUseID: "tu-2", Messages: []types.Message{}}
	boom := errors.New("engine down")
	failing := &mockSubEngine{runFn: func(context.Context, agenttool.AgentOpts) (*types.SubQueryResult, error) {
		return nil, boom
	}}

	_, err := executeForkSkill(context.Background(), skillCmd("f", nil, ""), "f", "", reg, depsWith(failing), tctx)
	if err == nil {
		t.Fatal("fork must surface the engine error")
	}
	if err.Error() != `skill "f" (fork): engine down` {
		t.Errorf("fork error = %q, want %q", err.Error(), `skill "f" (fork): engine down`)
	}
	if !errors.Is(err, boom) {
		t.Errorf("fork error %v must wrap the engine error", err)
	}

	_, err = executeNewSkill(context.Background(), skillCmd("n", nil, ""), "n", "", reg, depsWith(failing), tctx)
	if err == nil {
		t.Fatal("new must surface the engine error")
	}
	if err.Error() != `skill "n" (new): engine down` {
		t.Errorf("new error = %q, want %q", err.Error(), `skill "n" (new): engine down`)
	}
	if !errors.Is(err, boom) {
		t.Errorf("new error %v must wrap the engine error", err)
	}
}

// The sub-agent options must carry the skill's declared model, agent type,
// tool allow-list, parent tool-use id and substituted prompt.
func TestExecuteForkAndNew_AgentOpts(t *testing.T) {
	reg := skills.NewRegistry(t.TempDir())
	tctx := &tool.ToolUseContext{ToolUseID: "tu-9", Messages: []types.Message{}}

	var got agenttool.AgentOpts
	capture := &mockSubEngine{runFn: func(_ context.Context, opts agenttool.AgentOpts) (*types.SubQueryResult, error) {
		got = opts
		return &types.SubQueryResult{Content: "ok"}, nil
	}}

	cmd := skillCmd("s", []string{"Read", "Grep"}, "zhipu/glm-5.3-flash")
	cmd.AgentType = "worker"
	cmd.Content = "body $ARGUMENTS"

	res, err := executeNewSkill(context.Background(), cmd, "s", "go", reg, depsWith(capture), tctx)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got.Prompt != "body go" {
		t.Errorf("new Prompt = %q, want %q (argument substitution)", got.Prompt, "body go")
	}
	if got.AgentType != "worker" || got.Model != "zhipu/glm-5.3-flash" || got.ParentToolUseID != "tu-9" {
		t.Errorf("new opts = (%q, %q, %q), want (worker, zhipu/glm-5.3-flash, tu-9)", got.AgentType, got.Model, got.ParentToolUseID)
	}
	if len(got.AllowedTools) != 2 || got.AllowedTools[0] != "Read" || got.AllowedTools[1] != "Grep" {
		t.Errorf("new AllowedTools = %v, want [Read Grep]", got.AllowedTools)
	}
	out := res.Data.(skillOutput)
	if out.Status != "forked" || out.CommandName != "s" || out.AgentID != "skill-new:s" || out.Result != "ok" {
		t.Errorf("new output = %+v, want forked/s/skill-new:s/ok", out)
	}
	if left := reg.GetInvokedSkillsForAgent("skill-new:s"); len(left) != 0 {
		t.Errorf("invoked skills for %q still present after the run: %d", "skill-new:s", len(left))
	}

	got = agenttool.AgentOpts{}
	res, err = executeForkSkill(context.Background(), cmd, "s", "go", reg, depsWith(capture), tctx)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if got.SystemPrompt != "sys" {
		t.Errorf("fork SystemPrompt = %q, want the deps prompt", got.SystemPrompt)
	}
	if got.AgentType != "worker" || got.Model != "zhipu/glm-5.3-flash" || got.ParentToolUseID != "tu-9" {
		t.Errorf("fork opts = (%q, %q, %q), want (worker, zhipu/glm-5.3-flash, tu-9)", got.AgentType, got.Model, got.ParentToolUseID)
	}
	if len(got.AllowedTools) != 2 {
		t.Errorf("fork AllowedTools = %v, want 2 entries", got.AllowedTools)
	}
	if len(got.ForkMessages) == 0 {
		t.Fatal("fork ForkMessages empty — the parent transcript must be passed")
	}
	var directive string
	for _, msg := range got.ForkMessages {
		for _, blk := range msg.Content {
			if strings.Contains(blk.Text, "body go") {
				directive = blk.Text
			}
		}
	}
	if directive == "" {
		t.Errorf("fork messages carry no directive containing %q", "body go")
	}
	if out := res.Data.(skillOutput); out.Status != "forked" || out.AgentID != "skill-fork:s" {
		t.Errorf("fork output = %+v, want status forked and agent id skill-fork:s", out)
	}
}

// A deps struct without an engine is an error, not a nil dereference.
func TestExecuteForkAndNew_NilEngineField(t *testing.T) {
	t.Parallel()
	reg := skills.NewRegistry(t.TempDir())
	tctx := &tool.ToolUseContext{ToolUseID: "tu-0", Messages: []types.Message{}}
	deps := &agenttool.SubagentDeps{SysPromptFn: func() string { return "sys" }}

	if _, err := executeNewSkill(context.Background(), skillCmd("n", nil, ""), "n", "", reg, deps, tctx); err == nil ||
		!strings.Contains(err.Error(), `skill: "n" has context=new but no sub-agent engine`) {
		t.Errorf("new with nil engine = %v, want the missing-engine error", err)
	}
	if _, err := executeForkSkill(context.Background(), skillCmd("f", nil, ""), "f", "", reg, deps, tctx); err == nil ||
		!strings.Contains(err.Error(), `skill: "f" has context=fork but no sub-agent engine`) {
		t.Errorf("fork with nil engine = %v, want the missing-engine error", err)
	}
}

func TestRenderResult_ExactShapes(t *testing.T) {
	t.Parallel()
	reg := skills.NewRegistry(t.TempDir())
	tk := New(reg, nil)
	cases := []struct {
		out  any
		want string
	}{
		{skillOutput{CommandName: "c", Status: "inline"}, "Successfully loaded skill"},
		{skillOutput{CommandName: "c", Status: "inline", AllowedTools: []string{"Read"}}, "Successfully loaded skill · 1 tool allowed"},
		{skillOutput{CommandName: "c", Status: "inline", AllowedTools: []string{"Read", "Grep"}}, "Successfully loaded skill · 2 tools allowed"},
		{skillOutput{CommandName: "c", Status: "inline", Model: "zhipu/glm-5.3-flash"}, "Successfully loaded skill · zhipu/glm-5.3-flash"},
		{skillOutput{CommandName: "c", Status: "inline", AllowedTools: []string{"Read"}, Model: "zhipu/glm-5.3-flash"}, "Successfully loaded skill · 1 tool allowed · zhipu/glm-5.3-flash"},
		{skillOutput{CommandName: "c", Status: "forked"}, "Done"},
		{42, "42"},
	}
	for _, c := range cases {
		if got := tk.RenderResult(c.out); got != c.want {
			t.Errorf("RenderResult(%v) = %q, want %q", c.out, got, c.want)
		}
	}
}

func TestDecodeResult_Errors(t *testing.T) {
	t.Parallel()
	tk := New(skills.NewRegistry(t.TempDir()), nil)
	dec := tk.(tool.ToolWithDecodeResult)

	_, err := dec.DecodeResult(json.RawMessage(`{"success":true}`))
	if err == nil {
		t.Fatal("non-array wire must be rejected")
	}
	want := `tool: UnmarshalSingleBlock expects array-form content, got "{\"success\":true}"`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}

	_, err = dec.DecodeResult(tool.WrapSingleBlock(`{"success":`))
	if err == nil {
		t.Fatal("malformed JSON wire must be rejected")
	}
	if err.Error() != "unexpected end of JSON input" {
		t.Errorf("err = %q, want the JSON error", err.Error())
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("err %v must be a *json.SyntaxError", err)
	}
}

func TestToolDef_Fields(t *testing.T) {
	t.Parallel()
	tk := New(skills.NewRegistry(t.TempDir()), nil)
	if got := tk.MaxResultSize(); got != 100000 {
		t.Errorf("MaxResultSize() = %d, want 100000", got)
	}
	if got := tk.Name(); got != skillToolName {
		t.Errorf("Name() = %q, want %q", got, skillToolName)
	}
	if got := tk.Prompt(); got != skillDescription {
		t.Errorf("Prompt() not wired to skillDescription")
	}
	if got := tk.IsConcurrencySafe(nil); got != false {
		t.Errorf("IsConcurrencySafe() = %v, want false", got)
	}
	if got := tk.IsReadOnly(nil); got != true {
		t.Errorf("IsReadOnly() = %v, want true", got)
	}
}

// The invocation audit line records whether the skill declares tools.
func TestExecuteInlineSkill_AuditLog(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	reg := skills.NewRegistry(t.TempDir())
	if _, err := executeInlineSkill(skillCmd("withtools", []string{"Read"}, "zhipu/glm-5.3-flash"), "withtools", "", reg); err != nil {
		t.Fatalf("inline: %v", err)
	}
	if _, err := executeInlineSkill(skillCmd("plain", nil, ""), "plain", "", reg); err != nil {
		t.Fatalf("inline: %v", err)
	}
	logged := buf.String()
	if !strings.Contains(logged, `msg="skill: invoked" name=withtools hasAllowedTools=true model=zhipu/glm-5.3-flash`) {
		t.Errorf("audit line for a tool-bearing skill missing; got:\n%s", logged)
	}
	if !strings.Contains(logged, `msg="skill: invoked" name=plain hasAllowedTools=false model="" source=user`) {
		t.Errorf("audit line for a plain skill missing; got:\n%s", logged)
	}
}
