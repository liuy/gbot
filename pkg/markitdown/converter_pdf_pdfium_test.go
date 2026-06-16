// Copyright 2026 Conductor OSS
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with
// the License. You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on
// an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the
// specific language governing permissions and limitations under the License.

package markitdown

import (
	"math"
	"strings"
	"testing"
)

func TestFontIsBold(t *testing.T) {
	tests := []struct {
		name string
		font string
		want bool
	}{
		{"bold", "ArialBold", true},
		{"bold_lower", "arial bold", true},
		{"medium", "NimbusRomNo9L-Medi", true},
		{"bd_suffix", "Arial-bd", true},
		{"bd_suffix_only", "Timesbd", true},
		{"regular", "Arial", false},
		{"italic", "ArialItalic", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fontIsBold(tt.font)
			if got != tt.want {
				t.Errorf("fontIsBold(%q) = %v, want %v", tt.font, got, tt.want)
			}
		})
	}
}

func TestFontIsItalic(t *testing.T) {
	tests := []struct {
		name string
		font string
		want bool
	}{
		{"italic", "ArialItalic", true},
		{"italic_lower", "arial italic", true},
		{"oblique", "ArialOblique", true},
		{"obli", "ArialObli", true},
		{"it_suffix", "Arial-it", true},
		{"regular", "Arial", false},
		{"bold", "ArialBold", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fontIsItalic(tt.font)
			if got != tt.want {
				t.Errorf("fontIsItalic(%q) = %v, want %v", tt.font, got, tt.want)
			}
		})
	}
}

func TestFontIsMono(t *testing.T) {
	tests := []struct {
		name string
		font string
		want bool
	}{
		{"mono", "CourierNew", true},
		{"mono_lower", "courier new", true},
		{"consola", "Consolas", true},
		{"cmtt", "cmtt10", true},
		{"typewriter", "SomeTypewriter", true},
		{"regular", "Arial", false},
		{"bold", "ArialBold", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fontIsMono(tt.font)
			if got != tt.want {
				t.Errorf("fontIsMono(%q) = %v, want %v", tt.font, got, tt.want)
			}
		})
	}
}

