package tool_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
)

// ValidateToolConventions checks that a tool follows standard conventions:
//   - Description_ with nil input returns a short, single-line summary
//   - Prompt_ is non-empty
func ValidateToolConventions(t *testing.T, tt tool.Tool) {
	t.Helper()

	t.Run("description_nil_input_is_short_single_line", func(t *testing.T) {
		t.Helper()
		desc, err := tt.Description(nil)
		if err != nil {
			t.Fatalf("Description_(nil) returned error: %v", err)
		}
		if desc == "" {
			t.Fatal("Description_(nil) returned empty string")
		}
		if strings.Contains(desc, "\n") {
			t.Errorf("Description_(nil) should be a single line, got multi-line:\n%s", desc)
		}
		if len(desc) > 80 {
			t.Errorf("Description_(nil) should be ≤80 chars, got %d: %q", len(desc), desc)
		}
	})

	t.Run("description_empty_input_is_short_single_line", func(t *testing.T) {
		t.Helper()
		desc, err := tt.Description(json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("Description_({}) returned error: %v", err)
		}
		if desc == "" {
			t.Fatal("Description_({}) returned empty string")
		}
		if strings.Contains(desc, "\n") {
			t.Errorf("Description_({}) should be a single line, got multi-line:\n%s", desc)
		}
		if len(desc) > 80 {
			t.Errorf("Description_({}) should be ≤80 chars, got %d: %q", len(desc), desc)
		}
	})

	t.Run("prompt_is_non_empty", func(t *testing.T) {
		t.Helper()
		prompt := tt.Prompt()
		if prompt == "" {
			t.Fatal("Prompt_ should not be empty")
		}
	})
}
