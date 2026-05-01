package engine

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNewAbortController(t *testing.T) {
	t.Parallel()
	ac := NewAbortController(context.Background())
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
	ac := NewAbortController(context.Background())
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
	ac := NewAbortController(parent)

	cancel()

	ctx := ac.Context()
	select {
	case <-ctx.Done():
		// Expected: child inherits parent cancellation
	case <-time.After(time.Second):
		t.Fatal("expected child context to be cancelled when parent cancels")
	}
	// Verify the context error is "context canceled" (not a deadline exceeded).
	if ctx.Err() != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", ctx.Err())
	}
	// Reason stays empty because we cancelled the parent, not ac.Abort().
	if ac.Reason() != "" {
		t.Errorf("expected empty reason on parent cancel, got %q", ac.Reason())
	}
}

func TestShouldInterruptTool_NoAbort(t *testing.T) {
	ctx := context.Background()
	// Both InterruptCancel (0) and InterruptBlock (1) should return false when ctx is alive.
	if ShouldInterruptTool(0, ctx) {
		t.Error("expected false for InterruptCancel with live context")
	}
}

func TestShouldInterruptTool_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// InterruptCancel (0) should return true when context is cancelled
	if !ShouldInterruptTool(0, ctx) {
		t.Error("expected true for InterruptCancel with cancelled context")
	}
	// Verify the cancelled context actually reports an error.
	if ctx.Err() != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", ctx.Err())
	}
}

func TestShouldAbort_NoAbort(t *testing.T) {
	ctx := context.Background()
	if err := ShouldAbort(ctx, "streaming"); err != nil {
		t.Errorf("expected nil with live context, got: %v", err)
	}
}

func TestShouldAbort_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := ShouldAbort(ctx, "streaming")
	if err == nil {
		t.Fatal("expected error with cancelled context")
	}

	var ae *AbortError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *AbortError, got %T", err)
	}
	if ae.Phase != "streaming" {
		t.Errorf("Phase = %q, want %q", ae.Phase, "streaming")
	}
	if !errors.Is(ae.Err, context.Canceled) {
		t.Errorf("underlying error = %v, want context.Canceled", ae.Err)
	}
}
