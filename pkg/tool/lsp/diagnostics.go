package lsptool

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

// diagnosticsAction returns cached LSP diagnostics at three granularity levels:
// - no params: all open files across all servers
// - file= : diagnostics for that file only
// - symbol= : diagnostics filtered to the symbol's range
func diagnosticsAction(ctx context.Context, reg *lsp.Registry, in Input, wd string) (*tool.ToolResult, error) {
	// Symbol-level: resolve to range, filter diagnostics within that range
	if in.Symbol != "" {
		uri, pos, c, _, err := resolveAndOpen(ctx, reg, in, wd)
		if err != nil {
			return nil, err
		}
		diags := c.DiagnosticsFor(uri)
		if len(diags) == 0 {
			return &tool.ToolResult{Data: "No diagnostics for this symbol"}, nil
		}

		syms, err := lsp.DocumentSymbols(ctx, c, uri)
		if err != nil {
			return nil, fmt.Errorf("documentSymbol: %w", err)
		}
		item := findInnermostSymbol(syms, pos)
		if item == nil {
			return &tool.ToolResult{Data: "No diagnostics for this symbol"}, nil
		}

		var filtered []lsp.Diagnostic
		for _, d := range diags {
			if rangesOverlap(d.Range, item.Range) {
				filtered = append(filtered, d)
			}
		}
		if len(filtered) == 0 {
			return &tool.ToolResult{Data: "No diagnostics for this symbol"}, nil
		}

		rel := lsp.URItoRelativePath(uri, wd)
		var b strings.Builder
		fmt.Fprintf(&b, "%d diagnostic(s) for %s in %s:\n\n", len(filtered), in.Symbol, rel)
		for _, d := range filtered {
			b.WriteString(formatDiagnostic(d, rel))
		}
		return &tool.ToolResult{Data: b.String()}, nil
	}

	// File-level
	if in.File != "" {
		targetFile := resolvePath(in.File, wd)
		uri := lsp.FileToURI(targetFile)
		c, err := reg.ForFile(ctx, targetFile)
		if err != nil {
			return nil, fmt.Errorf("lsp for file: %w", err)
		}
		langID := lsp.DetectLanguage(targetFile)
		if langID == "" {
			return nil, fmt.Errorf("unknown language: %s", targetFile)
		}
		if err := ensureFileOpenWithGuard(ctx, c, uri, langID, targetFile); err != nil {
			return nil, err
		}
		diags := c.DiagnosticsFor(uri)
		rel := lsp.URItoRelativePath(uri, wd)
		if len(diags) == 0 {
			return &tool.ToolResult{Data: fmt.Sprintf("No diagnostics in %s", rel)}, nil
		}

		var b strings.Builder
		fmt.Fprintf(&b, "%d diagnostic(s) in %s:\n\n", len(diags), rel)
		for _, d := range diags {
			b.WriteString(formatDiagnostic(d, rel))
		}
		return &tool.ToolResult{Data: b.String()}, nil
	}

	// Project-level: iterate all servers and their open files
	specs := reg.Snapshot()
	if len(specs) == 0 {
		return nil, fmt.Errorf("no language server configured")
	}

	type fileDiags struct {
		rel  string
		diag []lsp.Diagnostic
	}
	var allFiles []fileDiags
	for _, spec := range specs {
		c, err := reg.ForSpec(ctx, spec)
		if err != nil {
			continue
		}
		for _, uri := range c.OpenURIs() {
			diags := c.DiagnosticsFor(uri)
			if len(diags) == 0 {
				continue
			}
			allFiles = append(allFiles, fileDiags{
				rel:  lsp.URItoRelativePath(uri, wd),
				diag: diags,
			})
		}
	}

	if len(allFiles) == 0 {
		return &tool.ToolResult{Data: "No diagnostics across open files"}, nil
	}

	sort.Slice(allFiles, func(i, j int) bool {
		return allFiles[i].rel < allFiles[j].rel
	})

	total := 0
	for _, f := range allFiles {
		total += len(f.diag)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d diagnostic(s) across %d file(s):\n\n", total, len(allFiles))
	for _, f := range allFiles {
		fmt.Fprintf(&b, "%s (%d):\n", f.rel, len(f.diag))
		for _, d := range f.diag {
			b.WriteString(formatDiagnostic(d, f.rel))
		}
		b.WriteString("\n")
	}
	return &tool.ToolResult{Data: b.String()}, nil
}

func formatDiagnostic(d lsp.Diagnostic, rel string) string {
	severity := severityName(d.Severity)
	line := d.Range.Start.Line + 1
	col := d.Range.Start.Character + 1
	s := fmt.Sprintf("  [%s] %s:%d:%d %s", severity, rel, line, col, d.Message)
	if d.Source != "" {
		s += fmt.Sprintf(" (%s)", d.Source)
	}
	return s + "\n"
}

func severityName(s int) string {
	switch s {
	case 1:
		return "ERROR"
	case 2:
		return "WARN"
	case 3:
		return "INFO"
	case 4:
		return "HINT"
	default:
		return fmt.Sprintf("?(%d)", s)
	}
}

func rangesOverlap(a, b lsp.Range) bool {
	if a.End.Line < b.Start.Line || a.Start.Line > b.End.Line {
		return false
	}
	if a.End.Line == b.Start.Line && a.End.Character < b.Start.Character {
		return false
	}
	if a.Start.Line == b.End.Line && a.Start.Character > b.End.Character {
		return false
	}
	return true
}
