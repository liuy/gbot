package fileedit

import (
	"strings"
	"testing"
)

func TestFindNearbyHint_OneWordDifferent(t *testing.T) {
	// Real case: "layer" vs "level" — only one word differs.
	fileContent := `func parse() {
	// Fallback: try unwrapping one more level (gbot's format).
	var obj struct {
		Output string ` + "`" + `json:"output"` + "`" + `
	}
	if json.Unmarshal(
		return rest, elapsed
}`

	oldString := "\t// Fallback: try unwrapping one more layer (gbot's format).\n\tvar obj struct {\n\t\tOutput string `json:\"output\"`\n\t}\n\tif json.Unmarshal(\n\t\treturn rest, elapsed"

	hint := findNearbyHint(fileContent, oldString)
	if hint == "" {
		t.Fatal("expected hint, got empty string")
	}
	// Should point to the "level" line, not some random place.
	if !strings.Contains(hint, "level") {
		t.Errorf("hint should contain 'level' (the actual file content), got:\n%s", hint)
	}
	if !strings.Contains(hint, "1:") {
		// Line numbers should be present.
		t.Errorf("hint should contain line numbers, got:\n%s", hint)
	}
}

func TestFindNearbyHint_FunctionRenamed(t *testing.T) {
	// Real case: function was renamed, but body is similar.
	fileContent := `func (e *StreamingToolExecutor) emitToolError(tt *TrackedTool, err error) {
	e.firePostToolUseHook(tt, true)
	// Error content
	slog.Error("tool error")
	e.doEmit(types.QueryEvent{
		Type: types.EventToolEnd,
	})
}`

	oldString := "func (e *StreamingToolExecutor) emitToolError(tt *TrackedTool, err error, elapsed time.Duration) {\n\te.firePostToolUseHook(tt, true)\n\t// Error content\n\tslog.Error(\"tool error\")\n\te.doEmit(types.QueryEvent{"

	hint := findNearbyHint(fileContent, oldString)
	if hint == "" {
		t.Fatal("expected hint, got empty string")
	}
	if !strings.Contains(hint, "emitToolError") {
		t.Errorf("hint should contain 'emitToolError', got:\n%s", hint)
	}
}

func TestFindNearbyHint_AllGenericLines(t *testing.T) {
	// old_string is just closing braces — no meaningful lines.
	fileContent := "func foo() {\n\tif true {\n\t\tdoStuff()\n\t}\n}\n"
	oldString := "\t}\n}\n"

	hint := findNearbyHint(fileContent, oldString)
	if hint != "" {
		// All lines are too short/generic, should return empty.
		t.Errorf("expected empty hint for all-generic old_string, got:\n%s", hint)
	}
}

func TestFindNearbyHint_EmptyFile(t *testing.T) {
	hint := findNearbyHint("", "func foo() {")
	if hint != "" {
		t.Errorf("expected empty hint for empty file, got:\n%s", hint)
	}
}

func TestFindNearbyHint_EmptyOldString(t *testing.T) {
	hint := findNearbyHint("some file content", "")
	if hint != "" {
		t.Errorf("expected empty hint for empty old_string, got:\n%s", hint)
	}
}

func TestFindNearbyHint_NoMatch(t *testing.T) {
	// old_string has no token overlap with file content.
	fileContent := "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"
	oldString := "SELECT * FROM users WHERE id = 42;"

	hint := findNearbyHint(fileContent, oldString)
	if hint != "" {
		t.Errorf("expected empty hint when no overlap, got:\n%s", hint)
	}
}

func TestFindNearbyHint_MatchAtFileStart(t *testing.T) {
	// Best match is at the very start — context should not go negative.
	fileContent := "func importantThing() {\n\treturn 42\n}\n\nfunc other() {\n\treturn 0\n}\n"
	oldString := "func importantThing() {\n\treturn 99\n}"

	hint := findNearbyHint(fileContent, oldString)
	if hint == "" {
		t.Fatal("expected hint, got empty string")
	}
	if !strings.Contains(hint, "importantThing") {
		t.Errorf("hint should contain 'importantThing', got:\n%s", hint)
	}
}

func TestFindNearbyHint_MatchAtFileEnd(t *testing.T) {
	// Best match is at the end — context should not exceed file length.
	fileContent := "package main\n\nfunc a() {}\n\nfunc b() {}\n\nfunc importantThing() {\n\treturn 42\n}\n"
	oldString := "func importantThing() {\n\treturn 99\n}"

	hint := findNearbyHint(fileContent, oldString)
	if hint == "" {
		t.Fatal("expected hint, got empty string")
	}
	if !strings.Contains(hint, "importantThing") {
		t.Errorf("hint should contain 'importantThing', got:\n%s", hint)
	}
}

func TestFindNearbyHint_LongComment(t *testing.T) {
	// Real case: 72-line function comment, only part of it changed.
	fileContent := "// TestTimestampInjection_AttachmentUserPrompt verifies the full chain:\n" +
		"// EnqueueAttachment(prompt) -> createAttachmentMessages -> normalizeAttachment\n" +
		"func TestTimestampInjection(t *testing.T) {\n\tt.Parallel()\n}\n"

	// Old string has "normalizeAttachmentForAPI" (extra suffix).
	oldString := "// TestTimestampInjection_AttachmentUserPrompt verifies the full chain:\n" +
		"// EnqueueAttachment(prompt) -> createAttachmentMessages -> normalizeAttachmentForAPI\n" +
		"func TestTimestampInjection(t *testing.T) {\n\tt.Parallel()\n"

	hint := findNearbyHint(fileContent, oldString)
	if hint == "" {
		t.Fatal("expected hint, got empty string")
	}
	if !strings.Contains(hint, "TestTimestampInjection") {
		t.Errorf("hint should contain 'TestTimestampInjection', got:\n%s", hint)
	}
}

func TestFindNearbyHint_PrefersBetterMatch(t *testing.T) {
	// File has two similar regions; should pick the better one.
	fileContent := `func handleRequest(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	w.WriteHeader(200)
}

func handleRequest(w http.ResponseWriter, r *http.Request, extra string) {
	id := r.URL.Query().Get("id")
	w.WriteHeader(200)
	w.Write([]byte(extra))
}`

	// old_string matches the second signature better (has extra param).
	oldString := "func handleRequest(w http.ResponseWriter, r *http.Request, extra string) {\n\tid := r.URL.Query().Get(\"id\")\n\tw.WriteHeader(200)\n\tw.Write([]byte(extra))"

	hint := findNearbyHint(fileContent, oldString)
	if hint == "" {
		t.Fatal("expected hint, got empty string")
	}
	// Should match the second function (with extra.Write).
	if !strings.Contains(hint, "Write([]byte(extra))") {
		t.Errorf("hint should prefer the better-matching second function, got:\n%s", hint)
	}
}

func TestTokenOverlapScore(t *testing.T) {
	tests := []struct {
		a, b []string
		want int
	}{
		{[]string{"hello", "world"}, []string{"hello", "world"}, 2},
		{[]string{"hello", "world"}, []string{"hello"}, 1},
		{[]string{"hello"}, []string{"world"}, 0},
		{[]string{}, []string{"hello"}, 0},
		{[]string{"hello"}, []string{}, 0},
	}
	for _, tt := range tests {
		got := tokenOverlapScore(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("tokenOverlapScore(%v, %v) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestIsGeneric(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"}", true},
		{"{", true},
		{"", true},
		{"ab", true},
		{"func", false},
		{"return", true},
		{"return x", false},
	}
	for _, tt := range tests {
		got := isGeneric(tt.input)
		if got != tt.want {
			t.Errorf("isGeneric(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
