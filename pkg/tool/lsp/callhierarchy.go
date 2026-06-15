package lsptool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

// callHierarchyItem represents a function/method in the call hierarchy.
type callHierarchyItem struct {
	Name   string    `json:"name"`
	Kind   int       `json:"kind"`
	URI    string    `json:"uri"`
	Range  lsp.Range `json:"range"`
	Detail string    `json:"detail,omitempty"`
}

// callerEntry represents a caller of the target function.
type callerEntry struct {
	From       callHierarchyItem `json:"from"`
	FromRanges []lsp.Range       `json:"fromRanges"`
}

// calleeEntry represents a callee of the target function.
type calleeEntry struct {
	To         callHierarchyItem `json:"to"`
	FromRanges []lsp.Range       `json:"fromRanges"`
}

func callHierarchy(ctx context.Context, c *lsp.Client, uri string, pos lsp.Position, wd, direction string) (*tool.ToolResult, error) {
	raw, err := c.Request(ctx, "textDocument/prepareCallHierarchy", map[string]any{
		"textDocument": map[string]string{"uri": uri},
		"position":     pos,
	})
	if err != nil {
		return nil, fmt.Errorf("prepareCallHierarchy: %w", err)
	}

	var items []callHierarchyItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("decode call hierarchy items: %w", err)
	}
	if len(items) == 0 {
		return &tool.ToolResult{Data: "No call hierarchy item found at this position"}, nil
	}

	method := "callHierarchy/incomingCalls"
	if direction == "callees" {
		method = "callHierarchy/outgoingCalls"
	}

	callRaw, err := c.Request(ctx, method, map[string]any{
		"item": items[0],
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}

	if direction == "callers" {
		return formatCallers(ctx, callRaw, wd)
	}
	return formatCallees(ctx, callRaw, wd)
}

func formatCallers(ctx context.Context, raw json.RawMessage, wd string) (*tool.ToolResult, error) {
	var calls []callerEntry
	if err := json.Unmarshal(raw, &calls); err != nil {
		return nil, fmt.Errorf("decode callers: %w", err)
	}
	if len(calls) == 0 {
		return &tool.ToolResult{Data: "No callers found (nothing calls this function)"}, nil
	}

	calls = filterGitIgnoredCallers(ctx, calls, wd)
	if len(calls) == 0 {
		return &tool.ToolResult{Data: "No callers found (all gitignored)"}, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d caller(s):\n", len(calls))

	byFile := groupCallersByFile(calls, wd)
	for _, fp := range sortedKeys(byFile) {
		fmt.Fprintf(&b, "\n%s:\n", fp)
		for _, call := range byFile[fp] {
			kind := symbolKindStr(call.From.Kind)
			line := call.From.Range.Start.Line + 1
			fmt.Fprintf(&b, "  %s (%s) - Line %d", call.From.Name, kind, line)
			if sites := formatRanges(call.FromRanges); sites != "" {
				fmt.Fprintf(&b, " [calls at: %s]", sites)
			}
			fmt.Fprintln(&b)
		}
	}
	return &tool.ToolResult{Data: b.String()}, nil
}

func formatCallees(ctx context.Context, raw json.RawMessage, wd string) (*tool.ToolResult, error) {
	var calls []calleeEntry
	if err := json.Unmarshal(raw, &calls); err != nil {
		return nil, fmt.Errorf("decode callees: %w", err)
	}
	if len(calls) == 0 {
		return &tool.ToolResult{Data: "No callees found (this function calls nothing)"}, nil
	}

	calls = filterGitIgnoredCallees(ctx, calls, wd)
	if len(calls) == 0 {
		return &tool.ToolResult{Data: "No callees found (all gitignored)"}, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d callee(s):\n", len(calls))

	byFile := groupCalleesByFile(calls, wd)
	for _, fp := range sortedKeys(byFile) {
		fmt.Fprintf(&b, "\n%s:\n", fp)
		for _, call := range byFile[fp] {
			kind := symbolKindStr(call.To.Kind)
			line := call.To.Range.Start.Line + 1
			fmt.Fprintf(&b, "  %s (%s) - Line %d", call.To.Name, kind, line)
			if sites := formatRanges(call.FromRanges); sites != "" {
				fmt.Fprintf(&b, " [called from: %s]", sites)
			}
			fmt.Fprintln(&b)
		}
	}
	return &tool.ToolResult{Data: b.String()}, nil
}

func groupCallersByFile(calls []callerEntry, wd string) map[string][]callerEntry {
	byFile := make(map[string][]callerEntry)
	for _, call := range calls {
		fp := lsp.URItoRelativePath(call.From.URI, wd)
		byFile[fp] = append(byFile[fp], call)
	}
	return byFile
}

func groupCalleesByFile(calls []calleeEntry, wd string) map[string][]calleeEntry {
	byFile := make(map[string][]calleeEntry)
	for _, call := range calls {
		fp := lsp.URItoRelativePath(call.To.URI, wd)
		byFile[fp] = append(byFile[fp], call)
	}
	return byFile
}

func filterGitIgnoredCallers(ctx context.Context, calls []callerEntry, wd string) []callerEntry {
	locs := make([]lsp.Location, len(calls))
	for i, call := range calls {
		locs[i] = lsp.Location{URI: call.From.URI}
	}
	keptLocs := filterGitIgnored(ctx, locs, wd)
	if len(keptLocs) == len(calls) {
		return calls
	}

	kept := make(map[string]bool, len(keptLocs))
	for _, loc := range keptLocs {
		kept[loc.URI] = true
	}
	out := calls[:0]
	for _, call := range calls {
		if kept[call.From.URI] {
			out = append(out, call)
		}
	}
	return out
}

func filterGitIgnoredCallees(ctx context.Context, calls []calleeEntry, wd string) []calleeEntry {
	locs := make([]lsp.Location, len(calls))
	for i, call := range calls {
		locs[i] = lsp.Location{URI: call.To.URI}
	}
	keptLocs := filterGitIgnored(ctx, locs, wd)
	if len(keptLocs) == len(calls) {
		return calls
	}

	kept := make(map[string]bool, len(keptLocs))
	for _, loc := range keptLocs {
		kept[loc.URI] = true
	}
	out := calls[:0]
	for _, call := range calls {
		if kept[call.To.URI] {
			out = append(out, call)
		}
	}
	return out
}

func formatRanges(ranges []lsp.Range) string {
	if len(ranges) == 0 {
		return ""
	}
	parts := make([]string, len(ranges))
	for i, r := range ranges {
		parts[i] = fmt.Sprintf("%d:%d", r.Start.Line+1, r.Start.Character+1)
	}
	return strings.Join(parts, ", ")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func symbolKindStr(kind int) string {
	names := map[int]string{
		1: "File", 2: "Module", 3: "Namespace", 4: "Package", 5: "Class",
		6: "Method", 7: "Property", 8: "Field", 9: "Constructor", 10: "Enum",
		11: "Interface", 12: "Function", 13: "Variable", 14: "Constant",
		15: "String", 16: "Number", 17: "Boolean", 18: "Array", 19: "Object",
		20: "Key", 21: "Null", 22: "EnumMember", 23: "Struct", 24: "Event",
		25: "Operator", 26: "TypeParameter",
	}
	if name, ok := names[kind]; ok {
		return name
	}
	return fmt.Sprintf("Kind(%d)", kind)
}
