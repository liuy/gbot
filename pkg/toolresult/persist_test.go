package toolresult

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestMaybePersistLargeToolResult_BelowThreshold(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	output := mustMarshal("short content")
	result := MaybePersistLargeToolResult(output, "Bash", 50000, "tool-1", "test-session")
	if result.Persisted {
		t.Error("should not persist below threshold")
	}
	// Output should be unchanged
	var s string
	if err := json.Unmarshal(result.Output, &s); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if s != "short content" {
		t.Errorf("output = %q, want %q", s, "short content")
	}
}

func TestMaybePersistLargeToolResult_OverThreshold(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	// Create output larger than threshold
	bigContent := strings.Repeat("x", 60000)
	output := mustMarshal(bigContent)

	result := MaybePersistLargeToolResult(output, "Bash", 50000, "tool-2", "test-session")
	if !result.Persisted {
		t.Error("should persist over threshold")
	}
	if result.FilePath == "" {
		t.Error("FilePath should be set")
	}

	// Output should be valid JSON containing the persisted-output tag
	var s string
	if err := json.Unmarshal(result.Output, &s); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if !strings.Contains(s, PersistedOutputTag) {
		t.Error("output missing persisted-output tag")
	}

	// File should exist on disk
	if _, err := os.Stat(result.FilePath); err != nil {
		t.Errorf("persisted file not found: %v", err)
	}
}

func TestMaybePersistLargeToolResult_EmptySessionID(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	bigContent := strings.Repeat("x", 60000)
	output := mustMarshal(bigContent)

	result := MaybePersistLargeToolResult(output, "Bash", 50000, "tool-3", "")
	if result.Persisted {
		t.Error("should not persist when sessionID is empty")
	}
}

func TestMaybePersistLargeToolResult_UnlimitedThreshold(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	bigContent := strings.Repeat("x", 60000)
	output := mustMarshal(bigContent)

	// threshold -1 means unlimited (Read tool)
	result := MaybePersistLargeToolResult(output, "Read", -1, "tool-4", "test-session")
	if result.Persisted {
		t.Error("should not persist when threshold is -1 (unlimited)")
	}
}

func TestMaybePersistLargeToolResult_OverMaxPersistSize(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	// Content over 64MB — should return error message, not persist
	bigContent := strings.Repeat("x", MaxPersistSizeBytes+1)
	output := mustMarshal(bigContent)

	result := MaybePersistLargeToolResult(output, "Bash", 50000, "tool-5", "test-session")
	// Should not crash, output should be valid JSON
	var s string
	if err := json.Unmarshal(result.Output, &s); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
}

func TestMaybePersistLargeToolResult_OutputAlwaysValidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	tests := []struct {
		name    string
		content string
		sid     string
	}{
		{"small content", "hello", "test-valid-1"},
		{"large content", strings.Repeat("x", 60000), "test-valid-2"},
		{"empty session", strings.Repeat("x", 60000), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := mustMarshal(tt.content)
			result := MaybePersistLargeToolResult(output, "Bash", 50000, "tool-valid", tt.sid)
			if !json.Valid(result.Output) {
				t.Errorf("output is not valid JSON: %q", string(result.Output))
			}
		})
	}
}

func TestIsToolResultContentEmpty(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"nil", "", true},
		{"empty string", `""`, true},
		{"whitespace", `"   "`, true},
		{"empty array", `[]`, true},
		{"empty text blocks", `[{"type":"text","text":""},{"type":"text","text":"  "}]`, true},
		{"has content", `"hello"`, false},
		{"has text block", `[{"type":"text","text":"content"}]`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsToolResultContentEmpty(json.RawMessage(tt.content))
			if got != tt.want {
				t.Errorf("IsToolResultContentEmpty(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}


func TestMaybePersistLargeToolResult_EmptyOutput(t *testing.T) {
	output := mustMarshal("")
	result := MaybePersistLargeToolResult(output, "Bash", 50000, "tool-empty", "test-session")
	var s string
	if err := json.Unmarshal(result.Output, &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(s, "completed with no output") {
		t.Errorf("expected empty output message, got %q", s)
	}
}

func TestMaybePersistLargeToolResult_ImageBlock(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	// Large output with image block should not be persisted
	output := []byte(`[{"type":"image","source":{}},{"type":"text","text":"` + strings.Repeat("x", 60000) + `"}]`)
	result := MaybePersistLargeToolResult(output, "Bash", 50000, "tool-img", "test-session")
	if result.Persisted {
		t.Error("should not persist image content")
	}
}

func TestMaybePersistLargeToolResult_NonStringContent(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	// Content that doesn't start with " — triggers fallback decode
	bigContent := strings.Repeat("x", 60000)
	output := []byte(bigContent) // raw bytes, not JSON string
	result := MaybePersistLargeToolResult(output, "Bash", 50000, "tool-raw", "test-session")
	if !result.Persisted {
		t.Error("should persist non-string large content")
	}
}

func TestIsToolResultContentEmpty_NonTextBlock(t *testing.T) {
	// Array with non-text type block should not be considered empty
	content := json.RawMessage(`[{"type":"tool_use","id":"123"}]`)
	if IsToolResultContentEmpty(content) {
		t.Error("tool_use block should not be empty")
	}
}

func TestIsToolResultContentEmpty_NonEmptyArrayTextBlock(t *testing.T) {
	// Array with non-empty text block
	content := json.RawMessage(`[{"type":"text","text":"content"}]`)
	if IsToolResultContentEmpty(content) {
		t.Error("non-empty text block should not be empty")
	}
}

func mustMarshal(s string) []byte {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return b
}
