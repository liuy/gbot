package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/types"
)

func TestNewAbortController(t *testing.T) {
	t.Parallel()
	ac := engine.NewAbortController(context.Background())
	if ac == nil {
		t.Fatal("expected non-nil controller")
	}
	if ac.Context() == nil {
		t.Fatal("expected non-nil context")
	}
	if ac.Reason() != "" {
		t.Errorf("expected empty reason, got %q", ac.Reason())
	}
}

func TestAbortController_Abort(t *testing.T) {
	ac := engine.NewAbortController(context.Background())
	ac.Abort("user interrupt")

	if ac.Reason() != "user interrupt" {
		t.Errorf("expected reason 'user interrupt', got %q", ac.Reason())
	}

	ctx := ac.Context()
	select {
	case <-ctx.Done():
		// Expected
	case <-time.After(time.Second):
		t.Fatal("expected context to be done")
	}
}

func TestAbortController_ParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ac := engine.NewAbortController(parent)

	cancel()

	ctx := ac.Context()
	select {
	case <-ctx.Done():
		// Expected: child inherits parent cancellation
	case <-time.After(time.Second):
		t.Fatal("expected child context to be cancelled when parent cancels")
	}
}

func TestShouldInterruptTool_NoAbort(t *testing.T) {
	ctx := context.Background()
	if engine.ShouldInterruptTool(0, ctx) {
		t.Error("expected false when context is not cancelled")
	}
}

func TestShouldInterruptTool_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// InterruptCancel (0) should return true
	if !engine.ShouldInterruptTool(0, ctx) {
		t.Error("expected true for InterruptCancel with cancelled context")
	}
}

func TestCheckAbort_NoAbort(t *testing.T) {
	ctx := context.Background()
	if reason := engine.CheckAbort(ctx, "streaming"); reason != "" {
		t.Errorf("expected empty reason, got %s", reason)
	}
}

func TestCheckAbort_WithAbort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		phase    string
		expected types.TerminalReason
	}{
		{"streaming", types.TerminalAbortedStreaming},
		{"tools", types.TerminalAbortedTools},
		{"unknown", types.TerminalAbortedStreaming},
	}

	for _, tt := range tests {
		t.Run(tt.phase, func(t *testing.T) {
			reason := engine.CheckAbort(ctx, tt.phase)
			if reason != tt.expected {
				t.Errorf("CheckAbort(%q) = %s, want %s", tt.phase, reason, tt.expected)
			}
		})
	}
}
