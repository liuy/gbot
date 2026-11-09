package skill

import (
	"context"
	"encoding/json"
	"errors"
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

	result := formatCommandPermissions([]string{"Bash", "Read"}, "zhipu/glm-5.3-flash")
	if !strings.Contains(result, "<command-permissions>") {
		t.Errorf("should contain opening tag, got %q", result)
	}
	if !strings.Contains(result, "<allowed-tools>Bash,Read</allowed-tools>") {
		t.Errorf("should contain allowed tools, got %q", result)
	}
	if !strings.Contains(result, "<model>zhipu/glm-5.3-flash</model>") {
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
		Model: "zhipu/glm-5.3-flash",
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
		Model:           "zhipu/glm-5.3-flash",
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
	if data.Model != "zhipu/glm-5.3-flash" {
		t.Errorf("Model = %q, want %q", data.Model, "zhipu/glm-5.3-flash")
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
		Model:        "zhipu/glm-5.3-flash",
	})
	if !strings.Contains(got, "Successfully loaded skill") {
		t.Errorf("should contain base message, got %q", got)
	}
	if !strings.Contains(got, "3 tools allowed") {
		t.Errorf("should contain tool count, got %q", got)
	}
	if !strings.Contains(got, "zhipu/glm-5.3-flash") {
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
