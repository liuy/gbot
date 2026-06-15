// Package types defines shared types for gbot.
package types

import (
	"fmt"
	"math"
)

// Token estimation ported 1:1 from johannschopplich/tokenx.

const (
	defaultCharsPerToken = 6
	shortTokenThreshold  = 3
)

// EstimateTokens returns a heuristic token count. Not for billing.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}

	total := 0
	for _, seg := range splitSegments(text) {
		total += estimateSegmentTokens(seg)
	}
	return total
}

// splitSegments splits text into word runs and separator runs (whitespace
// or punctuation), mirroring JS String.prototype.split with a capturing group.
func splitSegments(text string) []string {
	runes := []rune(text)
	var segs []string
	i := 0
	for i < len(runes) {
		r := runes[i]
		if isWS(r) || isPunctRune(r) {
			wp := isWS(r)
			j := i + 1
			for j < len(runes) {
				r2 := runes[j]
				if wp && !isWS(r2) { break }
				if !wp && !isPunctRune(r2) { break }
				j++
			}
			segs = append(segs, string(runes[i:j]))
			i = j
		} else {
			j := i + 1
			for j < len(runes) {
				r2 := runes[j]
				if isWS(r2) || isPunctRune(r2) { break }
				j++
			}
			segs = append(segs, string(runes[i:j]))
			i = j
		}
	}
	return segs
}

func estimateSegmentTokens(seg string) int {
	if seg == "" {
		return 0
	}

	runeCount := 0
	allWhitespace := true
	allNumeric := true
	allAlpha := true
	hasCJK := false
	allPunct := true

	for _, r := range seg {
		runeCount++
		if isWS(r) {
			allAlpha = false
			allNumeric = false
			allPunct = false
			continue
		}
		allWhitespace = false
		if isCJKRune(r) {
			hasCJK = true
			allAlpha = false
			allNumeric = false
			allPunct = false
			continue
		}
		if r >= '0' && r <= '9' {
			allAlpha = false
			allPunct = false
			continue
		}
		if r == '.' || r == ',' {
			allAlpha = false
			allPunct = false
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '\u00C0' && r <= '\u00D6') || (r >= '\u00D8' && r <= '\u00F6') ||
			(r >= '\u00F8' && r <= '\u00FF') {
			allNumeric = false
			allPunct = false
			continue
		}
		allNumeric = false
		allAlpha = false
		if !isPunctRune(r) {
			allPunct = false
		}
	}

	if allWhitespace && runeCount > 0 {
		return 0
	}
	if hasCJK {
		return runeCount
	}
	if allNumeric && runeCount > 0 {
		return 1
	}
	if runeCount <= shortTokenThreshold {
		return 1
	}
	if allPunct {
		return (runeCount + 1) / 2
	}
	if allAlpha {
		cpt := getLanguageSpecificCharsPerToken(seg)
		if cpt == 0 {
			cpt = float64(defaultCharsPerToken)
		}
		return int(math.Ceil(float64(runeCount) / cpt))
	}
	return int(math.Ceil(float64(runeCount) / defaultCharsPerToken))
}

func isWS(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r' ||
		r == '\u00a0' || r == '\u3000' || r == '\uFEFF' ||
		(r >= '\u2000' && r <= '\u200a') ||
		r == '\u2028' || r == '\u2029' ||
		r == '\u1680' || r == '\u205f' || r == '\u0085'
}

func isPunctRune(r rune) bool {
	switch r {
	case '.', ',', '!', '?', ';', '(', ')', '{', '}', '[', ']',
		'<', '>', '/', '\\', '|', '@', '#', '$', '%', '^',
		'&', '*', '+', '=', '`', '~', '-', '_', ':':
		return true
	}
	return false
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

func getLanguageSpecificCharsPerToken(seg string) float64 {
	for _, r := range seg {
		switch {
		case r == 'ä' || r == 'ö' || r == 'ü' || r == 'ß' || r == 'Ä' || r == 'Ö' || r == 'Ü' || r == 'ẞ':
			return 3
		case r == 'é' || r == 'è' || r == 'ê' || r == 'ë' || r == 'à' || r == 'â' ||
			r == 'î' || r == 'ï' || r == 'ô' || r == 'û' || r == 'ù' || r == 'ü' ||
			r == 'ÿ' || r == 'ç' || r == 'œ' || r == 'æ' || r == 'á' || r == 'í' ||
			r == 'ó' || r == 'ú' || r == 'ñ':
			return 3
		case r == 'ą' || r == 'ć' || r == 'ę' || r == 'ł' || r == 'ń' || r == 'ó' ||
			r == 'ś' || r == 'ź' || r == 'ż' || r == 'ě' || r == 'š' || r == 'č' ||
			r == 'ř' || r == 'ž' || r == 'ý' || r == 'ů' || r == 'ú' || r == 'ď' ||
			r == 'ť' || r == 'ň':
			return 3.5
		}
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
