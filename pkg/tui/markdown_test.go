package tui

import (
	"strings"
	"testing"
)

func TestRender_PlainText(t *testing.T) {
	t.Parallel()
	result := Render("hello world")
	if !strings.Contains(result, "hello world") {
		t.Errorf("expected plain text, got: %q", result)
	}
}

func TestRender_Bold(t *testing.T) {
	t.Parallel()
	result := Render("**bold**")
	if !strings.Contains(result, "bold") {
		t.Errorf("expected bold text, got: %q", result)
	}
}

func TestRender_Italic(t *testing.T) {
	t.Parallel()
	result := Render("*italic*")
	if !strings.Contains(result, "italic") {
		t.Errorf("expected italic text, got: %q", result)
	}
}

func TestRender_Heading(t *testing.T) {
	t.Parallel()
	result := Render("# Heading 1")
	if !strings.Contains(result, "Heading 1") {
		t.Errorf("expected heading text, got: %q", result)
	}
}

func TestRender_InlineCode(t *testing.T) {
	t.Parallel()
	result := Render("use `code` here")
	if !strings.Contains(result, "code") {
		t.Errorf("expected inline code, got: %q", result)
	}
}

func TestRender_CodeBlock(t *testing.T) {
	t.Parallel()
	result := Render("```go\nfmt.Println(\"hello\")\n```")
	// ANSI codes may split the text, check for fmt and Println separately
	if !strings.Contains(result, "fmt") || !strings.Contains(result, "Println") {
		t.Errorf("expected code block content, got: %q", result)
	}
}

func TestRender_CodeBlock_WithHighlight(t *testing.T) {
	t.Parallel()
	result := highlightCode("fmt.Println(\"hello\")", "go")
	// ANSI codes may split tokens, check separately
	if !strings.Contains(result, "fmt") || !strings.Contains(result, "Println") {
		t.Errorf("expected highlighted code, got: %q", result)
	}
}

func TestRender_Link(t *testing.T) {
	t.Parallel()
	result := Render("[click here](https://example.com)")
	if !strings.Contains(result, "click here") {
		t.Errorf("expected link text, got: %q", result)
	}
	if !strings.Contains(result, "https://example.com") {
		t.Errorf("expected URL, got: %q", result)
	}
}

func TestRender_Image(t *testing.T) {
	t.Parallel()
	result := Render("![alt text](https://example.com/image.png)")
	if !strings.Contains(result, "https://example.com/image.png") {
		t.Errorf("expected image URL, got: %q", result)
	}
}

func TestRender_UnorderedList(t *testing.T) {
	t.Parallel()
	result := Render("- item1\n- item2\n- item3")
	if !strings.Contains(result, "item1") {
		t.Errorf("expected list item, got: %q", result)
	}
	if !strings.Contains(result, "-") {
		t.Errorf("expected bullet, got: %q", result)
	}
}

func TestRender_OrderedList(t *testing.T) {
	t.Parallel()
	result := Render("1. first\n2. second\n3. third")
	if !strings.Contains(result, "first") {
		t.Errorf("expected list item, got: %q", result)
	}
	if !strings.Contains(result, "1.") {
		t.Errorf("expected ordered number, got: %q", result)
	}
}

func TestRender_BlockQuote(t *testing.T) {
	t.Parallel()
	result := Render("> quote text")
	if !strings.Contains(result, "quote text") {
		t.Errorf("expected quote text, got: %q", result)
	}
}

func TestRender_HorizontalRule(t *testing.T) {
	t.Parallel()
	result := Render("above\n\n---\n\nbelow")
	if !strings.Contains(result, "above") || !strings.Contains(result, "below") {
		t.Errorf("expected text around hr, got: %q", result)
	}
	if !strings.Contains(result, "───") && !strings.Contains(result, "---") {
		t.Errorf("expected horizontal rule, got: %q", result)
	}
}

func TestRender_Strikethrough(t *testing.T) {
	t.Parallel()
	result := Render("~~deleted~~")
	if !strings.Contains(result, "deleted") {
		t.Errorf("expected strikethrough text, got: %q", result)
	}
}

func TestRender_MixedFormatting(t *testing.T) {
	t.Parallel()
	result := Render("# Title\n\nSome **bold** and *italic* text with `code`.\n\n- item1\n- item2")
	if !strings.Contains(result, "Title") {
		t.Errorf("expected heading, got: %q", result)
	}
	if !strings.Contains(result, "bold") {
		t.Errorf("expected bold, got: %q", result)
	}
	if !strings.Contains(result, "italic") {
		t.Errorf("expected italic, got: %q", result)
	}
	if !strings.Contains(result, "code") {
		t.Errorf("expected code, got: %q", result)
	}
}

func TestRender_Escapes(t *testing.T) {
	t.Parallel()
	result := Render("not \\*bold\\*")
	if !strings.Contains(result, "not") {
		t.Errorf("expected escaped text, got: %q", result)
	}
}

func TestRender_Empty(t *testing.T) {
	t.Parallel()
	result := Render("")
	if result != "" {
		t.Errorf("expected empty, got: %q", result)
	}
}

func TestRender_NestedFormatting(t *testing.T) {
	t.Parallel()
	result := Render("**bold *italic* text**")
	if !strings.Contains(result, "bold") {
		t.Errorf("expected nested formatting, got: %q", result)
	}
}

func TestRenderWidth(t *testing.T) {
	t.Parallel()
	result := RenderWidth("hello world", 40)
	if !strings.Contains(result, "hello") {
		t.Errorf("expected wrapped text, got: %q", result)
	}
}

func TestHighlightCode_UnknownLang(t *testing.T) {
	t.Parallel()
	result := highlightCode("some code here", "unknownlang123")
	if !strings.Contains(result, "some code here") {
		t.Errorf("expected fallback plain text, got: %q", result)
	}
}

func TestHighlightCode_EmptyCode(t *testing.T) {
	t.Parallel()
	result := highlightCode("", "go")
	if result != "" {
		t.Errorf("expected empty, got: %q", result)
	}
}

func TestRender_Table(t *testing.T) {
	t.Parallel()
	input := "| Name | Age |\n|------|-----|\n| Alice | 30 |\n| Bob | 25 |"
	result := Render(input)
	if !strings.Contains(result, "Alice") || !strings.Contains(result, "Bob") {
		t.Errorf("expected table content, got: %q", result)
	}
}

func TestRender_NestedList(t *testing.T) {
	t.Parallel()
	input := "- item1\n  - nested1\n  - nested2\n- item2"
	result := Render(input)
	if !strings.Contains(result, "item1") || !strings.Contains(result, "nested1") {
		t.Errorf("expected nested list, got: %q", result)
	}
}

func TestRender_MultipleHeadings(t *testing.T) {
	t.Parallel()
	input := "# H1\n## H2\n### H3"
	result := Render(input)
	if !strings.Contains(result, "H1") || !strings.Contains(result, "H2") || !strings.Contains(result, "H3") {
		t.Errorf("expected all headings, got: %q", result)
	}
}

func TestRender_BoldItalic(t *testing.T) {
	t.Parallel()
	result := Render("***bold italic***")
	if !strings.Contains(result, "bold italic") {
		t.Errorf("expected bold italic text, got: %q", result)
	}
}
