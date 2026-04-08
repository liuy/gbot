package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// minimalTool is a minimal tool implementation for covers skip path in executeTools.
type minimalTool struct{}

func (m *minimalTool) Name() string                                                { return "test" }
func (m *minimalTool) Aliases() []string                                           { return nil }
func (m *minimalTool) Description(json.RawMessage) (string, error)                 { return "test", nil }
func (m *minimalTool) InputSchema() json.RawMessage                                { return nil }
func (m *minimalTool) Call(context.Context, json.RawMessage, *types.ToolUseContext) (*tool.ToolResult, error) {
	return nil, nil
}
func (m *minimalTool) CheckPermissions(json.RawMessage, *types.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (m *minimalTool) IsReadOnly(json.RawMessage) bool            { return true }
func (m *minimalTool) IsDestructive(json.RawMessage) bool         { return false }
func (m *minimalTool) IsConcurrencySafe(json.RawMessage) bool     { return true }
func (m *minimalTool) IsEnabled() bool                            { return true }
func (m *minimalTool) InterruptBehavior() tool.InterruptBehavior {
	return tool.InterruptCancel
}
func (m *minimalTool) Prompt() string                             { return "" }

func TestInternalMinimalTool(t *testing.T) {
	t.Parallel()
	mt := &minimalTool{}
	if mt.Name() != "test" {
		t.Errorf("Name() = %q, want %q", mt.Name(), "test")
	}
	if !mt.IsEnabled() {
		t.Error("IsEnabled() should be true")
	}
	if !mt.IsReadOnly(nil) {
		t.Error("IsReadOnly() should be true")
	}
	if mt.IsDestructive(nil) {
		t.Error("IsDestructive() should be false")
	}
	if !mt.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe() should be true")
	}
	if mt.InterruptBehavior() != tool.InterruptCancel {
		t.Error("InterruptBehavior() should be InterruptCancel")
	}
	if mt.Prompt() != "" {
		t.Errorf("Prompt() = %q, want empty", mt.Prompt())
	}
	if mt.InputSchema() != nil {
		t.Error("InputSchema() should be nil")
	}
	aliases := mt.Aliases()
	if aliases != nil {
		t.Errorf("Aliases() = %v, want nil", aliases)
	}
	desc, err := mt.Description(nil)
	if err != nil {
		t.Errorf("Description() error: %v", err)
	}
	if desc != "test" {
		t.Errorf("Description() = %q, want %q", desc, "test")
	}

	// Test CheckPermissions returns allow
	result := mt.CheckPermissions(nil, nil)
	if _, ok := result.(types.PermissionAllowDecision); !ok {
		t.Errorf("CheckPermissions() = %T, want PermissionAllowDecision", result)
	}

	// Test Call returns nil
	toolResult, err := mt.Call(context.Background(), nil, nil)
	if err != nil {
		t.Errorf("Call() error: %v", err)
	}
	if toolResult != nil {
		t.Errorf("Call() = %v, want nil", toolResult)
	}
}

