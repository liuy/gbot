package mcp

// Mutation-escape kill tests for sanitization.go. Each test pins a behavior
// that a specific escaped mutant breaks; assertions are exact on purpose.

import (
	"encoding/json"
	"testing"
)

// 173 statement/return: the empty-input guard must return the input itself.
// Returning nil collapses "present but empty" into "absent" — a nil
// json.RawMessage marshals as null downstream, an empty one is the caller's
// own value passed through.
func TestSanitizeJSON_EmptyInputReturnsInputNotNil(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(" ")
	got := sanitizeJSON(raw[:0])
	if got == nil {
		t.Fatal("sanitizeJSON must return the empty input itself, not nil")
	}
	if len(got) != 0 {
		t.Fatalf("empty input must stay empty, got %q", string(got))
	}
}
