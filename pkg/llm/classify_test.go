package llm_test

import (
	"fmt"
	"testing"

	"github.com/liuy/gbot/pkg/llm"
)

// ---------------------------------------------------------------------------
// Classifier functions — IsRetryable, IsRateLimit, etc.
//
// BUG: These use direct type assertion err.(*APIError) which fails when the
// error is wrapped (e.g., fmt.Errorf("stream request: %w", err)).
// The engine wraps provider errors in callLLM, so retryable errors that
// exhaust provider-level retries are treated as terminal by the engine.
// ---------------------------------------------------------------------------

func TestIsRetryable_DirectAPIError(t *testing.T) {
	t.Parallel()
	err := &llm.APIError{Message: "rate limited", Status: 429, Retryable: true}
	if !llm.IsRetryable(err) {
		t.Error("IsRetryable should return true for direct *APIError with Retryable=true")
	}
}

func TestIsRetryable_WrappedAPIError(t *testing.T) {
	t.Parallel()
	// BUG: before fix, wrapped errors return false because err.(*APIError) fails
	inner := &llm.APIError{Message: "rate limited", Status: 429, Retryable: true}
	wrapped := fmt.Errorf("stream request: %w", inner)
	if !llm.IsRetryable(wrapped) {
		t.Error("IsRetryable should return true for wrapped *APIError — use errors.As instead of type assertion")
	}
}

func TestIsRetryable_NonRetryable(t *testing.T) {
	t.Parallel()
	err := &llm.APIError{Message: "bad request", Status: 400, Retryable: false}
	if llm.IsRetryable(err) {
		t.Error("IsRetryable should return false for non-retryable *APIError")
	}
}

func TestIsRetryable_NonAPIError(t *testing.T) {
	t.Parallel()
	if llm.IsRetryable(fmt.Errorf("some error")) {
		t.Error("IsRetryable should return false for non-APIError")
	}
}

func TestIsRateLimit_Wrapped(t *testing.T) {
	t.Parallel()
	inner := &llm.APIError{Message: "rate limited", Status: 429}
	wrapped := fmt.Errorf("stream request: %w", inner)
	if !llm.IsRateLimit(wrapped) {
		t.Error("IsRateLimit should detect wrapped 429 error via errors.As")
	}
}

func TestIsContextOverflow_Wrapped(t *testing.T) {
	t.Parallel()
	inner := &llm.APIError{Message: "too long", Status: 400, ErrorCode: "prompt_too_long"}
	wrapped := fmt.Errorf("stream request: %w", inner)
	if !llm.IsContextOverflow(wrapped) {
		t.Error("IsContextOverflow should detect wrapped prompt_too_long via errors.As")
	}
}

func TestIsOverloaded_Wrapped(t *testing.T) {
	t.Parallel()
	inner := &llm.APIError{Message: "overloaded", Status: 529}
	wrapped := fmt.Errorf("stream request: %w", inner)
	if !llm.IsOverloaded(wrapped) {
		t.Error("IsOverloaded should detect wrapped 529 error via errors.As")
	}
}

func TestIsServerError_Wrapped(t *testing.T) {
	t.Parallel()
	inner := &llm.APIError{Message: "internal error", Status: 500}
	wrapped := fmt.Errorf("stream request: %w", inner)
	if !llm.IsServerError(wrapped) {
		t.Error("IsServerError should detect wrapped 500 error via errors.As")
	}
}

func TestIsMaxOutputTokens_Wrapped(t *testing.T) {
	t.Parallel()
	inner := &llm.APIError{Message: "max output", Type: "max_output_tokens"}
	wrapped := fmt.Errorf("stream request: %w", inner)
	if !llm.IsMaxOutputTokens(wrapped) {
		t.Error("IsMaxOutputTokens should detect wrapped max_output_tokens via errors.As")
	}
}

// Double-wrapped should also work (errors.As traverses the full chain)
func TestIsRetryable_DoubleWrapped(t *testing.T) {
	t.Parallel()
	inner := &llm.APIError{Message: "rate limited", Status: 429, Retryable: true}
	wrapped := fmt.Errorf("stream request: %w", inner)
	doubleWrapped := fmt.Errorf("callLLM: %w", wrapped)
	if !llm.IsRetryable(doubleWrapped) {
		t.Error("IsRetryable should traverse double-wrapped errors via errors.As")
	}
}
