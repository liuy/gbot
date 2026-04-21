package short

import (
	"strings"
	"testing"
)

// TestGetCompactPrompt verifies GetCompactPrompt returns non-empty string.
func TestGetCompactPrompt(t *testing.T) {
	prompt := GetCompactPrompt("")

	if prompt == "" {
		t.Fatal("GetCompactPrompt returned empty string")
	}

	// Verify key sections exist
	if !strings.Contains(prompt, "CRITICAL: Respond with TEXT ONLY") {
		t.Error("prompt should contain no-tools preamble")
	}
	if !strings.Contains(prompt, "Primary Request and Intent") {
		t.Error("prompt should contain Primary Request section")
	}
	if !strings.Contains(prompt, "REMINDER: Do NOT call any tools") {
		t.Error("prompt should contain no-tools trailer")
	}
}

// TestGetCompactPrompt_WithCustomInstructions verifies custom instructions are appended.
func TestGetCompactPrompt_WithCustomInstructions(t *testing.T) {
	custom := "Focus on Go code changes"
	prompt := GetCompactPrompt(custom)

	if !strings.Contains(prompt, custom) {
		t.Errorf("prompt should contain custom instructions: %s", custom)
	}
	if !strings.Contains(prompt, "Additional Instructions:") {
		t.Error("prompt should contain Additional Instructions section")
	}
}

// TestGetPartialCompactPrompt verifies GetPartialCompactPrompt returns non-empty string with preservedSegment description.
func TestGetPartialCompactPrompt(t *testing.T) {
	prompt := GetPartialCompactPrompt("", "from")

	if prompt == "" {
		t.Fatal("GetPartialCompactPrompt returned empty string")
	}

	// Verify key sections exist
	if !strings.Contains(prompt, "CRITICAL: Respond with TEXT ONLY") {
		t.Error("prompt should contain no-tools preamble")
	}
	if !strings.Contains(prompt, "RECENT portion of the conversation") {
		t.Error("prompt should mention RECENT portion (preservedSegment description)")
	}
	if !strings.Contains(prompt, "earlier messages are being kept intact") {
		t.Error("prompt should mention earlier messages are intact")
	}
}

// TestGetPartialCompactPrompt_UpToDirection verifies up_to direction has different wording.
func TestGetPartialCompactPrompt_UpToDirection(t *testing.T) {
	prompt := GetPartialCompactPrompt("", "up_to")

	if !strings.Contains(prompt, "summary will be placed at the start of a continuing session") {
		t.Error("up_to prompt should mention summary at start")
	}
	if !strings.Contains(prompt, "newer messages that build on this context will follow") {
		t.Error("up_to prompt should mention newer messages follow")
	}
	// up_to should NOT have the "recent messages" phrasing
	if strings.Contains(prompt, "RECENT portion") {
		t.Error("up_to prompt should not mention RECENT portion")
	}
}

// TestGetPartialCompactPrompt_WithCustomInstructions verifies custom instructions work.
func TestGetPartialCompactPrompt_WithCustomInstructions(t *testing.T) {
	custom := "Focus on test failures"
	prompt := GetPartialCompactPrompt(custom, "from")

	if !strings.Contains(prompt, custom) {
		t.Errorf("prompt should contain custom instructions: %s", custom)
	}
}

// TestFormatCompactSummary verifies summary formatting strips analysis and reformats summary tags.
func TestFormatCompactSummary(t *testing.T) {
	raw := `<analysis>
Some analysis text here.
More thoughts.
</analysis>

<summary>
1. Primary Request and Intent:
   The user wants to implement a feature.

2. Key Technical Concepts:
   - Go
   - SQLite
</summary>`

	formatted := FormatCompactSummary(raw)

	// Analysis should be stripped
	if strings.Contains(formatted, "<analysis>") {
		t.Error("formatted summary should not contain <analysis> tag")
	}
	if strings.Contains(formatted, "Some analysis text here") {
		t.Error("formatted summary should not contain analysis content")
	}

	// Summary tag should be replaced
	if strings.Contains(formatted, "<summary>") {
		t.Error("formatted summary should not contain <summary> tag")
	}
	if !strings.Contains(formatted, "Summary:\n") {
		t.Error("formatted summary should contain 'Summary:' header")
	}

	// Content should be preserved
	if !strings.Contains(formatted, "Primary Request and Intent") {
		t.Error("formatted summary should preserve primary request section")
	}
	if !strings.Contains(formatted, "Key Technical Concepts") {
		t.Error("formatted summary should preserve technical concepts section")
	}
	if !strings.Contains(formatted, "- Go") {
		t.Error("formatted summary should preserve Go concept")
	}
}

