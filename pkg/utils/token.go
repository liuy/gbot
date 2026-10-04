package utils

import (
	"fmt"
	"strings"
)

// Token estimation via a 6-feature linear model (TokenFeatures below).
// Display-side call sites apply the shared seed ratios (SeedTokenRatios)
// directly; pkg/engine's calibration layer starts from the same seeds and
// refits them online per model from real API usage.

// SeedTokenRatios holds the seed ratios of the 6-feature linear model in
// the feature order [CJK, Struct, Digit, Indent, Words, Other] — the field
// order of TokenFeatures.
//
// Measured 2026-10-04 against three tokenizers (qwen/Strata, zhipu glm,
// deepseek) by least-squares fit over live prompts: the 6-feature model
// lands 5-10% median error on held-out samples where the 2-feature heuristic
// is off 25-29%. The fitted ratios were stable across the three models, so
// one seed table serves every provider until per-model observations
// accumulate.
var SeedTokenRatios = [6]float64{0.72, 1.38, 1.34, -0.04, 0.48, 0.11}

// EstimateTokens returns the seed-model token estimate for text. The sum can
// go negative (Indent's ratio is negative), so the result is floored at 0.
func EstimateTokens(text string) int {
	f := CountTokenFeatures(text)
	v := float64(f.CJK)*SeedTokenRatios[0] +
		float64(f.Struct)*SeedTokenRatios[1] +
		float64(f.Digit)*SeedTokenRatios[2] +
		float64(f.Indent)*SeedTokenRatios[3] +
		float64(f.Words)*SeedTokenRatios[4] +
		float64(f.Other)*SeedTokenRatios[5]
	return max(int(v), 0)
}

func isCJKRune(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) ||
		(r >= 0x3400 && r <= 0x4DBF) ||
		(r >= 0x3000 && r <= 0x303F) ||
		(r >= 0xFF00 && r <= 0xFFEF) ||
		(r >= 0x30A0 && r <= 0x30FF) ||
		(r >= 0x2E80 && r <= 0x2EFF) ||
		(r >= 0x31C0 && r <= 0x31EF) ||
		(r >= 0x3200 && r <= 0x32FF) ||
		(r >= 0x3300 && r <= 0x33FF) ||
		(r >= 0xAC00 && r <= 0xD7AF) ||
		(r >= 0x1100 && r <= 0x11FF) ||
		(r >= 0x3130 && r <= 0x318F) ||
		(r >= 0xA960 && r <= 0xA97F) ||
		(r >= 0xD7B0 && r <= 0xD7FF)
}

// TokenFeatures is a 6-feature characterization of a text consumed by
// EstimateTokens below and by the engine's calibrated estimator
// (pkg/engine/token_calibration.go).
//
// The char counts form a disjoint partition: every rune of the input is
// counted by exactly one of CJK, Struct, Digit, Indent, Other. Indent holds
// only leading (per-line) space/tab chars; mid-line whitespace and newlines
// fall into Other because they belong to no char class. Words is an
// independent feature (whitespace-separated tokens) and deliberately may
// overlap the char classes.
type TokenFeatures struct {
	CJK    int // chars in the isCJKRune ranges
	Struct int // chars in isStructRune (punctuation-heavy code/JSON chars)
	Digit  int // chars '0'-'9'
	Indent int // leading whitespace (spaces+tabs) per line, summed
	Words  int // whitespace-separated tokens (len(strings.Fields(s)))
	Other  int // total chars - CJK - Struct - Digit - Indent
}

// CountTokenFeatures extracts the 6-feature vector from s.
func CountTokenFeatures(s string) TokenFeatures {
	var f TokenFeatures
	atLineStart := true
	for _, r := range s {
		switch {
		case r == '\n':
			// Newline belongs to no char class → Other; it also opens the
			// next line's indent run.
			atLineStart = true
			f.Other++
		case atLineStart && (r == ' ' || r == '\t'):
			f.Indent++
		default:
			// Any non-indent char (including mid-line whitespace) ends the
			// line's leading run.
			atLineStart = false
			switch {
			case isCJKRune(r):
				f.CJK++
			case isStructRune(r):
				f.Struct++
			case r >= '0' && r <= '9':
				f.Digit++
			default:
				f.Other++
			}
		}
	}
	f.Words = len(strings.Fields(s))
	return f
}

// isStructRune reports whether r is a punctuation char typical of code and
// JSON — the text that dominates agent tool output and is worst-served by a
// plain chars-per-token heuristic.
func isStructRune(r rune) bool {
	switch r {
	case '{', '}', '[', ']', '(', ')', '<', '>', '=', ';', ':', '"', '\'',
		'+', '/', '&', '|', '@', '#', '$', '`', '~', '^', '\\':
		return true
	}
	return false
}

// FormatTokenCount formats a token count with K/M/G suffixes.
// <1000: as-is, >=1K: "1.2k", >1M: "1.2M", >1G: "1.2G".
// Uses 1024 as the base.
func FormatTokenCount(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1fk", float64(n)/1024)
	}
	if n < 1024*1024*1024 {
		return fmt.Sprintf("%.1fM", float64(n)/(1024*1024))
	}
	return fmt.Sprintf("%.1fG", float64(n)/(1024*1024*1024))
}
