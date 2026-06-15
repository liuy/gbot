package lsptool

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

// sourceAction extracts the full source text of a symbol by name.
// Resolves the symbol to a range via documentSymbol or workspace_symbol,
// then reads that range from disk. Replaces the workspace_symbol → Read(offset,limit) two-step.
func sourceAction(ctx context.Context, reg *lsp.Registry, in Input, wd string) (*tool.ToolResult, error) {
	uri, pos, c, _, err := resolveAndOpen(ctx, reg, in, wd)
	if err != nil {
		return nil, err
	}
	targetFile := lsp.URItoPath(uri)

	syms, err := lsp.DocumentSymbols(ctx, c, uri)
	if err != nil {
		return nil, fmt.Errorf("documentSymbol: %w", err)
	}

	item := findInnermostSymbol(syms, pos)
	if item == nil {
		return nil, fmt.Errorf("no symbol found at resolved position in %s", targetFile)
	}

	content, err := os.ReadFile(targetFile)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	start := item.Range.Start.Line
	end := item.Range.End.Line
	if start >= len(lines) {
		return &tool.ToolResult{Data: "Symbol range beyond file end"}, nil
	}
	if end >= len(lines) {
		end = len(lines) - 1
	}

	body := strings.Join(lines[start:end+1], "\n")

	var b strings.Builder
	rel := lsp.URItoRelativePath(uri, wd)
	fmt.Fprintf(&b, "%s %s — %s:%d-%d\n\n", symbolKindName(item.Kind), item.Name, rel, start+1, end+1)
	b.WriteString(body)
	return &tool.ToolResult{Data: b.String()}, nil
}

// findInnermostSymbol walks the DocumentSymbol tree and returns the deepest
// symbol whose Range contains pos. Mirrors agent-lsp findInnermostSymbol.
func findInnermostSymbol(syms []lsp.DocumentSymbol, pos lsp.Position) *lsp.DocumentSymbol {
	for i := range syms {
		s := &syms[i]
		if !positionInRange(pos, s.Range) {
			continue
		}
		if len(s.Children) > 0 {
			if inner := findInnermostSymbol(s.Children, pos); inner != nil {
				return inner
			}
		}
		return s
	}
	return nil
}

func positionInRange(pos lsp.Position, r lsp.Range) bool {
	if pos.Line < r.Start.Line || pos.Line > r.End.Line {
		return false
	}
	if pos.Line == r.Start.Line && pos.Character < r.Start.Character {
		return false
	}
	if pos.Line == r.End.Line && pos.Character > r.End.Character {
		return false
	}
	return true
}

// inspectAction combines hover + definition + callers into one response.
// The "understand this function" universal entry point.
func inspectAction(ctx context.Context, reg *lsp.Registry, in Input, wd string) (*tool.ToolResult, error) {
	uri, pos, c, _, err := resolveAndOpen(ctx, reg, in, wd)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Inspect: %s\n\n", in.Symbol)

	// Hover
	hoverResult, err := hover(ctx, c, uri, pos)
	if err != nil {
		fmt.Fprintf(&b, "## Type Info\nError: %v\n\n", err)
	} else {
		fmt.Fprintf(&b, "## Type Info\n%s\n\n", hoverResult.Data)
	}

	// Definition
	defResult, err := definition(ctx, c, uri, pos, wd)
	if err != nil {
		fmt.Fprintf(&b, "## Definition\nError: %v\n\n", err)
	} else {
		fmt.Fprintf(&b, "## Definition\n%s\n\n", defResult.Data)
	}

	// Incoming calls
	callResult, err := callHierarchy(ctx, c, uri, pos, wd, "callers")
	if err != nil {
		fmt.Fprintf(&b, "## Callers\nError: %v\n\n", err)
	} else {
		fmt.Fprintf(&b, "## Callers\n%s\n", callResult.Data)
	}

	return &tool.ToolResult{Data: b.String()}, nil
}

// impactAction combines references + callers + callees into one response.
// The blast-radius assessment before making changes.
func impactAction(ctx context.Context, reg *lsp.Registry, in Input, wd string) (*tool.ToolResult, error) {
	uri, pos, c, spec, err := resolveAndOpen(ctx, reg, in, wd)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Impact: %s\n\n", in.Symbol)

	// References
	refResult, err := references(ctx, c, uri, pos, wd, spec)
	if err != nil {
		fmt.Fprintf(&b, "## References\nError: %v\n\n", err)
	} else {
		fmt.Fprintf(&b, "## References\n%s\n\n", refResult.Data)
	}

	// Incoming calls
	inResult, err := callHierarchy(ctx, c, uri, pos, wd, "callers")
	if err != nil {
		fmt.Fprintf(&b, "## Callers\nError: %v\n\n", err)
	} else {
		fmt.Fprintf(&b, "## Callers\n%s\n\n", inResult.Data)
	}

	// Outgoing calls
	outResult, err := callHierarchy(ctx, c, uri, pos, wd, "callees")
	if err != nil {
		fmt.Fprintf(&b, "## Callees\nError: %v\n\n", err)
	} else {
		fmt.Fprintf(&b, "## Callees\n%s\n", outResult.Data)
	}

	return &tool.ToolResult{Data: b.String()}, nil
}
