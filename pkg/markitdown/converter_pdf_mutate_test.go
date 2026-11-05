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
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestPdfConstructorReturnsInstance(t *testing.T) {
	if NewPdfConverter() == nil {
		t.Fatal("NewPdfConverter() = nil, want non-nil")
	}
}

func TestPdfConvertReadErrorPreservesCause(t *testing.T) {
	c := NewPdfConverter()
	_, err := c.Convert(&errorReadSeeker{}, StreamInfo{Extension: ".pdf"})
	if err == nil {
		t.Fatal("expected read error")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("read error must wrap the reader error via %%w, got: %v", err)
	}
}

func TestPdfConvertOpenErrorPreservesCause(t *testing.T) {
	c := NewPdfConverter()
	_, err := c.Convert(strings.NewReader("not a pdf at all"), StreamInfo{Extension: ".pdf"})
	if err == nil {
		t.Fatal("expected open error")
	}
	if !strings.Contains(err.Error(), "open PDF") {
		t.Errorf("error = %v, should contain 'open PDF'", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("open error must wrap the pdfium error via %%w, got: %v", err)
	}
}

const blankPDF = `%PDF-1.4
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> /Contents 4 0 R >>
endobj
4 0 obj
<< /Length 0 >>
stream
endstream
endobj
trailer
<< /Size 5 /Root 1 0 R >>
`

func TestPdfConvertNoTextPlaceholder(t *testing.T) {
	c := NewPdfConverter()
	result, err := c.Convert(strings.NewReader(blankPDF), StreamInfo{Extension: ".pdf"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if result.Markdown != "[No readable text content found in PDF]" {
		t.Errorf("Markdown = %q, want the no-text placeholder", result.Markdown)
	}
}

// Both pages of a multi-page document convert in order.
func TestPdfConvertMultiPage(t *testing.T) {
	data, err := os.ReadFile("testdata/twopage.pdf")
	if err != nil {
		t.Fatal(err)
	}
	c := NewPdfConverter()
	result, err := c.Convert(strings.NewReader(string(data)), StreamInfo{Extension: ".pdf"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	firstIdx := strings.Index(result.Markdown, "FIRSTPAGE-ALPHA")
	secondIdx := strings.Index(result.Markdown, "SECONDPAGE-BRAVO")
	if firstIdx < 0 || secondIdx < 0 {
		t.Fatalf("both pages must render, got: %q", result.Markdown)
	}
	if firstIdx > secondIdx {
		t.Errorf("pages must render in order, got: %q", result.Markdown)
	}
	if strings.Count(result.Markdown, "FIRSTPAGE-ALPHA") != 1 {
		t.Errorf("page one must render exactly once, got: %q", result.Markdown)
	}
}

// --- line grouping ---

func line(rects ...pdfRect) []pdfTextLine { return groupRectsIntoLines(rects) }

func TestGroupRectsSameLineJoinsByLeft(t *testing.T) {
	// tops within 2 units: same line; left order decides text order.
	lines := line(
		pdfRect{text: "X", left: 50, top: 100, bottom: 90},
		pdfRect{text: "Y", left: 10, top: 100, bottom: 90},
	)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	if got := lines[0].text(); got != "YX" {
		t.Errorf("line text = %q, want YX (left order)", got)
	}
}

func TestGroupRectsTwoUnitGapIsSameLine(t *testing.T) {
	lines := line(
		pdfRect{text: "A", left: 0, top: 100, bottom: 90},
		pdfRect{text: "B", left: 10, top: 98, bottom: 88},
	)
	if len(lines) != 1 {
		t.Fatalf("2-unit top gap must stay one line, got %d", len(lines))
	}
	if got := lines[0].text(); got != "AB" {
		t.Errorf("line text = %q, want AB", got)
	}
}

func TestGroupRectsThreeUnitGapSplitsLines(t *testing.T) {
	lines := line(
		pdfRect{text: "A", left: 0, top: 100, bottom: 90},
		pdfRect{text: "B", left: 10, top: 97, bottom: 87},
	)
	if len(lines) != 2 {
		t.Fatalf("3-unit top gap must split lines, got %d", len(lines))
	}
	if lines[0].text() != "A" || lines[1].text() != "B" {
		t.Errorf("lines = %q,%q want A then B (top-descending)", lines[0].text(), lines[1].text())
	}
}

func TestGroupRectsLinesSortedTopDescending(t *testing.T) {
	lines := line(
		pdfRect{text: "LOW", left: 0, top: 80, bottom: 70},
		pdfRect{text: "HIGH", left: 0, top: 100, bottom: 90},
	)
	if len(lines) != 2 || lines[0].text() != "HIGH" || lines[1].text() != "LOW" {
		t.Fatalf("lines must sort top-descending, got %+v", lines)
	}
}

func TestGroupRectsRectsSortedLeftWithinLine(t *testing.T) {
	lines := line(
		pdfRect{text: "R", left: 50, top: 100, bottom: 90},
		pdfRect{text: "L", left: 10, top: 100, bottom: 90},
	)
	if len(lines) != 1 || lines[0].text() != "LR" {
		t.Fatalf("rects within a line must sort by left, got %+v", lines)
	}
}

// --- dominant font / body size ---

func TestDominantFontLastChunkDoesNotWin(t *testing.T) {
	// A totals 2+2=4, B totals 3: A must win even though B's single chunk
	// (3) beats A's last chunk (2).
	size, name := dominantFont([]pdfRect{
		{text: "ab", fontSize: 12, fontName: "A"},
		{text: "cd", fontSize: 12, fontName: "A"},
		{text: "efg", fontSize: 14, fontName: "B"},
	})
	if size != 12 || name != "A" {
		t.Errorf("dominantFont = (%v,%q), want (12,A)", size, name)
	}
}

func TestDetectBodyFontSizeSingleChar(t *testing.T) {
	size := detectBodyFontSize([]pdfTextLine{{rects: []pdfRect{{text: "x", fontSize: 12}}}})
	if size != 12 {
		t.Errorf("detectBodyFontSize = %v, want 12", size)
	}
}

// --- font classification ---

func TestFontIsBoldMedi(t *testing.T) {
	if !fontIsBold("NimbusRomNo9L-Medi") {
		t.Error("fontIsBold(NimbusRomNo9L-Medi) = false, want true")
	}
}

func TestFontIsMonoByName(t *testing.T) {
	if !fontIsMono("DejaVuSansMono") {
		t.Error("fontIsMono(DejaVuSansMono) = false, want true")
	}
}

func TestAllRectsAreBoldWhitespaceThenRegular(t *testing.T) {
	got := allRectsAreBold([]pdfRect{
		{text: "  ", fontName: "Arial"},
		{text: "a", fontName: "Arial"},
	})
	if got {
		t.Error("whitespace followed by a regular rect must not count as all-bold")
	}
}

// --- heading level boundaries ---

func TestHeadingLevelBoundaries(t *testing.T) {
	cases := []struct {
		fontSize, bodySize float64
		bold               bool
		want               int
	}{
		{2.0, 1.0, false, 1},  // bodySize 1 must not trip the zero-body guard
		{11.0, 10.0, true, 3}, // ratio exactly 1.1
	}
	for _, tc := range cases {
		if got := headingLevel(tc.fontSize, tc.bodySize, tc.bold); got != tc.want {
			t.Errorf("headingLevel(%v,%v,%v) = %d, want %d", tc.fontSize, tc.bodySize, tc.bold, got, tc.want)
		}
	}
}

// --- markdown rendering ---

func mdLine(text string, fontSize float64, fontName string, top, bottom float64) pdfTextLine {
	return pdfTextLine{rects: []pdfRect{{text: text, fontSize: fontSize, fontName: fontName, left: 0, top: top, bottom: bottom}}, top: top, bottom: bottom, fontSize: fontSize, fontName: fontName}
}

func TestRenderMarkdownFootnoteSizedZeroFontKept(t *testing.T) {
	lines := []pdfTextLine{
		mdLine("x", 0, "Arial", 100, 90), // zero font size: not footnote logic
		mdLine("body text", 12, "Arial", 80, 70),
	}
	got := renderMarkdownFromLines(lines, 12)
	if !strings.Contains(got, "x") {
		t.Errorf("zero-font-size line must render, got: %q", got)
	}
}

func TestRenderMarkdownTinyFootnoteDropped(t *testing.T) {
	lines := []pdfTextLine{
		mdLine("1", 5, "Arial", 100, 90), // < 0.6*12 and <= 3 chars: dropped
		mdLine("body text", 12, "Arial", 80, 70),
	}
	got := renderMarkdownFromLines(lines, 12)
	if strings.Contains(got, "\n1\n") || strings.HasPrefix(got, "1") {
		t.Errorf("tiny footnote marker must be dropped, got: %q", got)
	}
	if !strings.Contains(got, "body text") {
		t.Errorf("body must render, got: %q", got)
	}
}

func TestRenderMarkdownFootnoteBoundary72Kept(t *testing.T) {
	lines := []pdfTextLine{
		mdLine("x", 7.2, "Arial", 100, 90), // exactly 0.6*12: kept
		mdLine("body text", 12, "Arial", 80, 70),
	}
	got := renderMarkdownFromLines(lines, 12)
	if !strings.Contains(got, "x") {
		t.Errorf("7.2pt line must render, got: %q", got)
	}
}

func TestRenderMarkdownLongTinyLineKept(t *testing.T) {
	lines := []pdfTextLine{
		mdLine("abcdef", 5, "Arial", 100, 90), // tiny but longer than 3 chars
		mdLine("body text", 12, "Arial", 80, 70),
	}
	got := renderMarkdownFromLines(lines, 12)
	if !strings.Contains(got, "abcdef") {
		t.Errorf("long tiny line must render, got: %q", got)
	}
}

func TestRenderMarkdownFourCharFootnoteDropped(t *testing.T) {
	lines := []pdfTextLine{
		mdLine("1234", 5, "Arial", 100, 90), // exactly 4 chars: kept (> 3)
		mdLine("123", 5, "Arial", 105, 95),  // 3 chars: dropped
		mdLine("body", 12, "Arial", 80, 70),
	}
	got := renderMarkdownFromLines(lines, 12)
	if !strings.Contains(got, "1234") {
		t.Errorf("4-char tiny line must render, got: %q", got)
	}
	if strings.Contains(got, "\n123\n") {
		t.Errorf("3-char tiny line must drop, got: %q", got)
	}
}

func TestRenderMarkdownBoldShortBodyLineIsSubheading(t *testing.T) {
	lines := []pdfTextLine{mdLine("Notes", 12, "ArialBold", 100, 90)}
	got := renderMarkdownFromLines(lines, 12)
	if want := "#### Notes\n\n"; got != want {
		t.Errorf("short bold body line must be an H4 subheading, got: %q, want %q", got, want)
	}
}

func TestRenderMarkdownNonBoldShortLineIsBody(t *testing.T) {
	lines := []pdfTextLine{mdLine("Notes", 12, "Arial", 100, 90)}
	got := renderMarkdownFromLines(lines, 12)
	if strings.Contains(got, "#") {
		t.Errorf("non-bold short line must stay body, got: %q", got)
	}
}

func TestRenderMarkdownBoldSmallLineNotSubheading(t *testing.T) {
	lines := []pdfTextLine{
		mdLine("Small", 8, "ArialBold", 100, 90),
		mdLine("body", 12, "Arial", 80, 70),
	}
	got := renderMarkdownFromLines(lines, 12)
	if strings.Contains(got, "#### Small") {
		t.Errorf("bold line below body size must not be a subheading, got: %q", got)
	}
}

func TestRenderMarkdownMixedBoldnessNotSubheading(t *testing.T) {
	line := pdfTextLine{
		rects: []pdfRect{
			{text: "Mi", fontSize: 12, fontName: "ArialBold"},
			{text: "xed", fontSize: 12, fontName: "Arial"},
		},
		fontSize: 12, fontName: "ArialBold", top: 100, bottom: 90,
	}
	got := renderMarkdownFromLines([]pdfTextLine{line}, 12)
	if strings.Contains(got, "####") {
		t.Errorf("mixed-boldness line must not be a subheading, got: %q", got)
	}
}

func TestRenderMarkdownHeadingLevelUnchanged(t *testing.T) {
	lines := []pdfTextLine{mdLine("Title", 24, "ArialBold", 100, 90)}
	got := renderMarkdownFromLines(lines, 12)
	if !strings.Contains(got, "# Title") {
		t.Errorf("24pt line must be H1, got: %q", got)
	}
}

func TestRenderMarkdownSubheadingLengthBoundaries(t *testing.T) {
	exactly79 := strings.Repeat("a", 79)
	exactly80 := strings.Repeat("b", 80)
	lines := []pdfTextLine{
		mdLine(exactly79, 12, "ArialBold", 200, 190),
		mdLine(exactly80, 12, "ArialBold", 100, 90),
	}
	got := renderMarkdownFromLines(lines, 12)
	if !strings.Contains(got, "#### "+exactly79) {
		t.Errorf("79-char bold line must be a subheading, got: %.60s...", got)
	}
	if strings.Contains(got, "#### "+exactly80) {
		t.Errorf("80-char bold line is a paragraph, not a subheading, got: %.60s...", got)
	}
}

func TestRenderMarkdownFirstHeadingNoLeadingBlank(t *testing.T) {
	lines := []pdfTextLine{mdLine("Title", 24, "Arial", 100, 90)}
	got := renderMarkdownFromLines(lines, 12)
	if strings.HasPrefix(got, "\n") {
		t.Errorf("first heading must not have a leading blank line, got: %q", got)
	}
}

func TestRenderMarkdownHeadingAfterBodyHasBlank(t *testing.T) {
	lines := []pdfTextLine{
		mdLine("body", 12, "Arial", 200, 190),
		mdLine("Title", 24, "Arial", 100, 90),
	}
	got := renderMarkdownFromLines(lines, 12)
	if !strings.Contains(got, "body\n\n# Title") {
		t.Errorf("heading after body must have a blank line, got: %q", got)
	}
}

func TestRenderMarkdownGapAfterFirstLine(t *testing.T) {
	// Second line with a large gap from the first: paragraph break applies
	// from index 1 on.
	lines := []pdfTextLine{
		mdLine("first", 12, "Arial", 200, 190),
		mdLine("second", 12, "Arial", 100, 90),
	}
	got := renderMarkdownFromLines(lines, 12)
	if !strings.Contains(got, "first\n\nsecond") {
		t.Errorf("paragraph gap after the first line must render a blank, got: %q", got)
	}
}

func TestRenderMarkdownSmallGapNoBlank(t *testing.T) {
	lines := []pdfTextLine{
		mdLine("first", 12, "Arial", 200, 190),
		mdLine("second", 12, "Arial", 185, 175),
	}
	got := renderMarkdownFromLines(lines, 12)
	if strings.Contains(got, "first\n\nsecond") {
		t.Errorf("small gap must not render a blank line, got: %q", got)
	}
}

func TestRenderMarkdownGapBoundaryNoBlank(t *testing.T) {
	// gap exactly 1.5x lineHeight (10): no blank line.
	lines := []pdfTextLine{
		mdLine("first", 12, "Arial", 200, 190),
		mdLine("second", 12, "Arial", 180, 170),
	}
	got := renderMarkdownFromLines(lines, 12)
	if strings.Contains(got, "first\n\nsecond") {
		t.Errorf("gap of exactly 1.5x line height must not blank, got: %q", got)
	}
}

func TestRenderMarkdownZeroHeightLineUsesBodyFallback(t *testing.T) {
	// Zero-height line: lineHeight falls back to bodySize, so a 15-unit gap
	// is only 1.25x and must not blank.
	lines := []pdfTextLine{
		{rects: []pdfRect{{text: "flat", fontSize: 12, fontName: "Arial", top: 200}}, top: 200, bottom: 200, fontSize: 12, fontName: "Arial"},
		{rects: []pdfRect{{text: "next", fontSize: 12, fontName: "Arial", top: 185}}, top: 185, bottom: 185, fontSize: 12, fontName: "Arial"},
	}
	got := renderMarkdownFromLines(lines, 12)
	if strings.Contains(got, "flat\n\nnext") {
		t.Errorf("zero-height lines must use the body-size fallback, got: %q", got)
	}
}

// --- buildLineMarkdown ---

func TestBuildLineMarkdownTinyRectDropped(t *testing.T) {
	got := buildLineMarkdown([]pdfRect{
		{text: "1", fontSize: 5, fontName: "Arial"},
		{text: "kept", fontSize: 12, fontName: "Arial"},
	}, 12)
	if !strings.Contains(got, "kept") || strings.HasPrefix(got, "1") {
		t.Errorf("tiny rect must drop, got: %q", got)
	}
}

func TestBuildLineMarkdownZeroFontSizeRectKept(t *testing.T) {
	got := buildLineMarkdown([]pdfRect{
		{text: "x", fontSize: 0, fontName: "Arial"},
	}, 12)
	if got != "x" {
		t.Errorf("zero-font-size rect must render, got: %q", got)
	}
}

func TestBuildLineMarkdownFourCharTinyKept(t *testing.T) {
	got := buildLineMarkdown([]pdfRect{{text: "1234", fontSize: 5, fontName: "Arial"}}, 12)
	if got != "1234" {
		t.Errorf("4-char tiny rect must render, got: %q", got)
	}
}

func TestBuildLineMarkdownNoTrailingSpaceWhenTrimmed(t *testing.T) {
	got := buildLineMarkdown([]pdfRect{{text: "code", fontSize: 12, fontName: "Courier"}}, 12)
	if got != "`code`" {
		t.Errorf("mono run without trailing space = %q, want `code`", got)
	}
	got = buildLineMarkdown([]pdfRect{{text: "bold", fontSize: 12, fontName: "ArialBold"}}, 12)
	if got != "**bold**" {
		t.Errorf("bold run = %q, want **bold**", got)
	}
	got = buildLineMarkdown([]pdfRect{{text: "it", fontSize: 12, fontName: "ArialItalic"}}, 12)
	if got != "*it*" {
		t.Errorf("italic run = %q, want *it*", got)
	}
}

func TestBuildLineMarkdownTrailingSpacePreserved(t *testing.T) {
	got := buildLineMarkdown([]pdfRect{
		{text: "code ", fontSize: 12, fontName: "Courier"},
		{text: "next", fontSize: 12, fontName: "Arial"},
	}, 12)
	if got != "`code` next" {
		t.Errorf("mono run trailing space = %q, want \"`code` next\"", got)
	}
}

// --- strip ---

func TestStripMarkdownFormattingExact(t *testing.T) {
	if got := stripMarkdownFormatting("***a**b*c`d`"); got != "abcd" {
		t.Errorf("stripMarkdownFormatting = %q, want abcd", got)
	}
}

// An empty middle page contributes nothing: exactly two rendered pages,
// no stray blank lines from the skipped page.
func TestPdfConvertEmptyMiddlePage(t *testing.T) {
	data, err := os.ReadFile("testdata/threepage.pdf")
	if err != nil {
		t.Fatal(err)
	}
	c := NewPdfConverter()
	result, err := c.Convert(strings.NewReader(string(data)), StreamInfo{Extension: ".pdf"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if want := "PAGEONE\n\n\nPAGETHREE\n\n\n"; result.Markdown != want {
		t.Errorf("Markdown = %q, want %q", result.Markdown, want)
	}
}

// --- review-fix batch: boundary pins for the tiny-text skip and the
// bold-subheading heuristic (previously surviving mutant idxs) ---

// A short line between 0.6x and 0.75x body size is mid-small, not a footnote
// marker: it must render (the 0.6 threshold only drops true tiny text).
func TestRenderMarkdownShortMidSmallLineKept(t *testing.T) {
	lines := []pdfTextLine{
		mdLine("123", 8, "Arial", 100, 90), // 8pt of 12pt body: mid-small, 3 chars
		mdLine("body", 12, "Arial", 80, 70),
	}
	got := renderMarkdownFromLines(lines, 12)
	if !strings.Contains(got, "123") {
		t.Errorf("mid-small short line must render, got: %q", got)
	}
}

// A line whose dominant font is non-bold (space runs carry more characters
// than the bold run) is not a subheading, even though every non-space rect
// is bold.
func TestRenderMarkdownSpaceRunDominantNotSubheading(t *testing.T) {
	lines := []pdfTextLine{
		{rects: []pdfRect{
			{text: "      ", fontSize: 12, fontName: "Arial"},
			{text: "Refs", fontSize: 12, fontName: "ArialBold"},
		}, top: 100, bottom: 90, fontSize: 12, fontName: "Arial"},
		mdLine("body", 12, "Arial", 80, 70),
	}
	got := renderMarkdownFromLines(lines, 12)
	if strings.Contains(got, "####") {
		t.Errorf("space-dominated line must not be a subheading, got: %q", got)
	}
}

// A rect exactly at the 0.6x-body boundary is not tiny: strict < keeps it.
func TestBuildLineMarkdownTinyAtExactThresholdKept(t *testing.T) {
	bodySize := 10.0
	fs := bodySize * 0.6
	got := buildLineMarkdown([]pdfRect{{text: "x1", fontSize: fs, fontName: "Arial"}}, bodySize)
	if got != "x1" {
		t.Errorf("rect at exactly 0.6x body size must render, got: %q", got)
	}
}

// A 3-char rect below the 0.6x threshold is a footnote marker and must drop.
func TestBuildLineMarkdownThreeCharMarkerDropped(t *testing.T) {
	got := buildLineMarkdown([]pdfRect{{text: "abc", fontSize: 5, fontName: "Arial"}}, 12)
	if got != "" {
		t.Errorf("3-char tiny rect must drop, got: %q", got)
	}
}

// Any positive sub-1pt font size still counts as tiny, so a 1-char marker
// drops even below 1pt.
func TestBuildLineMarkdownSubUnitMarkerDropped(t *testing.T) {
	got := buildLineMarkdown([]pdfRect{{text: "x", fontSize: 0.5, fontName: "Arial"}}, 12)
	if got != "" {
		t.Errorf("0.5pt 1-char rect must drop, got: %q", got)
	}
}

// The tiny-marker skip stays active when the body itself is only 1pt.
func TestBuildLineMarkdownTinyMarkerDroppedWhenBodyTiny(t *testing.T) {
	got := buildLineMarkdown([]pdfRect{{text: "x", fontSize: 0.5, fontName: "Arial"}}, 1)
	if got != "" {
		t.Errorf("0.5pt marker over 1pt body must drop, got: %q", got)
	}
}
