package mcp

// Mutation-killing tests for elicitation.go (batch mcp-small8).
// Verdicts: /home/yliu/mutate-mcp1/ledger-A.txt — all other escapes in this
// file are pure slog lines (no behavioral surface).

import (
	"context"
	"testing"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Kills elicitation.go:62 expression/context-nil (ctx → nil): the UI must
// receive the caller's context unchanged, not a nil context.
func TestElicitationHandler_PassesCallerContext(t *testing.T) {
	type ctxKey struct{}
	wantCtx := context.WithValue(context.Background(), ctxKey{}, "marker")
	var gotCtx context.Context

	cm := NewClientManager(TransportFactory{}, true, "")
	cm.SetElicitationUI(&mockElicitationUI{
		fn: func(ctx context.Context, serverName string, params *mcp.ElicitParams) (*mcp.ElicitResult, error) {
			gotCtx = ctx
			return &mcp.ElicitResult{Action: "accept"}, nil
		},
	})

	if _, err := cm.makeElicitationHandler("srv")(wantCtx, &mcp.ClientRequest[*mcp.ElicitParams]{
		Params: &mcp.ElicitParams{Message: "test", Mode: "form"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotCtx != wantCtx {
		t.Errorf("UI context = %v, want the caller's context passed through unchanged", gotCtx)
	}
}
