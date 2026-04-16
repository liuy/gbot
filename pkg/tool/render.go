package tool

import (
	"fmt"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Line-level diff — equivalent to diff npm's diffLines + structuredPatch.
// Source: node_modules/diff/lib/structuredPatch.js + libcjs/diff/line.js + libcjs/diff/base.js
// Uses O(ND) Myers diff on lines.
// ---------------------------------------------------------------------------

// maxDiffEntries caps the LCS DP table to prevent OOM.
const maxDiffEntries = 10_000_000

// lcsEntry is a position in the LCS result.
type lcsEntry struct {
	oldIdx, newIdx int
}

// diffComponent is a single component in the diff result.
type diffComponent struct {
	added   bool
	removed bool
	count   int
}

// tokenizeLines splits text into lines, each including its trailing newline
// except the last. Mirrors the diff npm's splitLines().
// Source: diff/libcjs/patch/create.js — splitLines()
func tokenizeLines(text string) []string {
	hasTrailingNL := strings.HasSuffix(text, "\n")
	parts := strings.Split(text, "\n")
	result := make([]string, 0, len(parts))
	for i, line := range parts {
		if i == len(parts)-1 && !hasTrailingNL {
			result = append(result, line)
		} else {
			result = append(result, line+"\n")
		}
	}
	return result
}

// removeEmptyStrings removes empty and all-whitespace strings from a slice.
func removeEmptyStrings(ss []string) []string {
	r := make([]string, 0, len(ss))
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			r = append(r, s)
		}
	}
	return r
}

// appendDiffComponent merges with the last component if it has the same type.
func appendDiffComponent(list []diffComponent, added, removed bool, count int) []diffComponent {
	if count == 0 {
		return list
	}
	if len(list) > 0 {
		last := &list[len(list)-1]
		if last.added == added && last.removed == removed {
			last.count += count
			return list
		}
	}
	return append(list, diffComponent{added: added, removed: removed, count: count})
}

// lcsDP computes LCS of two string slices using standard O(mn) DP with
// backtracking. Capped at maxDiffEntries (10M) to prevent OOM.
func lcsDP(a, b []string) []lcsEntry {
	m, n := len(a), len(b)
	if m == 0 || n == 0 {
		return nil
	}

	// Direction: 0=diag, 1=up, 2=left
	dir := make([]byte, (m+1)*(n+1))
	dp := make([]int, (m+1)*(n+1))
	idx := func(i, j int) int { return i*(n+1) + j }

	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				dp[idx(i, j)] = dp[idx(i-1, j-1)] + 1
				dir[idx(i, j)] = 0 // diagonal — match
			} else if dp[idx(i-1, j)] >= dp[idx(i, j-1)] {
				dp[idx(i, j)] = dp[idx(i-1, j)]
				dir[idx(i, j)] = 1 // up
			} else {
				dp[idx(i, j)] = dp[idx(i, j-1)]
				dir[idx(i, j)] = 2 // left
			}
		}
	}

	// Backtrack following direction arrows
	var rev []lcsEntry
	i, j := m, n
	for i > 0 && j > 0 {
		if dir[idx(i, j)] == 0 {
			// Diagonal — must be a match (guaranteed by forward pass)
			rev = append(rev, lcsEntry{oldIdx: i - 1, newIdx: j - 1})
			i--
			j--
		} else if dir[idx(i, j)] == 1 {
			i--
		} else {
			j--
		}
	}

	// Reverse to forward order
	lcs := make([]lcsEntry, len(rev))
	for k := 0; k < len(rev); k++ {
		lcs[k] = rev[len(rev)-1-k]
	}
	return lcs
}

