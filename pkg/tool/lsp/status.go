package lsptool

import (
	"context"
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

// status reports the list of configured language servers, distinguishing
// "configured" (PATH-resolvable, never spawned) from "started" (live process).
// Mirrors omp action="status" (index.ts:1347-1416).
func status(_ context.Context, reg *lsp.Registry) (*tool.ToolResult, error) {
	specs := reg.Snapshot()
	if len(specs) == 0 {
		return &tool.ToolResult{Data: "No language servers configured for this project"}, nil
	}

	// One server kind can be live at several roots, so readiness is a list per
	// name rather than a single lookup; LiveServers already orders roots with
	// the launch workspace first.
	rootsByName := make(map[string][]string, len(specs))
	for _, ls := range reg.LiveServers() {
		rootsByName[ls.Spec.Name] = append(rootsByName[ls.Spec.Name], ls.Root)
	}

	var b strings.Builder
	b.WriteString("Language servers:\n")
	for _, s := range specs {
		roots := rootsByName[s.Name]
		if len(roots) == 0 {
			fmt.Fprintf(&b, "  • %s — not started\n", s.Name)
			continue
		}
		for _, root := range roots {
			fmt.Fprintf(&b, "  • %s — ready (root: %s)\n", s.Name, root)
		}
	}
	b.WriteString("\n")
	b.WriteString("'not started' = binary found on PATH, will spawn on first request.\n")
	b.WriteString("'ready' = server process is live for this cwd.\n")
	b.WriteString("'root' = the project directory that server is indexing.\n")
	return &tool.ToolResult{Data: b.String()}, nil
}
