package markdown

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestParseFrontmatter_WithValidYAML(t *testing.T) {
	input := "---\nname: my-agent\ndescription: \"A test agent\"\nmodel: sonnet\n---\nSystem prompt here."
	result := ParseFrontmatter(input, "")

	if len(result.Frontmatter) != 3 {
		t.Fatalf("expected 3 frontmatter keys, got %d: %v", len(result.Frontmatter), result.Frontmatter)
	}
	if result.Frontmatter["name"] != "my-agent" {
		t.Errorf("name = %v, want my-agent", result.Frontmatter["name"])
	}
	if result.Frontmatter["description"] != "A test agent" {
		t.Errorf("description = %v, want 'A test agent'", result.Frontmatter["description"])
	}
	if result.Frontmatter["model"] != "sonnet" {
		t.Errorf("model = %v, want sonnet", result.Frontmatter["model"])
	}
	// Content is exactly what follows the closing --- delimiter, including the
	// delimiter's own newline being consumed and nothing else.
	if result.Content != "System prompt here." {
		t.Errorf("Content = %q, want %q", result.Content, "System prompt here.")
	}
}

func TestParseFrontmatter_NoFrontmatter(t *testing.T) {
	input := "Just some markdown content\nwith no frontmatter."
	result := ParseFrontmatter(input, "")

	// Callers range/index the map unconditionally; a nil map would be a
	// different (and surprising) shape than an empty one.
	if result.Frontmatter == nil {
		t.Error("Frontmatter = nil, want non-nil empty map")
	}
	if len(result.Frontmatter) != 0 {
		t.Errorf("expected empty frontmatter, got %v", result.Frontmatter)
	}
	if result.Content != input {
		t.Errorf("Content = %q, want %q", result.Content, input)
	}
}

func TestParseFrontmatter_EmptyFrontmatter(t *testing.T) {
	// A blank line inside the block is YAML whitespace, not a delimiter: the
	// captured group is empty, not the whole file.
	input := "---\n\n---\nBody content here."
	result := ParseFrontmatter(input, "")

	if result.Frontmatter == nil {
		t.Error("Frontmatter = nil, want non-nil empty map")
	}
	if len(result.Frontmatter) != 0 {
		t.Errorf("expected empty frontmatter, got %v", result.Frontmatter)
	}
	if result.Content != "Body content here." {
		t.Errorf("Content = %q, want %q", result.Content, "Body content here.")
	}
}

// The closing delimiter does not need its own line: the captured YAML group is
// delimited by the literal `---`, not by `\n---`. Without this the delimiters
// leak into Content and the body is unreadable.
func TestParseFrontmatter_EmptyDelimiters(t *testing.T) {
	input := "---\n---\nBody content here."
	result := ParseFrontmatter(input, "")

	if result.Frontmatter == nil {
		t.Error("Frontmatter = nil, want non-nil empty map")
	}
	if len(result.Frontmatter) != 0 {
		t.Errorf("expected empty frontmatter, got %v", result.Frontmatter)
	}
	if result.Content != "Body content here." {
		t.Errorf("Content = %q, want %q", result.Content, "Body content here.")
	}
}

func TestParseFrontmatter_InvalidYAML_RetryWithQuoting(t *testing.T) {
	// Glob pattern with special chars that need quoting
	input := "---\ntools: **/*.{ts,tsx}\n---\nBody."
	result := ParseFrontmatter(input, "")

	// After quoteProblematicValues retry, tools should be parsed
	if len(result.Frontmatter) == 0 {
		t.Fatalf("expected frontmatter after retry, got empty")
	}
	// The value should be the glob pattern
	tools, ok := result.Frontmatter["tools"].(string)
	if !ok {
		t.Fatalf("tools = %v (%T), want string", result.Frontmatter["tools"], result.Frontmatter["tools"])
	}
	if tools != "**/*.{ts,tsx}" {
		t.Errorf("tools = %q, want '**/*.{ts,tsx}'", tools)
	}
}

func TestParseFrontmatter_ListValues(t *testing.T) {
	input := "---\ntools:\n  - Read\n  - Grep\n  - Glob\n---\nBody."
	result := ParseFrontmatter(input, "")

	tools, ok := result.Frontmatter["tools"].([]any)
	if !ok {
		t.Fatalf("tools = %v (%T), want []any", result.Frontmatter["tools"], result.Frontmatter["tools"])
	}
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d: %v", len(tools), tools)
	}
	if tools[0] != "Read" {
		t.Errorf("tools[0] = %v, want Read", tools[0])
	}
}

