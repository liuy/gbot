package engine

import (
	"context"

	"github.com/liuy/gbot/pkg/tool"
)

// AbortController manages cancellation for the query loop.
// Source: utils/abortController.ts — createAbortController, createChildAbortController.
//
// The TS source uses a three-layer abort hierarchy:
//   1. Parent controller (QueryEngine level) — user interrupt (ESC/new message)
//   2. Sibling controller — cascading Bash error cancellation
//   3. Per-tool controller — individual tool abort
//
// In Go, context.Context provides the same capability natively.
// Current implementation uses a single-layer: context.Context for user interrupt.
type AbortController struct {
	ctx    context.Context
	cancel context.CancelFunc
	reason string
}

// NewAbortController creates a new abort controller.
// Source: utils/abortController.ts — createAbortController()
func NewAbortController(parent context.Context) *AbortController {
	ctx, cancel := context.WithCancel(parent)
	return &AbortController{ctx: ctx, cancel: cancel}
}

// Context returns the managed context.
func (ac *AbortController) Context() context.Context {
	return ac.ctx
}

// Abort cancels the controller with an optional reason.
// Source: utils/abortController.ts — abortController.abort(reason)
func (ac *AbortController) Abort(reason string) {
	ac.reason = reason
	ac.cancel()
}

// Reason returns the abort reason, if any.
func (ac *AbortController) Reason() string {
	return ac.reason
}

// ShouldInterruptTool determines if a tool should be interrupted based on
// its interrupt behavior and the abort state.
// Source: StreamingToolExecutor.ts:218-231 — getAbortReason()
//
// TS behavior:
//   - 'cancel' tools: abort on user interrupt
//   - 'block' tools: keep running, new message waits
func ShouldInterruptTool(behavior tool.InterruptBehavior, ctx context.Context) bool {
	if ctx.Err() == nil {
		return false
	}
	// All tools are interrupted on context cancellation.
	return behavior == tool.InterruptCancel
}

// AbortError wraps a context cancellation error with phase information
// indicating where the abort occurred (streaming or tool execution).
// Use errors.As to discriminate: var ae *AbortError; errors.As(err, &ae)
type AbortError struct {
	Phase string // "streaming" or "tools"
	Err   error  // underlying ctx.Err()
}

func (e *AbortError) Error() string { return e.Err.Error() }
func (e *AbortError) Unwrap() error { return e.Err }

// ShouldAbort returns nil if the context is alive, or an AbortError wrapping
// the context cancellation with the given phase.
// Source: query.ts — abort checks at Stages 18 and 23.
func ShouldAbort(ctx context.Context, phase string) error {
	if ctx.Err() == nil {
		return nil
	}
	return &AbortError{Phase: phase, Err: ctx.Err()}
}
