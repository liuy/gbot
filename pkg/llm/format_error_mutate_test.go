package llm

import (
	"context"
	"fmt"
	"testing"
)

// wrappedCancelErr satisfies errors.Is(err, context.Canceled) while its text
// shares no substring with the plain-text fallbacks, so only the typed check
// can classify it.
type wrappedCancelErr struct{}

func (e *wrappedCancelErr) Error() string { return "dial failed" }
func (e *wrappedCancelErr) Unwrap() error { return context.Canceled }

type wrappedDeadlineErr struct{}

func (e *wrappedDeadlineErr) Error() string { return "dial failed" }
func (e *wrappedDeadlineErr) Unwrap() error { return context.DeadlineExceeded }

// fakeNetErr implements net.Error with a fixed Timeout verdict and a message
// free of the string-fallback signatures.
type fakeNetErr struct {
	timeout bool
}

func (e *fakeNetErr) Error() string   { return "socket went away" }
func (e *fakeNetErr) Timeout() bool   { return e.timeout }
func (e *fakeNetErr) Temporary() bool { return false }

// An APIError must win before the string fallbacks: its message may itself
// contain a network-y phrase, but the rendered form is apiErr.Error().
func TestFormatLLMError_APIErrorBeatsStringFallback(t *testing.T) {
	t.Parallel()
	got := FormatLLMError(&APIError{Message: "read: connection refused"})
	if got != "read: connection refused" {
		t.Errorf("got %q, want the APIError rendering passthrough", got)
	}
}

func TestFormatLLMError_WrappedCanceled(t *testing.T) {
	t.Parallel()
	got := FormatLLMError(&wrappedCancelErr{})
	if got != "Request canceled" {
		t.Errorf("got %q, want %q", got, "Request canceled")
	}
}

func TestFormatLLMError_WrappedDeadline(t *testing.T) {
	t.Parallel()
	got := FormatLLMError(&wrappedDeadlineErr{})
	if got != "Request timed out" {
		t.Errorf("got %q, want %q", got, "Request timed out")
	}
}

func TestFormatLLMError_NetErrorTimeout(t *testing.T) {
	t.Parallel()
	got := FormatLLMError(&fakeNetErr{timeout: true})
	if got != "Network Error: Request timed out" {
		t.Errorf("got %q, want %q", got, "Network Error: Request timed out")
	}
}

func TestFormatLLMError_NetErrorConnectionFailed(t *testing.T) {
	t.Parallel()
	got := FormatLLMError(&fakeNetErr{timeout: false})
	if got != "Network Error: Connection failed" {
		t.Errorf("got %q, want %q", got, "Network Error: Connection failed")
	}
}

func TestFormatLLMError_StringFallbackTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		msg  string
		want string
	}{
		{"dial tcp 1.2.3.4:443: connect: connection refused", "Network Error: Connection refused"},
		{"read tcp: i/o timeout", "Network Error: Request timed out"},
		{"wrapped: context deadline exceeded", "Request timed out"},
		{"wrapped: context canceled", "Request canceled"},
	}
	for _, tc := range cases {
		if got := FormatLLMError(fmt.Errorf("%s", tc.msg)); got != tc.want {
			t.Errorf("FormatLLMError(%q) = %q, want %q", tc.msg, got, tc.want)
		}
	}
}

type deadlineNetErr struct{}

func (e *deadlineNetErr) Error() string   { return "op stalled" }
func (e *deadlineNetErr) Timeout() bool   { return true }
func (e *deadlineNetErr) Temporary() bool { return false }
func (e *deadlineNetErr) Unwrap() error   { return context.DeadlineExceeded }

// A net.Error carrying DeadlineExceeded resolves via the typed context check
// first: context deadlines must render as "Request timed out", not the
// network wording, so the ordering between the two branches is pinned.
func TestFormatLLMError_NetErrorDeadlinePrefersContextWording(t *testing.T) {
	t.Parallel()
	got := FormatLLMError(fmt.Errorf("dial: %w", &deadlineNetErr{}))
	if got != "Request timed out" {
		t.Errorf("got %q, want %q", got, "Request timed out")
	}
}
