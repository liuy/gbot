package llm

import (
	"context"
	"errors"
	"net"
	"strings"
)

// FormatLLMError converts a raw LLM error into a user-friendly string.
// APIError already has a good format ("API Error 429: ...").
// Network errors and context cancellations get simplified descriptions
// instead of dumping raw Go error strings with full URLs and IP addresses.
func FormatLLMError(err error) string {
	if err == nil {
		return ""
	}

	// APIError already formats as "API Error 429: message"
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Error()
	}

	// Context canceled
	if errors.Is(err, context.Canceled) {
		return "Request canceled"
	}

	// Context deadline
	if errors.Is(err, context.DeadlineExceeded) {
		return "Request timed out"
	}

	// Network errors
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "Network Error: Request timed out"
		}
		return "Network Error: Connection failed"
	}

	// Fallback: check for common network error patterns in the error string.
	// HTTP client wraps socket-level errors in fmt.Errorf("send request: ...")
	// which may obscure the underlying net.Error type. Match known signatures
	// so users never see raw IP addresses and stack traces.
	msg := err.Error()
	switch {
	case strings.Contains(msg, "software caused connection abort"):
		return "Network Error: Connection aborted"
	case strings.Contains(msg, "connection reset"):
		return "Network Error: Connection reset"
	case strings.Contains(msg, "connection refused"):
		return "Network Error: Connection refused"
	case strings.Contains(msg, "no such host"):
		return "Network Error: DNS resolution failed"
	case strings.Contains(msg, "i/o timeout"):
		return "Network Error: Request timed out"
	case strings.Contains(msg, "context deadline exceeded"):
		return "Request timed out"
	case strings.Contains(msg, "context canceled"):
		return "Request canceled"
	}

	return msg
}
