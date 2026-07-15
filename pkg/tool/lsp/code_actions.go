package lsptool

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

// codeActions lists available quick-fixes and refactors at uri:pos, or applies
// one selected by index/title via the query parameter (apply=true).
// Mirrors omp action="code_actions" (index.ts:2291-2373).
func codeActions(ctx context.Context, c *lsp.Client, uri string, pos lsp.Position, in Input) (*tool.ToolResult, error) {
	rng := lsp.Range{Start: pos, End: pos}

	apply := boolPtrVal(in.Apply, false)

	cctx := lsp.CodeActionContext{
		TriggerKind: 1,
		Diagnostics: c.DiagnosticsFor(uri),
	}
	// Filter by CodeActionKind only when listing (not applying) and query is a kind filter.
	if !apply && in.Query != "" {
		cctx.Only = []string{in.Query}
	}

	actions, err := lsp.CodeActions(ctx, c, uri, rng, cctx)
	if err != nil {
		return nil, fmt.Errorf("code actions: %w", err)
	}

	if len(actions) == 0 {
		return &tool.ToolResult{Data: "No code actions available"}, nil
	}

	// APPLY mode: select by index or title.
	if apply {
		if strings.TrimSpace(in.Query) == "" {
			return &tool.ToolResult{Data: "Error: query parameter required when apply=true for code_actions"}, nil
		}
		normalized := strings.TrimSpace(in.Query)

		var selected *lsp.CodeAction
		if idxRe.MatchString(normalized) {
			idx, _ := strconv.Atoi(normalized)
			if idx >= 0 && idx < len(actions) {
				selected = &actions[idx]
			}
		}
		if selected == nil {
			lowered := strings.ToLower(normalized)
			for i := range actions {
				if strings.Contains(strings.ToLower(actions[i].Title), lowered) {
					selected = &actions[i]
					break
				}
			}
		}

		if selected == nil {
			var b strings.Builder
			fmt.Fprintf(&b, "No code action matches %q. Available actions:\n", normalized)
			for i, a := range actions {
				fmt.Fprintf(&b, "  %s\n", formatCodeAction(a, i))
			}
			return &tool.ToolResult{Data: b.String()}, nil
		}

		applied, err := lsp.ApplyCodeAction(ctx, c, *selected, func(we *lsp.WorkspaceEdit) ([]string, error) {
			changed, werr := lsp.ApplyWorkspaceEdit(we)
			if werr == nil {
				c.NotifyFilesChanged(ctx, changed)
			}
			return changed, werr
		})
		if err != nil {
			return nil, fmt.Errorf("apply code action: %w", err)
		}
		if applied == nil {
			return &tool.ToolResult{Data: fmt.Sprintf("Action %q has no workspace edit or command to apply", selected.Title)}, nil
		}

		var b strings.Builder
		fmt.Fprintf(&b, "Applied %q:\n", applied.Title)
		if len(applied.Edits) > 0 {
			b.WriteString("  Workspace edit:\n")
			for _, e := range applied.Edits {
				fmt.Fprintf(&b, "    %s\n", e)
			}
		}
		if len(applied.ExecutedCommands) > 0 {
			b.WriteString("  Executed command(s):\n")
			for _, cmd := range applied.ExecutedCommands {
				fmt.Fprintf(&b, "    %s\n", cmd)
			}
		}
		return &tool.ToolResult{Data: b.String()}, nil
	}

	// LIST mode: enumerate actions with index, kind, preferred/disabled.
	var b strings.Builder
	fmt.Fprintf(&b, "%d code action(s):\n\n", len(actions))
	for i, a := range actions {
		fmt.Fprintf(&b, "  %s\n", formatCodeAction(a, i))
	}
	return &tool.ToolResult{Data: b.String()}, nil
}

var idxRe = regexp.MustCompile(`^\d+$`)

// formatCodeAction mirrors omp formatCodeAction (utils.ts:489-494).
func formatCodeAction(a lsp.CodeAction, index int) string {
	kind := a.Kind
	if kind == "" {
		kind = "action"
	}
	suffix := ""
	if a.IsPreferred {
		suffix = " (preferred)"
	}
	if a.Disabled != nil {
		suffix = fmt.Sprintf(" (disabled: %s)", a.Disabled.Reason)
	}
	return fmt.Sprintf("%d: [%s] %s%s", index, kind, a.Title, suffix)
}
