package skills

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/liuy/gbot/pkg/markdown"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Skill frontmatter parser
// Source: src/skills/loadSkillsDir.ts:191-265 — parseSkillFrontmatterFields
// ---------------------------------------------------------------------------

// ParseSkill parses a skill directory's SKILL.md content into a SkillCommand.
// dirName is the directory name (used as skill name).
// filePath is the absolute path to SKILL.md.
// content is the raw file content.
// source identifies where this skill was loaded from.
//
// Source: loadSkillsDir.ts:407-480 — loadSkillsFromSkillsDir
func ParseSkill(dirName, filePath, content string, source types.SkillSource) *types.SkillCommand {
	parsed := markdown.ParseFrontmatter(content, filePath)
	return parseSkillFromFrontmatter(parsed.Frontmatter, parsed.Content, dirName, filePath, source)
}

// parseSkillFromFrontmatter builds a SkillCommand from parsed frontmatter and body content.
// Source: loadSkillsDir.ts:270-401 — createSkillCommand + parseSkillFrontmatterFields
func parseSkillFromFrontmatter(
	fm map[string]any,
	markdownContent string,
	skillName string,
	filePath string,
	source types.SkillSource,
) *types.SkillCommand {
	// Source: loadSkillsDir.ts:237-264 — parseSkillFrontmatterFields
	displayName := stringField(fm, "name")
	description := coerceDescription(fm, skillName, markdownContent)
	hasUserSpecifiedDesc := fm["description"] != nil
	whenToUse := stringField(fm, "when_to_use")
	version := stringField(fm, "version")
	argumentHint := stringField(fm, "argument-hint")
	allowedTools := parseAllowedTools(fm["allowed-tools"])
	argumentNames := parseArgumentNames(fm["arguments"])
	paths := parseSkillPaths(fm["paths"])
	model := parseModel(fm["model"])
	effort := stringField(fm, "effort")
	disableModelInvocation := boolField(fm, "disable-model-invocation", false)
	userInvocable := boolField(fm, "user-invocable", true) // default true
	context := parseContext(fm["context"])
	agentType := stringField(fm, "agent")
	shell := parseShell(fm["shell"])
	aliases := parseStringList(fm["aliases"])

	// Source: loadSkillsDir.ts:336 — isHidden = !userInvocable
	// Source: loadSkillsDir.ts:336 — progressMessage = "running"
	// Source: loadSkillsDir.ts:334 — contentLength = markdownContent.length

	// Derive SourceDir from filePath (parent directory of SKILL.md)
	sourceDir := ""
	if idx := strings.LastIndex(filePath, "/"); idx >= 0 {
		sourceDir = filePath[:idx]
	}

	return &types.SkillCommand{
		Name:                   skillName,
		DisplayName:            displayName,
		Description:            description,
		HasUserSpecifiedDesc:   hasUserSpecifiedDesc,
		WhenToUse:              whenToUse,
		Version:                version,
		ArgumentHint:           argumentHint,
		AllowedTools:           allowedTools,
		Arguments:              toSkillArguments(argumentNames),
		Paths:                  paths,
		Model:                  model,
		Effort:                 effort,
		DisableModelInvocation: disableModelInvocation,
		IsUserInvocable:        userInvocable,
		Context:                context,
		AgentType:              agentType,
		Shell:                  shell,
		Aliases:                aliases,
		Content:                markdownContent,
		SourcePath:             filePath,
		SourceDir:              sourceDir,
		Source:                 source,
		LoadedFrom:             sourceToLoadedFrom(source),
		ContentLength:          len(markdownContent),
		ProgressMessage:        "running",
		Type:                   "prompt",
	}
}

// ---------------------------------------------------------------------------
// Field parsing helpers
// Source: src/utils/frontmatterParser.ts — parseBooleanFrontmatter, etc.
// ---------------------------------------------------------------------------

