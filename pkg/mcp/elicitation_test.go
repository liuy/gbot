package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// Elicitation handler tests
// ---------------------------------------------------------------------------

func TestElicitationHandler_DefaultCancel(t *testing.T) {
	cm := NewClientManager(TransportFactory{}, true, "")
	handler := cm.makeElicitationHandler("test-server")

	result, err := handler(context.Background(), &mcp.ClientRequest[*mcp.ElicitParams]{
		Params: &mcp.ElicitParams{Message: "test", Mode: "form"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "cancel" {
		t.Errorf("action = %q, want %q", result.Action, "cancel")
	}
}

func TestElicitationHandler_WithUI(t *testing.T) {
	cm := NewClientManager(TransportFactory{}, true, "")

	var capturedServer string
	var capturedMsg string
	mockUI := &mockElicitationUI{
		fn: func(ctx context.Context, serverName string, params *mcp.ElicitParams) (*mcp.ElicitResult, error) {
			capturedServer = serverName
			capturedMsg = params.Message
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"key": "value"}}, nil
		},
	}
	cm.SetElicitationUI(mockUI)

	handler := cm.makeElicitationHandler("my-server")
	result, err := handler(context.Background(), &mcp.ClientRequest[*mcp.ElicitParams]{
		Params: &mcp.ElicitParams{Message: "Please confirm", Mode: "form"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "accept" {
		t.Errorf("action = %q, want %q", result.Action, "accept")
	}
	if capturedServer != "my-server" {
		t.Errorf("server = %q, want %q", capturedServer, "my-server")
	}
	if capturedMsg != "Please confirm" {
		t.Errorf("message = %q, want %q", capturedMsg, "Please confirm")
	}
}

func TestElicitationHandler_AfterSetUI(t *testing.T) {
	cm := NewClientManager(TransportFactory{}, true, "")
	handler := cm.makeElicitationHandler("srv")

	// Before UI set — should return cancel
	result, err := handler(context.Background(), &mcp.ClientRequest[*mcp.ElicitParams]{
		Params: &mcp.ElicitParams{Message: "test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "cancel" {
		t.Errorf("before UI: action = %q, want %q", result.Action, "cancel")
	}

	// Set UI
	cm.SetElicitationUI(&mockElicitationUI{
		fn: func(ctx context.Context, serverName string, params *mcp.ElicitParams) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: "decline"}, nil
		},
	})

	// After UI set — should use the UI
	result, err = handler(context.Background(), &mcp.ClientRequest[*mcp.ElicitParams]{
		Params: &mcp.ElicitParams{Message: "test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "decline" {
		t.Errorf("after UI: action = %q, want %q", result.Action, "decline")
	}
}

// ---------------------------------------------------------------------------
// Mock
// ---------------------------------------------------------------------------

type mockElicitationUI struct {
	fn func(ctx context.Context, serverName string, params *mcp.ElicitParams) (*mcp.ElicitResult, error)
}

func (m *mockElicitationUI) HandleElicitation(ctx context.Context, serverName string, params *mcp.ElicitParams) (*mcp.ElicitResult, error) {
	return m.fn(ctx, serverName, params)
}

func TestElicitationHandler_UIError(t *testing.T) {
	cm := NewClientManager(TransportFactory{}, true, "")

	cm.SetElicitationUI(&mockElicitationUI{
		fn: func(ctx context.Context, serverName string, params *mcp.ElicitParams) (*mcp.ElicitResult, error) {
			return nil, fmt.Errorf("ui error")
		},
	})

	handler := cm.makeElicitationHandler("err-server")
	result, err := handler(context.Background(), &mcp.ClientRequest[*mcp.ElicitParams]{
		Params: &mcp.ElicitParams{Message: "test", Mode: "form"},
	})
	if err == nil {
		t.Fatal("expected error from UI handler")
	}
	if !strings.Contains(err.Error(), "ui error") {
		t.Errorf("error = %q, want 'ui error'", err.Error())
	}
	if result != nil {
		t.Errorf("expected nil result on error, got %+v", result)
	}
}
