package permission

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// Decision is the result of a permission check.
//
// Checker does bare-tool-name matching only.
// Content-specific matching delegated to tool.CheckPermissions().
type Decision struct {
	Action       RuleAction
	Message      string
	Rule         *Rule          // matching rule, nil for default allow
	ContentRules []Rule         // content-specific rules for tool to evaluate
}

// DefaultAllow is the default decision when no rules match.
var DefaultAllow = Decision{Action: ActionAllow}

// Checker evaluates bare-tool-name permission rules only.
// Content-specific matching delegated to tool.CheckPermissions().
// Immutable after construction — no mutex needed.
type Checker struct {
	denyByTool    map[string][]Rule // indexed by toolName
	askByTool     map[string][]Rule
	denyWildcards []Rule // ToolName contains wildcard
	askWildcards  []Rule
	allRules      []Rule // flat copy for ContentRules queries
}

// NewChecker creates an immutable Checker from rules.
// Partitions rules by action and indexes by toolName.
func NewChecker(rules []Rule) *Checker {
	c := &Checker{
		denyByTool: make(map[string][]Rule),
		askByTool:  make(map[string][]Rule),
		allRules:   rules,
	}
	for i := range rules {
		r := rules[i]
		toolName := r.Value.ToolName
		switch r.Action {
		case ActionDeny:
			if containsWildcard(toolName) {
				c.denyWildcards = append(c.denyWildcards, r)
			} else {
				c.denyByTool[toolName] = append(c.denyByTool[toolName], r)
			}
		case ActionAsk:
			if containsWildcard(toolName) {
				c.askWildcards = append(c.askWildcards, r)
			} else {
				c.askByTool[toolName] = append(c.askByTool[toolName], r)
			}
		// ActionAllow rules are ignored — gbot defaults to allow
		default:
			// ignore
		}
	}
	return c
}

// Check evaluates bare-tool-name rules against a tool invocation.
// Three-phase: deny → ask → passthrough with ContentRules.
//
// Returns ContentRules for tool-level content matching when ActionAllow.
// Early return: ~40ns when no rules configured.
func (c *Checker) Check(toolName string, input json.RawMessage) Decision {
	// Phase 1: deny bare-tool-name check
	if denyRules := c.denyByTool[toolName]; len(denyRules) > 0 {
		for i := range denyRules {
			r := denyRules[i]
			if r.Value.RuleContent == nil {
				// Bare tool name — matches all invocations
				auditLog("deny", toolName, "", &r)
				return Decision{
					Action:  ActionDeny,
					Message: fmt.Sprintf("tool %s is denied by rule from %s", toolName, r.Source),
					Rule:    &r,
				}
			}
		}
	}

	// Phase 1b: deny wildcard tool names (MCP wildcards)
	for i := range c.denyWildcards {
		r := c.denyWildcards[i]
		if matchToolWildcard(r.Value.ToolName, toolName) {
			if r.Value.RuleContent == nil {
				auditLog("deny", toolName, "", &r)
				return Decision{
					Action:  ActionDeny,
					Message: fmt.Sprintf("tool %s is denied by wildcard rule from %s", toolName, r.Source),
					Rule:    &r,
				}
			}
		}
	}

	// Phase 1c: MCP server-level deny
	// Source: permissions.ts:258-268 — bare "mcp__server" matches all tools from that server.
	if mcpInfo := MCPInfoFromString(toolName); mcpInfo != nil {
		bareServer := "mcp__" + mcpInfo.Server
		if denyRules := c.denyByTool[bareServer]; len(denyRules) > 0 {
			for i := range denyRules {
				r := denyRules[i]
				if r.Value.RuleContent == nil {
					auditLog("deny", toolName, "", &r)
					return Decision{
						Action:  ActionDeny,
						Message: fmt.Sprintf("tool %s is denied by server-level rule from %s", toolName, r.Source),
						Rule:    &r,
					}
				}
			}
		}
	}

	// Phase 2: ask bare-tool-name check
	if askRules := c.askByTool[toolName]; len(askRules) > 0 {
		for i := range askRules {
			r := askRules[i]
			if r.Value.RuleContent == nil {
				auditLog("ask", toolName, "", &r)
				return Decision{
					Action:  ActionAsk,
					Message: fmt.Sprintf("tool %s requires permission by rule from %s", toolName, r.Source),
					Rule:    &r,
				}
			}
		}
	}

	// Phase 2b: ask wildcard tool names
	for i := range c.askWildcards {
		r := c.askWildcards[i]
		if matchToolWildcard(r.Value.ToolName, toolName) {
			if r.Value.RuleContent == nil {
				auditLog("ask", toolName, "", &r)
				return Decision{
					Action:  ActionAsk,
					Message: fmt.Sprintf("tool %s requires permission by wildcard rule from %s", toolName, r.Source),
					Rule:    &r,
				}
			}
		}
	}

	// Phase 2c: MCP server-level ask
	if mcpInfo := MCPInfoFromString(toolName); mcpInfo != nil {
		bareServer := "mcp__" + mcpInfo.Server
		if askRules := c.askByTool[bareServer]; len(askRules) > 0 {
			for i := range askRules {
				r := askRules[i]
				if r.Value.RuleContent == nil {
					auditLog("ask", toolName, "", &r)
					return Decision{
						Action:  ActionAsk,
						Message: fmt.Sprintf("tool %s requires permission by server-level rule from %s", toolName, r.Source),
						Rule:    &r,
					}
				}
			}
		}
	}

	// Phase 3: passthrough — return ContentRules for tool-level matching
	contentRules := c.ContentRulesForTool(toolName)
	return Decision{
		Action:       ActionAllow,
		ContentRules: contentRules,
	}
}

