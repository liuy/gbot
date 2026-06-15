package lsptool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

// request sends an arbitrary LSP method to the server for the file's language.
// If payload is provided, it overrides the auto-built {textDocument, position} shape.
// Mirrors omp action="request" (index.ts:1880-1975).
func request(ctx context.Context, reg *lsp.Registry, in Input, workingDir string) (*tool.ToolResult, error) {
	if in.Query == "" {
		return nil, fmt.Errorf("query parameter required for request (LSP method name)")
	}
	method := in.Query

	var chosenSpec *lsp.ServerSpec
	var resolvedFile string

	if in.File != "" {
		targetFile := resolvePath(in.File, workingDir)
		resolvedFile = targetFile
		specs := reg.Snapshot()
		ext := filepath.Ext(targetFile)
		for _, s := range specs {
			if slices.Contains(s.FileExts, ext) {
				chosenSpec = &s
			}
			if chosenSpec != nil {
				break
			}
		}
	}

	if chosenSpec == nil {
		specs := reg.Snapshot()
		if len(specs) == 0 {
			return nil, fmt.Errorf("no language server configured")
		}
		chosenSpec = &specs[0]
	}

	c, err := reg.ForSpec(ctx, *chosenSpec)
	if err != nil {
		return nil, fmt.Errorf("lsp: %w", err)
	}

	var requestParams any
	if in.Payload != "" {
		var p any
		if err := json.Unmarshal([]byte(in.Payload), &p); err != nil {
			return nil, fmt.Errorf("invalid payload JSON: %w", err)
		}
		requestParams = p
	} else if resolvedFile != "" {
		content, err := os.ReadFile(resolvedFile)
		if err != nil {
			return nil, fmt.Errorf("read file: %w", err)
		}
		uri := lsp.FileToURI(resolvedFile)
		langID := lsp.DetectLanguage(resolvedFile)
		if langID != "" {
			_ = c.EnsureFileOpen(ctx, uri, langID, string(content))
		}

		if in.Line > 0 {
			col := 0
			if in.Symbol != "" {
				resolvedCol, err := resolveSymbolColumn(resolvedFile, in.Line, in.Symbol)
				if err != nil {
					return nil, err
				}
				col = resolvedCol
			}
			requestParams = map[string]any{
				"textDocument": map[string]string{"uri": uri},
				"position":     map[string]int{"line": in.Line - 1, "character": col},
			}
		} else {
			requestParams = map[string]any{
				"textDocument": map[string]string{"uri": uri},
			}
		}
	} else {
		requestParams = struct{}{}
	}

	raw, err := c.Request(ctx, method, requestParams)
	if err != nil {
		return nil, fmt.Errorf("lsp %s: %w", method, err)
	}

	var formatted string
	if len(raw) == 0 || string(raw) == "null" {
		formatted = "null"
	} else {
		formatted = formatJSON(raw)
	}
	return &tool.ToolResult{Data: fmt.Sprintf("%s ← %s:\n%s", chosenSpec.Name, method, formatted)}, nil
}
