package utils

import (
	"fmt"
	"strings"
)

// Token estimation calibrated against real tokenizer APIs.
//
// CJK ratio differs by provider (calibrated via /paas/v4/tokenizer for GLM,
// and via prompt_tokens for DeepSeek/MiMo):
//
//	GLM (zhipu):   CJK 0.85 tokens/char
//	DeepSeek:      CJK 0.50 tokens/char
//	MiMo (xiaomi): CJK 0.50 tokens/char
//
// non-CJK is stable at ~0.20 across all providers.
// Average error: <10% per provider when using the correct ratio.

const (
	defaultCJKTokensPerChar    = 0.65 // fallback for unknown providers
	defaultNonCJKTokensPerChar = 0.20
)

// CJKTokensPerChar returns the CJK token ratio for a given provider.
// Calibrated against real tokenizer APIs. Returns a conservative default
// for unknown providers.
func CJKTokensPerChar(provider string) float64 {
	switch provider {
	case "zhipu":
		return 0.85
	case "deepseek":
		return 0.50
	case "xiaomi":
		return 0.50
	default:
		return defaultCJKTokensPerChar
	}
}

// EstimateTokens returns a heuristic token count using the default CJK ratio.
// Prefer EstimateTokensForProvider when the provider is known.
func EstimateTokens(text string) int {
	return EstimateTokensForProvider(text, "")
}

// EstimateTokensForProvider returns a heuristic token count calibrated for
// the given provider's tokenizer.
func EstimateTokensForProvider(text string, provider string) int {
	if text == "" {
		return 0
	}
	cjkRatio := CJKTokensPerChar(provider)
	cjk := 0
	for _, r := range text {
		if isCJKRune(r) {
			cjk++
		}
	}
	nonCJK := len([]rune(text)) - cjk
	return int(float64(cjk)*cjkRatio + float64(nonCJK)*defaultNonCJKTokensPerChar)
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

// TokenFeatures is a 6-feature characterization of a text used by the
// engine's calibrated token estimator (pkg/engine/token_calibration.go).
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
