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

	// Output should be array form: single text block containing persisted-output tag.
	if len(result.Output) == 0 || result.Output[0] != '[' {
		t.Fatalf("expected array-form output, got %q", string(result.Output)[:min(80, len(result.Output))])
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(result.Output, &blocks); err != nil {
		t.Fatalf("output not valid JSON array: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Type != "text" {
		t.Fatalf("expected single text block, got %+v", blocks)
	}
	if !strings.Contains(blocks[0].Text, PersistedOutputTag) {
		t.Errorf("output text missing persisted-output tag, got %q", blocks[0].Text)
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

func TestMaybePersistLargeToolResult_OptedOutThreshold(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	bigContent := strings.Repeat("x", 60000)
	output := mustMarshal(bigContent)

	// threshold -1 means the tool opted out of persistence
	result := MaybePersistLargeToolResult(output, "Read", -1, "tool-4", "test-session")
	if result.Persisted {
		t.Error("should not persist when threshold is -1 (opted out of persistence)")
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
	// Should not crash, output should be valid JSON array.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(result.Output, &blocks); err != nil {
		t.Fatalf("output not valid JSON array: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Type != "text" {
		t.Fatalf("expected single text block, got %+v", blocks)
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
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(result.Output, &blocks); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Type != "text" {
		t.Fatalf("expected single text block, got %+v", blocks)
	}
	if !strings.Contains(blocks[0].Text, "completed with no output") {
		t.Errorf("expected empty output message, got %q", blocks[0].Text)
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

func TestIsToolResultContentEmpty_InvalidJSONArray(t *testing.T) {
	// Array with invalid JSON blocks should return false (persist.go:113-115)
	content := json.RawMessage(`[{invalid}]`)
	got := IsToolResultContentEmpty(content)
	if got {
		t.Error("invalid JSON array should not be considered empty")
	}
}

func TestIsToolResultContentEmpty_InvalidJSONString(t *testing.T) {
	// String that starts with " but fails to unmarshal (persist.go:105 -> falls through)
	// e.g. a raw byte array that starts with 0x22 (") but isn't valid JSON
	content := json.RawMessage(`"unclosed string`)
	got := IsToolResultContentEmpty(content)
	if got {
		t.Error("invalid JSON string should not be considered empty")
	}
}

func TestIsToolResultContentEmpty_NonEmptyRawBytes(t *testing.T) {
	// Content that doesn't start with " or [ — falls through to return false (persist.go:135)
	content := json.RawMessage(`some plain text`)
	got := IsToolResultContentEmpty(content)
	if got {
		t.Error("plain text content should not be considered empty")
	}
}

func TestMaybePersistLargeToolResult_PersistFailure(t *testing.T) {
	// Trigger the PersistToolResult failure path (persist.go:73-76)
	// Use an invalid sessionID so GetToolResultsDir fails inside PersistToolResult
	bigContent := strings.Repeat("x", 60000)
	output := mustMarshal(bigContent)

	result := MaybePersistLargeToolResult(output, "Bash", 50000, "tool-fail", "../../etc")
	if result.Persisted {
		t.Error("should not persist with invalid sessionID")
	}
	// Output should be original output (fallback)
	var s string
	if err := json.Unmarshal(result.Output, &s); err != nil {
		t.Fatalf("output should be valid JSON: %v", err)
	}
	if s != bigContent {
		t.Errorf("fallback output mismatch: got length %d, want %d", len(s), len(bigContent))
	}
}

func TestMaybePersistLargeToolResult_EmptyOutput_VerifyToolName(t *testing.T) {
	// Verify the empty output message includes the tool name
	output := mustMarshal("")
	result := MaybePersistLargeToolResult(output, "MyTool", 50000, "tool-ename", "test-session")
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(result.Output, &blocks); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Type != "text" {
		t.Fatalf("expected single text block, got %+v", blocks)
	}
	text := blocks[0].Text
	if !strings.Contains(text, "MyTool") {
		t.Errorf("empty output message should contain tool name, got: %q", text)
	}
	if !strings.Contains(text, "completed with no output") {
		t.Errorf("expected 'completed with no output', got: %q", text)
	}
}

func TestMaybePersistLargeToolResult_BelowThresholdExactSize(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	// Output exactly at threshold should NOT be persisted.
	// mustMarshal adds 2 quote bytes, so the JSON string "xxx...xxx" is len(content)+2 bytes.
	// We need len(output) == threshold exactly, meaning the raw output bytes equal the threshold.
	// With threshold=102 and 100 'x' chars, mustMarshal produces 102 bytes (100 + 2 quotes).
	content := strings.Repeat("x", 100)
	output := mustMarshal(content) // 102 bytes
	if len(output) != 102 {
		t.Fatalf("expected output 102 bytes, got %d", len(output))
	}
	result := MaybePersistLargeToolResult(output, "Bash", 102, "tool-exact", "test-session")
	if result.Persisted {
		t.Error("output at exactly threshold size should not be persisted")
	}
}

func mustMarshal(s string) []byte {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return b
}

// ---------------------------------------------------------------------------
// Array-form input tests (Step 9)
// ---------------------------------------------------------------------------

func TestMaybePersistLargeToolResult_ArrayFormTextContent(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	// Array form: a single text block whose Text is over the threshold.
	// Input mimics the wire-blocks shape produced by executeTool.
	rawText := strings.Repeat("x", 60000)
	textJSON, _ := json.Marshal(rawText)
	arrayInput := []byte(`[{"type":"text","text":` + string(textJSON) + `}]`)

	result := MaybePersistLargeToolResult(arrayInput, "Bash", 50000, "tool-arr-1", "test-session")
	if !result.Persisted {
		t.Fatal("should persist array-form text content over threshold")
	}
	if result.FilePath == "" {
		t.Fatal("FilePath should be set")
	}

	// Persisted file MUST contain the RAW text (60000 x's), NOT the JSON
	// array wrapper. This preserves replay via DecodeResult.
	saved, err := os.ReadFile(result.FilePath)
	if err != nil {
		t.Fatalf("read persisted file: %v", err)
	}
	if string(saved) != rawText {
		t.Errorf("persisted content = %q (len %d), want raw text (len %d)", string(saved)[:min(50, len(saved))], len(saved), len(rawText))
	}
}

func TestMaybePersistLargeToolResult_ArrayFormImageNotPersisted(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	// Array form with an image block alongside an oversized text block —
	// HasImageBlock short-circuits and persistence is skipped. This is the
	// side-benefit noted in the task: images never hit disk.
	rawText := strings.Repeat("x", 60000)
	textJSON, _ := json.Marshal(rawText)
	arrayInput := []byte(`[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"abc"}},{"type":"text","text":` + string(textJSON) + `}]`)

	result := MaybePersistLargeToolResult(arrayInput, "Bash", 50000, "tool-arr-2", "test-session")
	if result.Persisted {
		t.Fatal("array-form input with image block must NOT persist (HasImageBlock short-circuit)")
	}
	if result.FilePath != "" {
		t.Errorf("FilePath = %q, want empty", result.FilePath)
	}
	// Output should be unchanged.
	if string(result.Output) != string(arrayInput) {
		t.Errorf("Output was modified: got %q, want %q", string(result.Output)[:50], string(arrayInput)[:50])
	}
}