// ContentRulesForTool returns content-specific rules for a tool name.
// Used by tool.CheckPermissions() to evaluate content-level matching.
func (c *Checker) ContentRulesForTool(toolName string) []Rule {
	var result []Rule

	// Exact match rules
	for i := range c.denyByTool[toolName] {
		r := c.denyByTool[toolName][i]
		if r.Value.RuleContent != nil {
			result = append(result, r)
		}
	}
	for i := range c.askByTool[toolName] {
		r := c.askByTool[toolName][i]
		if r.Value.RuleContent != nil {
			result = append(result, r)
		}
	}

	// Wildcard tool name rules
	for i := range c.denyWildcards {
		r := c.denyWildcards[i]
		if matchToolWildcard(r.Value.ToolName, toolName) && r.Value.RuleContent != nil {
			result = append(result, r)
		}
	}
	for i := range c.askWildcards {
		r := c.askWildcards[i]
		if matchToolWildcard(r.Value.ToolName, toolName) && r.Value.RuleContent != nil {
			result = append(result, r)
		}
	}

	return result
}

// HasRules returns true if any rules are configured.
func (c *Checker) HasRules() bool {
	return len(c.allRules) > 0
}

// matchShellWithXargs matches a shell rule against a command, including xargs prefix.
// Source: bashPermissions.ts:906-911 — xargs prefix matching.
// If the command starts with "xargs ", also matches the payload after "xargs ".
func matchShellWithXargs(shellRule ShellRule, cmd string) bool {
	if MatchShellCommand(shellRule, cmd) {
		return true
	}
	// xargs prefix: "xargs rm -rf /" should match deny rule "rm *"
	if payload, ok := strings.CutPrefix(cmd, "xargs "); ok {
		if MatchShellCommand(shellRule, payload) {
			return true
		}
	}
	return false
}

