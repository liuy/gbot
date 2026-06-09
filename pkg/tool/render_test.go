package tool

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// ---------------------------------------------------------------------------
// CountLines
// ---------------------------------------------------------------------------

func TestCountLines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"hello", 1},
		{"hello\n", 1},
		{"hello\nworld", 2},
		{"hello\nworld\n", 2},
		{"a\nb\nc\n", 3},
		{"\n", 1},
		{"\n\n", 2},
	}
	for _, tt := range tests {
		got := CountLines(tt.input)
		if got != tt.want {
			t.Errorf("CountLines(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// CountPatchChanges
// ---------------------------------------------------------------------------

func TestCountPatchChanges_Empty(t *testing.T) {
	t.Parallel()
	added, removed := CountPatchChanges(nil)
	if added != 0 || removed != 0 {
		t.Errorf("CountPatchChanges(nil) = %d, %d, want 0, 0", added, removed)
	}
}

func TestCountPatchChanges_Mixed(t *testing.T) {
	t.Parallel()
	hunks := []DiffHunk{
		{
			OldStart: 1, OldLines: 5, NewStart: 1, NewLines: 6,
			Lines: []string{" ctx1", "-del1", "+add1", "+add2", " ctx2", "-del2", "+add3"},
		},
	}
	added, removed := CountPatchChanges(hunks)
	if added != 3 {
		t.Errorf("added = %d, want 3", added)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
}

func TestCountPatchChanges_OnlyContext(t *testing.T) {
	t.Parallel()
	hunks := []DiffHunk{
		{Lines: []string{" a", " b", " c"}},
	}
	added, removed := CountPatchChanges(hunks)
	if added != 0 || removed != 0 {
		t.Errorf("context only: added=%d, removed=%d, want 0,0", added, removed)
	}
}

// ---------------------------------------------------------------------------
// FormatDiffSummary
// ---------------------------------------------------------------------------

func TestFormatDiffSummary_AddedOnly(t *testing.T) {
	t.Parallel()
	got := FormatDiffSummary(3, 0)
	if !strings.Contains(got, "Added") {
		t.Errorf("expected 'Added', got %q", got)
	}
	if !strings.Contains(got, "lines") {
		t.Errorf("expected plural 'lines', got %q", got)
	}
	if strings.Contains(got, "removed") {
		t.Errorf("should not contain 'removed', got %q", got)
	}
}

func TestFormatDiffSummary_SingleAddition(t *testing.T) {
	t.Parallel()
	got := FormatDiffSummary(1, 0)
	if !strings.Contains(got, "line") {
		t.Errorf("expected singular 'line', got %q", got)
	}
	if strings.Contains(got, "lines") {
		t.Errorf("should be singular, got %q", got)
	}
}

func TestFormatDiffSummary_RemovedOnly(t *testing.T) {
	t.Parallel()
	got := FormatDiffSummary(0, 2)
	if !strings.Contains(got, "Removed") {
		t.Errorf("expected capital 'Removed', got %q", got)
	}
	if !strings.Contains(got, "lines") {
		t.Errorf("expected plural 'lines', got %q", got)
	}
}

func TestFormatDiffSummary_Both(t *testing.T) {
	t.Parallel()
	got := FormatDiffSummary(3, 2)
	if !strings.Contains(got, "Added") {
		t.Errorf("expected 'Added', got %q", got)
	}
	if !strings.Contains(got, "removed") {
		t.Errorf("expected lowercase 'removed', got %q", got)
	}
	if !strings.Contains(got, ", ") {
		t.Errorf("expected comma separator, got %q", got)
	}
}

func TestFormatDiffSummary_Zero(t *testing.T) {
	t.Parallel()
	got := FormatDiffSummary(0, 0)
	if got != "" {
		t.Errorf("expected empty for zero changes, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// RenderDiff
// ---------------------------------------------------------------------------

func TestRenderDiff_EmptyHunks(t *testing.T) {
	t.Parallel()
	got := RenderDiff(nil, 0)
	if got != "" {
		t.Errorf("expected empty for nil hunks, got %q", got)
	}
	got = RenderDiff([]DiffHunk{}, 0)
	if got != "" {
		t.Errorf("expected empty for empty hunks, got %q", got)
	}
}

func TestRenderDiff_SingleAddition(t *testing.T) {
	t.Parallel()
	hunks := []DiffHunk{
		{
			OldStart: 1, OldLines: 1, NewStart: 1, NewLines: 2,
			Lines: []string{" ctx", "+added"},
		},
	}
	got := RenderDiff(hunks, 0)
	plain := stripDiffANSI(got)
	if !strings.Contains(plain, "1  ctx") {
		t.Errorf("expected context line '1  ctx', got:\n%s", plain)
	}
	if !strings.Contains(plain, "2 +added") {
		t.Errorf("expected added line '2 +added', got:\n%s", plain)
	}
}

func TestRenderDiff_SingleDeletion(t *testing.T) {
	t.Parallel()
	hunks := []DiffHunk{
		{
			OldStart: 1, OldLines: 2, NewStart: 1, NewLines: 1,
			Lines: []string{" ctx", "-deleted"},
		},
	}
	got := RenderDiff(hunks, 0)
	plain := stripDiffANSI(got)
	if !strings.Contains(plain, "2 -deleted") {
		t.Errorf("expected deleted line '2 -deleted', got:\n%s", plain)
	}
}

func TestRenderDiff_MixedChanges(t *testing.T) {
	t.Parallel()
	hunks := []DiffHunk{
		{
			OldStart: 1, OldLines: 5, NewStart: 1, NewLines: 5,
			Lines: []string{" ctx1", "-old", "+new", " ctx2"},
		},
	}
	got := RenderDiff(hunks, 0)
	plain := stripDiffANSI(got)
	if !strings.Contains(plain, "-old") {
		t.Errorf("expected '-old', got:\n%s", plain)
	}
	if !strings.Contains(plain, "+new") {
		t.Errorf("expected '+new', got:\n%s", plain)
	}
	// Should have ANSI colors for added/deleted
	if !strings.Contains(got, "\x1b[48;5;22m") {
		t.Error("expected green bg (added) ANSI code")
	}
	if !strings.Contains(got, "\x1b[48;5;52m") {
		t.Error("expected red bg (deleted) ANSI code")
	}
}

func TestRenderDiff_MultipleHunks(t *testing.T) {
	t.Parallel()
	hunks := []DiffHunk{
		{
			OldStart: 1, OldLines: 2, NewStart: 1, NewLines: 2,
			Lines: []string{" a", "+b"},
		},
		{
			OldStart: 10, OldLines: 2, NewStart: 11, NewLines: 2,
			Lines: []string{" c", "-d"},
		},
	}
	got := RenderDiff(hunks, 0)
	plain := stripDiffANSI(got)
	// Should have "..." separator between hunks
	if !strings.Contains(plain, "...") {
		t.Errorf("expected '...' separator between hunks, got:\n%s", plain)
	}
	if !strings.Contains(plain, "+b") {
		t.Errorf("expected '+b' from first hunk, got:\n%s", plain)
	}
	if !strings.Contains(plain, "-d") {
		t.Errorf("expected '-d' from second hunk, got:\n%s", plain)
	}
}

func TestRenderDiff_LineNumberAlignment(t *testing.T) {
	t.Parallel()
	// Line numbers should be right-aligned
	hunks := []DiffHunk{
		{
			OldStart: 1, OldLines: 1, NewStart: 100, NewLines: 1,
			Lines: []string{"+new"},
		},
	}
	got := RenderDiff(hunks, 0)
	plain := stripDiffANSI(got)
	// newLine=100, maxDigits=3, so "100" is 3 chars
	if !strings.Contains(plain, "100 +new") {
		t.Errorf("expected '100 +new' with right-aligned line number, got:\n%s", plain)
	}
}

func TestRenderDiff_HunkWithEmptyLines(t *testing.T) {
	t.Parallel()
	hunks := []DiffHunk{
		{
			OldStart: 1, OldLines: 2, NewStart: 1, NewLines: 3,
			Lines: []string{" ctx", "+"}, // empty addition
		},
	}
	got := RenderDiff(hunks, 0)
	plain := stripDiffANSI(got)
	// Should not panic, should contain the addition marker
	lines := strings.Split(plain, "\n")
	if len(lines) < 2 {
		t.Errorf("expected at least 2 lines, got %d: %q", len(lines), plain)
	}
}

func TestRenderDiff_SingleContextHunk(t *testing.T) {
	t.Parallel()
	hunks := []DiffHunk{
		{
			OldStart: 1, OldLines: 3, NewStart: 1, NewLines: 3,
			Lines: []string{" a", " b", " c"},
		},
	}
	got := RenderDiff(hunks, 0)
	plain := stripDiffANSI(got)
	if !strings.Contains(plain, "1  a") {
		t.Errorf("expected context line '1  a', got:\n%s", plain)
	}
}

// ---------------------------------------------------------------------------
// FormatMoreLines
// ---------------------------------------------------------------------------

func TestFormatMoreLines(t *testing.T) {
	t.Parallel()
	got := FormatMoreLines(5)
	plain := stripDiffANSI(got)
	if !strings.Contains(plain, "… +5 lines (ctrl+o to expand)") {
		t.Errorf("expected '… +5 lines (ctrl+o to expand)', got: %q", plain)
	}
}

func TestFormatMoreLines_Singular(t *testing.T) {
	t.Parallel()
	got := FormatMoreLines(1)
	plain := stripDiffANSI(got)
	if !strings.Contains(plain, "… +1 line (ctrl+o to expand)") {
		t.Errorf("expected singular '… +1 line (ctrl+o to expand)', got: %q", plain)
	}
	if strings.Contains(plain, "lines") {
		t.Errorf("should be singular, got: %q", plain)
	}
}

// ---------------------------------------------------------------------------
// TruncateStringLines
// ---------------------------------------------------------------------------

func TestTruncateStringLines_NoTruncation(t *testing.T) {
	t.Parallel()
	input := "line1\nline2\nline3"
	got := TruncateStringLines(input, 10)
	if got != input {
		t.Errorf("expected no truncation, got: %q", got)
	}
}

func TestTruncateStringLines_Truncates(t *testing.T) {
	t.Parallel()
	input := "line1\nline2\nline3\nline4\nline5"
	got := TruncateStringLines(input, 3)
	plain := stripDiffANSI(got)
	if !strings.Contains(plain, "line1") {
		t.Errorf("expected first 3 lines, got: %q", plain)
	}
	if strings.Contains(plain, "line4") {
		t.Errorf("should not contain line4, got: %q", plain)
	}
	if !strings.Contains(plain, "… +2 lines (ctrl+o to expand)") {
		t.Errorf("expected '… +2 lines (ctrl+o to expand)', got: %q", plain)
	}
}

func TestTruncateStringLines_ExactLimit(t *testing.T) {
	t.Parallel()
	input := "line1\nline2\nline3"
	got := TruncateStringLines(input, 3)
	if got != input {
		t.Errorf("exact limit should not truncate, got: %q", got)
	}
}

func TestTruncateStringLines_Empty(t *testing.T) {
	t.Parallel()
	got := TruncateStringLines("", 5)
	if got != "" {
		t.Errorf("expected empty, got: %q", got)
	}
}

func TestTruncateStringLines_ZeroMax(t *testing.T) {
	t.Parallel()
	got := TruncateStringLines("line1\nline2", 0)
	if got != "line1\nline2" {
		t.Errorf("zero maxLines should return original, got: %q", got)
	}
}

// ---------------------------------------------------------------------------
// RenderDiff — wrap + pad to terminal width (TS StructuredDiff/Fallback.tsx)
// ---------------------------------------------------------------------------
//
// Source: src/components/StructuredDiff/Fallback.tsx formatDiff() / wrapDiffLine()
// — each diff line wraps to fit availableContentWidth = max(1, width - maxWidth - 1 - diffPrefixWidth).
// — sub-lines after the first keep the gutter width but blank out the line number column.
// — every sub-line is padded with the line's background color to fill width.
//
// width <= 0 disables wrapping (back-compat escape hatch for tests that
// don't care about wrapping).

// A long line that exceeds the requested terminal width wraps into multiple
// sub-lines, each carrying the diffAddBg so the visual stripe is unbroken.
func TestRenderDiff_LongAddedLine_WrapsWithAddBg(t *testing.T) {
	t.Parallel()
	// 100-char content line, width=40 → must wrap (maxDigits=1, so available=36)
	long := "+" + strings.Repeat("a", 100)
	hunks := []DiffHunk{{
		OldStart: 1, OldLines: 0, NewStart: 1, NewLines: 1,
		Lines: []string{long},
	}}
	got := RenderDiff(hunks, 40)
	plain := stripDiffANSI(got)
	lines := strings.Split(plain, "\n")
	wantSubLines := 3 // 100 chars / 36 cols per sub-line = 3
	if len(lines) != wantSubLines {
		t.Fatalf("expected exactly %d sub-lines after wrap, got %d:\n%s",
			wantSubLines, len(lines), plain)
	}
	// diffAddBg must appear in both the gutter and the content of every
	// sub-line (gutter + content are written as two separate styled spans).
	// We assert at-least-N-per-line by counting \x1b[48;5;22m per sub-line
	// when we split the raw output on \x1b[0m boundaries.
	rawLines := strings.Split(got, "\x1b[0m")
	bgCount := 0
	for _, raw := range rawLines {
		bgCount += strings.Count(raw, "\x1b[48;5;22m")
	}
	if bgCount < wantSubLines {
		t.Errorf("expected diffAddBg >= %d (one per sub-line's content area), got %d in:\n%s",
			wantSubLines, bgCount, got)
	}
	// Sanity: no diffDelBg should leak into an added-only hunk.
	if c := strings.Count(got, "\x1b[48;5;52m"); c != 0 {
		t.Errorf("expected zero diffDelBg on an added-only hunk, got %d", c)
	}
	// All 100 'a' chars must be preserved across sub-lines (wrap must not
	// truncate or duplicate content).
	if n := strings.Count(plain, "a"); n != 100 {
		t.Errorf("expected 100 'a' chars preserved across wrap, got %d in:\n%s", n, plain)
	}
	// First sub-line must be exactly width columns (gutter + content + pad).
	if w := len([]rune(lines[0])); w != 40 {
		t.Errorf("first sub-line width = %d, want 40 (full row fill)", w)
	}
}

// Long removed line wraps and every sub-line carries diffDelBg.
func TestRenderDiff_LongRemovedLine_WrapsWithDelBg(t *testing.T) {
	t.Parallel()
	long := "-" + strings.Repeat("b", 100)
	hunks := []DiffHunk{{
		OldStart: 1, OldLines: 1, NewStart: 1, NewLines: 0,
		Lines: []string{long},
	}}
	got := RenderDiff(hunks, 40)
	plain := stripDiffANSI(got)
	lines := strings.Split(plain, "\n")
	wantSubLines := 3
	if len(lines) != wantSubLines {
		t.Fatalf("expected exactly %d sub-lines after wrap, got %d:\n%s",
			wantSubLines, len(lines), plain)
	}
	// diffDelBg must appear in every sub-line's content area (counting
	// occurrences after \x1b[0m boundaries). The dim (red-fg) attribute is
	// also expected on every sub-line, since removed-line markers use it.
	rawLines := strings.Split(got, "\x1b[0m")
	delBgCount, delFgCount := 0, 0
	for _, raw := range rawLines {
		delBgCount += strings.Count(raw, "\x1b[48;5;52m")
		delFgCount += strings.Count(raw, "\x1b[38;5;9m")
	}
	if delBgCount < wantSubLines {
		t.Errorf("expected diffDelBg >= %d (one per sub-line), got %d in:\n%s",
			wantSubLines, delBgCount, got)
	}
	if delFgCount < wantSubLines {
		t.Errorf("expected diffDelFg >= %d (one per sub-line marker), got %d", wantSubLines, delFgCount)
	}
	// Sanity: no diffAddBg on a removed-only hunk.
	if c := strings.Count(got, "\x1b[48;5;22m"); c != 0 {
		t.Errorf("expected zero diffAddBg on removed-only hunk, got %d", c)
	}
	// All 100 'b' chars must be preserved across sub-lines.
	if n := strings.Count(plain, "b"); n != 100 {
		t.Errorf("expected 100 'b' chars preserved across wrap, got %d in:\n%s", n, plain)
	}
	// First sub-line width must equal full terminal width.
	if w := len([]rune(lines[0])); w != 40 {
		t.Errorf("first sub-line width = %d, want 40 (full row fill)", w)
	}
}

// Each sub-line is padded with trailing spaces under the same bg color so
// the right edge of every visual row reaches exactly `width` columns.
// Verifies by stripping ANSI and counting runes per line == width.
func TestRenderDiff_Wrap_PadsEachSubLineToWidth(t *testing.T) {
	t.Parallel()
	const width = 40
	long := "+" + strings.Repeat("x", 100)
	hunks := []DiffHunk{{
		OldStart: 1, OldLines: 0, NewStart: 1, NewLines: 1,
		Lines: []string{long},
	}}
	got := RenderDiff(hunks, width)
	plain := stripDiffANSI(got)
	for i, line := range strings.Split(plain, "\n") {
		// Use visible (rune) width, not byte length.
		if w := len([]rune(line)); w != width {
			t.Errorf("sub-line %d width = %d, want %d (line=%q)", i, w, width, line)
		}
	}
}

// Sub-lines after the first leave the gutter (line-number column) blank but
// preserve column width so the diff marker still aligns vertically.
func TestRenderDiff_Wrap_SubsequentSubLinesBlankGutter(t *testing.T) {
	t.Parallel()
	const width = 40
	// Gutter layout: " " + paddedNum + " " = maxDigits+2 chars; continuation
	// sub-lines replace paddedNum with spaces. For a single new line at line 1,
	// maxDigits = 1, so the gutter is " 1 " (3 chars) on the first sub-line and
	// "   " (3 spaces) on continuations.
	long := "+" + strings.Repeat("z", 100)
	hunks := []DiffHunk{{
		OldStart: 1, OldLines: 0, NewStart: 1, NewLines: 1,
		Lines: []string{long},
	}}
	got := RenderDiff(hunks, width)
	plain := stripDiffANSI(got)
	lines := strings.Split(plain, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2+ sub-lines, got %d: %q", len(lines), plain)
	}
	// First sub-line: line-number column is " 1 " (line 1, right-aligned in 1 digit).
	if !strings.HasPrefix(lines[0], " 1 ") {
		t.Errorf("first sub-line should start with ' 1 ', got %q", lines[0])
	}
	// Second sub-line: line-number column is all spaces (no digit), but the
	// diff marker is still rendered after it for visual consistency.
	// Layout: " " + "1" + " " + "+" + content on first sub-line,
	//         " " + " " + " " + "+" + content on continuation.
	if !strings.HasPrefix(lines[1], "   ") {
		t.Errorf("continuation gutter should be 3 blank spaces, got %q (full line=%q)",
			lines[1][:3], lines[1])
	}
	if !strings.HasPrefix(lines[1], "   +") {
		t.Errorf("continuation should keep '+' marker after blank gutter, got %q", lines[1])
	}
}

// Short lines (well under width) must render exactly one line each — no
// extra blank lines or padding-induced duplicates.
func TestRenderDiff_ShortLine_NoExtraBlankLines(t *testing.T) {
	t.Parallel()
	hunks := []DiffHunk{{
		OldStart: 1, OldLines: 0, NewStart: 1, NewLines: 1,
		Lines: []string{"+hi"},
	}}
	got := RenderDiff(hunks, 120)
	plain := stripDiffANSI(got)
	lines := strings.Split(plain, "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 line for short content, got %d: %q", len(lines), plain)
	}
	if !strings.Contains(lines[0], "+hi") {
		t.Errorf("expected '+hi' in line, got %q", lines[0])
	}
}

// width == 0 (or negative) disables wrapping entirely — back-compat escape
// hatch used by tests that only check color/marker behavior.
func TestRenderDiff_ZeroWidth_FallsBackToNoWrap(t *testing.T) {
	t.Parallel()
	long := "+" + strings.Repeat("k", 200)
	hunks := []DiffHunk{{
		OldStart: 1, OldLines: 0, NewStart: 1, NewLines: 1,
		Lines: []string{long},
	}}
	got := RenderDiff(hunks, 0)
	plain := stripDiffANSI(got)
	lines := strings.Split(plain, "\n")
	if len(lines) != 1 {
		t.Fatalf("width=0 should not wrap, got %d lines: %q", len(lines), plain)
	}
	if !strings.Contains(plain, strings.Repeat("k", 200)) {
		t.Errorf("expected full unwrapped content in output, got %q", plain)
	}

	// Same for negative width.
	got = RenderDiff(hunks, -5)
	if got == "" || strings.Count(got, "\n") != 0 {
		t.Errorf("width<0 should not wrap, got %q", got)
	}
}

// stripDiffANSI removes all ANSI escape sequences for content comparison.
func stripDiffANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// ---------------------------------------------------------------------------
// ComputePatch — line-level diff (equivalent to diff npm's structuredPatch)
// ---------------------------------------------------------------------------

func TestComputePatch_NoChange(t *testing.T) {
	t.Parallel()
	result := ComputePatch("hello world", "hello world")
	if len(result) != 0 {
		t.Fatalf("ComputePatch(same, same) should return empty, got %d hunks", len(result))
	}
}

func TestComputePatch_SimpleChange(t *testing.T) {
	t.Parallel()
	// Whole-line change: "line2" → "mod2"
	result := ComputePatch("line1\nline2\nline3\n", "line1\nmod2\nline3\n")
	if len(result) == 0 {
		t.Fatal("ComputePatch returned empty, want at least one hunk")
	}
	// Should have whole-line diff, not character-level
	foundDel := false
	foundIns := false
	for _, hunk := range result {
		for _, line := range hunk.Lines {
			if line == "-line2" {
				foundDel = true
			}
			if line == "+mod2" {
				foundIns = true
			}
		}
	}
	if !foundDel {
		t.Errorf("missing '-line2', got hunks: %+v", result)
	}
	if !foundIns {
		t.Errorf("missing '+mod2', got hunks: %+v", result)
	}
}

func TestComputePatch_ContextLines(t *testing.T) {
	t.Parallel()
	old := "line1\nline2\nline3\nline4\nline5\nline6\nline7\n"
	new_ := "line1\nline2\nline3\nmodified\nline5\nline6\nline7\n"
	result := ComputePatch(old, new_)
	if len(result) == 0 {
		t.Fatal("ComputePatch returned empty")
	}
	hunk := result[0]

	// Hunk should contain context lines before and after the change
	hasLeadingCtx := false
	hasTrailingCtx := false
	for _, l := range hunk.Lines {
		if l == " line3" {
			hasLeadingCtx = true
		}
		if l == " line5" {
			hasTrailingCtx = true
		}
	}
	if !hasLeadingCtx {
		t.Error("hunk missing leading context line ' line3'")
	}
	if !hasTrailingCtx {
		t.Error("hunk missing trailing context line ' line5'")
	}

	// Verify change lines
	foundDel := false
	foundIns := false
	for _, l := range hunk.Lines {
		if l == "-line4" {
			foundDel = true
		}
		if l == "+modified" {
			foundIns = true
		}
	}
	if !foundDel {
		t.Error("hunk missing '-line4'")
	}
	if !foundIns {
		t.Error("hunk missing '+modified'")
	}
}

func TestComputePatch_TwoChangesMergedHunk(t *testing.T) {
	t.Parallel()
	// Two changes close together — should produce a single merged hunk
	old := "aaa\nbbb\nccc\nddd\neee\nfff\nggg\nhhh\niii\n"
	new_ := "aaa\nBBB\nccc\nddd\neee\nfff\nGGG\nhhh\niii\n"
	result := ComputePatch(old, new_)
	if len(result) == 0 {
		t.Fatal("ComputePatch returned empty")
	}
	// Close changes should produce at most 2 hunks
	if len(result) > 2 {
		t.Errorf("got %d hunks, expected at most 2 for close changes", len(result))
	}
	// Verify both changes are present
	var allLines strings.Builder
	for _, h := range result {
		for _, l := range h.Lines {
			allLines.WriteString(l + "\n")
		}
	}
	if !strings.Contains(allLines.String(), "-bbb") || !strings.Contains(allLines.String(), "+BBB") {
		t.Error("missing first change (bbb→BBB)")
	}
	if !strings.Contains(allLines.String(), "-ggg") || !strings.Contains(allLines.String(), "+GGG") {
		t.Error("missing second change (ggg→GGG)")
	}
}

func TestComputePatch_EmptyOld(t *testing.T) {
	t.Parallel()
	result := ComputePatch("", "new content\n")
	if len(result) == 0 {
		t.Fatal("ComputePatch returned empty for empty→new")
	}
}

func TestComputePatch_EmptyNew(t *testing.T) {
	t.Parallel()
	result := ComputePatch("old content\n", "")
	if len(result) == 0 {
		t.Fatal("ComputePatch returned empty for old→empty")
	}
}

func TestComputePatch_BothEmpty(t *testing.T) {
	t.Parallel()
	result := ComputePatch("", "")
	if result != nil {
		t.Errorf("ComputePatch('', '') = %v, want nil", result)
	}
}

func TestComputePatch_DiffHunkFields(t *testing.T) {
	t.Parallel()
	old := "line1\nline2\nline3\n"
	new_ := "line1\nchanged\nline3\n"
	result := ComputePatch(old, new_)
	if len(result) == 0 {
		t.Fatal("got 0 hunks")
	}
	h := result[0]
	// TS structuredPatch behavior: OldStart points to first line of hunk (including
	// leading context), OldLines counts total old-file lines in the hunk.
	// Changed line 2 with 1 leading context (line1) and 1 trailing context (line3)
	// → oldStart=1 (first line = context), oldLines=3 (lines 1-3)
	if h.OldStart != 1 {
		t.Errorf("OldStart = %d, want 1", h.OldStart)
	}
	if h.OldLines != 3 {
		t.Errorf("OldLines = %d, want 3 (leading ctx + changed + trailing ctx)", h.OldLines)
	}
	// New: same structure
	if h.NewStart != 1 {
		t.Errorf("NewStart = %d, want 1", h.NewStart)
	}
	if h.NewLines != 3 {
		t.Errorf("NewLines = %d, want 3 (leading ctx + changed + trailing ctx)", h.NewLines)
	}
}

func TestComputePatch_LineNumberInContext(t *testing.T) {
	t.Parallel()
	// Context lines should show new file line numbers
	result := ComputePatch("a\nb\nc\nd\ne\n", "a\nX\nc\nd\ne\n")
	if len(result) == 0 {
		t.Fatal("got 0 hunks")
	}
	h := result[0]
	// Line 2 was changed: context should include lines from new file
	// Check that context line " c" appears (line 3 of new file)
	foundC := false
	for _, l := range h.Lines {
		if l == " c" {
			foundC = true
		}
	}
	if !foundC {
		t.Errorf("expected context line ' c' in hunk, got: %v", h.Lines)
	}
}

// ---------------------------------------------------------------------------
// Extended ComputePatch coverage tests
// ---------------------------------------------------------------------------

func TestComputePatch_LineDiffBranches(t *testing.T) {
	t.Parallel()
	// Cover lineDiff prefix/suffix branches
	tests := []struct {
		name string
		old  string
		new  string
	}{
		// oldStart=0, newStart=0 (no prefix)
		{"no_prefix", "b\nc\n", "x\nb\nc\n"},
		// oldStart>0, newStart>0 with prefix
		{"with_prefix", "a\nb\nc\n", "a\nx\nc\n"},
		// oldEnd<oldLen (has suffix)
		{"has_suffix", "a\nb\n", "a\nx\n"},
		// Both prefix and suffix stripped
		{"both_prefix_suffix", "a\nb\nc\n", "a\nx\nc\n"},
		// Empty mid section (identical content)
		{"empty_mid", "a\nb\nc\n", "a\nb\nc\n"},
		// Suffix strip: both oldEnd<oldLen and newEnd<newLen
		{"suffix_strip_both", "a\nb\nc\n", "x\na\nb\nc\ny\n"},
		// Long middle section with multiple changes
		{"long_mid_multi_changes", "a\nx\nb\ny\nc\n", "a\np\nb\nq\nc\n"},
		// Old longer than new (deletions)
		{"old_longer", "a\nb\nc\nd\ne\n", "a\nb\nc\n"},
		// New longer than old (insertions)
		{"new_longer", "a\nb\nc\n", "a\nb\nx\nc\nd\ne\n"},
		// Single deletion
		{"single_deletion", "a\nx\nb\n", "a\nb\n"},
		// Single insertion
		{"single_insertion", "a\nb\n", "a\nx\nb\n"},
		// Remaining deletions after LCS
		{"remaining_del", "a\nb\nx\ny\nz\n", "a\nb\nc\n"},
		// Remaining insertions after LCS
		{"remaining_ins", "a\nb\nc\n", "a\nb\nx\ny\nz\n"},
		// Deletion loop: deletions before first LCS entry
		// oldMid=["x","y"], newMid=["x"] → LCS entry at (0,0), oldPos stays 0, no pre-entry del
		// Need: oldMid longer, first LCS entry at oldIdx>0
		{"del_before_first_lcs", "a\nx\ny\nz\n", "a\nb\nz\n"},
		// Insertion loop: insertions before first LCS entry
		{"ins_before_first_lcs", "a\nz\n", "a\nx\ny\nz\n"},
		// commonCount accumulation (consecutive matches > 1)
		{"common_count_accum", "a\nb\nc\nd\ne\n", "a\nx\nc\nd\ne\n"},
		// commonCount > 0 at end of LCS (suffix context)
		{"common_suffix_context", "line1\nline2\nline3\nline4\nline5\n", "line1\nmodified\nline3\nline4\nline5\n"},
		// Exact suffix strip (oldEnd==oldStart, newEnd==newStart)
		{"exact_suffix", "a\nb\nc\n", "x\na\nb\nc\ny\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ComputePatch(tt.old, tt.new)
			if tt.name == "empty_mid" {
				if result != nil {
					t.Errorf("identical content should produce nil, got %v", result)
				}
				return
			}
			if len(result) == 0 {
				t.Fatalf("case %q: expected at least one hunk", tt.name)
			}
			for i, h := range result {
				if len(h.Lines) == 0 {
					t.Errorf("empty hunk %d for case %q", i, tt.name)
				}
				hasContent := false
				for _, l := range h.Lines {
					if strings.HasPrefix(l, "-") || strings.HasPrefix(l, "+") || strings.HasPrefix(l, " ") {
						hasContent = true
						break
					}
				}
				if !hasContent {
					t.Errorf("hunk %d for case %q has no diff/context lines, got: %v", i, tt.name, h.Lines)
				}
			}
		})
	}
}

func TestComputePatch_LCSBranches(t *testing.T) {
	t.Parallel()
	// Cover lcsDP backtrack branches (up vs left)
	// These are exercised by various input patterns
	tests := []struct {
		name string
		old  string
		new  string
	}{
		// LCS backtrack: diagonal branch
		{"lcs_diagonal", "a\nb\nc\n", "a\nb\nc\n"},
		// LCS backtrack: up branch (deletion)
		{"lcs_up", "a\nb\nc\nd\n", "a\nx\nc\nd\n"},
		// LCS backtrack: left branch (insertion)
		{"lcs_left", "a\nx\nc\nd\n", "a\nb\nc\nd\n"},
		// Empty LCS result
		{"lcs_empty", "x\ny\nz\n", "a\nb\nc\n"},
		// Single element match
		{"lcs_single", "x\n", "x\n"},
		// Single element no match
		{"lcs_single_nomatch", "x\n", "y\n"},
		// Consecutive LCS entries with commonCount > 0:
		// oldMid=["b","c","d"], newMid=["x","b","c","d"]
		// LCS=[(1,1),(2,2)] — consecutive matches trigger commonCount merge guard
		{"lcs_consecutive", "a\nb\nc\nd\n", "a\nx\nb\nc\nd\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ComputePatch(tt.old, tt.new)
			switch tt.name {
			case "lcs_diagonal", "lcs_single":
				if result != nil {
					t.Errorf("identical inputs should produce nil, got %v", result)
				}
			case "lcs_empty":
				if len(result) == 0 {
					t.Fatal("expected at least one hunk for completely different inputs")
				}
				hasChanges := false
				for _, h := range result {
					for _, l := range h.Lines {
						if strings.HasPrefix(l, "-") || strings.HasPrefix(l, "+") {
							hasChanges = true
							break
						}
					}
				}
				if !hasChanges {
					t.Error("expected additions/removals for completely different inputs")
				}
			default:
				if len(result) == 0 {
					t.Fatalf("case %q: expected at least one hunk with changes", tt.name)
				}
				hasChanges := false
				for _, h := range result {
					for _, l := range h.Lines {
						if strings.HasPrefix(l, "-") || strings.HasPrefix(l, "+") {
							hasChanges = true
							break
						}
					}
				}
				if !hasChanges {
					t.Errorf("case %q: expected at least one change line in hunks", tt.name)
				}
			}
		})
	}
}

func TestComputePatch_AppendDiffComponentEdge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		old  string
		new  string
	}{
		{"merge_same", "a\nb\nc\n", "x\na\nb\ny\nc\n"},
		{"merge_diff", "a\nb\nc\n", "x\nb\nc\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ComputePatch(tt.old, tt.new)
			if len(result) == 0 {
				t.Fatalf("case %q: expected at least one hunk", tt.name)
			}
			hasChanges := false
			for _, h := range result {
				for _, l := range h.Lines {
					if strings.HasPrefix(l, "-") || strings.HasPrefix(l, "+") {
						hasChanges = true
						break
					}
				}
			}
			if !hasChanges {
				t.Errorf("case %q: expected change lines in hunks, got %v", tt.name, result)
			}
		})
	}
}

func TestRenderDiff_EmptyLine(t *testing.T) {
	t.Parallel()
	// Cover empty line in hunk
	hunks := []DiffHunk{
		{
			OldStart: 1, OldLines: 3, NewStart: 1, NewLines: 3,
			Lines: []string{" line1", "", " line3"},
		},
	}
	got := RenderDiff(hunks, 0)
	if got == "" {
		t.Error("expected non-empty output")
	}
	if !strings.Contains(got, "line1") {
		t.Error("output should contain line1")
	}
	if !strings.Contains(got, "line3") {
		t.Error("output should contain line3")
	}
}

func TestComputePatch_MinimalDiff(t *testing.T) {
	t.Parallel()
	// Changing 1 line should produce exactly 1 added + 1 removed, not delete-all + insert-all
	old := "a\nb\nc\nd\n"
	new_ := "a\nB\nc\nd\n"
	hunks := ComputePatch(old, new_)
	if len(hunks) == 0 {
		t.Fatal("expected at least one hunk")
	}
	added, removed := 0, 0
	for _, h := range hunks {
		for _, l := range h.Lines {
			if strings.HasPrefix(l, "+") {
				added++
			}
			if strings.HasPrefix(l, "-") {
				removed++
			}
		}
	}
	if added != 1 {
		t.Errorf("expected 1 added line, got %d", added)
	}
	if removed != 1 {
		t.Errorf("expected 1 removed line, got %d", removed)
	}
}

func TestComputePatch_MinimalDiffMiddle(t *testing.T) {
	t.Parallel()
	// Change line 5 out of 10 — only 1 added + 1 removed
	old := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n"
	new_ := "a\nb\nc\nd\nX\nf\ng\nh\ni\nj\n"
	hunks := ComputePatch(old, new_)
	if len(hunks) == 0 {
		t.Fatal("expected at least one hunk")
	}
	added, removed := 0, 0
	for _, h := range hunks {
		for _, l := range h.Lines {
			if strings.HasPrefix(l, "+") {
				added++
			}
			if strings.HasPrefix(l, "-") {
				removed++
			}
		}
	}
	if added != 1 {
		t.Errorf("expected 1 added, got %d", added)
	}
	if removed != 1 {
		t.Errorf("expected 1 removed, got %d", removed)
	}
	// Context lines should be present
	hasCtx := false
	for _, h := range hunks {
		for _, l := range h.Lines {
			if strings.HasPrefix(l, " ") {
				hasCtx = true
				break
			}
		}
	}
	if !hasCtx {
		t.Error("expected context lines around the change")
	}
}

func TestComputePatch_TooLarge(t *testing.T) {
	t.Parallel()
	// len(old)*len(new) > maxDiffEntries → should return nil
	oldLines := strings.Repeat("a\n", 4000)
	newLines := strings.Repeat("b\n", 4000)
	result := ComputePatch(oldLines, newLines)
	if result != nil {
		t.Errorf("expected nil for too-large input, got %d hunks", len(result))
	}
}

func TestComputePatch_TrulyEmptyStrings(t *testing.T) {
	t.Parallel()
	// Whitespace-only strings
	result := ComputePatch("   \n   \n", "   \n   \n")
	if result != nil {
		t.Errorf("expected nil for identical whitespace, got %v", result)
	}
}

func TestComputePatch_WhitespaceOnly(t *testing.T) {
	t.Parallel()
	// removeEmptyStrings trims whitespace, so these become empty and are removed
	result := ComputePatch("hello\n   \n", "hello\n   \n")
	if result != nil {
		t.Errorf("expected nil for identical whitespace, got %v", result)
	}
}

func TestRenderContentWithLineNumbers(t *testing.T) {
	t.Parallel()

	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		got := RenderContentWithLineNumbers("")
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("single line", func(t *testing.T) {
		t.Parallel()
		got := StripANSI(RenderContentWithLineNumbers("hello"))
		if got != " 1  hello" {
			t.Errorf("expected ' 1  hello', got %q", got)
		}
	})

	t.Run("multi line with trailing newline", func(t *testing.T) {
		t.Parallel()
		got := StripANSI(RenderContentWithLineNumbers("aaa\nbbb\nccc\n"))
		lines := strings.Split(got, "\n")
		if len(lines) != 3 {
			t.Fatalf("expected 3 lines, got %d: %q", len(lines), got)
		}
		if lines[0] != " 1  aaa" {
			t.Errorf("line 1 = %q, want ' 1  aaa'", lines[0])
		}
		if lines[1] != " 2  bbb" {
			t.Errorf("line 2 = %q, want ' 2  bbb'", lines[1])
		}
		if lines[2] != " 3  ccc" {
			t.Errorf("line 3 = %q, want ' 3  ccc'", lines[2])
		}
	})

	t.Run("gutter width for 10+ lines", func(t *testing.T) {
		t.Parallel()
		var content strings.Builder
		for i := 1; i <= 15; i++ {
			fmt.Fprintf(&content, "line %d\n", i)
		}
		got := StripANSI(RenderContentWithLineNumbers(content.String()))
		lines := strings.Split(got, "\n")
		// Single-digit lines should be right-aligned with 2-digit width: " 1  line 1"
		if !strings.HasPrefix(lines[0], "  1  ") {
			t.Errorf("line 1 should have padded gutter, got %q", lines[0])
		}
		// Double-digit lines: "10  line 10"
		if !strings.HasPrefix(lines[9], " 10  ") {
			t.Errorf("line 10 should have 2-digit gutter, got %q", lines[9])
		}
	})
}
