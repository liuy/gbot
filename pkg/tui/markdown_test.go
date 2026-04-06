package tui

import (
	"os"
	"regexp"
	"strings"
	"testing"

	ast "github.com/gomarkdown/markdown/ast"
)

// ansiStyleRe matches common ANSI SGR sequences (e.g., \x1b[1m = bold, \x1b[3m = italic, etc.)
var ansiStyleRe = regexp.MustCompile(`\x1b\[\d+.*?m`)

// hasANSIStyle returns true if s contains any ANSI styling escape sequences.
func hasANSIStyle(s string) bool {
	return ansiStyleRe.MatchString(s)
}

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

func TestRender_Heading1(t *testing.T) {
	t.Parallel()
	result := Render("# Heading 1")
	if !strings.Contains(result, "Heading 1") {
		t.Errorf("expected heading text, got: %q", result)
	}
}

func TestRender_Heading2(t *testing.T) {
	t.Parallel()
	result := Render("## Heading 2")
	if !strings.Contains(result, "Heading 2") {
		t.Errorf("expected h2 text, got: %q", result)
	}
}

func TestRender_Heading3Plus(t *testing.T) {
	t.Parallel()
	result := Render("### Heading 3")
	if !strings.Contains(result, "Heading 3") {
		t.Errorf("expected h3 text, got: %q", result)
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
	if !strings.Contains(result, "fmt") || !strings.Contains(result, "Println") {
		t.Errorf("expected code block content, got: %q", result)
	}
}

func TestRender_CodeBlock_WithHighlight(t *testing.T) {
	t.Parallel()
	result := highlightCode("fmt.Println(\"hello\")", "go")
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
}

func TestRender_Link_Mailto(t *testing.T) {
	t.Parallel()
	result := Render("[email me](mailto:user@example.com)")
	if !strings.Contains(result, "user@example.com") {
		t.Errorf("expected email text, got: %q", result)
	}
	// Should NOT show "mailto:" prefix
	if strings.Contains(result, "mailto:") {
		t.Errorf("mailto: should be stripped, got: %q", result)
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

func TestRender_OrderedList_Depth2_Letters(t *testing.T) {
	t.Parallel()
	input := "1. item1\n   1. nested-a\n   2. nested-b\n2. item2"
	result := Render(input)
	if !strings.Contains(result, "item1") {
		t.Errorf("expected item1, got: %q", result)
	}
	// depth 2 should use letter numbering
	if !strings.Contains(result, "a.") {
		t.Errorf("expected letter a., got: %q", result)
	}
	if !strings.Contains(result, "b.") {
		t.Errorf("expected letter b., got: %q", result)
	}
}

func TestRender_OrderedList_Depth3_Roman(t *testing.T) {
	t.Parallel()
	input := "1. level1\n   1. level2-a\n      1. level3-i\n      2. level3-ii\n   2. level2-b"
	result := Render(input)
	if !strings.Contains(result, "level1") {
		t.Errorf("expected level1, got: %q", result)
	}
	// depth 3 should use roman numerals
	if !strings.Contains(result, "i.") {
		t.Errorf("expected roman i., got: %q", result)
	}
	if !strings.Contains(result, "ii.") {
		t.Errorf("expected roman ii., got: %q", result)
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

func TestRender_Strikethrough_Disabled(t *testing.T) {
	t.Parallel()
	// Strikethrough is disabled; ~~ should be treated as literal text
	result := Render("about ~~100~~ things")
	if !strings.Contains(result, "100") {
		t.Errorf("expected text content, got: %q", result)
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

func TestRender_Table_SeparatorRow(t *testing.T) {
	t.Parallel()
	input := "| Name | Age |\n|------|-----|\n| Alice | 30 |"
	result := Render(input)
	// Should contain box-drawing separator row with ┼
	if !strings.Contains(result, "┼") {
		t.Errorf("expected separator row with box-drawing chars, got: %q", result)
	}
}

func TestRender_Table_ColumnWidths(t *testing.T) {
	t.Parallel()
	input := "| Name | Age |\n|------|-----|\n| Alice | 30 |\n| BobTheBuilder | 25 |"
	result := Render(input)
	if !strings.Contains(result, "BobTheBuilder") {
		t.Errorf("expected long cell content, got: %q", result)
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

// ---- New feature tests ----

func TestNumberToLetter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input    int
		expected string
	}{
		{1, "a"},
		{2, "b"},
		{26, "z"},
		{27, "aa"},
	}
	for _, tt := range tests {
		got := numberToLetter(tt.input)
		if got != tt.expected {
			t.Errorf("numberToLetter(%d) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestNumberToRoman(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input    int
		expected string
	}{
		{1, "i"},
		{2, "ii"},
		{4, "iv"},
		{5, "v"},
		{9, "ix"},
		{10, "x"},
		{14, "xiv"},
		{26, "xxvi"},
	}
	for _, tt := range tests {
		got := numberToRoman(tt.input)
		if got != tt.expected {
			t.Errorf("numberToRoman(%d) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestGetListNumber(t *testing.T) {
	t.Parallel()
	tests := []struct {
		depth    int
		num      int
		expected string
	}{
		{0, 1, "1"},
		{1, 3, "3"},
		{2, 1, "a"},
		{2, 2, "b"},
		{3, 1, "i"},
		{3, 4, "iv"},
		{4, 5, "5"}, // default: numbers
	}
	for _, tt := range tests {
		got := getListNumber(tt.depth, tt.num)
		if got != tt.expected {
			t.Errorf("getListNumber(%d, %d) = %q, want %q", tt.depth, tt.num, got, tt.expected)
		}
	}
}

func TestPadAligned(t *testing.T) {
	t.Parallel()
	// Left/default alignment
	got := padAligned("hi", 2, 6, 0)
	if got != "hi    " {
		t.Errorf("padAligned left = %q, want %q", got, "hi    ")
	}
	// Right alignment
	got = padAligned("hi", 2, 6, ast.TableAlignmentRight)
	if got != "    hi" {
		t.Errorf("padAligned right = %q, want %q", got, "    hi")
	}
	// Center alignment
	got = padAligned("hi", 2, 6, ast.TableAlignmentCenter)
	if got != "  hi  " {
		t.Errorf("padAligned center = %q, want %q", got, "  hi  ")
	}
}

func TestStripANSI(t *testing.T) {
	t.Parallel()
	got := stripANSI("\x1b[31mred\x1b[0m text")
	if got != "red text" {
		t.Errorf("stripANSI = %q, want %q", got, "red text")
	}
}

func TestStringWidth(t *testing.T) {
	t.Parallel()
	got := stringWidth("\x1b[31mhello\x1b[0m")
	if got != 5 {
		t.Errorf("stringWidth = %d, want 5", got)
	}
}

func TestCreateHyperlink(t *testing.T) {
	t.Parallel()
	got := createHyperlink("https://example.com", "click")
	if !strings.Contains(got, "https://example.com") {
		t.Errorf("createHyperlink missing URL, got: %q", got)
	}
	if !strings.Contains(got, "click") {
		t.Errorf("createHyperlink missing text, got: %q", got)
	}
	// Should contain OSC 8 escape sequences
	if !strings.Contains(got, "\x1b]8;;") {
		t.Errorf("createHyperlink missing OSC 8 start, got: %q", got)
	}
}

func TestLinkifyIssueReferences(t *testing.T) {
	t.Parallel()
	got := linkifyIssueReferences("see anthropics/claude-code#100 for details")
	if !strings.Contains(got, "anthropics/claude-code#100") {
		t.Errorf("linkifyIssueReferences lost reference, got: %q", got)
	}
	if !strings.Contains(got, "https://github.com/anthropics/claude-code/issues/100") {
		t.Errorf("linkifyIssueReferences missing GitHub URL, got: %q", got)
	}
}

func TestLinkifyIssueReferences_NoMatch(t *testing.T) {
	t.Parallel()
	input := "no references here"
	got := linkifyIssueReferences(input)
	if got != input {
		t.Errorf("linkifyIssueReferences should not modify text without refs, got: %q", got)
	}
}

func TestLinkifyIssueReferences_DoesNotMatchBareHash(t *testing.T) {
	t.Parallel()
	input := "see #123"
	got := linkifyIssueReferences(input)
	// Bare #NNN should NOT be linkified (only owner/repo#NNN)
	if got != input {
		t.Errorf("bare #123 should not be linkified, got: %q", got)
	}
}

func TestRender_HTMLBlock_Skipped(t *testing.T) {
	t.Parallel()
	result := Render("<div>hidden</div>")
	if strings.Contains(result, "<div>") {
		t.Errorf("HTML block should be skipped, got: %q", result)
	}
}

func TestRender_Math(t *testing.T) {
	t.Parallel()
	result := Render("$E = mc^2$")
	if !strings.Contains(result, "E = mc^2") {
		t.Errorf("expected math content, got: %q", result)
	}
}

func TestRenderWidth_ZeroWidth(t *testing.T) {
	t.Parallel()
	result := RenderWidth("hello", 0)
	if !strings.Contains(result, "hello") {
		t.Errorf("expected unwrapped text, got: %q", result)
	}
}

func TestRender_TableEmpty(t *testing.T) {
	t.Parallel()
	result := Render("")
	if result != "" {
		t.Errorf("expected empty, got: %q", result)
	}
}

func TestRender_Softbreak(t *testing.T) {
	t.Parallel()
	// Single newline within paragraph → Softbreak node
	result := Render("hello\nworld")
	if !strings.Contains(result, "hello") || !strings.Contains(result, "world") {
		t.Errorf("expected softbreak content, got: %q", result)
	}
}

func TestRender_Hardbreak(t *testing.T) {
	t.Parallel()
	// Two trailing spaces → Hardbreak node
	result := Render("hello  \nworld")
	if !strings.Contains(result, "hello") || !strings.Contains(result, "world") {
		t.Errorf("expected hardbreak content, got: %q", result)
	}
}

func TestRender_MathBlock(t *testing.T) {
	t.Parallel()
	result := Render("$$\nE = mc^2\n$$")
	if !strings.Contains(result, "E = mc^2") {
		t.Errorf("expected math block content, got: %q", result)
	}
}

func TestPadAligned_NegativePadding(t *testing.T) {
	t.Parallel()
	// displayWidth > targetWidth → padding = 0
	got := padAligned("longtext", 8, 4, 0)
	if got != "longtext" {
		t.Errorf("padAligned negative padding = %q, want %q", got, "longtext")
	}
}

func TestPadAligned_CenterOddPadding(t *testing.T) {
	t.Parallel()
	// padding=5 → leftPad=2, right=3
	got := padAligned("ab", 2, 7, ast.TableAlignmentCenter)
	if got != "  ab   " {
		t.Errorf("padAligned center odd = %q, want %q", got, "  ab   ")
	}
}

func TestTableAlign_Nil(t *testing.T) {
	t.Parallel()
	got := tableAlign(nil, 0)
	if got != ast.CellAlignFlags(0) {
		t.Errorf("tableAlign nil = %v, want 0", got)
	}
}

func TestTableAlign_OutOfRange(t *testing.T) {
	t.Parallel()
	got := tableAlign([]ast.CellAlignFlags{ast.TableAlignmentLeft}, 5)
	if got != ast.CellAlignFlags(0) {
		t.Errorf("tableAlign out of range = %v, want 0", got)
	}
}

func TestRender_TableWithShortCells(t *testing.T) {
	t.Parallel()
	// Table with cells shorter than min width (3)
	input := "| A | B |\n|---|---|\n| 1 | 2 |"
	result := Render(input)
	if !strings.Contains(result, "1") || !strings.Contains(result, "2") {
		t.Errorf("expected table with short cells, got: %q", result)
	}
}

func TestRender_Footnotes_Skipped(t *testing.T) {
	t.Parallel()
	// Footnotes should be silently skipped
	result := Render("text[^1]\n\n[^1]: footnote content")
	if !strings.Contains(result, "text") {
		t.Errorf("expected main text, got: %q", result)
	}
}

func TestRender_MultipleCodeBlocks(t *testing.T) {
	t.Parallel()
	input := "```go\nfmt.Println(\"a\")\n```\n\nSome text\n\n```python\nprint(\"b\")\n```"
	result := Render(input)
	if !strings.Contains(result, "fmt") || !strings.Contains(result, "print") {
		t.Errorf("expected multiple code blocks, got: %q", result)
	}
}

func TestLinkifyIssueReferences_NoHyperlinkSupport(t *testing.T) {
	t.Parallel()
	orig := os.Getenv("TERM")
	_ = os.Setenv("TERM", "dumb")
	defer func() { _ = os.Setenv("TERM", orig) }()

	input := "see owner/repo#123"
	got := linkifyIssueReferences(input)
	if got != input {
		t.Errorf("linkifyIssueReferences with dumb TERM should return unchanged, got: %q", got)
	}
}

func TestRender_Link_NoHyperlinkSupport(t *testing.T) {
	// Non-parallel: modifies TERM env
	orig := os.Getenv("TERM")
	_ = os.Setenv("TERM", "dumb")
	defer func() { _ = os.Setenv("TERM", orig) }()

	result := Render("[click](https://example.com)")
	if !strings.Contains(result, "click") {
		t.Errorf("expected link text, got: %q", result)
	}
	if !strings.Contains(result, "(https://example.com)") {
		t.Errorf("expected URL in parens when no hyperlink support, got: %q", result)
	}
}

func TestRender_Hardbreak_TwoSpaces(t *testing.T) {
	t.Parallel()
	result := Render("hello  \nworld")
	if !strings.Contains(result, "hello") || !strings.Contains(result, "world") {
		t.Errorf("expected hardbreak content, got: %q", result)
	}
}

func TestRender_HTMLBlock(t *testing.T) {
	t.Parallel()
	result := Render("<div>\nsome html\n</div>\n\ntext after")
	if !strings.Contains(result, "text after") {
		t.Errorf("expected text after HTML block, got: %q", result)
	}
	if strings.Contains(result, "<div>") {
		t.Errorf("HTML block should be skipped, got: %q", result)
	}
}

func TestRender_TableHeaderOnly(t *testing.T) {
	t.Parallel()
	// Table with header row only, no data rows
	input := "| H1 | H2 |\n|----|----|"
	result := Render(input)
	if !strings.Contains(result, "H1") || !strings.Contains(result, "H2") {
		t.Errorf("expected header-only table, got: %q", result)
	}
}

// ---- ANSI styling tests: verify inline styles produce actual escape codes ----

func TestRender_Bold_HasANSI(t *testing.T) {
	t.Parallel()
	result := Render("**bold**")
	if !hasANSIStyle(result) {
		t.Errorf("bold text should contain ANSI escape codes, got: %q", result)
	}
}

func TestRender_Italic_HasANSI(t *testing.T) {
	t.Parallel()
	result := Render("*italic*")
	if !hasANSIStyle(result) {
		t.Errorf("italic text should contain ANSI escape codes, got: %q", result)
	}
}

func TestRender_InlineCode_HasANSI(t *testing.T) {
	t.Parallel()
	result := Render("use `code` here")
	if !hasANSIStyle(result) {
		t.Errorf("inline code should contain ANSI escape codes, got: %q", result)
	}
}

func TestRender_Heading1_HasANSI(t *testing.T) {
	t.Parallel()
	result := Render("# Title")
	if !hasANSIStyle(result) {
		t.Errorf("heading should contain ANSI escape codes, got: %q", result)
	}
}

func TestRender_Link_HasANSI(t *testing.T) {
	t.Parallel()
	result := Render("[click](https://example.com)")
	// Links use OSC 8 hyperlinks (\x1b]8;;) not SGR
	if !strings.Contains(result, "\x1b]8;;") {
		t.Errorf("link should contain OSC 8 hyperlink, got: %q", result)
	}
}

func TestRender_BoldItalic_HasANSI(t *testing.T) {
	t.Parallel()
	result := Render("***bold italic***")
	if !hasANSIStyle(result) {
		t.Errorf("bold italic text should contain ANSI escape codes, got: %q", result)
	}
}

func TestStringWidth_CJK(t *testing.T) {
	t.Parallel()
	// CJK characters are 2 columns wide each
	if w := stringWidth("张三"); w != 4 {
		t.Errorf("stringWidth(\"张三\") = %d, want 4", w)
	}
	if w := stringWidth("hello"); w != 5 {
		t.Errorf("stringWidth(\"hello\") = %d, want 5", w)
	}
	// Mixed: 2 CJK (4 cols) + 1 space (1 col) + "abc" (3 cols) = 8
	if w := stringWidth("张三 abc"); w != 8 {
		t.Errorf("stringWidth(\"张三 abc\") = %d, want 8", w)
	}
}

func TestRender_Table_CJK(t *testing.T) {
	t.Parallel()
	input := "| 姓名 | 分数 |\n|------|------|\n| 张三 | 85 |"
	result := Render(input)
	// The table should have top border, header, separator, data row, bottom border
	// With box-drawing characters: ┌─┬─┐, │, ├─┼─┤, └─┴─┘
	lines := strings.Split(result, "\n")
	// Should have 5 lines: top border, header, separator, data row, bottom border
	if len(lines) < 5 {
		t.Fatalf("expected 5+ lines (with top/bottom borders), got %d: %q", len(lines), result)
	}
	// Top border should exist
	if !strings.Contains(lines[0], "┌") {
		t.Errorf("expected top border with ┌, got: %q", lines[0])
	}
	// Separator line should use box-drawing ─
	if !strings.Contains(lines[2], "─") {
		t.Errorf("expected separator line with box-drawing chars, got: %q", lines[2])
	}
	// "张三" should be in the output
	if !strings.Contains(result, "张三") {
		t.Errorf("expected 张三 in output, got: %q", result)
	}
	// Bottom border should exist
	if !strings.Contains(lines[len(lines)-1], "└") {
		t.Errorf("expected bottom border with └, got: %q", lines[len(lines)-1])
	}
}
