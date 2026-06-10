package bash

import (
	"encoding/json"
	"strings"

	"github.com/liuy/gbot/pkg/tool"
)

// Command classification sets for TUI collapse behavior.
var (
	bashSearchCommands = map[string]bool{
		"find": true, "grep": true, "rg": true, "ag": true, "ack": true,
		"locate": true, "which": true, "whereis": true,
	}
	bashReadCommands = map[string]bool{
		"cat": true, "head": true, "tail": true, "less": true, "more": true,
		"wc": true, "sort": true, "uniq": true, "diff": true, "comm": true,
		"file": true, "stat": true,
	}
	bashListCommands = map[string]bool{
		"ls": true, "tree": true, "du": true,
	}
	bashNeutralCommands = map[string]bool{
		"echo": true, "printf": true, "true": true, "false": true, ":": true,
	}
)

// isSearchOrReadBashCommand classifies a bash command for TUI collapse behavior.
//
// Simplified vs TS: handles |, ||, &&, ; separators and redirect skipping.
// Does NOT handle heredocs, subshells, continuation lines, or comment stripping.
func isSearchOrReadBashCommand(command string) tool.SearchReadKind {
	if command == "" {
		return tool.SearchReadKind{}
	}

	parts := splitOnOperators(command)

	var hasSearch, hasRead, hasList bool
	hasNonNeutral := false

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Extract base command from original part (before skipRedirects
		// mangles the content inside quotes).
		origPart := part

		// Skip redirects and their targets
		part = skipRedirects(part)

		// Extract base command (first word). For xargs, skip it and its
		// flags to classify the command it runs (e.g. "xargs -0 grep" → "grep").
		baseCmd := extractBaseCommand(part)
		if baseCmd == "xargs" {
			baseCmd = extractBaseCommandAfterXargs(part)
		}
		if baseCmd == "" {
			continue
		}

		if bashNeutralCommands[baseCmd] {
			continue
		}

		hasNonNeutral = true

		// sed: search unless -i (inplace edit) flag is present.
		if baseCmd == "sed" {
			if isSedInplace(origPart) {
				return tool.SearchReadKind{}
			}
			hasSearch = true
			continue
		}

		// awk: search unless output is redirected, system() is called,
		// or -i inplace flag is present.
		if baseCmd == "awk" {
			if isAwkDestructive(origPart) {
				return tool.SearchReadKind{}
			}
			hasSearch = true
			continue
		}

		isSearch := bashSearchCommands[baseCmd]
		isRead := bashReadCommands[baseCmd]
		isList := bashListCommands[baseCmd]

		if !isSearch && !isRead && !isList {
			return tool.SearchReadKind{}
		}

		if isSearch {
			hasSearch = true
		}
		if isRead {
			hasRead = true
		}
		if isList {
			hasList = true
		}
	}

	if !hasNonNeutral {
		return tool.SearchReadKind{}
	}

	return tool.SearchReadKind{
		IsSearch: hasSearch,
		IsRead:   hasRead,
		IsList:   hasList,
	}
}

// splitOnOperators splits a command string on shell operators ||, &&, |, ;
// while respecting single and double quotes.
func splitOnOperators(cmd string) []string {
	var parts []string
	var buf strings.Builder
	inSingle := false
	inDouble := false
	i := 0

	for i < len(cmd) {
		ch := cmd[i]

		if inSingle {
			buf.WriteByte(ch)
			if ch == '\'' {
				inSingle = false
			}
			i++
			continue
		}

		if inDouble {
			buf.WriteByte(ch)
			if ch == '"' && (i == 0 || cmd[i-1] != '\\') {
				inDouble = false
			}
			i++
			continue
		}

		// Not in quotes
		switch {
		case ch == '\'':
			buf.WriteByte(ch)
			inSingle = true
			i++
		case ch == '"':
			buf.WriteByte(ch)
			inDouble = true
			i++
		case ch == '|' && i+1 < len(cmd) && cmd[i+1] == '|':
			// || operator
			parts = append(parts, buf.String())
			buf.Reset()
			i += 2
		case ch == '&' && i+1 < len(cmd) && cmd[i+1] == '&':
			// && operator
			parts = append(parts, buf.String())
			buf.Reset()
			i += 2
		case ch == '|':
			// | operator
			parts = append(parts, buf.String())
			buf.Reset()
			i++
		case ch == ';':
			// ; operator
			parts = append(parts, buf.String())
			buf.Reset()
			i++
		default:
			buf.WriteByte(ch)
			i++
		}
	}

	if buf.Len() > 0 {
		parts = append(parts, buf.String())
	}

	return parts
}