// stringField extracts a string field from frontmatter. Returns "" if absent or not a string.
func stringField(fm map[string]any, key string) string {
	v, ok := fm[key]
	if !ok || v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case int:
		return fmt.Sprintf("%d", val)
	case float64:
		return fmt.Sprintf("%v", val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

// boolField extracts a boolean field from frontmatter with a default.
// Source: frontmatterParser.ts — parseBooleanFrontmatter
func boolField(fm map[string]any, key string, defaultVal bool) bool {
	v, ok := fm[key]
	if !ok || v == nil {
		return defaultVal
	}
	switch val := v.(type) {
	case bool:
		return val
	case string:
		lower := strings.ToLower(val)
		return lower == "true"
	default:
		return defaultVal
	}
}

// coerceDescription extracts the description from frontmatter.
// Falls back to extracting from markdown content if not in frontmatter.
// Source: loadSkillsDir.ts:208-214 — coerceDescriptionToString
func coerceDescription(fm map[string]any, skillName string, content string) string {
	v := fm["description"]
	if v == nil {
		return extractDescriptionFromMarkdown(content, skillName)
	}
	switch val := v.(type) {
	case string:
		return val
	default:
		return fmt.Sprintf("%v", val)
	}
}

// extractDescriptionFromMarkdown extracts a description from the first line of markdown.
// Source: utils/markdownConfigLoader.ts — extractDescriptionFromMarkdown
func extractDescriptionFromMarkdown(content string, fallback string) string {
	lines := strings.SplitSeq(strings.TrimSpace(content), "\n")
	for line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Strip leading markdown heading markers
		trimmed = strings.TrimLeft(trimmed, "# ")
		if trimmed != "" {
			return trimmed
		}
	}
	return fallback
}

// parseStringOrArray parses a field that can be a delimiter-separated string or a string array.
// Returns nil if empty or nil input.
func parseStringOrArray(v any, splitFn func(string) []string) []string {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case string:
		if splitFn == nil {
			if val == "" {
				return nil
			}
			return []string{val}
		}
		parts := splitFn(val)
		if len(parts) == 0 {
			return nil
		}
		return parts
	case []any:
		result := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok && s != "" {
				result = append(result, s)
			}
		}
		if len(result) == 0 {
			return nil
		}
		return result
	default:
		return nil
	}
}

// parseAllowedTools parses the allowed-tools field.
// Source: utils/markdownConfigLoader.ts — parseSlashCommandToolsFromFrontmatter
// Accepts comma-separated string or string array.
func parseAllowedTools(v any) []string {
	return parseStringOrArray(v, func(s string) []string {
		parts := strings.Split(s, ",")
		result := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				result = append(result, p)
			}
		}
		return result
	})
}

// parseArgumentNames parses the arguments field.
// Source: utils/argumentSubstitution.ts:50-68 — parseArgumentNames
// Accepts space-separated string or string array.
func parseArgumentNames(v any) []string {
	return parseStringOrArray(v, strings.Fields)
}

// toSkillArguments converts argument names to SkillArgument slices.
func toSkillArguments(names []string) []types.SkillArgument {
	if len(names) == 0 {
		return nil
	}
	args := make([]types.SkillArgument, len(names))
	for i, name := range names {
		args[i] = types.SkillArgument{Name: name}
	}
	return args
}

// parseModel parses the model field.
// Source: loadSkillsDir.ts:221-226 — "inherit" maps to empty string.
func parseModel(v any) string {
	s := stringFieldFromAny(v)
	if s == "inherit" {
		return ""
	}
	return s
}

// parseContext parses the context field.
// Source: loadSkillsDir.ts:260 — 'fork' if context === 'fork', else undefined
// Correction 25: empty string = inline (default), "fork" = fork
func parseContext(v any) string {
	s := stringFieldFromAny(v)
	if s == "fork" {
		return "fork"
	}
	return "" // empty = inline (default)
}

// parseShell parses the shell field.
// Source: loadSkillsDir.ts:263 — parseShellFrontmatter
func parseShell(v any) *string {
	if v == nil {
		return nil
	}
	s := stringFieldFromAny(v)
	switch s {
	case "bash", "powershell":
		return &s
	default:
		// Invalid shell value — warn and default to nil (bash)
		if s != "" {
			slog.Warn("skills: invalid shell value, defaulting to bash", "value", s)
		}
		return nil
	}
}

// parseSkillPaths parses the paths frontmatter field.
// Source: loadSkillsDir.ts:159-183 — parseSkillPaths
// Removes /** suffix (ignore library treats path as matching contents).
func parseSkillPaths(v any) []string {
	if v == nil {
		return nil
	}

	var patterns []string
	switch val := v.(type) {
	case string:
		patterns = strings.Split(val, ",")
	case []any:
		for _, item := range val {
			if s, ok := item.(string); ok {
				patterns = append(patterns, s)
			}
		}
	default:
		return nil
	}

	var result []string
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Source: loadSkillsDir.ts:167-168 — remove /** suffix
		p = strings.TrimSuffix(p, "/**")
		// Skip match-all patterns
		if p == "*" || p == "**" || p == "" {
			continue
		}
		result = append(result, p)
	}
	return result
}

// parseStringList parses a string or array into a string slice.
func parseStringList(v any) []string {
	return parseStringOrArray(v, nil) // nil splitFn: single string → []string{val}
}

// stringFieldFromAny converts any value to a string.
func stringFieldFromAny(v any) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case int:
		return fmt.Sprintf("%d", val)
	case float64:
		return fmt.Sprintf("%v", val)
	case bool:
		if val {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", val)
	}
}

// sourceToLoadedFrom maps SkillSource to LoadedFrom string.
func sourceToLoadedFrom(source types.SkillSource) string {
	switch source {
	case types.SkillSourceBundled:
		return "bundled"
	case types.SkillSourceMCP:
		return "mcp"
	case types.SkillSourcePlugin:
		return "plugin"
	default:
		return "skills" // user, project, managed all use "skills"
	}
}