func TestParseFrontmatter_NumericMaxTurns(t *testing.T) {
	input := "---\nmaxTurns: 50\n---\nBody."
	result := ParseFrontmatter(input, "")

	val, ok := result.Frontmatter["maxTurns"].(int)
	if !ok {
		t.Fatalf("maxTurns = %v (%T), want int", result.Frontmatter["maxTurns"], result.Frontmatter["maxTurns"])
	}
	if val != 50 {
		t.Errorf("maxTurns = %d, want 50", val)
	}
}

func TestParseFrontmatter_BOMStripped(t *testing.T) {
	// BOM + valid frontmatter
	input := "\xEF\xBB\xBF---\nname: test\n---\nBody."
	result := ParseFrontmatter(input, "")

	if result.Frontmatter["name"] != "test" {
		t.Errorf("name = %v, want 'test' (BOM not stripped?)", result.Frontmatter["name"])
	}
}

func TestParseFrontmatter_OversizedFile(t *testing.T) {
	// This tests that the caller is responsible for size checks;
	// ParseFrontmatter itself parses whatever it receives.
	// The MaxFrontmatterFileSize constant is used by the loader.
	input := "---\nname: test\n---\n" + strings.Repeat("x", 2*1024*1024)
	if len(input) <= MaxFrontmatterFileSize {
		t.Fatal("test input should be larger than MaxFrontmatterFileSize")
	}
	// ParseFrontmatter still works — the loader checks size before calling
	result := ParseFrontmatter(input, "")
	if result.Frontmatter["name"] != "test" {
		t.Errorf("name = %v, want 'test'", result.Frontmatter["name"])
	}
}

