package tool

import (
	"fmt"
	"strconv"
	"strings"

	"znkr.io/diff"
)

// ---------------------------------------------------------------------------
// Line-level diff — equivalent to diff npm's diffLines + structuredPatch.
// Source: node_modules/diff/lib/structuredPatch.js + libcjs/diff/line.js + libcjs/diff/base.js
// Uses znkr.io/diff's Myers-based line diff with heuristics.
// ---------------------------------------------------------------------------

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

// ComputePatch computes a line-level structured patch between old and new content.
// Delegates hunk generation (context, merging, splitting) to znkr.io/diff.Hunks.
func ComputePatch(oldContent, newContent string) []DiffHunk {
	oldLines := removeEmptyStrings(tokenizeLines(oldContent))
	newLines := removeEmptyStrings(tokenizeLines(newContent))

	if len(oldLines) == 0 && len(newLines) == 0 {
		return nil
	}

	var hunks []DiffHunk
	for _, h := range diff.Hunks(oldLines, newLines) {
		var lines []string
		oldCnt, newCnt := 0, 0
		for _, e := range h.Edits {
			switch e.Op {
			case diff.Match:
				lines = append(lines, " "+strings.TrimSuffix(e.X, "\n"))
				oldCnt++
				newCnt++
			case diff.Delete:
				lines = append(lines, "-"+strings.TrimSuffix(e.X, "\n"))
				oldCnt++
			case diff.Insert:
				lines = append(lines, "+"+strings.TrimSuffix(e.Y, "\n"))
				newCnt++
			}
		}
		hunks = append(hunks, DiffHunk{
			OldStart: h.PosX + 1,
			OldLines: oldCnt,
			NewStart: h.PosY + 1,
			NewLines: newCnt,
			Lines:    lines,
		})
	}
	return hunks
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
	diffDimFg   = "\x1b[38;5;246m"
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
		fmt.Fprintf(&sb, "Added %s%d%s %s", diffBold, added, diffBoldOff, PluralWord(added, "lines"))
	}
	if added > 0 && removed > 0 {
		sb.WriteString(", ")
	}
	if removed > 0 {
		prefix := "Removed"
		if added > 0 {
			prefix = "removed"
		}
		fmt.Fprintf(&sb, "%s %s%d%s %s", prefix, diffBold, removed, diffBoldOff, PluralWord(removed, "lines"))
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
		if hi > 0 {
			sb.WriteString("...\n")
		}

		oldLine := hunk.OldStart
		newLine := hunk.NewStart

		for _, line := range hunk.Lines {
			if len(line) == 0 {
				continue
			}
			marker := line[0]
			content := line[1:]

			var lineNum int
			switch marker {
			case '+':
				lineNum = newLine
				newLine++
			case '-':
				lineNum = oldLine
				oldLine++
			default:
				lineNum = newLine
				oldLine++
				newLine++
			}

			paddedNum := fmt.Sprintf("%*d", maxDigits, lineNum)
			switch marker {
			case '+':
				fmt.Fprintf(&sb, " %s +%s", paddedNum, content)
			case '-':
				fmt.Fprintf(&sb, " %s -%s", paddedNum, content)
			default:
				fmt.Fprintf(&sb, " %s  %s", paddedNum, content)
			}
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
// PluralWord returns the singular form when n==1 by stripping the trailing "s"
// from word. Otherwise returns word as-is. Callers pass the plural form
// (e.g. "files", "matches") and the function trims it for n==1.
func PluralWord(n int, word string) string {
	if n == 1 {
		return strings.TrimSuffix(word, "s")
	}
	return word
}

// RenderContentWithLineNumbers renders content with dim line numbers,
// matching the context-line style from RenderDiff (dim gray gutter + content).
// Used by Write tool's create path to show file content with line numbers.
func RenderContentWithLineNumbers(content string) string {
	if content == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	maxDigits := len(strconv.Itoa(len(lines)))

	var sb strings.Builder
	for i, line := range lines {
		paddedNum := fmt.Sprintf("%*d", maxDigits, i+1)
		sb.WriteString(diffDimFg)
		fmt.Fprintf(&sb, " %s  ", paddedNum)
		sb.WriteString(diffReset)
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}
