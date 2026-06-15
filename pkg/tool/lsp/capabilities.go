package lsptool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

// capabilities reports LSP server capabilities, optionally filtered by file extension.
// Mirrors omp action="capabilities" (index.ts:1828-1878).
func capabilities(ctx context.Context, reg *lsp.Registry, in Input) (*tool.ToolResult, error) {
	var specs []lsp.ServerSpec
	if in.File != "" {
		s := reg.Snapshot()
		ext := filepath.Ext(in.File)
		for _, spec := range s {
			for _, e := range spec.FileExts {
				if e == ext {
					specs = append(specs, spec)
				}
			}
		}
	} else {
		specs = reg.Snapshot()
	}

	if len(specs) == 0 {
		return nil, fmt.Errorf("no language server found for this action")
	}

	var outputs []string
	for _, spec := range specs {
		c, err := reg.ForSpec(ctx, spec)
		if err != nil {
			outputs = append(outputs, fmt.Sprintf("%s: failed - %v", spec.Name, err))
			continue
		}
		// Re-marshal to indented multi-line JSON for readability.
		var v any
		raw := c.Capabilities()
		if err := json.Unmarshal(raw, &v); err != nil {
			outputs = append(outputs, fmt.Sprintf("%s:\n  %s", spec.Name, string(raw)))
			continue
		}
		pretty, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			outputs = append(outputs, fmt.Sprintf("%s:\n  %s", spec.Name, string(raw)))
			continue
		}
		outputs = append(outputs, fmt.Sprintf("%s:\n  %s", spec.Name, string(pretty)))
	}
	return &tool.ToolResult{Data: strings.Join(outputs, "\n")}, nil
}
