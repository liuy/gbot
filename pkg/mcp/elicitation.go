// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
//
// This file: Elicitation handler — server→user interaction requests.
// Source: client.ts:1191-1197 (default cancel), elicitationHandler.ts (form/URL modes)
//
// Elicitation allows MCP servers to request user input (forms, URL confirmation).
// The handler is set at client creation time via ClientOptions.ElicitationHandler.
// A late-binding interface allows injection after ClientManager creation.
package mcp

import (
	"context"
	"log/slog"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// ElicitationUI — interface for TUI layer implementation
// Source: elicitationHandler.ts — form/URL two modes
// ---------------------------------------------------------------------------

// ElicitationUI is implemented by the TUI layer to handle server-initiated
// user interaction requests. The interface is injected via SetElicitationUI
// after ClientManager creation, using an atomic pointer for safe concurrent access.
//
// Source: elicitationHandler.ts — two modes:
//   - "form": presents a form to the user based on requestedSchema
//   - "url": presents a URL for the user to visit and confirm
type ElicitationUI interface {
	// HandleElicitation processes an elicitation request from an MCP server.
	// Returns the user's response (accept/decline/cancel) or an error.
	HandleElicitation(ctx context.Context, serverName string, params *mcp.ElicitParams) (*mcp.ElicitResult, error)
}

// ---------------------------------------------------------------------------
// ClientManager — Elicitation integration
// Source: client.ts:1191-1197 — default: {action: "cancel"} when no UI
// ---------------------------------------------------------------------------

// SetElicitationUI sets the elicitation UI handler.
// Can be called after ClientManager creation to inject the TUI implementation.
// Source: client.ts:1191 — UI not available → default cancel response.
func (cm *ClientManager) SetElicitationUI(ui ElicitationUI) {
	cm.elicitationUI.Store(&ui)
}

// makeElicitationHandler creates the ClientOptions.ElicitationHandler closure.
// Source: client.ts:1191-1197 — default returns {action: "cancel"} when UI is nil.
//
// "cancel" (not "decline"): cancel means "user didn't make a choice, server can retry".
// "decline" means "user explicitly refused" — different semantics.
func (cm *ClientManager) makeElicitationHandler(serverName string) func(context.Context, *mcp.ClientRequest[*mcp.ElicitParams]) (*mcp.ElicitResult, error) {
	return func(ctx context.Context, req *mcp.ClientRequest[*mcp.ElicitParams]) (*mcp.ElicitResult, error) {
		slog.Info("mcp: elicitation request received", "server", serverName, "mode", req.Params.Mode)
		ui := cm.elicitationUI.Load()
		if ui == nil {
			// Source: client.ts:1191-1197 — no UI available, return cancel
			slog.Warn("mcp: no elicitation UI configured, returning cancel", "server", serverName)
			return &mcp.ElicitResult{Action: "cancel"}, nil
		}
		result, err := (*ui).HandleElicitation(ctx, serverName, req.Params)
		if err != nil {
			slog.Warn("mcp: elicitation handler error", "server", serverName, "error", err)
		} else {
			slog.Info("mcp: elicitation response sent", "server", serverName, "action", result.Action)
		}
		return result, err
	}
}
