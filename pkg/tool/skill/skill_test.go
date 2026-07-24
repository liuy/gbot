package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/tool"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/types"
)

// mockSubEngine implements agent.SubagentEngine for testing.
type mockSubEngine struct {
	runFn func(ctx context.Context, opts agenttool.AgentOpts) (*types.SubQueryResult, error)
}

func (m *mockSubEngine) RunAgent(ctx context.Context, opts agenttool.AgentOpts) (*types.SubQueryResult, error) {
	if m.runFn != nil {
		return m.runFn(ctx, opts)
	}
	return &types.SubQueryResult{AgentType: opts.AgentType, Content: "ok"}, nil
}

// setupRegistry creates a registry with test skills.
func setupRegistry(t *testing.T) *skills.Registry {
	t.Helper()
	reg := skills.NewRegistry(t.TempDir())

	// Manually register skills
	reg.RegisterBundledSkill(types.SkillCommand{
		Name:            "commit",
		Description:     "Create a git commit",
		Type:            "prompt",
		Source:          types.SkillSourceBundled,
		LoadedFrom:      "bundled",
		IsUserInvocable: true,
		Content:         "Create a commit following conventions.",
	})

	reg.RegisterBundledSkill(types.SkillCommand{
		Name:            "review",
		Description:     "Review code",
		Type:            "prompt",
		Source:          types.SkillSourceBundled,
		LoadedFrom:      "bundled",
		IsUserInvocable: true,
		Content:         "Review the code changes.",
	})

	reg.RegisterBundledSkill(types.SkillCommand{
		Name:                   "internal",
		Description:            "Internal agent skill",
		Type:                   "prompt",
		Source:                 types.SkillSourceUser,
		LoadedFrom:             "skills",
		DisableModelInvocation: true,
		Content:                "Internal processing.",
	})

	return reg
}

func TestNew_CreatesTool(t *testing.T) {
	t.Parallel()

	reg := skills.NewRegistry(t.TempDir())
	tool := New(reg, nil)

	if tool.Name() != "Skill" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "Skill")
	}
	if !tool.IsReadOnly(nil) {
		t.Error("SkillTool should be read-only")
	}
	if !tool.IsEnabled() {
		t.Error("SkillTool should be enabled by default")
	}
}

func TestNew_InputSchema(t *testing.T) {
	t.Parallel()

	reg := skills.NewRegistry(t.TempDir())
	tool := New(reg, nil)

	schema := tool.InputSchema()
	if !strings.Contains(string(schema), "skill") {
		t.Errorf("schema should contain 'skill' field, got %s", schema)
	}
	if !strings.Contains(string(schema), "args") {
		t.Errorf("schema should contain 'args' field, got %s", schema)
	}
}

