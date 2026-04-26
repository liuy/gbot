// Package permission implements a configurable permission rules system for tool invocations.
//
// Source reference: permissionRuleParser.ts, PermissionRule.ts
// Core algorithm aligned with TypeScript; default behavior inverted (gbot defaults allow, TS defaults ask).
package permission

import (
	"regexp"
	"strings"
)

// RuleAction is the action to take when a permission rule matches.
//
// Source: PermissionRule.ts — permissionBehaviorSchema z.enum(['allow', 'deny', 'ask'])
type RuleAction string

const (
	ActionAllow RuleAction = "allow"
	ActionDeny  RuleAction = "deny"
	ActionAsk   RuleAction = "ask"
)

// RuleValue is the parsed content of a rule string.
// Aligned with TS PermissionRuleValue (*string tri-state semantics).
//
// Source: permissionRuleParser.ts:93-133 — permissionRuleValueFromString
//
// Format: "ToolName" or "ToolName(ruleContent)".
// RuleContent nil = bare tool name (match all invocations of that tool).
// RuleContent non-nil = content-specific rule (e.g. "rm -rf *" for Bash).
type RuleValue struct {
	ToolName    string
	RuleContent *string // nil = bare tool name (match all); non-nil = content-specific rule
}

// Rule is a single permission rule with an associated action.
type Rule struct {
	Value      RuleValue
	Action     RuleAction
	Source     string // "user", "project", "local"
	ConfigRoot string // directory of the config file this rule came from (root-relative matching)
}

// ShellRuleType discriminates the parsed form of a shell command rule.
// Only Exact and Wildcard — no legacy Prefix (:* syntax) per user direction.
type ShellRuleType int

const (
	ShellRuleExact    ShellRuleType = iota // "git status"
	ShellRuleWildcard                      // "git *"
)

// ShellRule is a parsed shell command rule with optional pre-compiled regex.
type ShellRule struct {
	Type    ShellRuleType
	Pattern string
	re      *regexp.Regexp // pre-compiled for ShellRuleWildcard
}

// ParseRuleValue parses "ToolName(content)" into RuleValue.
//
// Source: permissionRuleParser.ts:93-133 — permissionRuleValueFromString
//
// Algorithm:
//  1. findFirstUnescapedChar(s, '(') — find first unescaped '('
//  2. Not found → bare tool name {toolName: s, ruleContent: nil}
//  3. findLastUnescapedChar(s, ')') — find last unescaped ')'
//  4. Not found or not at end → whole string is tool name (malformed)
//  5. Empty toolName → whole string is tool name (malformed)
//  6. Empty or "*" content → bare tool name (matches all)
//  7. Otherwise unescape content
func ParseRuleValue(ruleString string) RuleValue {
	openParen := findFirstUnescapedChar(ruleString, '(')
	if openParen == -1 {
		return RuleValue{ToolName: ruleString, RuleContent: nil}
	}

	closeParen := findLastUnescapedChar(ruleString, ')')
	if closeParen == -1 || closeParen <= openParen {
		return RuleValue{ToolName: ruleString, RuleContent: nil}
	}

	if closeParen != len(ruleString)-1 {
		return RuleValue{ToolName: ruleString, RuleContent: nil}
	}

	toolName := ruleString[:openParen]
	rawContent := ruleString[openParen+1 : closeParen]

	if toolName == "" {
		return RuleValue{ToolName: ruleString, RuleContent: nil}
	}

	if rawContent == "" || rawContent == "*" {
		return RuleValue{ToolName: toolName, RuleContent: nil}
	}

	content := UnescapeRuleContent(rawContent)
	return RuleValue{ToolName: toolName, RuleContent: &content}
}

// RuleValueToString converts a RuleValue back to its string representation.
//
// Source: permissionRuleParser.ts:144-152 — permissionRuleValueToString
func RuleValueToString(rv RuleValue) string {
	if rv.RuleContent == nil {
		return rv.ToolName
	}
	return rv.ToolName + "(" + EscapeRuleContent(*rv.RuleContent) + ")"
}

// EscapeRuleContent escapes special characters in rule content for safe storage.
//
// Source: permissionRuleParser.ts:55-60
//
// Escaping order matters:
//  1. Escape existing backslashes first (\ → \\)
//  2. Then escape parentheses (( → \(, ) → \))
func EscapeRuleContent(content string) string {
	s := strings.ReplaceAll(content, `\`, `\\`)
	s = strings.ReplaceAll(s, `(`, `\(`)
	s = strings.ReplaceAll(s, `)`, `\)`)
	return s
}

// UnescapeRuleContent unescapes special characters in rule content after parsing.
//
// Source: permissionRuleParser.ts:74-79
//
// Unescaping order (reverse of escaping):
//  1. Unescape parentheses first (\( → (, \) → ))
//  2. Then unescape backslashes (\\ → \)
func UnescapeRuleContent(content string) string {
	s := strings.ReplaceAll(content, `\(`, `(`)
	s = strings.ReplaceAll(s, `\)`, `)`)
	s = strings.ReplaceAll(s, `\\`, `\`)
	return s
}

// findFirstUnescapedChar finds the index of the first unescaped occurrence of char.
//
// Source: permissionRuleParser.ts:158-175
//
// A character is escaped if preceded by an odd number of backslashes.
func findFirstUnescapedChar(s string, char byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == char {
			backslashCount := 0
			for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
				backslashCount++
			}
			if backslashCount%2 == 0 {
				return i
			}
		}
	}
	return -1
}

// findLastUnescapedChar finds the index of the last unescaped occurrence of char.
//
// Source: permissionRuleParser.ts:182-198
//
// A character is escaped if preceded by an odd number of backslashes.
func findLastUnescapedChar(s string, char byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == char {
			backslashCount := 0
			for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
				backslashCount++
			}
			if backslashCount%2 == 0 {
				return i
			}
		}
	}
	return -1
}
