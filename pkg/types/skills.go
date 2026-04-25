package types

import (
	"time"
)

// ---------------------------------------------------------------------------
// Skill system types
// Source: src/types/command.ts — Command / CommandBase / PromptCommand
// ---------------------------------------------------------------------------

// SkillSource identifies where a skill was loaded from.
// TS: SettingSource | 'builtin' | 'mcp' | 'plugin' | 'bundled'
type SkillSource string

const (
	SkillSourceBundled SkillSource = "bundled"  // compiled-in skills
	SkillSourceUser    SkillSource = "user"     // ~/.gbot/skills/
	SkillSourceProject SkillSource = "project"  // <gitroot>/.gbot/skills/
	SkillSourceManaged SkillSource = "managed"  // /etc/gbot/skills/ (policy)
	SkillSourceMCP     SkillSource = "mcp"      // MCP server-provided
	SkillSourcePlugin  SkillSource = "plugin"   // plugin-provided
)

// SkillCommand represents a fully loaded and parsed skill.
// TS: CommandBase & PromptCommand — src/types/command.ts
type SkillCommand struct {
	// Identity — TS: CommandBase.name, description, aliases, userFacingName
	Name                 string
	DisplayName          string   // frontmatter "name" override
	Aliases              []string // TS: CommandBase.aliases
	Description          string
	WhenToUse            string // TS: whenToUse
	HasUserSpecifiedDesc bool   // TS: hasUserSpecifiedDescription

	// Content
	Content    string // body after frontmatter
	SourcePath string // absolute path to SKILL.md
	SourceDir  string // directory (for ${SKILL_DIR})
	Source     SkillSource
	LoadedFrom string // "skills" | "bundled" | "mcp" | "plugin"

	// Execution control — TS: PromptCommand fields
	Type                   string   // "prompt" (default)
	AllowedTools           []string // nil = all tools
	Model                  string   // "", "inherit", "haiku", etc.
	Effort                 string   // "low"/"medium"/"high"/"max"
	Context                string   // "" (inline, default) or "fork"
	DisableModelInvocation bool
	IsUserInvocable        bool // default true; false = agent-only
	DisableNonInteractive  bool // TS: disableNonInteractive
	Immediate              bool // TS: immediate — bypasses queue
	IsSensitive            bool // TS: isSensitive — args redacted in history

	// Arguments — TS: argumentHint, argNames, arguments
	ArgumentHint string
	Arguments    []SkillArgument

	// Conditional activation — TS: PromptCommand.paths
	Paths []string // glob patterns

	// Hooks — TS: PromptCommand.hooks
	Hooks map[string]any // lifecycle hooks

	// Shell — TS: parseShellFrontmatter
	Shell *string // "bash"|"powershell"; nil = bash

	// Auth gating — TS: CommandBase.availability
	Availability []string // "claude-ai"|"console"

	// Plugin info — TS: PromptCommand.pluginInfo
	PluginInfo *PluginSkillInfo

	// Dynamic enablement — TS: CommandBase.isEnabled
	IsEnabledFunc func() bool

	// Metadata
	Version         string
	ProgressMessage string // TS: progressMessage (default: "running")
	ContentLength   int    // TS: contentLength
	SkillRoot       string // TS: skillRoot (for CLAUDE_PLUGIN_ROOT env)

	// AgentType — TS: frontmatter.agent — fork skills use this to select sub-agent type.
	AgentType string
}

// IsHidden returns true if the skill is not user-invocable.
// TS: loadSkillsDir.ts:335 — isHidden = userInvocable === false
func (c *SkillCommand) IsHidden() bool { return !c.IsUserInvocable }

// UserFacingName returns the display name.
// TS: getCommandName() → userFacingName || name
func (c *SkillCommand) UserFacingName() string {
	if c.DisplayName != "" {
		return c.DisplayName
	}
	return c.Name
}

// MeetsAvailabilityRequirement checks auth gating.
// TS: commands.ts:417-443
func (c *SkillCommand) MeetsAvailabilityRequirement() bool {
	if len(c.Availability) == 0 {
		return true
	}
	// gbot has no auth tiers, so all availability requirements pass.
	return true
}

// PluginSkillInfo holds plugin metadata.
// TS: command.ts:33-36 pluginInfo
type PluginSkillInfo struct {
	PluginName string
	Repository string
}

// SkillArgument defines a single skill argument.
// TS: argNames from arguments frontmatter field
type SkillArgument struct {
	Name        string
	Description string
	Required    bool
	Default     string
}

// InvokedSkillInfo tracks skills that have been invoked for compaction protection.
// TS: state.ts InvokedSkillInfo
type InvokedSkillInfo struct {
	SkillName string
	SkillPath string    // e.g., "project:commit"
	Content   string    // full expanded content
	InvokedAt time.Time
	AgentID   string    // scoping key for sub-agents
}

// CommandPermissionsAttachment represents tool/model permissions granted by a skill.
// TS: attachments.ts:604-608 — {type: 'command_permissions', allowedTools, model}
// gbot does not have an AttachmentMessage system; this is conveyed via XML user message.
type CommandPermissionsAttachment struct {
	AllowedTools []string
	Model        string
}
