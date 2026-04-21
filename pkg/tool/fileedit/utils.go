package fileedit

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	LeftSingleCurlyQuote  = '\u2018' // '
	RightSingleCurlyQuote = '\u2019' // '
	LeftDoubleCurlyQuote  = '\u201C' // "
	RightDoubleCurlyQuote = '\u201D' // "
)

func NormalizeQuotes(str string) string {
	var b strings.Builder
	b.Grow(len(str))
	for _, r := range str {
		switch r {
		case LeftSingleCurlyQuote, RightSingleCurlyQuote:
			b.WriteByte('\'')
		case LeftDoubleCurlyQuote, RightDoubleCurlyQuote:
			b.WriteByte('"')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func FindActualString(fileContent, searchString string) (string, bool) {
	// First try exact match
	if strings.Contains(fileContent, searchString) {
		return searchString, true
	}

	// Try with normalized quotes. Must use rune-level indexing because
	// curly quotes (3 bytes UTF-8) normalize to straight quotes (1 byte),
	// so byte offsets differ between normalized and original content.
	normalizedSearch := NormalizeQuotes(searchString)
	normalizedFile := NormalizeQuotes(fileContent)

	before, _, ok := strings.Cut(normalizedFile, normalizedSearch)
	if ok {
		// Convert byte offset in normalized → rune offset
		runeIdx := utf8.RuneCountInString(before)
		searchRuneLen := utf8.RuneCountInString(searchString)

		fileRunes := []rune(fileContent)
		return string(fileRunes[runeIdx : runeIdx+searchRuneLen]), true
	}

	return "", false
}

func PreserveQuoteStyle(oldString, actualOldString, newString string) string {
	// If they're the same, no normalization happened
	if oldString == actualOldString {
		return newString
	}

	// Detect which curly quote types were in the file
	hasDoubleQuotes := strings.ContainsRune(actualOldString, LeftDoubleCurlyQuote) ||
		strings.ContainsRune(actualOldString, RightDoubleCurlyQuote)
	hasSingleQuotes := strings.ContainsRune(actualOldString, LeftSingleCurlyQuote) ||
		strings.ContainsRune(actualOldString, RightSingleCurlyQuote)

	if !hasDoubleQuotes && !hasSingleQuotes {
		return newString
	}

	result := newString
	if hasDoubleQuotes {
		result = applyCurlyDoubleQuotes(result)
	}
	if hasSingleQuotes {
		result = applyCurlySingleQuotes(result)
	}

	return result
}

func isOpeningContext(chars []rune, index int) bool {
	if index == 0 {
		return true
	}
	prev := chars[index-1]
	switch prev {
	case ' ', '\t', '\n', '\r', '(', '[', '{', '\u2014', '\u2013': // em dash, en dash
		return true
	}
	return false
}

func applyCurlyDoubleQuotes(str string) string {
	chars := []rune(str)
	var b strings.Builder
	b.Grow(len(chars))
	for i, ch := range chars {
		if ch == '"' {
			if isOpeningContext(chars, i) {
				b.WriteRune(LeftDoubleCurlyQuote)
			} else {
				b.WriteRune(RightDoubleCurlyQuote)
			}
		} else {
			b.WriteRune(ch)
		}
	}
	return b.String()
}

func applyCurlySingleQuotes(str string) string {
	chars := []rune(str)
	var b strings.Builder
	b.Grow(len(chars))
	for i, ch := range chars {
		if ch == '\'' {
			// Don't convert apostrophes in contractions
			var prev, next rune
			if i > 0 {
				prev = chars[i-1]
			}
			if i < len(chars)-1 {
				next = chars[i+1]
			}
			prevIsLetter := prev != 0 && unicode.IsLetter(prev)
			nextIsLetter := next != 0 && unicode.IsLetter(next)
			if prevIsLetter && nextIsLetter {
				// Apostrophe in a contraction
				b.WriteRune(RightSingleCurlyQuote)
			} else if isOpeningContext(chars, i) {
				b.WriteRune(LeftSingleCurlyQuote)
			} else {
				b.WriteRune(RightSingleCurlyQuote)
			}
		} else {
			b.WriteRune(ch)
		}
	}
	return b.String()
}

func ApplyEditToFile(originalContent, oldString, newString string, replaceAll bool) string {
	var doReplace func(content, search, replace string) string
	if replaceAll {
		doReplace = func(content, search, replace string) string {
			return strings.ReplaceAll(content, search, replace)
		}
	} else {
		doReplace = func(content, search, replace string) string {
			return strings.Replace(content, search, replace, 1)
		}
	}

	if newString != "" {
		return doReplace(originalContent, oldString, newString)
	}

	// newString is empty — deletion
	stripTrailingNewline := !strings.HasSuffix(oldString, "\n") &&
		strings.Contains(originalContent, oldString+"\n")

	if stripTrailingNewline {
		return doReplace(originalContent, oldString+"\n", newString)
	}
	return doReplace(originalContent, oldString, newString)
}