func TestNew_Description(t *testing.T) {
	t.Parallel()

	reg := skills.NewRegistry(t.TempDir())
	tool := New(reg, nil)

	input := json.RawMessage(`{"skill": "commit"}`)
	desc, err := tool.Description(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(desc, "commit") {
		t.Errorf("description should contain skill name, got %q", desc)
	}
}

func TestTool_Call_Inline(t *testing.T) {
	reg := setupRegistry(t)
	tool := New(reg, nil)

	input := json.RawMessage(`{"skill": "commit"}`)
	result, err := tool.Call(context.TODO(), input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, ok := result.Data.(skillOutput)
	if !ok {
		t.Fatalf("expected skillOutput, got %T", result.Data)
	}
	if !data.Success {
		t.Error("expected success")
	}
	if data.CommandName != "commit" {
		t.Errorf("CommandName = %q, want %q", data.CommandName, "commit")
	}
	if data.Status != "inline" {
		t.Errorf("Status = %q, want %q", data.Status, "inline")
	}
	if len(result.NewMessages) < 2 {
		t.Errorf("expected at least 2 new messages (metadata + content), got %d", len(result.NewMessages))
	}
}

func TestTool_Call_WithArgs(t *testing.T) {
	reg := setupRegistry(t)
	tool := New(reg, nil)

	input := json.RawMessage(`{"skill": "commit", "args": "-m fix"}`)
	result, err := tool.Call(context.TODO(), input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data := result.Data.(skillOutput)
	if !data.Success {
		t.Error("expected success")
	}

	// Content message should contain the substituted args
	contentMsg := result.NewMessages[1]
	found := false
	for _, block := range contentMsg.Content {
		if strings.Contains(block.Text, "ARGUMENTS: -m fix") || strings.Contains(block.Text, "-m fix") {
			found = true
		}
	}
	if !found {
		t.Errorf("content message should contain args, got %+v", contentMsg.Content)
	}
}

func TestTool_Call_StripLeadingSlash(t *testing.T) {
	reg := setupRegistry(t)
	tool := New(reg, nil)

	input := json.RawMessage(`{"skill": "/commit"}`)
	result, err := tool.Call(context.TODO(), input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data := result.Data.(skillOutput)
	if data.CommandName != "commit" {
		t.Errorf("should strip leading slash, got %q", data.CommandName)
	}
}

func TestTool_Call_UnknownSkill(t *testing.T) {
	reg := setupRegistry(t)
	tool := New(reg, nil)

	input := json.RawMessage(`{"skill": "nonexistent"}`)
	_, err := tool.Call(context.TODO(), input, nil)
	if err == nil {
		t.Fatal("expected error for unknown skill")
	}
	if !strings.Contains(err.Error(), "unknown skill") {
		t.Errorf("error should mention unknown skill, got %q", err.Error())
	}
}

func TestTool_Call_NewSkill(t *testing.T) {
	reg := setupRegistry(t)
	reg.RegisterBundledSkill(types.SkillCommand{
		Name:            "deep-review",
		Description:     "Deep code review",
		Type:            "prompt",
		Context:         "new",
		Source:          types.SkillSourceBundled,
		LoadedFrom:      "bundled",
		IsUserInvocable: true,
		Content:         "Perform a deep review.",
	})

	t.Run("nil deps returns error", func(t *testing.T) {
		tool := New(reg, nil)
		input := json.RawMessage(`{"skill": "deep-review"}`)
		_, err := tool.Call(context.TODO(), input, nil)
		if err == nil {
			t.Fatal("expected error for new with nil deps")
		}
		if !strings.Contains(err.Error(), "no sub-agent engine") {
			t.Errorf("error = %q, want mention of no sub-agent engine", err.Error())
		}
	})

	t.Run("engine invoked", func(t *testing.T) {
		var capturedOpts agenttool.AgentOpts
		mockAgent := agenttool.New()
		mockAgent.SetEngine(&mockSubEngine{runFn: func(ctx context.Context, opts agenttool.AgentOpts) (*types.SubQueryResult, error) {
			capturedOpts = opts
			return &types.SubQueryResult{Content: "Review complete."}, nil
		}})

		skillTool := New(reg, mockAgent)
		input := json.RawMessage(`{"skill": "deep-review"}`)
		result, err := skillTool.Call(context.TODO(), input, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if capturedOpts.Prompt != "Perform a deep review." {
			t.Errorf("prompt = %q, want skill content", capturedOpts.Prompt)
		}
		if capturedOpts.AgentType != "" {
			t.Errorf("agentType = %q, want empty (resolved by Engine.RunAgent)", capturedOpts.AgentType)
		}
		if len(capturedOpts.ForkMessages) != 0 {
			t.Errorf("ForkMessages should be empty for context=new, got %d", len(capturedOpts.ForkMessages))
		}

		out, ok := result.Data.(skillOutput)
		if !ok {
			t.Fatalf("result.Data type = %T, want skillOutput", result.Data)
		}
		if out.Status != "forked" {
			t.Errorf("status = %q, want forked", out.Status)
		}
		if out.Result != "Review complete." {
			t.Errorf("result = %q, want factory content", out.Result)
		}

		agentID := "skill-new:deep-review"
		invoked := reg.GetInvokedSkillsForAgent(agentID)
		if len(invoked) != 0 {
			t.Errorf("invoked skills for %s = %d, want 0 after cleanup", agentID, len(invoked))
		}
	})

	t.Run("factory error cleans up invoked skill", func(t *testing.T) {
		mockAgent := agenttool.New()
		mockAgent.SetEngine(&mockSubEngine{runFn: func(ctx context.Context, opts agenttool.AgentOpts) (*types.SubQueryResult, error) {
			return nil, fmt.Errorf("sub-agent crashed")
		}})

		skillTool := New(reg, mockAgent)
		input := json.RawMessage(`{"skill": "deep-review"}`)
		_, err := skillTool.Call(context.TODO(), input, nil)
		if err == nil {
			t.Fatal("expected error when factory returns error")
		}
		if !strings.Contains(err.Error(), "sub-agent crashed") {
			t.Errorf("error = %q, want mention of sub-agent crashed", err.Error())
		}

		agentID := "skill-new:deep-review"
		invoked := reg.GetInvokedSkillsForAgent(agentID)
		if len(invoked) != 0 {
			t.Errorf("invoked skills for %s = %d, want 0 after error", agentID, len(invoked))
		}
	})
}

func TestTool_Call_ForkSkill(t *testing.T) {
	reg := setupRegistry(t)
	reg.RegisterBundledSkill(types.SkillCommand{
		Name:            "forked-review",
		Description:     "Forked review",
		Type:            "prompt",
		Context:         "fork",
		Source:          types.SkillSourceBundled,
		LoadedFrom:      "bundled",
		IsUserInvocable: true,
		Content:         "Continue the review.",
	})

	t.Run("nil deps returns error", func(t *testing.T) {
		tool := New(reg, nil)
		input := json.RawMessage(`{"skill": "forked-review"}`)
		_, err := tool.Call(context.TODO(), input, nil)
		if err == nil {
			t.Fatal("expected error for fork with nil deps")
		}
		if !strings.Contains(err.Error(), "no sub-agent engine") {
			t.Errorf("error = %q, want mention of no sub-agent engine", err.Error())
		}
	})

	t.Run("nil tctx returns error", func(t *testing.T) {
		mockAgent := agenttool.New()
		mockAgent.SetEngine(&mockSubEngine{})
		mockAgent.SetNotifyFn(func(string) {}, func() string { return "parent prompt" })

		skillTool := New(reg, mockAgent)
		input := json.RawMessage(`{"skill": "forked-review"}`)
		_, err := skillTool.Call(context.TODO(), input, nil)
		if err == nil {
			t.Fatal("expected error for fork with nil tctx")
		}
		if !strings.Contains(err.Error(), "parent messages unavailable") {
			t.Errorf("error = %q, want mention of parent messages unavailable", err.Error())
		}
	})

	t.Run("missing SysPromptFn returns error", func(t *testing.T) {
		// AgentTool without SetNotifyFn wired → SubagentDeps().SysPromptFn is nil.
		// SkillTool derives from AgentTool, so we construct one directly and
		// set only the engine (skip SetNotifyFn).
		mockAgent := agenttool.New()
		mockAgent.SetEngine(&mockSubEngine{})

		skillTool := New(reg, mockAgent)
		tctx := &tool.ToolUseContext{Messages: []types.Message{{Role: types.RoleUser}}}
		input := json.RawMessage(`{"skill": "forked-review"}`)
		_, err := skillTool.Call(context.TODO(), input, tctx)
		if err == nil {
			t.Fatal("expected error when SysPromptFn missing")
		}
		if !strings.Contains(err.Error(), "SysPromptFn not wired") {
			t.Errorf("error = %q, want mention of SysPromptFn not wired", err.Error())
		}
	})

	t.Run("engine invoked with fork messages", func(t *testing.T) {
		var capturedOpts agenttool.AgentOpts
		mockAgent := agenttool.New()
		mockAgent.SetEngine(&mockSubEngine{runFn: func(ctx context.Context, opts agenttool.AgentOpts) (*types.SubQueryResult, error) {
			capturedOpts = opts
			return &types.SubQueryResult{Content: "Fork done."}, nil
		}})
		mockAgent.SetNotifyFn(func(string) {}, func() string { return "parent system prompt" })

		skillTool := New(reg, mockAgent)

		// tctx with parent messages — fork should inherit these.
		tctx := &tool.ToolUseContext{
			Messages: []types.Message{
				{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("user asked something")}},
				{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("assistant replied")}},
			},
		}
		input := json.RawMessage(`{"skill": "forked-review"}`)
		result, err := skillTool.Call(context.TODO(), input, tctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(capturedOpts.ForkMessages) == 0 {
			t.Fatal("ForkMessages should be populated for context=fork")
		}
		if capturedOpts.SystemPrompt != "parent system prompt" {
			t.Errorf("SystemPrompt = %q, want parent system prompt", capturedOpts.SystemPrompt)
		}
		if capturedOpts.Prompt != "" {
			t.Errorf("Prompt should be empty for fork (embedded in ForkMessages), got %q", capturedOpts.Prompt)
		}

		out, ok := result.Data.(skillOutput)
		if !ok {
			t.Fatalf("result.Data type = %T, want skillOutput", result.Data)
		}
		if out.Status != "forked" {
			t.Errorf("status = %q, want forked", out.Status)
		}
		if out.Result != "Fork done." {
			t.Errorf("result = %q, want Fork done.", out.Result)
		}

		agentID := "skill-fork:forked-review"
		invoked := reg.GetInvokedSkillsForAgent(agentID)
		if len(invoked) != 0 {
			t.Errorf("invoked skills for %s = %d, want 0 after cleanup", agentID, len(invoked))
		}
	})
}

func TestTool_Call_InvalidContext(t *testing.T) {
	reg := setupRegistry(t)
	reg.RegisterBundledSkill(types.SkillCommand{
		Name:            "broken",
		Type:            "prompt",
		Context:         "weird",
		Source:          types.SkillSourceBundled,
		LoadedFrom:      "bundled",
		IsUserInvocable: true,
		Content:         "noop",
	})

	tool := New(reg, nil)
	input := json.RawMessage(`{"skill": "broken"}`)
	_, err := tool.Call(context.TODO(), input, nil)
	if err == nil {
		t.Fatal("expected error for invalid context")
	}
	if !strings.Contains(err.Error(), "invalid context") {
		t.Errorf("error = %q, want mention of invalid context", err.Error())
	}
	if !strings.Contains(err.Error(), "inline|new|fork") {
		t.Errorf("error = %q, want hint about allowed values", err.Error())
	}
}

func TestTool_CheckPermissions_SafeSkill(t *testing.T) {
	reg := setupRegistry(t)
	tool := New(reg, nil)

	input := json.RawMessage(`{"skill": "commit"}`)
	result := tool.CheckPermissions(input, nil)

	_, ok := result.(types.PermissionAllowDecision)
	if !ok {
		t.Fatalf("safe skill should be auto-allowed, got %T: %+v", result, result)
	}
	// PermissionAllowDecision is an empty struct; the type assertion above
	// already proves the correct decision was made.
}

func TestTool_CheckPermissions_UnsafeSkill(t *testing.T) {
	reg := skills.NewRegistry(t.TempDir())
	// Register skill with allowed-tools (unsafe)
	reg.RegisterBundledSkill(types.SkillCommand{
		Name:            "danger",
		Description:     "Dangerous skill",
		Type:            "prompt",
		Source:          types.SkillSourceUser,
		LoadedFrom:      "skills",
		IsUserInvocable: true,
		AllowedTools:    []string{"Bash"},
		Content:         "Do something dangerous.",
	})

	tool := New(reg, nil)
	input := json.RawMessage(`{"skill": "danger"}`)
	result := tool.CheckPermissions(input, nil)

	ask, ok := result.(types.PermissionAskDecision)
	if !ok {
		t.Errorf("unsafe skill should require permission, got %T", result)
	}
	// Verify the ask decision carries context about the skill
	if ask.Message == "" {
		t.Error("PermissionAskDecision.Message should not be empty")
	}
}

func TestFormatCommandLoadingMetadata_UserInvocable(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{Name: "commit", IsUserInvocable: true}
	result := formatCommandLoadingMetadata(cmd, "-m fix")

	if !strings.Contains(result, "<command-message>commit</command-message>") {
		t.Errorf("should contain command-message tag, got %q", result)
	}
	if !strings.Contains(result, "<command-name>/commit</command-name>") {
		t.Errorf("should contain command-name tag with slash, got %q", result)
	}
	if !strings.Contains(result, "<command-args>-m fix</command-args>") {
		t.Errorf("should contain command-args, got %q", result)
	}
}

func TestFormatCommandLoadingMetadata_ModelOnly(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{
		Name:            "internal",
		IsUserInvocable: false,
		LoadedFrom:      "skills",
	}
	result := formatCommandLoadingMetadata(cmd, "")

	if !strings.Contains(result, "<skill-format>true</skill-format>") {
		t.Errorf("model-only skill should have skill-format tag, got %q", result)
	}
	if !strings.Contains(result, "<command-name>internal</command-name>") {
		t.Errorf("should contain command-name tag without slash, got %q", result)
	}
}

func TestFormatCommandLoadingMetadata_NoArgs(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{Name: "commit", IsUserInvocable: true}
	result := formatCommandLoadingMetadata(cmd, "")

	if strings.Contains(result, "<command-args>") {
		t.Errorf("should not contain command-args when no args, got %q", result)
	}
}

func TestFormatCommandPermissions(t *testing.T) {
	t.Parallel()

	result := formatCommandPermissions([]string{"Bash", "Read"}, "haiku")
	if !strings.Contains(result, "<command-permissions>") {
		t.Errorf("should contain opening tag, got %q", result)
	}
	if !strings.Contains(result, "<allowed-tools>Bash,Read</allowed-tools>") {
		t.Errorf("should contain allowed tools, got %q", result)
	}
	if !strings.Contains(result, "<model>haiku</model>") {
		t.Errorf("should contain model, got %q", result)
	}
	if !strings.Contains(result, "</command-permissions>") {
		t.Errorf("should contain closing tag, got %q", result)
	}
}

func TestFormatCommandPermissions_Empty(t *testing.T) {
	t.Parallel()

	result := formatCommandPermissions(nil, "")
	if result != "" {
		// Should produce a valid but minimal permissions block
		t.Errorf("expected minimal permissions for empty, got %q", result)
	}
}

func TestSkillHasOnlySafeProperties_Safe(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{
		Name:        "safe",
		Description: "A safe skill",
	}
	if !skillHasOnlySafeProperties(cmd) {
		t.Error("plain skill should be safe")
	}
}

func TestSkillHasOnlySafeProperties_Unsafe_AllowedTools(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{
		Name:         "unsafe",
		AllowedTools: []string{"Bash"},
	}
	if skillHasOnlySafeProperties(cmd) {
		t.Error("skill with AllowedTools should be unsafe")
	}
}

func TestSkillHasOnlySafeProperties_Unsafe_Model(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{
		Name:  "unsafe",
		Model: "haiku",
	}
	if skillHasOnlySafeProperties(cmd) {
		t.Error("skill with Model override should be unsafe")
	}
}

func TestSkillHasOnlySafeProperties_Unsafe_New(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{
		Name:    "unsafe",
		Context: "new",
	}
	if skillHasOnlySafeProperties(cmd) {
		t.Error("skill with new context should be unsafe")
	}
}

func TestSkillHasOnlySafeProperties_Unsafe_Fork(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{
		Name:    "unsafe",
		Context: "fork",
	}
	if skillHasOnlySafeProperties(cmd) {
		t.Error("skill with fork context should be unsafe")
	}
}

func TestTool_Prompt(t *testing.T) {
	t.Parallel()

	reg := skills.NewRegistry(t.TempDir())
	tool := New(reg, nil)

	prompt := tool.Prompt()
	if !strings.Contains(prompt, "skill") {
		t.Errorf("prompt should mention skills, got %q", prompt)
	}
	if !strings.Contains(prompt, "BLOCKING REQUIREMENT") {
		t.Errorf("prompt should contain blocking requirement, got first 100 chars: %q", prompt[:100])
	}
}

// ---------------------------------------------------------------------------
// Additional skill.go coverage
// ---------------------------------------------------------------------------

func TestArgNames_WithArgs(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{
		Arguments: []types.SkillArgument{
			{Name: "file"},
			{Name: "pattern"},
		},
	}
	names := argNames(cmd)
	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %d", len(names))
	}
	if names[0] != "file" {
		t.Errorf("names[0] = %q, want %q", names[0], "file")
	}
	if names[1] != "pattern" {
		t.Errorf("names[1] = %q, want %q", names[1], "pattern")
	}
}

func TestArgNames_Empty(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{}
	names := argNames(cmd)
	if names != nil {
		t.Errorf("empty args should return nil, got %v", names)
	}
}

func TestSkillHasOnlySafeProperties_WithShell(t *testing.T) {
	t.Parallel()

	shell := "bash"
	cmd := &types.SkillCommand{Shell: &shell}
	if skillHasOnlySafeProperties(cmd) {
		t.Error("skill with Shell should be unsafe")
	}
}

func TestFormatCommandLoadingMetadata_SkillFormat(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{
		Name:            "internal-skill",
		IsUserInvocable: false,
		LoadedFrom:      "skills",
	}
	result := formatCommandLoadingMetadata(cmd, "")
	if !strings.Contains(result, "<skill-format>true</skill-format>") {
		t.Errorf("model-only skill should have skill-format tag, got %q", result)
	}
	if !strings.Contains(result, "<command-name>internal-skill</command-name>") {
		t.Errorf("should contain command-name without slash, got %q", result)
	}
	if strings.Contains(result, "<command-name>/") {
		t.Errorf("model-only skill should not have slash in command-name, got %q", result)
	}
}

func TestFormatCommandLoadingMetadata_FallbackSlashFormat(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{
		Name:            "fallback",
		IsUserInvocable: false,
		LoadedFrom:      "other",
	}
	result := formatCommandLoadingMetadata(cmd, "")
	if !strings.Contains(result, "<command-name>/fallback</command-name>") {
		t.Errorf("fallback should use slash format, got %q", result)
	}
}

func TestMakeSkillCallFn_InvalidJSON(t *testing.T) {
	reg := skills.NewRegistry(t.TempDir())
	callFn := makeSkillCallFn(reg, nil)

	_, err := callFn(context.TODO(), json.RawMessage(`{invalid`), nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid input") {
		t.Errorf("error should mention invalid input, got %q", err.Error())
	}
}

func TestMakeSkillCallFn_StripLeadingSlash(t *testing.T) {
	reg := setupRegistry(t)
	callFn := makeSkillCallFn(reg, nil)

	input := json.RawMessage(`{"skill": "/commit"}`)
	result, err := callFn(context.TODO(), input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data := result.Data.(skillOutput)
	if data.CommandName != "commit" {
		t.Errorf("should strip leading slash, got %q", data.CommandName)
	}
}

func TestExecuteInlineSkill_WithPermissions(t *testing.T) {
	reg := skills.NewRegistry(t.TempDir())
	cmd := &types.SkillCommand{
		Name:            "danger",
		Description:     "Dangerous skill",
		Type:            "prompt",
		Source:          types.SkillSourceUser,
		LoadedFrom:      "skills",
		IsUserInvocable: true,
		AllowedTools:    []string{"Bash", "Read"},
		Model:           "haiku",
		Content:         "Do something dangerous.",
	}

	result, err := executeInlineSkill(cmd, "danger", "", reg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data := result.Data.(skillOutput)
	if !data.Success {
		t.Error("expected success")
	}
	if len(data.AllowedTools) != 2 {
		t.Errorf("AllowedTools = %v, want 2 items", data.AllowedTools)
	}
	if data.Model != "haiku" {
		t.Errorf("Model = %q, want %q", data.Model, "haiku")
	}

	// Should have 3 messages: metadata + content + permissions
	if len(result.NewMessages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result.NewMessages))
	}

	// Third message should contain permissions XML
	permsMsg := result.NewMessages[2]
	if !strings.Contains(permsMsg.Content[0].Text, "<command-permissions>") {
		t.Errorf("third message should contain permissions, got %q", permsMsg.Content[0].Text)
	}
}

func TestMakeSkillPermissionsFn_InvalidJSON(t *testing.T) {
	reg := skills.NewRegistry(t.TempDir())
	permFn := makeSkillPermissionsFn(reg)

	result := permFn(json.RawMessage(`{invalid`), nil)
	_, ok := result.(types.PermissionAllowDecision)
	if !ok {
		t.Errorf("invalid JSON should auto-allow, got %T", result)
	}
}

func TestMakeSkillPermissionsFn_SkillNotFound(t *testing.T) {
	reg := skills.NewRegistry(t.TempDir())
	permFn := makeSkillPermissionsFn(reg)

	result := permFn(json.RawMessage(`{"skill": "nonexistent"}`), nil)
	_, ok := result.(types.PermissionAskDecision)
	if !ok {
		t.Errorf("unknown skill should ask permission, got %T", result)
	}
}

func TestNew_DescriptionInvalidJSON(t *testing.T) {
	t.Parallel()

	reg := skills.NewRegistry(t.TempDir())
	tool := New(reg, nil)

	// Description with invalid JSON should return fallback
	desc, err := tool.Description(json.RawMessage(`{invalid`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc != "Execute skill" {
		t.Errorf("fallback description = %q, want %q", desc, "Execute skill")
	}
}

func TestNew_IsReadOnly(t *testing.T) {
	t.Parallel()

	reg := skills.NewRegistry(t.TempDir())
	tool := New(reg, nil)

	if !tool.IsReadOnly(json.RawMessage(`{}`)) {
		t.Error("SkillTool should be read-only")
	}
}

func TestFormatCommandLoadingMetadata_FallbackWithArgs(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{
		Name:            "fallback",
		IsUserInvocable: false,
		LoadedFrom:      "other",
	}
	result := formatCommandLoadingMetadata(cmd, "some args")
	if !strings.Contains(result, "<command-name>/fallback</command-name>") {
		t.Errorf("fallback should use slash format, got %q", result)
	}
	if !strings.Contains(result, "<command-args>some args</command-args>") {
		t.Errorf("fallback should include args, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// FormatWireBlocks + RenderResult tests
// Source: SkillTool.ts:843-861 (mapToolResultToToolResultBlockParam)
// Source: UI.tsx:20-46 (renderToolResultMessage)
// ---------------------------------------------------------------------------

func TestTool_FormatWireBlocks_Inline(t *testing.T) {
	reg := setupRegistry(t)
	tk := New(reg, nil)
	wb, ok := tk.(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("SkillTool should implement ToolWithWireBlocks")
	}

	out := skillOutput{
		Success:     true,
		CommandName: "roast",
		Status:      "inline",
	}
	blocks := wb.FormatWireBlocks(out)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeText {
		t.Fatalf("blocks[0].Type = %q, want %q", blocks[0].Type, types.ContentTypeText)
	}
	want := "Launching skill: roast"
	if blocks[0].Text != want {
		t.Errorf("FormatWireBlocks(inline).Text = %q, want %q", blocks[0].Text, want)
	}
}

func TestTool_FormatWireBlocks_Forked(t *testing.T) {
	reg := setupRegistry(t)
	tk := New(reg, nil)
	wb, ok := tk.(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("SkillTool should implement ToolWithWireBlocks")
	}

	out := skillOutput{
		Success:     true,
		CommandName: "review",
		Status:      "forked",
		Result:      "LGTM",
	}
	blocks := wb.FormatWireBlocks(out)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeText {
		t.Fatalf("blocks[0].Type = %q, want %q", blocks[0].Type, types.ContentTypeText)
	}
	if !strings.Contains(blocks[0].Text, `Skill "review" completed (forked execution)`) {
		t.Errorf("FormatWireBlocks(forked).Text should contain forked message, got %q", blocks[0].Text)
	}
	if !strings.Contains(blocks[0].Text, "LGTM") {
		t.Errorf("FormatWireBlocks(forked).Text should contain result, got %q", blocks[0].Text)
	}
}

func TestTool_RenderResult_Inline(t *testing.T) {
	reg := setupRegistry(t)
	tk := New(reg, nil)

	got := tk.RenderResult(skillOutput{
		Success:     true,
		CommandName: "commit",
		Status:      "inline",
	})
	if !strings.Contains(got, "Successfully loaded skill") {
		t.Errorf("RenderResult should contain 'Successfully loaded skill', got %q", got)
	}
}

func TestTool_RenderResult_WithToolsAndModel(t *testing.T) {
	reg := setupRegistry(t)
	tk := New(reg, nil)

	got := tk.RenderResult(skillOutput{
		Success:      true,
		CommandName:  "danger",
		Status:       "inline",
		AllowedTools: []string{"Bash", "Read", "Write"},
		Model:        "haiku",
	})
	if !strings.Contains(got, "Successfully loaded skill") {
		t.Errorf("should contain base message, got %q", got)
	}
	if !strings.Contains(got, "3 tools allowed") {
		t.Errorf("should contain tool count, got %q", got)
	}
	if !strings.Contains(got, "haiku") {
		t.Errorf("should contain model, got %q", got)
	}
}

func TestTool_RenderResult_SingleTool(t *testing.T) {
	reg := setupRegistry(t)
	tk := New(reg, nil)

	got := tk.RenderResult(skillOutput{
		Success:      true,
		CommandName:  "limited",
		Status:       "inline",
		AllowedTools: []string{"Read"},
	})
	if !strings.Contains(got, "1 tool allowed") {
		t.Errorf("should say '1 tool allowed', got %q", got)
	}
}

func TestTool_RenderResult_Forked(t *testing.T) {
	reg := setupRegistry(t)
	tk := New(reg, nil)

	got := tk.RenderResult(skillOutput{
		Success:     true,
		CommandName: "review",
		Status:      "forked",
	})
	if got != "Done" {
		t.Errorf("RenderResult(forked) = %q, want %q", got, "Done")
	}
}

// TestSkill_EngineWiredAfterConstruction verifies that SkillTool picks up
// the engine even when SetEngine is called AFTER SubagentDeps() — which is
// what happens in production (CreateTools → SubagentDeps → WireEngine → SetEngine).
//
// Without a lazy accessor, SubagentDeps captures a nil engine at construction
// time and context=new skills fail with "no sub-agent engine".
func TestSkill_EngineWiredAfterConstruction(t *testing.T) {
	t.Parallel()

	reg := skills.NewRegistry(t.TempDir())
	reg.RegisterBundledSkill(types.SkillCommand{
		Name:            "wired-test",
		Type:            "prompt",
		Context:         "new",
		AgentType:       "General",
		IsUserInvocable: true,
		Source:          types.SkillSourceBundled,
		Content:         "Do the thing.",
	})

	// Construct agent + capture deps BEFORE wiring engine (mirrors CreateTools).
	mockAgent := agenttool.New()
	skillTool := New(reg, mockAgent)

	// Wire engine AFTER construction (mirrors WireEngine).
	var capturedOpts agenttool.AgentOpts
	mockAgent.SetEngine(&mockSubEngine{runFn: func(ctx context.Context, opts agenttool.AgentOpts) (*types.SubQueryResult, error) {
		capturedOpts = opts
		return &types.SubQueryResult{Content: "sub-agent ran"}, nil
	}})

	// Invoke the skill via the tool — must not error with "no sub-agent engine".
	input := `{"skill":"wired-test"}`
	result, err := skillTool.Call(context.Background(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("skill call failed: %v — engine wiring is stale", err)
	}
	if result == nil {
		t.Fatal("nil result")
	}
	// Verify the sub-agent was actually invoked.
	if capturedOpts.Prompt == "" {
		t.Error("sub-agent RunAgent was not called — engine wiring did not propagate")
	}
}

func TestSkill_DecodeResult_ArrayForm(t *testing.T) {
	t.Parallel()

	reg := skills.NewRegistry("")
	tt := New(reg, nil)
	inner := `{"success":true,"commandName":"commit","status":"forked"}`
	textBytes, _ := json.Marshal(inner)
	raw := json.RawMessage(`[{"type":"text","text":` + string(textBytes) + `}]`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	o, ok := v.(skillOutput)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want skillOutput", v)
	}
	if !o.Success {
		t.Errorf("Success = false, want true")
	}
	if o.CommandName != "commit" {
		t.Errorf("CommandName = %q, want commit", o.CommandName)
	}
	if o.Status != "forked" {
		t.Errorf("Status = %q, want forked", o.Status)
	}
}

func TestSkill_DecodeResult_RejectsBareStruct(t *testing.T) {
	t.Parallel()

	reg := skills.NewRegistry("")
	tt := New(reg, nil)
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(json.RawMessage(`{"success":true}`))
	if err == nil {
		t.Error("DecodeResult must reject bare struct form")
	}
}
