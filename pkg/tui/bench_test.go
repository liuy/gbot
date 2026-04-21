package tui

import (
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// wordWrap benchmarks
// ---------------------------------------------------------------------------

func BenchmarkWordWrap_PlainText(b *testing.B) {
	text := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 50)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		wordWrap(text, 80)
	}
}

func BenchmarkWordWrap_LongParagraph(b *testing.B) {
	text := strings.Repeat("word ", 2000) // ~10KB
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		wordWrap(text, 80)
	}
}

func BenchmarkWordWrap_WithANSI(b *testing.B) {
	// Simulate colored output: each word wrapped in ANSI color
	base := "\x1b[31mword\x1b[0m "
	text := strings.Repeat(base, 500)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		wordWrap(text, 80)
	}
}

func BenchmarkWordWrap_MultiLine(b *testing.B) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("Line %d: %s", i, strings.Repeat("x", 60))
	}
	text := strings.Join(lines, "\n")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		wordWrap(text, 80)
	}
}

func BenchmarkWordWrap_CJK(b *testing.B) {
	// CJK characters are double-width
	text := strings.Repeat("你好世界", 200)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		wordWrap(text, 40)
	}
}

// ---------------------------------------------------------------------------
// markdown.Render / RenderWidth benchmarks
// ---------------------------------------------------------------------------

func BenchmarkMarkdownRender_PlainText(b *testing.B) {
	text := strings.Repeat("This is a paragraph of plain text for benchmarking. ", 20)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Render(text)
	}
}

func BenchmarkMarkdownRender_CodeBlock(b *testing.B) {
	code := "```go\n" + strings.Repeat("func foo() { bar() }\n", 50) + "```"
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Render(code)
	}
}

func BenchmarkMarkdownRender_List(b *testing.B) {
	var sb strings.Builder
	for i := range 100 {
		fmt.Fprintf(&sb, "- Item %d with some description text\n", i)
	}
	text := sb.String()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Render(text)
	}
}

func BenchmarkMarkdownRender_Mixed(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("# Heading\n\n")
	sb.WriteString("Some **bold** and *italic* text with `code` inline.\n\n")
	sb.WriteString("```python\n")
	for i := range 20 {
		fmt.Fprintf(&sb, "def function_%d():\n    return %d\n", i, i)
	}
	sb.WriteString("```\n\n")
	for i := range 20 {
		fmt.Fprintf(&sb, "- List item %d\n", i)
	}
	sb.WriteString("\n| Col1 | Col2 | Col3 |\n|------|------|------|\n")
	for i := range 10 {
		fmt.Fprintf(&sb, "| %d | val%d | data |\n", i, i)
	}
	text := sb.String()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Render(text)
	}
}

func BenchmarkMarkdownRenderWidth_Mixed(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("# Heading\n\n")
	sb.WriteString("Some paragraph text.\n\n")
	for i := range 20 {
		fmt.Fprintf(&sb, "- Item %d\n", i)
	}
	text := sb.String()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		RenderWidth(text, 80)
	}
}
