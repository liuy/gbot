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

	var b strings.Builder
	b.WriteString("Language servers:\n")
	for _, s := range specs {
		if _, ok := reg.StartedClient(s.Name); ok {
			fmt.Fprintf(&b, "  • %s — ready\n", s.Name)
		} else {
			fmt.Fprintf(&b, "  • %s — not started\n", s.Name)
		}
	}
	b.WriteString("\n")
	b.WriteString("'not started' = binary found on PATH, will spawn on first request.\n")
	b.WriteString("'ready' = server process is live for this cwd.\n")
	return &tool.ToolResult{Data: b.String()}, nil
}
