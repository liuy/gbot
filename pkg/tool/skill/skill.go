package skill

// ---------------------------------------------------------------------------
// SkillTool — LLM-invocable skill execution
// Source: src/tools/SkillTool/SkillTool.ts
// ---------------------------------------------------------------------------

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

const (
	// skillToolName is the tool name as seen by the LLM.
	// TS: constants.ts:1 — SKILL_TOOL_NAME = 'Skill'
	skillToolName = "Skill"

	// commandMessageTag wraps the skill name in metadata messages.
	// TS: constants/xml.ts — COMMAND_MESSAGE_TAG
	commandMessageTag = "command-message"

	// commandNameTag wraps the skill name for LLM identification.
	// TS: constants/xml.ts — COMMAND_NAME_TAG
	commandNameTag = "command-name"
)

// skillDescription is the static tool description shown to the LLM.
// Source: SkillTool.ts:342
var skillDescription = `Execute a skill within the main conversation

When users ask you to perform tasks, check if any of the available skills match. Skills provide specialized capabilities and domain knowledge.

When users reference a "slash command" or "/<something>" (e.g., "/commit", "/review-pr"), they are referring to a skill. Use this tool to invoke it.

How to invoke:
- Use this tool with the skill name and optional arguments
- Examples:
  - skill: "pdf" - invoke the pdf skill
  - skill: "commit", args: "-m 'Fix bug'" - invoke with arguments
  - skill: "review-pr", args: "123" - invoke with arguments

Important:
- Available skills are listed in system-reminder messages in the conversation
- When a skill matches the user's request, this is a BLOCKING REQUIREMENT: invoke the relevant Skill tool BEFORE generating any other response about the task
- NEVER mention a skill without actually calling this tool
- Do not invoke a skill that is already running
- Do not use this tool for built-in CLI commands (like /help, /clear, etc.)
- If you see a <command-name> tag in the current conversation turn, the skill has ALREADY been loaded - follow the instructions directly instead of calling this tool again`

// skillInput is the JSON schema input for the Skill tool.
type skillInput struct {
	Skill string  `json:"skill"`
	Args  *string `json:"args,omitempty"`
}

// skillOutput is the tool result data.
type skillOutput struct {
	Success     bool     `json:"success"`
	CommandName string   `json:"commandName"`
	Status      string   `json:"status,omitempty"` // "inline" or "forked"
	AllowedTools []string `json:"allowedTools,omitempty"`
	Model       string   `json:"model,omitempty"`
	AgentID     string   `json:"agentId,omitempty"`
	Result      string   `json:"result,omitempty"`
}