// lineDiff runs O(ND) diff on two string slices, returning components.
func lineDiff(oldTokens, newTokens []string) []diffComponent {
	oldLen := len(oldTokens)
	newLen := len(newTokens)

	if oldLen == 0 && newLen == 0 {
		return nil
	}
	if oldLen == 0 {
		return []diffComponent{{added: true, removed: false, count: newLen}}
	}
	if newLen == 0 {
		return []diffComponent{{added: false, removed: true, count: oldLen}}
	}

	// Find common prefix
	oldStart, newStart := 0, 0
	for oldStart < oldLen && newStart < newLen && oldTokens[oldStart] == newTokens[newStart] {
		oldStart++
		newStart++
	}

	// Find common suffix
	oldEnd, newEnd := oldLen, newLen
	for oldEnd > oldStart && newEnd > newStart && oldTokens[oldEnd-1] == newTokens[newEnd-1] {
		oldEnd--
		newEnd--
	}

	oldMid := oldTokens[oldStart:oldEnd]
	newMid := newTokens[newStart:newEnd]

	// LCS DP for the middle portion
	lcs := lcsDP(oldMid, newMid)

	var result []diffComponent

	// Prefix
	if oldStart > 0 {
		result = appendDiffComponent(result, false, false, oldStart)
	}

	// Build diff from LCS
	oldPos, newPos, commonCount := 0, 0, 0
	for _, entry := range lcs {
		// Deletions before LCS match
		for oldPos < entry.oldIdx {
			if commonCount > 0 {
				result = appendDiffComponent(result, false, false, commonCount)
				commonCount = 0
			}
			result = appendDiffComponent(result, false, true, 1)
			oldPos++
		}
		// Insertions before LCS match
		for newPos < entry.newIdx {
			if commonCount > 0 {
				result = appendDiffComponent(result, false, false, commonCount)
				commonCount = 0
			}
			result = appendDiffComponent(result, true, false, 1)
			newPos++
		}
		// LCS match
		commonCount++
		oldPos++
		newPos++
	}

	// Remaining deletions
	for oldPos < len(oldMid) {
		if commonCount > 0 {
			result = appendDiffComponent(result, false, false, commonCount)
			commonCount = 0
		}
		result = appendDiffComponent(result, false, true, 1)
		oldPos++
	}
	// Remaining insertions
	for newPos < len(newMid) {
		if commonCount > 0 {
			result = appendDiffComponent(result, false, false, commonCount)
			commonCount = 0
		}
		result = appendDiffComponent(result, true, false, 1)
		newPos++
	}
	if commonCount > 0 {
		result = appendDiffComponent(result, false, false, commonCount)
	}

	// Note: the commonCount > 0 check below (line ~212) is provably unreachable.
	// Suffix stripping guarantees oldMid[last] ≠ newMid[last], so LCS never ends
	// at both ends, and the remaining deletion loop above always flushes commonCount.

	// Suffix
	if oldEnd < oldLen {
		result = appendDiffComponent(result, false, false, oldLen-oldEnd)
	}

	return result
}

// ComputePatch computes a line-level structured patch between old and new content.
// Returns nil if the content is too large so callers can fall back to character-level diff.
// Source: diff npm — tokenize + diffLines + structuredPatch, context=CONTEXT_LINES(3)
func ComputePatch(oldContent, newContent string) []DiffHunk {
	const ctxLines = 3

	oldLines := removeEmptyStrings(tokenizeLines(oldContent))
	newLines := removeEmptyStrings(tokenizeLines(newContent))

	if len(oldLines) == 0 && len(newLines) == 0 {
		return nil
	}

	if len(oldLines) > 0 && len(newLines) > 0 {
		if len(oldLines)*len(newLines) > maxDiffEntries {
			return nil // too large — caller should use diffmatchpatch fallback
		}
	}

	components := lineDiff(oldLines, newLines)
	return buildHunks(components, oldLines, newLines, ctxLines)
}

// lineEntry is a single line with its diff operation and line numbers.
type lineEntry struct {
	op   int // +1=added, -1=deleted, 0=equal
	line string
	oldN int // old file line number (1-based)
	newN int // new file line number (1-based)
}