func TestQuoteProblematicValues(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "glob pattern",
			input: "tools: **/*.{ts,tsx}",
			want:  `tools: "**/*.{ts,tsx}"`,
		},
		{
			name:  "colon space in value",
			input: "description: This: is a test",
			want:  `description: "This: is a test"`,
		},
		{
			name:  "already double quoted",
			input: `tools: "**/*.ts"`,
			want:  `tools: "**/*.ts"`,
		},
		{
			name:  "already single quoted",
			input: `tools: '**/*.ts'`,
			want:  `tools: '**/*.ts'`,
		},
		{
			name:  "no special chars",
			input: "name: my-agent",
			want:  "name: my-agent",
		},
		{
			// backslash alone doesn't trigger quoting (not in yamlSpecialChars)
			name:  "backslash no special chars",
			input: `path: C:\Users\test`,
			want:  `path: C:\Users\test`,
		},
		{
			// double quote alone doesn't trigger quoting
			name:  "double quote no special chars",
			input: `desc: He said "hello"`,
			want:  `desc: He said "hello"`,
		},
		{
			// backslash IS escaped when quoting triggered by other special chars
			name:  "backslash escaped with glob trigger",
			input: `tools: **\{src}/*.{ts}`,
			want:  `tools: "**\\{src}/*.{ts}"`,
		},
		{
			name:  "indented line not matched",
			input: "  - item with : colon",
			want:  "  - item with : colon",
		},
		{
			name:  "hash in value",
			input: "color: #ff0000",
			want:  `color: "#ff0000"`,
		},
		{
			// "already quoted" requires BOTH delimiters; a lone leading
			// double quote must not short-circuit the quoting path.
			name:  "lone leading double quote still quoted",
			input: `tools: "**/*.{ts}`,
			want:  `tools: "\"**/*.{ts}"`,
		},
		{
			name:  "lone trailing double quote still quoted",
			input: `tools: **/*.{ts}"`,
			want:  `tools: "**/*.{ts}\""`,
		},
		{
			name:  "lone leading single quote still quoted",
			input: `tools: '**/*.{ts}`,
			want:  `tools: "'**/*.{ts}"`,
		},
		{
			name:  "lone trailing single quote still quoted",
			input: `tools: **/*.{ts}'`,
			want:  `tools: "**/*.{ts}'"`,
		},
		{
			// The double quote is escaped only once quoting is triggered;
			// without the escape the emitted YAML is malformed.
			name:  "inner double quote escaped",
			input: `tools: **"{a}`,
			want:  `tools: "**\"{a}"`,
		},
		{
			// The `continue` after an already-quoted line must not stop the
			// loop: later lines still have to be emitted.
			name:  "already quoted line keeps following lines",
			input: "a: \"x\"\nname: my-agent",
			want:  "a: \"x\"\nname: my-agent",
		},
		{
			name:  "quoted line keeps following lines",
			input: "tools: **/*{a}\nname: b",
			want:  "tools: \"**/*{a}\"\nname: b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := quoteProblematicValues(tt.input)
			if got != tt.want {
				t.Errorf("quoteProblematicValues(%q)\n  got:  %q\n  want: %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseFrontmatter_FailOpenOnCompletelyBrokenYAML(t *testing.T) {
	// Unrecoverable YAML — even after quoting retry
	input := "---\n: [broken: {\n---\nBody."
	result := ParseFrontmatter(input, "test.md")

	// Should fail open: empty frontmatter, content is everything after ---
	if result.Frontmatter == nil {
		t.Error("Frontmatter = nil, want non-nil empty map (the retry's nil result must not leak)")
	}
	if len(result.Frontmatter) != 0 {
		t.Errorf("expected empty frontmatter on broken YAML, got %v", result.Frontmatter)
	}
	if result.Content != "Body." {
		t.Errorf("Content = %q, want %q", result.Content, "Body.")
	}
}

// The body slice starts at the end of the whole frontmatter match, not at the
// start of the match or at the start of the captured YAML group.
func TestParseFrontmatter_ContentBoundaryIsEndOfDelimiter(t *testing.T) {
	input := "---\nname: a\n---\nBody.\nMore."
	result := ParseFrontmatter(input, "")

	if result.Content != "Body.\nMore." {
		t.Errorf("Content = %q, want %q", result.Content, "Body.\nMore.")
	}
	if strings.Contains(result.Content, "---") {
		t.Errorf("Content = %q, must not include the closing delimiter", result.Content)
	}
	if strings.Contains(result.Content, "name: a") {
		t.Errorf("Content = %q, must not include the frontmatter text", result.Content)
	}
}

// MaxFrontmatterFileSize is the loader's 1 MiB cap; the exact value is the
// contract, so pin it rather than only comparing against it.
func TestMaxFrontmatterFileSize(t *testing.T) {
	if MaxFrontmatterFileSize != 1048576 {
		t.Errorf("MaxFrontmatterFileSize = %d, want %d", MaxFrontmatterFileSize, 1048576)
	}
}

type capturedRecord struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

type captureHandler struct{ recs *[]capturedRecord }

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	*h.recs = append(*h.recs, capturedRecord{level: r.Level, msg: r.Message, attrs: attrs})
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

// The fail-open path is silent unless it warns: the operator loses the fact
// that a file's frontmatter was ignored. The location attribute is what makes
// the warning actionable, and it is only prefixed when a source path exists.
func TestParseFrontmatter_FailOpenWarnsWithLocation(t *testing.T) {
	var recs []capturedRecord
	prev := slog.Default()
	slog.SetDefault(slog.New(&captureHandler{recs: &recs}))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ParseFrontmatter("---\n: [broken: {\n---\nBody.", "agents/x.md")

	if len(recs) != 1 {
		t.Fatalf("expected exactly 1 log record, got %d: %+v", len(recs), recs)
	}
	rec := recs[0]
	if rec.level != slog.LevelWarn {
		t.Errorf("level = %v, want %v", rec.level, slog.LevelWarn)
	}
	if rec.msg != "frontmatter: failed to parse YAML" {
		t.Errorf("msg = %q, want %q", rec.msg, "frontmatter: failed to parse YAML")
	}
	if got, want := rec.attrs["location"], " in agents/x.md"; got != want {
		t.Errorf("location = %q, want %q", got, want)
	}
	if _, ok := rec.attrs["error"].(error); !ok {
		t.Errorf("error attr = %#v, want an error value", rec.attrs["error"])
	}
}

func TestParseFrontmatter_FailOpenWarnsWithEmptyLocation(t *testing.T) {
	var recs []capturedRecord
	prev := slog.Default()
	slog.SetDefault(slog.New(&captureHandler{recs: &recs}))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ParseFrontmatter("---\n: [broken: {\n---\nBody.", "")

	if len(recs) != 1 {
		t.Fatalf("expected exactly 1 log record, got %d: %+v", len(recs), recs)
	}
	if got, want := recs[0].attrs["location"], ""; got != want {
		t.Errorf("location = %q, want %q (no source path, so no ' in ' prefix)", got, want)
	}
}

func TestParseYAML_EmptyInput(t *testing.T) {
	result, err := parseYAML("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil for empty YAML")
	}
	if len(result) != 0 {
		t.Errorf("expected empty map for empty YAML, got %v", result)
	}
}

func TestParseYAML_ValidInput(t *testing.T) {
	result, err := parseYAML("name: test\nvalue: 42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["name"] != "test" {
		t.Errorf("name = %v, want 'test'", result["name"])
	}
}

func TestParseYAML_InvalidInput(t *testing.T) {
	_, err := parseYAML(": [broken: {")
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
	if !strings.Contains(err.Error(), "yaml") {
		t.Errorf("error should mention yaml, got %v", err)
	}
}