// CheckBashPermission checks a bash command against content rules.
// Performs: env var stripping → safe wrapper stripping → AST parsing → rule matching.
// Returns (action, matchedRule, error). Used by Bash tool's CheckPermissions.
//
// This is the main content-matching function for shell commands,
// implementing multi-layer shell command permission checking.
func CheckBashPermission(command string, contentRules []Rule) (RuleAction, *Rule, error) {
	if len(contentRules) == 0 {
		return ActionAllow, nil, nil
	}

	// Build shell rules from content rules
	shellRules := make([]struct {
		rule    Rule
		shell   ShellRule
		isDeny  bool
	}, 0, len(contentRules))
	for i := range contentRules {
		r := contentRules[i]
		if r.Value.RuleContent == nil {
			continue
		}
		pattern := *r.Value.RuleContent
		sr := ParseShellRule(pattern)
		shellRules = append(shellRules, struct {
			rule    Rule
			shell   ShellRule
			isDeny  bool
		}{rule: r, shell: sr, isDeny: r.Action == ActionDeny})
	}

	// Generate command variants for matching:
	// 1. Original command
	// 2. After stripAllLeadingEnvVars (deny/ask)
	// 3. After stripSafeWrappers
	// 4. After both
	variants := []string{command}
	envStripped := StripAllLeadingEnvVars(command)
	if envStripped != command {
		variants = append(variants, envStripped)
	}
	wrapperStripped := StripSafeWrappers(command)
	if wrapperStripped != command {
		variants = append(variants, wrapperStripped)
	}
	bothStripped := StripSafeWrappers(envStripped)
	if bothStripped != envStripped && bothStripped != command {
		variants = append(variants, bothStripped)
	}

	// For each variant, parse shell commands and match rules.
	// Early exit on first match.
	for _, variant := range variants {
		cmds, err := ParseShellCommand(variant)
		if err != nil {
			// Parse failure → deny (fail-secure)
			return ActionDeny, nil, fmt.Errorf("shell parse error: %w", err)
		}
		for _, cmd := range cmds {
			// Phase 1: deny rules (includes xargs matching)
			for _, sr := range shellRules {
				if sr.isDeny && matchShellWithXargs(sr.shell, cmd) {
					auditLog("deny", "Bash", cmd, &sr.rule)
					return ActionDeny, &sr.rule, nil
				}
			}
			// Phase 2: ask rules
			for _, sr := range shellRules {
				if !sr.isDeny && matchShellWithXargs(sr.shell, cmd) {
					auditLog("ask", "Bash", cmd, &sr.rule)
					return ActionAsk, &sr.rule, nil
				}
			}
		}
	}

	return ActionAllow, nil, nil
}

// CheckFilePermission checks a file path against content rules.
// Returns (action, matchedRule, error). Used by Write/Edit tool's CheckPermissions.
func CheckFilePermission(filePath string, contentRules []Rule) (RuleAction, *Rule, error) {
	if len(contentRules) == 0 {
		return ActionAllow, nil, nil
	}

	// Check dangerous files first (returns ask, not deny)
	if IsDangerousFilePath(filePath) {
		auditLog("ask", "Write/Edit", filePath, nil)
		return ActionAsk, nil, nil
	}

	// Match against rules
	for i := range contentRules {
		r := contentRules[i]
		if r.Value.RuleContent == nil {
			continue
		}
		matched, err := MatchFilePath(r, filePath)
		if err != nil {
			// Path safety error → deny (fail-secure)
			return ActionDeny, nil, err
		}
		if matched {
			auditLog(string(r.Action), "Write/Edit", filePath, &r)
			return r.Action, &r, nil
		}
	}

	return ActionAllow, nil, nil
}

// ExtractBashCommand extracts the command field from Bash tool JSON input.
// Fast path for the most common tool.
func ExtractBashCommand(input json.RawMessage) string {
	var v struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &v); err != nil {
		return ""
	}
	return v.Command
}

// ExtractFilePath extracts the file_path field from tool JSON input.
func ExtractFilePath(input json.RawMessage) string {
	var v struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(input, &v); err != nil {
		return ""
	}
	return v.FilePath
}