// buildHunks converts diff components into structured hunks.
// Mirrors diffLinesResultToPatch in structuredPatch.js, context=3.
func buildHunks(components []diffComponent, oldLines, newLines []string, ctxLines int) []DiffHunk {
	var entries []lineEntry
	oldNum, newNum := 1, 1

	for _, c := range components {
		if c.added && !c.removed {
			for i := 0; i < c.count; i++ {
				if newNum-1 < len(newLines) {
					entries = append(entries, lineEntry{op: +1, line: newLines[newNum-1], oldN: oldNum, newN: newNum})
				}
				newNum++
			}
		} else if c.removed && !c.added {
			for i := 0; i < c.count; i++ {
				if oldNum-1 < len(oldLines) {
					entries = append(entries, lineEntry{op: -1, line: oldLines[oldNum-1], oldN: oldNum, newN: newNum})
				}
				oldNum++
			}
		} else {
			for i := 0; i < c.count; i++ {
				if oldNum-1 < len(oldLines) {
					entries = append(entries, lineEntry{op: 0, line: oldLines[oldNum-1], oldN: oldNum, newN: newNum})
				}
				oldNum++
				newNum++
			}
		}
	}

	// Strip trailing \n from each line (structuredPatch step 2)
	for i := range entries {
		entries[i].line = strings.TrimSuffix(entries[i].line, "\n")
	}

	var hunks []DiffHunk
	var curRange []lineEntry
	var oldRangeStart, newRangeStart int

	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if e.op != 0 {
			// Change line
			if len(curRange) == 0 {
				oldRangeStart = e.oldN
				newRangeStart = e.newN
				// Add trailing context from previous equal lines
				ctxStart := i - ctxLines
				if ctxStart < 0 {
					ctxStart = 0
				}
				for j := ctxStart; j < i; j++ {
					if entries[j].op == 0 {
						curRange = append(curRange, entries[j])
						oldRangeStart = entries[j].oldN
						newRangeStart = entries[j].newN
					}
				}
			}
			curRange = append(curRange, e)
		} else {
			// Equal line
			if len(curRange) > 0 {
				if i < len(entries)-1 && i < ctxLines*2 {
					// Close enough — add as context and continue
					curRange = append(curRange, e)
				} else {
					// End the hunk: add entries[i] and up to ctxLines-1 more as trailing context
					endCtx := ctxLines
					if endCtx > len(entries)-i {
						endCtx = len(entries) - i
					}
					for j := 0; j < endCtx; j++ {
						curRange = append(curRange, entries[i+j])
					}
					hunks = append(hunks, makeHunk(curRange, oldRangeStart, newRangeStart))
					curRange = nil
				}
			}
		}
	}

	// Flush remaining hunk
	if len(curRange) > 0 {
		hunks = append(hunks, makeHunk(curRange, oldRangeStart, newRangeStart))
	}

	return hunks
}

func makeHunk(lines []lineEntry, oldStart, newStart int) DiffHunk {
	hunkLines := make([]string, len(lines))
	oldCnt, newCnt := 0, 0
	for i, e := range lines {
		switch e.op {
		case -1:
			hunkLines[i] = "-" + e.line
			oldCnt++
		case +1:
			hunkLines[i] = "+" + e.line
			newCnt++
		default:
			hunkLines[i] = " " + e.line
			oldCnt++
			newCnt++
		}
	}
	return DiffHunk{
		OldStart: oldStart,
		OldLines: oldCnt,
		NewStart: newStart,
		NewLines: newCnt,
		Lines:    hunkLines,
	}
}

// ---------------------------------------------------------------------------
// Diff rendering — shared by Write/Edit tools
// Source: src/native-ts/color-diff/index.ts + src/components/StructuredDiff.tsx
// ---------------------------------------------------------------------------

// DiffHunk represents a single hunk in a unified diff.
type DiffHunk struct {
	OldStart int
	OldLines int
	NewStart int
	NewLines int
	Lines    []string // " text" = context, "+text" = added, "-text" = removed
}

// ANSI escape codes for diff rendering (256-color dark theme).
// Source: src/native-ts/color-diff/index.ts buildTheme() dark mode.
const (
	diffBold    = "\x1b[1m"
	diffBoldOff = "\x1b[22m"
	diffDim     = "\x1b[2m"
	diffReset   = "\x1b[0m"
	diffAddBg   = "\x1b[48;5;22m" // dark green bg for added lines (ansiIdx(22))
	diffAddFg   = "\x1b[38;5;10m" // green decoration for added marker/line#
	diffDelBg   = "\x1b[48;5;52m" // dark red bg for deleted lines
	diffDelFg   = "\x1b[38;5;9m"  // red decoration for deleted marker/line#
	diffDimFg = "\x1b[38;5;246m" // dim gray for context line numbers
)

// CountLines counts lines in content.
// Trailing newline is a terminator, not an extra line.
// Source: FileWriteTool/UI.tsx — countLines()
func CountLines(content string) int {
	if content == "" {
		return 0
	}
	n := strings.Count(content, "\n")
	if strings.HasSuffix(content, "\n") {
		return n
	}
	return n + 1
}

// CountPatchChanges counts added and removed lines across all hunks.
// Source: FileEditToolUpdatedMessage.tsx:32-33
func CountPatchChanges(hunks []DiffHunk) (added, removed int) {
	for _, hunk := range hunks {
		for _, line := range hunk.Lines {
			if len(line) > 0 {
				switch line[0] {
				case '+':
					added++
				case '-':
					removed++
				}
			}
		}
	}
	return
}

// FormatDiffSummary returns a summary line like "Added 3 lines, removed 2 lines".
// Source: FileEditToolUpdatedMessage.tsx:36-54
func FormatDiffSummary(added, removed int) string {
	var sb strings.Builder
	if added > 0 {
		fmt.Fprintf(&sb, "Added %s%d%s %s", diffBold, added, diffBoldOff, pluralWord(added, "line"))
	}
	if added > 0 && removed > 0 {
		sb.WriteString(", ")
	}
	if removed > 0 {
		prefix := "Removed"
		if added > 0 {
			prefix = "removed"
		}
		fmt.Fprintf(&sb, "%s %s%d%s %s", prefix, diffBold, removed, diffBoldOff, pluralWord(removed, "line"))
	}
	return sb.String()
}

