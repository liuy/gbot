package lsptool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

// symbols queries textDocument/documentSymbol for a file and renders the tree.
// Mirrors omp action="symbols" file-path (index.ts:2375-2407). Some servers
// return DocumentSymbol[] (hierarchical, with selectionRange), others return
// SymbolInformation[] (flat, with location). We detect by checking the raw
// bytes for "selectionRange" to avoid a full pre-unmarshal pass.
func symbols(ctx context.Context, c *lsp.Client, uri, filePath, wd string) (*tool.ToolResult, error) {
	raw, err := c.Request(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": lsp.TextDocumentIdentifier{URI: uri},
	})
	if err != nil {
		return nil, fmt.Errorf("symbols: %w", err)
	}

	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || bytes.Equal(bytes.TrimSpace(raw), []byte("[]")) {
		return &tool.ToolResult{Data: "No symbols found"}, nil
	}

	rel := lsp.URItoRelativePath(uri, wd)
	var b strings.Builder
	fmt.Fprintf(&b, "Symbols in %s:\n\n", rel)

	// Detect format: DocumentSymbol responses contain "selectionRange";
	// SymbolInformation responses contain "location" instead.
	isDocSymbol := bytes.Contains(raw, []byte(`"selectionRange"`))
	if isDocSymbol {
		var syms []lsp.DocumentSymbol
		if err := json.Unmarshal(raw, &syms); err != nil {
			return nil, fmt.Errorf("symbols DocumentSymbol decode: %w", err)
		}
		for _, s := range syms {
			formatDocumentSymbol(&b, s, 0)
		}
	} else {
		var syms []lsp.SymbolInformation
		if err := json.Unmarshal(raw, &syms); err != nil {
			return nil, fmt.Errorf("symbols SymbolInformation decode: %w", err)
		}
		for _, s := range syms {
			formatSymbolInformation(&b, s, wd)
		}
	}
	return &tool.ToolResult{Data: b.String()}, nil
}

// formatSymbolInformation mirrors omp formatSymbolInformation (utils.ts:460-465).
func formatSymbolInformation(b *strings.Builder, s lsp.SymbolInformation, wd string) {
	line := s.Location.Range.Start.Line + 1
	container := ""
	if s.ContainerName != "" {
		container = fmt.Sprintf(" (%s)", s.ContainerName)
	}
	fmt.Fprintf(b, "  %s `%s`%s @ line %d\n", symbolKindName(s.Kind), s.Name, container, line)
}