// skipRedirects removes redirect clauses (>file, >>file, >&file) from a command part.
func skipRedirects(part string) string {
	// Simple approach: remove tokens that start with > or >> or >&
	words := strings.Fields(part)
	var filtered []string
	skip := false
	for _, w := range words {
		if skip {
			skip = false
			continue
		}
		if w == ">" || w == ">>" || w == ">&" {
			skip = true
			continue
		}
		// Handle >file (no space)
		if strings.HasPrefix(w, ">") && len(w) > 1 && w[1] != '>' {
			continue
		}
		filtered = append(filtered, w)
	}
	return strings.Join(filtered, " ")
}

// extractBaseCommand returns the first word of a command string.
func extractBaseCommand(part string) string {
	part = strings.TrimSpace(part)
	if part == "" {
		return ""
	}
	// Split on whitespace, take first token
	for i := 0; i < len(part); i++ {
		if part[i] == ' ' || part[i] == '\t' {
			return part[:i]
		}
	}
	return part
}

// extractBaseCommandAfterXargs skips "xargs" and its flags (-0, -I{}, etc.)
// to find the actual command being executed.
func extractBaseCommandAfterXargs(part string) string {
	fields := strings.Fields(part)
	skipNext := false
	for i, f := range fields {
		if i == 0 {
			continue // skip "xargs" itself
		}
		if skipNext {
			skipNext = false
			continue
		}
		if strings.HasPrefix(f, "-") {
			// -I without {} in the same arg takes the next arg as replacement
			if strings.HasPrefix(f, "-I") && !strings.Contains(f[2:], "{}") {
				skipNext = true
			}
			continue
		}
		return f
	}
	return ""
}

// isSedInplace returns true if the sed command uses -i or -i.suffix (inplace).
func isSedInplace(part string) bool {
	fields := strings.FieldsSeq(part)
	for f := range fields {
		if f == "-i" {
			return true
		}
		// -i.bak, -i'' (no backup), -i"sfx"
		if len(f) > 2 && f[:2] == "-i" {
			return true
		}
	}
	return false
}

// isAwkDestructive returns true if the awk command writes output via
// redirect, system(), or -i inplace.
func isAwkDestructive(part string) bool {
	// Check for -i inplace flag
	fields := strings.Fields(part)
	for i, f := range fields {
		if f == "-i" && i+1 < len(fields) && fields[i+1] == "inplace" {
			return true
		}
		if f == "-iinplace" {
			return true
		}
	}
	// Scan the raw string for > inside single-quoted awk program.
	// Shell redirects are already stripped by skipRedirects, so any
	// remaining > inside quotes is awk-level output.
	inSingle := false
	for i := 0; i < len(part); i++ {
		ch := part[i]
		if ch == '\'' {
			inSingle = !inSingle
			continue
		}
		if inSingle && ch == '>' {
			return true
		}
	}
	return strings.Contains(part, "system(")
}

// IsSearchOrRead implements tool.ToolWithSearchOrRead for the Bash tool.
// This is called via the IsSearchOrRead_ field in the ToolDef builder pattern.
func IsSearchOrRead(input json.RawMessage) tool.SearchReadKind {
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil || in.Command == "" {
		return tool.SearchReadKind{}
	}
	return isSearchOrReadBashCommand(in.Command)
}