// RenderDiff renders hunks as ANSI-colored unified diff.
// Source: src/native-ts/color-diff/index.ts render() + src/components/StructuredDiff.tsx
//
// Format per line: " NNN + content" (added) / " NNN - content" (deleted) / " NNN   content" (context)
// Gutter width = maxLineNumber digits + 3 (space + paddedNum + space + marker).
// Hunks separated by dim "...".
func RenderDiff(hunks []DiffHunk) string {
	if len(hunks) == 0 {
		return ""
	}

	// Compute max line number across all hunks for gutter width.
	// Source: StructuredDiff.tsx computeGutterWidth()
	maxLineNum := 1
	for _, hunk := range hunks {
		oldEnd := hunk.OldStart + hunk.OldLines - 1
		newEnd := hunk.NewStart + hunk.NewLines - 1
		if oldEnd > maxLineNum {
			maxLineNum = oldEnd
		}
		if newEnd > maxLineNum {
			maxLineNum = newEnd
		}
	}
	maxDigits := len(strconv.Itoa(maxLineNum))

	var sb strings.Builder
	for hi, hunk := range hunks {
		// Hunk separator
		if hi > 0 {
			sb.WriteString(diffReset)
			sb.WriteString(diffDim)
			sb.WriteString("...")
			sb.WriteString(diffReset)
			sb.WriteByte('\n')
		}

		oldLine := hunk.OldStart
		newLine := hunk.NewStart

		for _, line := range hunk.Lines {
			if len(line) == 0 {
				continue
			}
			marker := line[0]
			content := line[1:]

			// Track line numbers.
			// Source: color-diff/index.ts render() — lineNumber logic
			var lineNum int
			switch marker {
			case '+':
				lineNum = newLine
				newLine++
			case '-':
				lineNum = oldLine
				oldLine++
			default: // context
				lineNum = newLine
				oldLine++
				newLine++
			}

			paddedNum := fmt.Sprintf("%*d", maxDigits, lineNum)

			sb.WriteString(diffReset)
			switch marker {
			case '+':
				sb.WriteString(diffAddFg)
				sb.WriteString(diffAddBg)
				fmt.Fprintf(&sb, " %s ", paddedNum)
				sb.WriteString(diffAddFg)
				sb.WriteByte('+')
				sb.WriteString(diffReset)
				sb.WriteString(diffAddBg)
				sb.WriteString(content)
			case '-':
				sb.WriteString(diffDelFg)
				sb.WriteString(diffDelBg)
				fmt.Fprintf(&sb, " %s ", paddedNum)
				sb.WriteString(diffDelFg)
				sb.WriteByte('-')
				sb.WriteString(diffReset)
				sb.WriteString(diffDim)
				sb.WriteString(diffDelBg)
				sb.WriteString(content)
			default: // context
				sb.WriteString(diffDimFg)
				fmt.Fprintf(&sb, " %s  ", paddedNum)
				sb.WriteString(diffReset)
				sb.WriteString(content)
			}
			sb.WriteString(diffReset)
			sb.WriteByte('\n')
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}

// MaxDiffLinesToRender is the maximum number of diff lines to show before truncating.
// Source: FileWriteTool/UI.tsx — MAX_LINES_TO_RENDER = 10; we use 15 for diffs
// since diffs have line-number gutters that take more space.
const MaxDiffLinesToRender = 15

// FormatMoreLines returns a dim "… +N lines" indicator for truncated output.
// Source: FileWriteTool/UI.tsx — "… +{plusLines} {line|lines} {CtrlOToExpand}"
func FormatMoreLines(n int) string {
	word := "lines"
	if n == 1 {
		word = "line"
	}
	return diffDim + fmt.Sprintf("… +%d %s (ctrl+o to expand)", n, word) + diffReset
}

// TruncateStringLines truncates a multi-line string to maxLines,
// appending FormatMoreLines(n) if lines were hidden.
func TruncateStringLines(s string, maxLines int) string {
	if s == "" || maxLines <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	hidden := len(lines) - maxLines
	return strings.Join(lines[:maxLines], "\n") + "\n" + FormatMoreLines(hidden)
}

// pluralWord returns "word" or "words" based on count.
func pluralWord(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