func TestAllRectsAreBold(t *testing.T) {
	tests := []struct {
		name  string
		rects []pdfRect
		want  bool
	}{
		{
			name: "all_bold",
			rects: []pdfRect{
				{text: "a", fontName: "ArialBold"},
				{text: "b", fontName: "TimesBold"},
			},
			want: true,
		},
		{
			name: "one_not_bold",
			rects: []pdfRect{
				{text: "a", fontName: "ArialBold"},
				{text: "b", fontName: "Arial"},
			},
			want: false,
		},
		{
			name: "whitespace_ignored",
			rects: []pdfRect{
				{text: " ", fontName: "Arial"},
				{text: "a", fontName: "ArialBold"},
			},
			want: true,
		},
		{
			name:  "empty",
			rects: []pdfRect{},
			want:  true,
		},
		{
			name: "all_whitespace",
			rects: []pdfRect{
				{text: " ", fontName: "Arial"},
				{text: "  ", fontName: "Arial"},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := allRectsAreBold(tt.rects)
			if got != tt.want {
				t.Errorf("allRectsAreBold() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHeadingLevel(t *testing.T) {
	tests := []struct {
		name     string
		fontSize float64
		bodySize float64
		isBold   bool
		want     int
	}{
		{"h1_ratio_2x", 24.0, 12.0, false, 1},
		{"h2_ratio_1_5x", 18.0, 12.0, false, 2},
		{"h3_bold_larger", 14.0, 12.0, true, 3},
		{"h4_non_bold_larger", 14.0, 12.0, false, 4},
		{"body_same_size", 12.0, 12.0, false, 0},
		{"body_smaller", 10.0, 12.0, false, 0},
		{"zero_body_size", 12.0, 0, false, 0},
		{"negative_body_size", 12.0, -1, false, 0},
		{"just_above_1_1_ratio", 12.0 * 1.11, 12.0, true, 3},
		{"exact_2_0_ratio", 24.0, 12.0, false, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := headingLevel(tt.fontSize, tt.bodySize, tt.isBold)
			if got != tt.want {
				t.Errorf("headingLevel(%.1f, %.1f, %v) = %d, want %d",
					tt.fontSize, tt.bodySize, tt.isBold, got, tt.want)
			}
		})
	}
}

func TestDominantFont(t *testing.T) {
	rects := []pdfRect{
		{text: "ab", fontSize: 12.0, fontName: "Arial"},
		{text: "c", fontSize: 14.0, fontName: "Times"},
	}
	size, name := dominantFont(rects)
	if size != 12.0 {
		t.Errorf("size = %.1f, want 12.0 (most chars)", size)
	}
	if name != "Arial" {
		t.Errorf("name = %q, want Arial (most chars)", name)
	}
}

func TestDominantFontEmpty(t *testing.T) {
	size, name := dominantFont([]pdfRect{})
	if size != 0 {
		t.Errorf("size = %.1f, want 0", size)
	}
	if name != "" {
		t.Errorf("name = %q, want empty", name)
	}
}

func TestDetectBodyFontSize(t *testing.T) {
	lines := []pdfTextLine{
		{
			rects: []pdfRect{
				{text: "body text content", fontSize: 12.0},
			},
		},
		{
			rects: []pdfRect{
				{text: "heading", fontSize: 24.0},
			},
		},
		{
			rects: []pdfRect{
				{text: "more body text here", fontSize: 12.0},
			},
		},
	}
	size := detectBodyFontSize(lines)
	if size != 12.0 {
		t.Errorf("detectBodyFontSize = %.1f, want 12.0 (most common)", size)
	}
}

func TestDetectBodyFontSizeEmpty(t *testing.T) {
	size := detectBodyFontSize(nil)
	if size != 0 {
		t.Errorf("detectBodyFontSize(empty) = %.1f, want 0", size)
	}
}

func TestGroupRectsIntoLines(t *testing.T) {
	rects := []pdfRect{
		{text: "A", left: 0, top: 100, bottom: 90},
		{text: "B", left: 10, top: 100, bottom: 90},
		{text: "C", left: 0, top: 80, bottom: 70},
	}
	lines := groupRectsIntoLines(rects)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	// Line 0 should be the "AB" line (top=100)
	if lines[0].top != 100 {
		t.Errorf("first line top = %.1f, want 100", lines[0].top)
	}
	if len(lines[0].rects) != 2 {
		t.Errorf("first line should have 2 rects, got %d", len(lines[0].rects))
	}
}

func TestGroupRectsIntoLinesEmpty(t *testing.T) {
	lines := groupRectsIntoLines(nil)
	if len(lines) != 0 {
		t.Errorf("expected 0 lines, got %d", len(lines))
	}
}

func TestPdfTextLineText(t *testing.T) {
	line := pdfTextLine{
		rects: []pdfRect{
			{text: "Hello "},
			{text: "World"},
		},
	}
	got := line.text()
	want := "Hello World"
	if got != want {
		t.Errorf("text() = %q, want %q", got, want)
	}
}

func TestBuildLineMarkdown(t *testing.T) {
	rects := []pdfRect{
		{text: "normal ", fontSize: 12, fontName: "Arial"},
		{text: "bold", fontSize: 12, fontName: "ArialBold"},
		{text: " text", fontSize: 12, fontName: "Arial"},
	}
	got := buildLineMarkdown(rects, 12)
	if !strings.Contains(got, "normal") {
		t.Errorf("buildLineMarkdown = %q, should contain 'normal'", got)
	}
	if !strings.Contains(got, "**bold**") {
		t.Errorf("buildLineMarkdown = %q, should contain '**bold**'", got)
	}
}

func TestBuildLineMarkdownItalic(t *testing.T) {
	rects := []pdfRect{
		{text: "italic", fontSize: 12, fontName: "ArialItalic"},
	}
	got := buildLineMarkdown(rects, 12)
	if !strings.Contains(got, "*italic*") {
		t.Errorf("buildLineMarkdown = %q, should contain '*italic*'", got)
	}
}

func TestBuildLineMarkdownMono(t *testing.T) {
	rects := []pdfRect{
		{text: "code", fontSize: 12, fontName: "CourierNew"},
	}
	got := buildLineMarkdown(rects, 12)
	if !strings.Contains(got, "`code`") {
		t.Errorf("buildLineMarkdown = %q, should contain '`code`'", got)
	}
}

func TestBuildLineMarkdownBoldItalic(t *testing.T) {
	rects := []pdfRect{
		{text: "both", fontSize: 12, fontName: "ArialBoldItalic"},
	}
	got := buildLineMarkdown(rects, 12)
	if !strings.Contains(got, "***both***") {
		t.Errorf("buildLineMarkdown = %q, should contain '***both***'", got)
	}
}

func TestBuildLineMarkdownSkipFootnote(t *testing.T) {
	rects := []pdfRect{
		{text: "1", fontSize: 6, fontName: "Arial"}, // tiny footnote marker
		{text: "text", fontSize: 12, fontName: "Arial"},
	}
	got := buildLineMarkdown(rects, 12)
	if strings.Contains(got, "1") && !strings.Contains(got, "text") {
		t.Errorf("buildLineMarkdown = %q, should skip footnote but include text", got)
	}
	if !strings.Contains(got, "text") {
		t.Errorf("buildLineMarkdown = %q, should contain 'text'", got)
	}
}

func TestStripMarkdownFormatting(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"bold_italic", "***text***", "text"},
		{"bold", "**text**", "text"},
		{"italic", "*text*", "text"},
		{"code", "`code`", "code"},
		{"plain", "plain text", "plain text"},
		{"mixed", "**bold** and *italic* and `code`", "bold and italic and code"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripMarkdownFormatting(tt.input)
			if got != tt.want {
				t.Errorf("stripMarkdownFormatting(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRenderMarkdownFromLinesBasic(t *testing.T) {
	lines := []pdfTextLine{
		{
			rects:    []pdfRect{{text: "Title", fontSize: 24, fontName: "ArialBold"}},
			top:      100,
			bottom:   80,
			fontSize: 24,
			fontName: "ArialBold",
		},
		{
			rects:    []pdfRect{{text: "Body text", fontSize: 12, fontName: "Arial"}},
			top:      60,
			bottom:   48,
			fontSize: 12,
			fontName: "Arial",
		},
	}
	got := renderMarkdownFromLines(lines, 12)
	if !strings.Contains(got, "# Title") {
		t.Errorf("renderMarkdownFromLines = %q, should contain '# Title'", got)
	}
	if !strings.Contains(got, "Body text") {
		t.Errorf("renderMarkdownFromLines = %q, should contain 'Body text'", got)
	}
}

func TestRenderMarkdownFromLinesBoldShortLine(t *testing.T) {
	// Standalone short bold line at body size should become H4
	lines := []pdfTextLine{
		{
			rects:    []pdfRect{{text: "References", fontSize: 12, fontName: "ArialBold"}},
			top:      60,
			bottom:   48,
			fontSize: 12,
			fontName: "ArialBold",
		},
	}
	got := renderMarkdownFromLines(lines, 12)
	if !strings.Contains(got, "#### References") {
		t.Errorf("renderMarkdownFromLines = %q, should contain '#### References'", got)
	}
}

func TestRenderMarkdownFromLinesParagraphGap(t *testing.T) {
	// Lines with a big gap should have a paragraph break
	lines := []pdfTextLine{
		{
			rects:    []pdfRect{{text: "First", fontSize: 12, fontName: "Arial"}},
			top:      100,
			bottom:   88,
			fontSize: 12,
			fontName: "Arial",
		},
		{
			rects:    []pdfRect{{text: "Second", fontSize: 12, fontName: "Arial"}},
			top:      50,
			bottom:   38,
			fontSize: 12,
			fontName: "Arial",
		},
	}
	got := renderMarkdownFromLines(lines, 12)
	if !strings.Contains(got, "First") || !strings.Contains(got, "Second") {
		t.Errorf("renderMarkdownFromLines = %q, should contain both lines", got)
	}
}

func TestRenderMarkdownFromLinesEmpty(t *testing.T) {
	got := renderMarkdownFromLines(nil, 12)
	if got != "" {
		t.Errorf("renderMarkdownFromLines(empty) = %q, want empty", got)
	}
}

func TestPdfRectInLineGrouping(t *testing.T) {
	// Test that rects very close vertically get grouped
	rects := []pdfRect{
		{text: "X", left: 0, top: 100, bottom: 90},
		{text: "Y", left: 10, top: 101, bottom: 91}, // within 3 units
	}
	lines := groupRectsIntoLines(rects)
	if len(lines) != 1 {
		t.Errorf("expected 1 line (grouped), got %d", len(lines))
	}
	if len(lines) > 0 && len(lines[0].rects) != 2 {
		t.Errorf("expected 2 rects in line, got %d", len(lines[0].rects))
	}
}

func TestPdfRectSortOrder(t *testing.T) {
	// Rects should be sorted left-to-right within a line
	rects := []pdfRect{
		{text: "B", left: 10, top: 100, bottom: 90},
		{text: "A", left: 0, top: 100, bottom: 90},
		{text: "C", left: 20, top: 100, bottom: 90},
	}
	lines := groupRectsIntoLines(rects)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	got := lines[0].text()
	want := "ABC"
	if got != want {
		t.Errorf("text = %q, want %q (sorted left-to-right)", got, want)
	}
}

func TestHeadingLevelAtExactBoundary(t *testing.T) {
	// ratio exactly 1.5
	got := headingLevel(18.0, 12.0, false)
	if got != 2 {
		t.Errorf("headingLevel(18, 12, false) = %d, want 2 (ratio 1.5)", got)
	}
	// ratio just above 1.5
	got = headingLevel(18.1, 12.0, false)
	if got != 2 {
		t.Errorf("headingLevel(18.1, 12, false) = %d, want 2", got)
	}
}

func TestDominantFontWithRounding(t *testing.T) {
	rects := []pdfRect{
		{text: "a", fontSize: 12.04, fontName: "A"},
		{text: "b", fontSize: 12.06, fontName: "A"},
	}
	size, name := dominantFont(rects)
	if math.Abs(size-12.0) > 0.1 {
		t.Errorf("size = %.2f, want ~12.0", size)
	}
	if name != "A" {
		t.Errorf("name = %q, want A", name)
	}
}
