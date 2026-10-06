package lsptool

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
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
// With file=, the reload is scoped to that file's project root and to the
// specs that serve its extension — the same filter resolveInWorkspace uses —
// so reloading a worktree touches only that worktree's servers and does not
// spawn a process for every configured spec. Without file=, every configured
// server at the launch root reloads, matching omp's workspace path.
func reload(ctx context.Context, reg *lsp.Registry, in Input, workingDir string) (*tool.ToolResult, error) {
	specs := reg.Snapshot()
	if len(specs) == 0 {
		return nil, fmt.Errorf("no language servers configured")
	}
	root := targetRoot(reg, in, workingDir)
	if in.File != "" {
		if ext := filepath.Ext(resolvePath(in.File, workingDir)); ext != "" {
			if filtered := slices.DeleteFunc(slices.Clone(specs), func(s lsp.ServerSpec) bool {
				return !slices.Contains(s.FileExts, ext)
			}); len(filtered) > 0 {
				specs = filtered
			}
		}
	}

	var outputs []string
	for _, spec := range specs {
		outputs = append(outputs, reloadServer(ctx, reg, spec, root))
	}
	return &tool.ToolResult{Data: strings.Join(outputs, "\n")}, nil
}

// reloadServer implements omp's three-step reload strategy against `root`.
// With no file= the caller passes the launch root, which is exactly what
// ForSpec would have resolved (registry.go: ForSpec = ForSpecInRoot(DefaultRoot)).
func reloadServer(ctx context.Context, reg *lsp.Registry, spec lsp.ServerSpec, root string) string {
	c, err := reg.ForSpecInRoot(ctx, spec, root)
	if err != nil {
		return fmt.Sprintf("Failed to reload %s: %v", spec.Name, err)
	}

	// 1. rust-analyzer's explicit reload request.
	if _, err := c.Request(ctx, "rust-analyzer/reloadWorkspace", nil); err == nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Sprintf("Failed to reload %s: %v", spec.Name, ctxErr)
		}
		return fmt.Sprintf("Reloaded %s", spec.Name)
	} else if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Sprintf("Failed to reload %s: %v", spec.Name, ctxErr)
	}

	// 2. Generic configuration-changed notification (spec says notification,
	// not request — sending it as a request hangs on tsserver).
	if err := c.Notify(ctx, "workspace/didChangeConfiguration", map[string]any{
		"settings": struct{}{},
	}); err == nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Sprintf("Failed to reload %s: %v", spec.Name, ctxErr)
		}
		return fmt.Sprintf("Reloaded %s", spec.Name)
	} else if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Sprintf("Failed to reload %s: %v", spec.Name, ctxErr)
	}

	// 3. Kill and respawn on next use, scoped to `root` — a worktree reload
	// must not take down the launch root's server.
	if reg.KillAndEvict(spec.Name, root) {
		return fmt.Sprintf("Restarted %s", spec.Name)
	}
	return fmt.Sprintf("Reloaded %s", spec.Name)
}
