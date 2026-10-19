package app

import (
	"context"
	"log/slog"
	"time"
)

// Cleanup tears down all resources held by the Instance: closes REPL
// sessions, all engines in the manager, media stores, and removes the
// PID file. Safe to call on a partially-initialized Instance (nil fields
// are skipped).
func (inst *Instance) Cleanup() {
	// Protocol modules first: they hold hub registrations and dialed
	// connections whose teardown the hub then observes. Both are nil when
	// no plugin declared a protocol.
	for _, r := range inst.ProtocolRunners {
		r.Close()
	}
	if inst.TransportHub != nil {
		inst.TransportHub.Close()
	}
	if inst.MainRefs != nil && inst.MainRefs.REPL != nil {
		inst.MainRefs.REPL.Close()
	}
	if inst.EngineMgr != nil {
		for _, vs := range inst.EngineMgr.List() {
			if vs.Engine != nil {
				vs.Engine.Close()
			}
		}
	}
	// Engines may final-persist on Close, so the store goes after them:
	// Close() runs the final WAL checkpoint (read pool first, write conn
	// last). Without it every exit path leaves committed frames only in
	// the -wal sidecar.
	if inst.DreamDone != nil {
		select {
		case <-inst.DreamDone:
		case <-time.After(dreamJoinTimeout):
			slog.Warn("cleanup: dream timer still running; closing store under it")
		}
	}
	if inst.Store != nil {
		if err := inst.Store.Close(); err != nil {
			slog.Warn("cleanup: store close", "error", err)
		}
	}
	for _, ms := range inst.MediaStores {
		if ms != nil {
			ms.Close()
		}
	}
	// MCP server process trees do not die on stdin EOF alone (children like
	// browsers are never signalled), so teardown closes the shared registry
	// here, engines only borrow it. Read through EnvHolder so a registry a
	// late reload built (after an MCP-less startup) is closed too.
	if inst.EnvHolder != nil {
		if reg := inst.EnvHolder.Load().McpReg; reg != nil {
			if err := reg.Close(); err != nil {
				slog.Warn("cleanup: mcp registry close", "error", err)
			}
		}
	}
	// A Registry now holds a process per project root, so the exit path has to
	// close them all: a leaked gopls keeps indexing a tree and holding its
	// memory after gbot is gone.
	if inst.LSPReg != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		inst.LSPReg.Shutdown(ctx)
	}
	if inst.PIDCleanup != nil {
		inst.PIDCleanup()
	}
}
