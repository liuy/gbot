package fileedit

import (
	"fmt"
	"strings"
)

// findNearbyHint searches fileContent for the region most similar to oldString
// and returns surrounding lines as a hint for the LLM to self-correct.
// Returns empty string if no meaningful match is found.
func findNearbyHint(fileContent, oldString string) string {
	fileLines := strings.Split(fileContent, "\n")
	oldLines := strings.Split(oldString, "\n")

	// Collect meaningful lines from old_string (skip short/generic ones).
	var meaningful []string
	for _, line := range oldLines {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > 5 && !isGeneric(trimmed) {
			meaningful = append(meaningful, trimmed)
		}
	}
	if len(meaningful) == 0 {
		return ""
	}

	// Tokenize each meaningful line once.
	oldTokenSets := make([][]string, len(meaningful))
	for i, m := range meaningful {
		oldTokenSets[i] = strings.Fields(m)
	}

	// Find the file line with highest total token overlap against
	// any meaningful old line.
	bestIdx := -1
	bestScore := 0
	for i, fLine := range fileLines {
		trimmed := strings.TrimSpace(fLine)
		if len(trimmed) == 0 {
			continue
		}
		fileTokens := strings.Fields(trimmed)
		if len(fileTokens) == 0 {
			continue
		}

		score := 0
		for _, oldTokens := range oldTokenSets {
			score += tokenOverlapScore(fileTokens, oldTokens)
		}
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}

	if bestIdx < 0 || bestScore == 0 {
		return ""
	}

	// Return ±contextLines around the best match with line numbers.
	const contextLines = 5
	start := bestIdx - contextLines
	if start < 0 {
		start = 0
	}
	end := bestIdx + contextLines + 1
	if end > len(fileLines) {
		end = len(fileLines)
	}

	var buf strings.Builder
	for i := start; i < end; i++ {
		fmt.Fprintf(&buf, "%d: %s\n", i+1, fileLines[i])
	}
	return buf.String()
}

func isGeneric(s string) bool {
	switch s {
	case "}", "{", ")", "(", "]", "[", "end", "return", "else", "break":
		return true
	}
	return len(s) <= 3
}

// tokenOverlapScore counts how many tokens from b appear in a.
func tokenOverlapScore(a, b []string) int {
	score := 0
	for _, bt := range b {
		for _, at := range a {
			if at == bt {
				score++
				break
			}
		}
	}
	return score
}
