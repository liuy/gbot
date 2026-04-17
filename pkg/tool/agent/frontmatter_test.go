package agent

import (
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
	// Content is everything after the closing ---
	if !strings.Contains(result.Content, "System prompt here.") {
		t.Errorf("Content = %q, should contain 'System prompt here.'", result.Content)
	}
}

func TestParseFrontmatter_NoFrontmatter(t *testing.T) {
	input := "Just some markdown content\nwith no frontmatter."
	result := ParseFrontmatter(input, "")

	if len(result.Frontmatter) != 0 {
		t.Errorf("expected empty frontmatter, got %v", result.Frontmatter)
	}
	if result.Content != input {
		t.Errorf("Content = %q, want %q", result.Content, input)
	}
}

func TestParseFrontmatter_EmptyFrontmatter(t *testing.T) {
	input := "---\n---\nBody content here."
	result := ParseFrontmatter(input, "")

	if len(result.Frontmatter) != 0 {
		t.Errorf("expected empty frontmatter, got %v", result.Frontmatter)
	}
	if !strings.Contains(result.Content, "Body content here.") {
		t.Errorf("Content = %q, should contain body", result.Content)
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
	// The maxFrontmatterFileSize constant is used by the loader.
	input := "---\nname: test\n---\n" + strings.Repeat("x", 2*1024*1024)
	if len(input) <= maxFrontmatterFileSize {
		t.Fatal("test input should be larger than maxFrontmatterFileSize")
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
	if len(result.Frontmatter) != 0 {
		t.Errorf("expected empty frontmatter on broken YAML, got %v", result.Frontmatter)
	}
}
