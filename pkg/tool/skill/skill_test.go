package skill

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/types"
)

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
		Name:                 "internal",
		Description:          "Internal agent skill",
		Type:                 "prompt",
		Source:               types.SkillSourceUser,
		LoadedFrom:           "skills",
		DisableModelInvocation: true,
		Content:              "Internal processing.",
	})

	return reg
}

func TestNew_CreatesTool(t *testing.T) {
	t.Parallel()

	reg := skills.NewRegistry(t.TempDir())
	tool := New(reg)

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
	tool := New(reg)

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
	tool := New(reg)

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
	tool := New(reg)

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
	tool := New(reg)

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
	tool := New(reg)

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
	tool := New(reg)

	input := json.RawMessage(`{"skill": "nonexistent"}`)
	_, err := tool.Call(context.TODO(), input, nil)
	if err == nil {
		t.Fatal("expected error for unknown skill")
	}
	if !strings.Contains(err.Error(), "unknown skill") {
		t.Errorf("error should mention unknown skill, got %q", err.Error())
	}
}

func TestTool_Call_ForkedSkill(t *testing.T) {
	reg := setupRegistry(t)
	// Register a fork skill
	reg.RegisterBundledSkill(types.SkillCommand{
		Name:            "deep-review",
		Description:     "Deep code review",
		Type:            "prompt",
		Context:         "fork",
		Source:          types.SkillSourceBundled,
		LoadedFrom:      "bundled",
		IsUserInvocable: true,
		Content:         "Perform a deep review.",
	})

	tool := New(reg)
	input := json.RawMessage(`{"skill": "deep-review"}`)
	_, err := tool.Call(context.TODO(), input, nil)
	if err == nil {
		t.Fatal("expected error for fork execution (not yet implemented)")
	}
	if !strings.Contains(err.Error(), "fork execution not yet implemented") {
		t.Errorf("error = %q, want mention of fork not implemented", err.Error())
	}
}

func TestTool_CheckPermissions_SafeSkill(t *testing.T) {
	reg := setupRegistry(t)
	tool := New(reg)

	input := json.RawMessage(`{"skill": "commit"}`)
	result := tool.CheckPermissions(input, nil)

	allow, ok := result.(types.PermissionAllowDecision)
	if !ok {
		t.Errorf("safe skill should be auto-allowed, got %T: %+v", result, result)
	}
	_ = allow
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

	tool := New(reg)
	input := json.RawMessage(`{"skill": "danger"}`)
	result := tool.CheckPermissions(input, nil)

	ask, ok := result.(types.PermissionAskDecision)
	if !ok {
		t.Errorf("unsafe skill should require permission, got %T", result)
	}
	_ = ask
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
	tool := New(reg)

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
	callFn := makeSkillCallFn(reg)

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
	callFn := makeSkillCallFn(reg)

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
	tool := New(reg)

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
	tool := New(reg)

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