// TestFormatCompactSummary_ExtraWhitespace verifies extra whitespace is cleaned up.
func TestFormatCompactSummary_ExtraWhitespace(t *testing.T) {
	raw := "<summary>\nContent\n\n\n\n</summary>"
	formatted := FormatCompactSummary(raw)

	// Should not have multiple consecutive newlines
	if strings.Contains(formatted, "\n\n\n") {
		t.Error("formatted summary should not have triple newlines")
	}
}

// TestFormatCompactSummary_EmptyAnalysis verifies empty analysis is handled.
func TestFormatCompactSummary_EmptyAnalysis(t *testing.T) {
	raw := `<analysis></analysis>
<summary>
Content here
</summary>`

	formatted := FormatCompactSummary(raw)

	if strings.Contains(formatted, "<analysis>") {
		t.Error("formatted summary should not contain analysis tags")
	}
	if !strings.Contains(formatted, "Content here") {
		t.Error("formatted summary should preserve content")
	}
}

// TestGetCompactUserSummaryMessage verifies user message contains summary content.
func TestGetCompactUserSummaryMessage(t *testing.T) {
	summary := `<summary>
1. Primary Request: Implement feature
</summary>`

	msg := GetCompactUserSummaryMessage(summary, false, "", "")

	if !strings.Contains(msg, "This session is being continued") {
		t.Error("message should mention session continuation")
	}
	if !strings.Contains(msg, "Primary Request: Implement feature") {
		t.Error("message should contain summary content")
	}
}

// TestGetCompactUserSummaryMessage_WithTranscriptPath verifies transcript path is included.
func TestGetCompactUserSummaryMessage_WithTranscriptPath(t *testing.T) {
	summary := "<summary>Content</summary>"
	transcriptPath := "/path/to/transcript.jsonl"

	msg := GetCompactUserSummaryMessage(summary, false, transcriptPath, "")

	if !strings.Contains(msg, transcriptPath) {
		t.Error("message should contain transcript path")
	}
	if !strings.Contains(msg, "read the full transcript at:") {
		t.Error("message should mention reading full transcript")
	}
}

// TestGetCompactUserSummaryMessage_RecentMessagesPreserved verifies preserved message note.
func TestGetCompactUserSummaryMessage_RecentMessagesPreserved(t *testing.T) {
	summary := "<summary>Content</summary>"

	msg := GetCompactUserSummaryMessage(summary, false, "", "true")

	if !strings.Contains(msg, "Recent messages are preserved verbatim") {
		t.Error("message should mention recent messages preserved")
	}
}

// TestGetCompactUserSummaryMessage_SuppressFollowUp verifies continuation instruction.
func TestGetCompactUserSummaryMessage_SuppressFollowUp(t *testing.T) {
	summary := "<summary>Content</summary>"

	msg := GetCompactUserSummaryMessage(summary, true, "", "")

	if !strings.Contains(msg, "Continue the conversation from where it left off") {
		t.Error("message should contain continuation instruction")
	}
	if !strings.Contains(msg, "without asking the user any further questions") {
		t.Error("message should mention no questions")
	}
	if !strings.Contains(msg, "do not acknowledge the summary") {
		t.Error("message should say not to acknowledge summary")
	}
}

// TestFormatCompactSummary_PreservesCodeSnippets verifies code snippets are preserved.
func TestFormatCompactSummary_PreservesCodeSnippets(t *testing.T) {
	raw := `<analysis>Thinking</analysis>
<summary>
Code:
func main() {
	fmt.Println("hello")
}
</summary>`

	formatted := FormatCompactSummary(raw)

	if !strings.Contains(formatted, "func main()") {
		t.Error("formatted summary should preserve code snippets")
	}
	if !strings.Contains(formatted, `fmt.Println("hello")`) {
		t.Error("formatted summary should preserve function call")
	}
}

// TestGetCompactPrompt_AllSections verifies all required sections are present.
func TestGetCompactPrompt_AllSections(t *testing.T) {
	prompt := GetCompactPrompt("")

	requiredSections := []string{
		"Primary Request and Intent",
		"Key Technical Concepts",
		"Files and Code Sections",
		"Errors and fixes",
		"Problem Solving",
		"All user messages",
		"Pending Tasks",
		"Current Work",
		"Optional Next Step",
	}

	for _, section := range requiredSections {
		if !strings.Contains(prompt, section) {
			t.Errorf("prompt should contain section: %s", section)
		}
	}
}