// skillPrompt returns the static prompt for this tool.
var skillInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "skill": {
      "type": "string",
      "description": "The skill name. E.g., \"commit\", \"review-pr\", or \"pdf\""
    },
    "args": {
      "type": "string",
      "description": "Optional arguments for the skill"
    }
  },
  "required": ["skill"]
}`)


// New creates a new SkillTool using the BuildTool factory pattern.
// Source: SkillTool.ts:331 — buildTool({...})
// Correction 17: must use BuildTool factory, matching pkg/tool/bash/ etc.
func New(registry *skills.Registry) tool.Tool {
	return tool.BuildTool(tool.ToolDef{
		Name_:        skillToolName,
		Prompt_:      skillDescription,
		MaxResultSizeChars: 100000,
		InputSchema_: func() json.RawMessage { return skillInputSchema },
		Description_: func(input json.RawMessage) (string, error) {
			var in skillInput
			if err := json.Unmarshal(input, &in); err != nil {
				return "Execute skill", nil
			}
			return "Execute skill: " + in.Skill, nil
		},
		Call_: makeSkillCallFn(registry),
		CheckPermissions_: makeSkillPermissionsFn(registry),
		IsConcurrencySafe_: func(json.RawMessage) bool { return false },
		IsReadOnly_:        func(json.RawMessage) bool { return true },
	})
}

// makeSkillCallFn returns the Call implementation.
// Source: SkillTool.ts:580-780 — call()
func makeSkillCallFn(registry *skills.Registry) func(context.Context, json.RawMessage, *types.ToolUseContext) (*tool.ToolResult, error) {
	return func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
		var in skillInput
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, fmt.Errorf("skill tool: invalid input: %w", err)
		}

		// Normalize: trim and strip leading slash
		commandName := strings.TrimPrefix(strings.TrimSpace(in.Skill), "/")

		// Find the command
		cmd := registry.FindSkill(commandName)
		if cmd == nil {
			return nil, fmt.Errorf("unknown skill: %s", commandName)
		}

		args := ""
		if in.Args != nil {
			args = *in.Args
		}

		slog.Debug("skill: executing", "name", commandName, "args", args, "context", cmd.Context)

		// Fork path: if context == "fork", delegate to sub-agent
		// Source: SkillTool.ts:622-632
		if cmd.Context == "fork" {
			return executeForkedSkill(commandName)
		}

		// Inline path (default)
		return executeInlineSkill(cmd, commandName, args, registry)
	}
}

// executeInlineSkill runs a skill inline, returning messages for the conversation.
// Source: SkillTool.ts:634-780 — inline path
// Corrections 8, 9: message structure with metadata + content + permissions
func executeInlineSkill(cmd *types.SkillCommand, commandName, args string, registry *skills.Registry) (*tool.ToolResult, error) {
	// 1. Argument substitution
	// Source: argumentSubstitution.ts — substituteArguments
	content := skills.SubstituteArguments(cmd.Content, args, argNames(cmd), true)

	// 2. Variable substitution
	// Source: loadSkillsDir.ts:361-369
	content = strings.ReplaceAll(content, "${SKILL_DIR}", cmd.SourceDir)

	// 3. Shell block execution (skip for MCP skills)
	// Source: loadSkillsDir.ts:371-396
	// Note: shell execution requires a bash tool reference; for now we skip
	// actual shell execution and leave it for future integration.
	// The API is ready — ExecuteShellBlocks will be called when bash tool is wired.

	// 4. Compaction protection — record invoked skill
	// Source: state.ts:1510 — addInvokedSkill
	agentID := "" // main agent has empty ID
	registry.AddInvokedSkill(commandName, string(cmd.Source)+":"+commandName, content, agentID)

	// Log skill invocation for audit trail
	slog.Info("skill: invoked",
		"name", commandName,
		"hasAllowedTools", len(cmd.AllowedTools) > 0,
		"model", cmd.Model,
		"source", cmd.Source,
	)

	// 5. Build messages
	// Source: processSlashCommand.tsx:902-912 — message sequence
	// Correction 7: two metadata formats
	// Correction 8: metadata + content + permissions messages
	var newMessages []types.Message

	// 5a. Metadata message
	metadata := formatCommandLoadingMetadata(cmd, args)
	newMessages = append(newMessages, types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: "text", Text: metadata},
		},
	})

	// 5b. Content message (skill body with substituted args)
	newMessages = append(newMessages, types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: "text", Text: content},
		},
	})

	// 5c. Command permissions attachment
	// Correction 9: allowedTools + model as XML message
	if len(cmd.AllowedTools) > 0 || cmd.Model != "" {
		perms := formatCommandPermissions(cmd.AllowedTools, cmd.Model)
		newMessages = append(newMessages, types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: "text", Text: perms},
			},
		})
	}

	return &tool.ToolResult{
		Data: skillOutput{
			Success:      true,
			CommandName:  commandName,
			Status:       "inline",
			AllowedTools: cmd.AllowedTools,
			Model:        cmd.Model,
		},
		NewMessages: newMessages,
	}, nil
}

// executeForkedSkill runs a skill in a sub-agent.
// Source: SkillTool.ts:122-289 — executeForkedSkill
// Correction 21: skill content as user message, NOT system prompt
// Correction 26: finally block clears invoked skills
func executeForkedSkill(commandName string) (*tool.ToolResult, error) {
	// Fork execution requires agent infrastructure not yet wired.
	// Return explicit error so caller knows it's unimplemented,
	// rather than silently succeeding with no result.
	return nil, fmt.Errorf("skill: fork execution not yet implemented for %q", commandName)
}

// formatCommandLoadingMetadata returns the metadata string for a skill invocation.
// Source: processSlashCommand.tsx:786-816 — formatCommandLoadingMetadata
// Correction 7: two different formats based on user-invocable flag
func formatCommandLoadingMetadata(cmd *types.SkillCommand, args string) string {
	if cmd.IsUserInvocable {
		// Slash command format
		// Source: processSlashCommand.tsx:794-795
		parts := []string{
			fmt.Sprintf("<%s>%s</%s>", commandMessageTag, cmd.Name, commandMessageTag),
			fmt.Sprintf("<%s>/%s</%s>", commandNameTag, cmd.Name, commandNameTag),
		}
		if args != "" {
			parts = append(parts, fmt.Sprintf("<command-args>%s</command-args>", args))
		}
		return strings.Join(parts, "\n")
	}
	// Skill format (model-only skills)
	// Source: processSlashCommand.tsx:786-789
	if cmd.LoadedFrom == "skills" || cmd.LoadedFrom == "plugin" || cmd.LoadedFrom == "mcp" {
		return strings.Join([]string{
			fmt.Sprintf("<%s>%s</%s>", commandMessageTag, cmd.Name, commandMessageTag),
			fmt.Sprintf("<%s>%s</%s>", commandNameTag, cmd.Name, commandNameTag),
			"<skill-format>true</skill-format>",
		}, "\n")
	}
	// Fallback: slash command format
	return formatCommandLoadingMetadata(cmd, args)
}

// formatCommandPermissions builds the XML permissions message.
// Returns empty string if no tools or model specified.
// Correction 9: command_permissions as XML user message
func formatCommandPermissions(allowedTools []string, model string) string {
	if len(allowedTools) == 0 && model == "" {
		return ""
	}
	var parts []string
	parts = append(parts, "<command-permissions>")
	if len(allowedTools) > 0 {
		parts = append(parts, fmt.Sprintf("  <allowed-tools>%s</allowed-tools>", strings.Join(allowedTools, ",")))
	}
	if model != "" {
		parts = append(parts, fmt.Sprintf("  <model>%s</model>", model))
	}
	parts = append(parts, "</command-permissions>")
	return strings.Join(parts, "\n")
}

// makeSkillPermissionsFn returns the CheckPermissions implementation.
// Source: SkillTool.ts:432-578 — checkPermissions
func makeSkillPermissionsFn(registry *skills.Registry) func(json.RawMessage, *types.ToolUseContext) types.PermissionResult {
	return func(input json.RawMessage, tctx *types.ToolUseContext) types.PermissionResult {
		var in skillInput
		if err := json.Unmarshal(input, &in); err != nil {
			return types.PermissionAllowDecision{}
		}

		commandName := strings.TrimPrefix(strings.TrimSpace(in.Skill), "/")
		cmd := registry.FindSkill(commandName)

		// If skill has only safe properties, auto-allow
		// Source: SkillTool.ts:529-538 — skillHasOnlySafeProperties
		if cmd != nil && skillHasOnlySafeProperties(cmd) {
			return types.PermissionAllowDecision{}
		}

		// Default: ask user
		return types.PermissionAskDecision{
			Message: "Execute skill: " + commandName,
		}
	}
}

// skillHasOnlySafeProperties checks if a skill only uses safe (auto-allowable) properties.
// Source: SkillTool.ts — skillHasOnlySafeProperties
func skillHasOnlySafeProperties(cmd *types.SkillCommand) bool {
	// Skills with allowed-tools, model override, or shell are NOT safe
	if len(cmd.AllowedTools) > 0 {
		return false
	}
	if cmd.Model != "" {
		return false
	}
	if cmd.Shell != nil {
		return false
	}
	if cmd.Context == "fork" {
		return false
	}
	return true
}

// argNames extracts argument names from a SkillCommand.
func argNames(cmd *types.SkillCommand) []string {
	if len(cmd.Arguments) == 0 {
		return nil
	}
	names := make([]string, len(cmd.Arguments))
	for i, a := range cmd.Arguments {
		names[i] = a.Name
	}
	return names
}
