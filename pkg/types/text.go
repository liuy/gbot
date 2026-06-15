// Package types defines shared types for gbot.
package types

import (
	"fmt"
	"math"
	"regexp"
	"unicode/utf8"
)

// Token estimation ported 1:1 from johannschopplich/tokenx.

const (
	defaultCharsPerToken = 6
	shortTokenThreshold  = 3
)

// Tokenx upstream punctuation class — character set is part of the
// algorithm contract; do not add or remove chars without verifying
// against johannschopplich/tokenx test/index.test.ts.
const punctClass = `.,!?;(){}[]<>:/\\|@#$%^&*+=` + "`" + `~_-`

// punctSet avoids RE2 character-class issues with * + ?.
var punctSet = func() map[rune]struct{} {
	m := make(map[rune]struct{}, len(punctClass))
	for _, r := range punctClass {
		m[r] = struct{}{}
	}
	return m
}()

var (
	patternWhitespace = regexp.MustCompile(`^[\p{Z}\t\n\v\f\r\x{FEFF}]+$`)
	patternNumeric    = regexp.MustCompile(`^\d+(?:[.,]\d+)*$`)
	patternAlphanumeric = regexp.MustCompile(`^[a-zA-Z0-9À-ÖØ-öø-ÿ]+$`)

	// Uses \p{Z}+\t\n\v\f\r+\x{FEFF} to match JS \s (Go \s is ASCII-only).
	patternSplit = regexp.MustCompile(`([\p{Z}\t\n\v\f\r\x{FEFF}]+|[-.,:!?;(){}\[\]<>/\\|@#$%^\&*+=` + "`" + `~_]+)`)
	// CJK: substring test — a segment with any CJK rune counts 1 token/char.
	patternCJK = regexp.MustCompile(`[\x{4E00}-\x{9FFF}\x{3400}-\x{4DBF}\x{3000}-\x{303F}\x{FF00}-\x{FFEF}\x{30A0}-\x{30FF}\x{2E80}-\x{2EFF}\x{31C0}-\x{31EF}\x{3200}-\x{32FF}\x{3300}-\x{33FF}\x{AC00}-\x{D7AF}\x{1100}-\x{11FF}\x{3130}-\x{318F}\x{A960}-\x{A97F}\x{D7B0}-\x{D7FF}]`)

	patternGerman        = regexp.MustCompile(`(?i)[äöüßẞ]`)
	patternFrenchSpanish = regexp.MustCompile(`(?i)[éèêëàâîïôûùüÿçœæáíóúñ]`)
	patternSlavic        = regexp.MustCompile(`(?i)[ąćęłńóśźżěščřžýůúďťň]`)
)

// EstimateTokens returns a heuristic token count. Not for billing.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}

	segments := splitSegments(text)
	total := 0
	for _, seg := range segments {
		total += estimateSegmentTokens(seg)
	}
	return total
}

// splitSegments mirrors JS String.prototype.split with a capturing group.
// Go's regexp.Split drops capture-group matches, so we hand-roll it here.
func splitSegments(text string) []string {
	matches := patternSplit.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return []string{text}
	}
	out := make([]string, 0, len(matches)*2+1)
	prev := 0
	for _, m := range matches {
		if m[0] > prev {
			out = append(out, text[prev:m[0]])
		}
		out = append(out, text[m[0]:m[1]])
		prev = m[1]
	}
	if prev < len(text) {
		out = append(out, text[prev:])
	}
	filtered := out[:0]
	for _, s := range out {
		if s != "" {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

func estimateSegmentTokens(seg string) int {
	if seg == "" {
		return 0
	}
	if patternWhitespace.MatchString(seg) {
		return 0
	}
	if patternCJK.MatchString(seg) {
		return utf8.RuneCountInString(seg)
	}
	if patternNumeric.MatchString(seg) {
		return 1
	}
	if utf8.RuneCountInString(seg) <= shortTokenThreshold {
		return 1
	}
	if isAllPunct(seg) {
		if utf8.RuneCountInString(seg) > 1 {
			return (utf8.RuneCountInString(seg) + 1) / 2
		}
		return 1
	}
	if patternAlphanumeric.MatchString(seg) {
		cpt := getLanguageSpecificCharsPerToken(seg)
		if cpt == 0 {
			cpt = float64(defaultCharsPerToken)
		}
		// ceil(runeCount / cpt) — matches tokenx Math.ceil(segment.length / charsPerToken).
		runeCount := utf8.RuneCountInString(seg)
		return int(math.Ceil(float64(runeCount) / cpt))
	}
	runeCount := utf8.RuneCountInString(seg)
	return int(math.Ceil(float64(runeCount) / defaultCharsPerToken))
}

// isAllPunct reports whether seg consists entirely of tokenx punctuation chars.
func isAllPunct(seg string) bool {
	for _, r := range seg {
		if _, ok := punctSet[r]; !ok {
			return false
		}
	}
	return true
}

// getLanguageSpecificCharsPerToken returns a chars/token ratio for diacritic-rich
// segments (German/French/Spanish=3, Slavic=3.5), else 0 (use default=6).
func getLanguageSpecificCharsPerToken(seg string) float64 {
	switch {
	case patternGerman.MatchString(seg):
		return 3
	case patternFrenchSpanish.MatchString(seg):
		return 3
	case patternSlavic.MatchString(seg):
		return 3.5
	}
	return 0
}

// IsCJK reports whether r is a CJK character (Chinese, Japanese, Korean).
// Kept for callers that need per-rune classification (e.g. status bar).
func IsCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified Ideographs
		(r >= 0x3400 && r <= 0x4DBF) || // CJK Extension A
		(r >= 0x20000 && r <= 0x2A6DF) || // CJK Extension B
		(r >= 0x2A700 && r <= 0x2B73F) || // CJK Extension C
		(r >= 0x2B740 && r <= 0x2B81F) || // CJK Extension D
		(r >= 0x2B820 && r <= 0x2CEAF) || // CJK Extension E
		(r >= 0x2CEB0 && r <= 0x2EBEF) || // CJK Extension F
		(r >= 0x30000 && r <= 0x3134F) || // CJK Extension G
		(r >= 0x3040 && r <= 0x309F) || // Hiragana
		(r >= 0x30A0 && r <= 0x30FF) || // Katakana
		(r >= 0xAC00 && r <= 0xD7AF) || // Hangul Syllables
		(r >= 0x1100 && r <= 0x11FF) || // Hangul Jamo
		(r >= 0x3130 && r <= 0x318F) || // Hangul Compatibility Jamo
		(r >= 0x3000 && r <= 0x303F) || // CJK Symbols and Punctuation
		(r >= 0xFF00 && r <= 0xFFEF) || // Halfwidth and Fullwidth Forms
		(r >= 0x2E80 && r <= 0x2EFF) || // CJK Radicals Supplement
		(r >= 0x31C0 && r <= 0x31EF) || // CJK Strokes
		(r >= 0x3200 && r <= 0x32FF) || // Enclosed CJK Letters and Months
		(r >= 0x3300 && r <= 0x33FF) || // CJK Compatibility
		(r >= 0xA960 && r <= 0xA97F) || // Hangul Jamo Extended-A
		(r >= 0xD7B0 && r <= 0xD7FF) // Hangul Jamo Extended-B
}

// FormatTokenCount formats a token count with K/M/G suffixes.
// <1000: as-is, >=1K: "1.2k", >=1M: "1.2M", >=1G: "1.2G".
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
