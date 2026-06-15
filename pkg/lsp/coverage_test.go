package lsp

import (
	"context"
	"testing"
	"time"
)

func TestRpcError_Error(t *testing.T) {
	e := &rpcError{Code: -32601, Message: "method not found"}
	if s := e.Error(); s != "lsp error -32601: method not found" {
		t.Errorf("Error = %q", s)
	}
}

func TestWaitLoop_CmdNil(t *testing.T) {
	c := &Client{}
	c.waitLoop() // in-process path: just returns
}

func TestInitialize_DeadClient(t *testing.T) {
	c, _, cleanup := newInProcessServer(t)
	// Kill before Initialize.
	c.teardownOnce.Do(func() { close(c.done); close(c.dead) })
	cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Initialize(ctx, "file:///repo"); err == nil {
		t.Error("Initialize: expected error on dead client")
	}
}

// TestWrappers_DeadClient exercises the error branch of every LSP method wrapper.
func TestWrappers_DeadClient(t *testing.T) {
	// In-process client that's immediately killed.
	c, _, cleanup := newInProcessServer(t)
	c.teardownOnce.Do(func() { close(c.done); close(c.dead) })
	cleanup()

	ctx := context.Background()

	if _, err := Definition(ctx, c, "file:///x.go", Position{}); err == nil {
		t.Error("Definition: expected error on dead client")
	}
	if _, err := References(ctx, c, "file:///x.go", Position{}); err == nil {
		t.Error("References: expected error on dead client")
	}
	if _, err := Implementation(ctx, c, "file:///x.go", Position{}); err == nil {
		t.Error("Implementation: expected error on dead client")
	}
	if _, err := WorkspaceSymbol(ctx, c, "x"); err == nil {
		t.Error("WorkspaceSymbol: expected error on dead client")
	}
	if _, err := DocumentSymbols(ctx, c, "file:///x.go"); err == nil {
		t.Error("DocumentSymbols: expected error on dead client")
	}
	if _, err := HoverAt(ctx, c, "file:///x.go", Position{}); err == nil {
		t.Error("HoverAt: expected error on dead client")
	}
	if _, err := PrepareRename(ctx, c, "file:///x.go", Position{}); err == nil {
		t.Error("PrepareRename: expected error on dead client")
	}
	if _, err := Rename(ctx, c, "file:///x.go", Position{}, "x"); err == nil {
		t.Error("Rename: expected error on dead client")
	}
	if _, err := CodeActions(ctx, c, "file:///x.go", Range{}, CodeActionContext{TriggerKind: 1}); err == nil {
		t.Error("CodeActions: expected error on dead client")
	}
	if _, err := ApplyCodeAction(ctx, c, CodeAction{Command: &Command{Command: "x"}}, func(_ *WorkspaceEdit) ([]string, error) {
		return nil, nil
	}); err == nil {
		t.Error("ApplyCodeAction: expected error on dead client")
	}
}

func TestHoverAt_BadJSON(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/hover" {
			// Return a string — can't unmarshal as Hover.
			return "not a hover object", true
		}
		return nil, false
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := HoverAt(ctx, c, "file:///x.go", Position{}); err == nil {
		t.Error("HoverAt: expected error for bad JSON")
	}
}

func TestRename_BadJSON(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/rename" {
			return "not a workspace edit", true
		}
		return nil, false
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := Rename(ctx, c, "file:///x.go", Position{}, "x"); err == nil {
		t.Error("Rename: expected error for bad JSON")
	}
}

func TestWorkspaceSymbol_BadJSON(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "workspace/symbol" {
			return "not a symbol at all", true
		}
		return nil, false
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := WorkspaceSymbol(ctx, c, "x")
	if err == nil {
		t.Error("WorkspaceSymbol: expected error for bad JSON")
	}
}

func TestDecodeLocations_BadFirstChar(t *testing.T) {
	_, err := decodeLocations([]byte(`"this is a string"`))
	if err == nil {
		t.Fatal("expected error for string input")
	}
	_ = err.Error()
}

func TestNotify_DeadClient(t *testing.T) {
	c, _, cleanup := newInProcessServer(t)
	c.teardownOnce.Do(func() { close(c.done); close(c.dead) })
	cleanup()

	err := c.Notify(context.Background(), "textDocument/didOpen", nil)
	if err == nil {
		t.Error("Notify: expected error on dead client")
	}
}
