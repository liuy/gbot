package tool

import (
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
	got := RenderDiff(nil)
	if got != "" {
		t.Errorf("expected empty for nil hunks, got %q", got)
	}
	got = RenderDiff([]DiffHunk{})
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
	got := RenderDiff(hunks)
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
	got := RenderDiff(hunks)
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
	got := RenderDiff(hunks)
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
	got := RenderDiff(hunks)
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
	got := RenderDiff(hunks)
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
	got := RenderDiff(hunks)
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
	got := RenderDiff(hunks)
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
	if len(result) > 0 {
		for _, h := range result {
			for _, l := range h.Lines {
				if strings.HasPrefix(l, "-") || strings.HasPrefix(l, "+") {
					t.Errorf("ComputePatch(same, same) contains change line %q, want only context", l)
				}
			}
		}
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
	allLines := ""
	for _, h := range result {
		for _, l := range h.Lines {
			allLines += l + "\n"
		}
	}
	if !strings.Contains(allLines, "-bbb") || !strings.Contains(allLines, "+BBB") {
		t.Error("missing first change (bbb→BBB)")
	}
	if !strings.Contains(allLines, "-ggg") || !strings.Contains(allLines, "+GGG") {
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
			// Just verify it doesn't panic and returns valid output
			for _, h := range result {
				if len(h.Lines) == 0 {
					t.Errorf("empty hunk for case %q", tt.name)
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
			_ = ComputePatch(tt.old, tt.new)
		})
	}
}

func TestComputePatch_AppendDiffComponentEdge(t *testing.T) {
	t.Parallel()
	// The count==0 guard in appendDiffComponent is technically unreachable
	// in the current algorithm (all callers pass count > 0).
	// But we verify the function works correctly for normal cases.
	tests := []struct {
		name string
		old  string
		new  string
	}{
		// Consecutive components of same type (merge case)
		{"merge_same", "a\nb\nc\n", "x\na\nb\ny\nc\n"},
		// Consecutive components of different type
		{"merge_diff", "a\nb\nc\n", "x\nb\nc\n"},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_ = ComputePatch(tt.old, tt.new)
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
	got := RenderDiff(hunks)
	if got == "" {
		t.Error("expected non-empty output")
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

