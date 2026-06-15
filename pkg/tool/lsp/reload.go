package lsptool

import (
	"context"
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

// reload tells each language server to reload its workspace view.
// Mirrors omp action="reload" workspace path (index.ts:2064-2094) and
// reloadServer (index.ts:459-477).
//
// Per-server strategy mirrors omp reloadServer:
//  1. rust-analyzer/reloadWorkspace as a Request (rust-analyzer responds).
//  2. workspace/didChangeConfiguration as a Notification (other servers).
//  3. Kill+evict so the next ForFile respawns (last-resort fallback).
//
// gbot has no per-file reload path; reload always targets every configured
// server, matching omp's workspace path.
func reload(ctx context.Context, reg *lsp.Registry) (*tool.ToolResult, error) {
	specs := reg.Snapshot()
	if len(specs) == 0 {
		return nil, fmt.Errorf("no language servers configured")
	}

	var outputs []string
	for _, spec := range specs {
		outputs = append(outputs, reloadServer(ctx, reg, spec))
	}
	return &tool.ToolResult{Data: strings.Join(outputs, "\n")}, nil
}

// reloadServer implements omp's three-step reload strategy.
func reloadServer(ctx context.Context, reg *lsp.Registry, spec lsp.ServerSpec) string {
	c, err := reg.ForSpec(ctx, spec)
	if err != nil {
		return fmt.Sprintf("Failed to reload %s: %v", spec.Name, err)
	}

	// 1. rust-analyzer's explicit reload request.
	if _, err := c.Request(ctx, "rust-analyzer/reloadWorkspace", nil); err == nil {
		return fmt.Sprintf("Reloaded %s", spec.Name)
	} else if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Sprintf("Failed to reload %s: %v", spec.Name, ctxErr)
	}

	// 2. Generic configuration-changed notification (spec says notification,
	// not request — sending it as a request hangs on tsserver).
	if err := c.Notify(ctx, "workspace/didChangeConfiguration", map[string]any{
		"settings": struct{}{},
	}); err == nil {
		return fmt.Sprintf("Reloaded %s", spec.Name)
	} else if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Sprintf("Failed to reload %s: %v", spec.Name, ctxErr)
	}

	// 3. Kill and respawn on next use.
	if reg.KillAndEvict(spec.Name) {
		return fmt.Sprintf("Restarted %s", spec.Name)
	}
	return fmt.Sprintf("Reloaded %s", spec.Name)
}