// TestGetPartialCompactPrompt_AllSections verifies partial prompt has correct sections.
func TestGetPartialCompactPrompt_AllSections(t *testing.T) {
	prompt := GetPartialCompactPrompt("", "from")

	requiredSections := []string{
		"Primary Request and Intent",
		"Key Technical Concepts",
		"Files and Code Sections",
		"Errors and fixes",
		"Problem Solving",
		"All user messages",
		"Pending Tasks",
		"Current Work",
		"Optional Next Step",
	}

	for _, section := range requiredSections {
		if !strings.Contains(prompt, section) {
			t.Errorf("partial prompt should contain section: %s", section)
		}
	}
}

// Lines 232-234, 236-238: ExtractFirstPrompt — content not a map, content field missing
func TestExtractFirstPrompt_NoMessageKey(t *testing.T) {
	content := `{"type":"user"}`
	result := ExtractFirstPrompt(content)
	if result != "" {
		t.Errorf("got %q, want empty (no message key)", result)
	}
}

func TestExtractFirstPrompt_NoContentKey(t *testing.T) {
	content := `{"type":"user","message":{}}`
	result := ExtractFirstPrompt(content)
	if result != "" {
		t.Errorf("got %q, want empty (no content key)", result)
	}
}

// Line 252-253: ExtractFirstPrompt — second command-name (should not override fallback)
func TestExtractFirstPrompt_MultipleCommandNames(t *testing.T) {
	content := `{"type":"user","message":{"content":"<command-name>first</command-name>"}}`
	result := ExtractFirstPrompt(content)
	if result != "first" {
		t.Errorf("got %q, want first", result)
	}
}

// Lines 298-299, 302-303: extractTextBlocks — non-text blocks, empty text
func TestExtractTextBlocks_NonTextBlock(t *testing.T) {
	// Array with non-text blocks
	content := []any{
		map[string]any{"type": "tool_use", "id": "tu1"},
		map[string]any{"type": "text", "text": "hello"},
		map[string]any{"type": "text", "text": ""},
		map[string]any{"type": "image"},
	}
	texts := extractTextBlocks(content)
	if len(texts) != 1 {
		t.Errorf("got %d texts, want 1 (only non-empty text blocks)", len(texts))
	}
	if texts[0] != "hello" {
		t.Errorf("texts[0] = %q, want hello", texts[0])
	}
}

func TestExtractTextBlocks_BlockNotMap(t *testing.T) {
	content := []any{
		"not-a-map",
		map[string]any{"type": "text", "text": "hello"},
	}
	texts := extractTextBlocks(content)
	if len(texts) != 1 {
		t.Errorf("got %d texts, want 1", len(texts))
	}
}

func TestExtractTextBlocks_StringContent(t *testing.T) {
	texts := extractTextBlocks("plain string content")
	if len(texts) != 1 || texts[0] != "plain string content" {
		t.Errorf("got %v for string content", texts)
	}
}

func TestExtractTextBlocks_NilContent(t *testing.T) {
	texts := extractTextBlocks(nil)
	if len(texts) != 0 {
		t.Errorf("got %d texts, want 0 for nil", len(texts))
	}
}

func TestExtractTextBlocks_IntContent(t *testing.T) {
	texts := extractTextBlocks(42)
	if len(texts) != 0 {
		t.Errorf("got %d texts, want 0 for int", len(texts))
	}
}

func TestExtractFirstPrompt_ObjectContent(t *testing.T) {
	// ExtractFirstPrompt takes a JSON string containing {"message":{"content":"string"}}
	// where content is a string (legacy format)
	input := `{"message":{"content":"hello world"}}`
	prompt := ExtractFirstPrompt(input)
	if prompt != "hello world" {
		t.Errorf("got %q, want hello world", prompt)
	}
}

func TestExtractFirstPrompt_EmptyContent_Coverage(t *testing.T) {
	prompt := ExtractFirstPrompt("")
	if prompt != "" {
		t.Errorf("got %q, want empty string for empty input", prompt)
	}
}

func TestExtractFirstPrompt_NonTextContent(t *testing.T) {
	// Content array with non-text block types
	input := `{"message":{"content":[{"type":"tool_use","id":"tu1","name":"bash","input":{}}]}}`
	prompt := ExtractFirstPrompt(input)
	if prompt != "" {
		t.Errorf("got %q, want empty for non-text only content", prompt)
	}
}

// TestExtractFirstPrompt_EmptyAfterStrip tests the path where text becomes empty
// after stripping newlines (session.go line 252-253).
func TestExtractFirstPrompt_EmptyAfterStrip(t *testing.T) {
	// ExtractFirstPrompt expects {"message":{"content":[...]}} structure
	content := `{"message":{"content":[{"type":"text","text":"\n\n\n"}]}}`
	result := ExtractFirstPrompt(content)
	// Text is only newlines -> stripped to empty -> continue -> returns ""
	if result != "" {
		t.Errorf("expected empty for whitespace-only text, got %q", result)
	}
}