// containsWildcard checks if a tool name contains glob wildcard characters.
func containsWildcard(name string) bool {
	return strings.Contains(name, "*")
}

// matchToolWildcard matches a wildcard tool name pattern against an actual tool name.
// For MCP patterns like "mcp__server__*".
func matchToolWildcard(pattern, toolName string) bool {
	if pattern == "*" {
		return true
	}
	if !containsWildcard(pattern) {
		return pattern == toolName
	}
	// Simple prefix matching for "mcp__server__*" style patterns
	prefix := strings.TrimSuffix(pattern, "*")
	return strings.HasPrefix(toolName, prefix)
}

// MCPInfo holds parsed MCP tool name components.
// Source: permissions.ts:258-268 — mcpInfoFromString
type MCPInfo struct {
	Server string
	Tool   string
}

// MCPInfoFromString parses an MCP tool name into server and tool components.
// Source: permissions.ts:258-268
func MCPInfoFromString(toolName string) *MCPInfo {
	parts := strings.SplitN(toolName, "__", 3)
	if len(parts) < 3 || parts[0] != "mcp" {
		return nil
	}
	return &MCPInfo{Server: parts[1], Tool: parts[2]}
}

// MatchesTool checks if this MCP info matches the given tool name.
func (m *MCPInfo) MatchesTool(toolName string) bool {
	info := MCPInfoFromString(toolName)
	if info == nil {
		return false
	}
	return info.Server == m.Server
}

// auditLog logs permission decisions.
// auditLog logs permission decisions with sanitization.
func auditLog(action, toolName, detail string, rule *Rule) {
	// Sanitize for log injection
	toolName = sanitizeForLog(toolName)
	detail = sanitizeForLog(detail)

	if rule != nil {
		slog.Warn("permission decision",
			"action", action,
			"tool", toolName,
			"detail", detail,
			"rule_source", rule.Source,
			"rule_action", string(rule.Action),
		)
	} else {
		slog.Warn("permission decision",
			"action", action,
			"tool", toolName,
			"detail", detail,
		)
	}
}

// sanitizeForLog replaces control characters to prevent log injection.
// sanitizeForLog replaces control characters to prevent log injection.
func sanitizeForLog(s string) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\t", "\\t")
	return s
}

// ---------------------------------------------------------------------------
// PermissionChecker interface — engine depends on abstraction, not concrete type.
// ---------------------------------------------------------------------------

// PermissionChecker is the interface for permission rule evaluation.
// Engine depends on this interface, not the concrete Checker struct.
type PermissionChecker interface {
	Check(toolName string, input json.RawMessage) Decision
	HasRules() bool
}

// Compile-time check: *Checker implements PermissionChecker.
var _ PermissionChecker = (*Checker)(nil)

// ---------------------------------------------------------------------------
// Content checker registry — eliminates hardcoded tool name switch in engine.
// ---------------------------------------------------------------------------

// ContentCheckFunc checks content-level permissions for a tool invocation.
// Tools register their content checker via RegisterContentChecker.
type ContentCheckFunc func(input json.RawMessage, contentRules []Rule) RuleAction

// contentCheckers maps tool names to their content-level permission checkers.
var contentCheckers = map[string]ContentCheckFunc{}

// RegisterContentChecker registers a content-level permission checker for a tool.
// Each tool that needs content matching (Bash, Write, Edit) calls this in its
// constructor. New tools register themselves — engine doesn't need modification.
func RegisterContentChecker(toolName string, fn ContentCheckFunc) {
	contentCheckers[toolName] = fn
}

// CheckContent dispatches content-level matching to registered checkers.
// Returns ActionAllow for tools without registered checkers.
func CheckContent(toolName string, input json.RawMessage, contentRules []Rule) RuleAction {
	fn, ok := contentCheckers[toolName]
	if !ok {
		return ActionAllow
	}
	return fn(input, contentRules)
}
