package tui

import (
	"errors"
	"fmt"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
)

// animateTokenValue increments displayed toward target by one step.
// <1000: +1 per tick. >=1000: +100 per tick (matches 0.1k display precision).
func animateTokenValue(displayed, target int) int {
	if displayed >= target {
		return target
	}
	step := 1
	if displayed >= 1000 {
		step = 100
	}
	displayed += step
	if displayed > target {
		displayed = target
	}
	return displayed
}

// errorPrefix returns a context-specific prefix for error display.
// APIError with a status gets "API error <status>"; APIError without one
// gets "API error"; everything else gets the generic "Error".
func errorPrefix(err error) string {
	var apiErr *llm.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Status > 0 {
			return fmt.Sprintf("API Error %d", apiErr.Status)
		}
		return "API Error"
	}
	return "Error"
}

// formatRetryError maps a RetryErrorType to a user-friendly message.
// Source: TS formatAPIError() in services/api/errorUtils.ts
func formatRetryError(errorType string) string {
	switch types.RetryErrorType(errorType) {
	case types.RetryErrorStreamInterrupted:
		return "Connection interrupted"
	case types.RetryErrorStreamEnded:
		return "Connection lost"
	default:
		return "Request timed out. Check your internet connection"
	}
}
