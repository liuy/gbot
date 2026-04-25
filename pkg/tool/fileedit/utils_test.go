package fileedit

import (
	"strings"
	"testing"
)

func TestNormalizeQuotes_NoCurly(t *testing.T) {
	t.Parallel()
	got := NormalizeQuotes("hello 'world' \"test\"")
	if got != "hello 'world' \"test\"" {
		t.Errorf("NormalizeQuotes = %q, want unchanged", got)
	}
}

func TestNormalizeQuotes_CurlyDouble(t *testing.T) {
	t.Parallel()
	got := NormalizeQuotes("\u201CHello\u201D")
	if got != `"Hello"` {
		t.Errorf("NormalizeQuotes = %q, want %q", got, `"Hello"`)
	}
}

func TestNormalizeQuotes_CurlySingle(t *testing.T) {
	t.Parallel()
	got := NormalizeQuotes("\u2018Hello\u2019")
	if got != "'Hello'" {
		t.Errorf("NormalizeQuotes = %q, want %q", got, "'Hello'")
	}
}

func TestNormalizeQuotes_Mixed(t *testing.T) {
	t.Parallel()
	input := "\u201CShe said \u2018hello\u2019\u201D"
	got := NormalizeQuotes(input)
	want := `"She said 'hello'"`
	if got != want {
		t.Errorf("NormalizeQuotes = %q, want %q", got, want)
	}
}

func TestNormalizeQuotes_Empty(t *testing.T) {
	t.Parallel()
	got := NormalizeQuotes("")
	if got != "" {
		t.Errorf("NormalizeQuotes = %q, want empty", got)
	}
}

func TestFindActualString_ExactMatch(t *testing.T) {
	t.Parallel()
	fileContent := "hello world"
	got, ok := FindActualString(fileContent, "hello")
	if !ok {
		t.Fatal("FindActualString ok = false, want true")
	}
	if got != "hello" {
		t.Errorf("FindActualString = %q, want %q", got, "hello")
	}
}

func TestFindActualString_CurlyQuoteMatch(t *testing.T) {
	t.Parallel()
	fileContent := "\u201CHello World\u201D"
	got, ok := FindActualString(fileContent, `"Hello World"`)
	if !ok {
		t.Fatal("FindActualString ok = false, want true")
	}
	if got != "\u201CHello World\u201D" {
		t.Errorf("FindActualString = %q, want curly-quoted version", got)
	}
}

func TestFindActualString_NotFound(t *testing.T) {
	t.Parallel()
	fileContent := "hello world"
	_, ok := FindActualString(fileContent, "not found")
	if ok {
		t.Error("FindActualString ok = true, want false")
	}
}

func TestFindActualString_EmptyStrings(t *testing.T) {
	t.Parallel()
	got, ok := FindActualString("hello", "")
	if !ok {
		t.Fatal("FindActualString with empty search ok = false, want true")
	}
	if got != "" {
		t.Errorf("FindActualString = %q, want empty", got)
	}
}

func TestFindActualString_MixedQuotes(t *testing.T) {
	t.Parallel()
	// File has both curly and straight quotes — should find the correct match
	fileContent := "\u201CHello\u201D and \"World\""
	got, ok := FindActualString(fileContent, `"Hello"`)
	if !ok {
		t.Fatal("FindActualString ok = false, want true")
	}
	// Should return the curly-quoted version from the file, not "World"
	if got != "\u201CHello\u201D" {
		t.Errorf("FindActualString = %q, want %q", got, "\u201CHello\u201D")
	}
}

func TestPreserveQuoteStyle_SameStrings(t *testing.T) {
	t.Parallel()
	got := PreserveQuoteStyle("hello", "hello", "world")
	if got != "world" {
		t.Errorf("PreserveQuoteStyle = %q, want %q", got, "world")
	}
}

func TestPreserveQuoteStyle_CurlyDouble(t *testing.T) {
	t.Parallel()
	// oldString has straight quotes, actualOldString has curly double quotes
	got := PreserveQuoteStyle(`say "hello"`, "\u201Csay \u201Chello\u201D\u201D", `say "goodbye"`)
	// Verify the result contains curly double quotes (opening context determines direction)
	if !strings.ContainsRune(got, LeftDoubleCurlyQuote) && !strings.ContainsRune(got, RightDoubleCurlyQuote) {
		t.Errorf("PreserveQuoteStyle = %q, should contain curly double quotes", got)
	}
}

func TestPreserveQuoteStyle_CurlySingle(t *testing.T) {
	t.Parallel()
	// oldString has straight single quotes, actualOldString has curly single quotes
	got := PreserveQuoteStyle("say 'hi'", "\u2018say \u2018hi\u2019\u2019", "say 'bye'")
	// Should contain curly single quotes
	if !strings.ContainsRune(got, LeftSingleCurlyQuote) && !strings.ContainsRune(got, RightSingleCurlyQuote) {
		t.Errorf("PreserveQuoteStyle = %q, should contain curly single quotes", got)
	}
}

func TestPreserveQuoteStyle_NoCurlyInActual(t *testing.T) {
	t.Parallel()
	// Different strings but no curly quotes in actualOldString
	got := PreserveQuoteStyle("hello", "HELLO", "world")
	if got != "world" {
		t.Errorf("PreserveQuoteStyle = %q, want %q", got, "world")
	}
}

func TestPreserveQuoteStyle_Contraction(t *testing.T) {
	t.Parallel()
	// new_string contains contraction "don't"
	got := PreserveQuoteStyle("it's", "\u2018it\u2019s", "don't go")
	// "don't" → apostrophe should be RightSingleCurlyQuote (between two letters)
	if !strings.ContainsRune(got, RightSingleCurlyQuote) {
		t.Errorf("PreserveQuoteStyle = %q, should contain right single curly quote for contraction", got)
	}
}

func TestIsOpeningContext_StartOfString(t *testing.T) {
	t.Parallel()
	chars := []rune("hello")
	if !isOpeningContext(chars, 0) {
		t.Error("isOpeningContext at index 0 = false, want true")
	}
}

func TestIsOpeningContext_AfterSpace(t *testing.T) {
	t.Parallel()
	chars := []rune("a \"b")
	// Index 3 is '"' preceded by space
	if !isOpeningContext(chars, 2) {
		t.Error("isOpeningContext after space = false, want true")
	}
}

func TestIsOpeningContext_AfterOpenBracket(t *testing.T) {
	t.Parallel()
	for _, ch := range []rune{'(', '[', '{'} {
		chars := []rune{ch, '"'}
		if !isOpeningContext(chars, 1) {
			t.Errorf("isOpeningContext after %q = false, want true", ch)
		}
	}
}

func TestIsOpeningContext_AfterDash(t *testing.T) {
	t.Parallel()
	// em dash and en dash
	for _, ch := range []rune{'\u2014', '\u2013'} {
		chars := []rune{ch, '"'}
		if !isOpeningContext(chars, 1) {
			t.Errorf("isOpeningContext after %q = false, want true", ch)
		}
	}
}

func TestIsOpeningContext_AfterLetter(t *testing.T) {
	t.Parallel()
	chars := []rune("ab")
	if isOpeningContext(chars, 1) {
		t.Error("isOpeningContext after 'a' = true, want false")
	}
}

func TestApplyCurlyDoubleQuotes_Opening(t *testing.T) {
	t.Parallel()
	got := applyCurlyDoubleQuotes(`say "hello"`)
	// First " is opening (preceded by space), second " is closing
	if !strings.ContainsRune(got, LeftDoubleCurlyQuote) {
		t.Errorf("applyCurlyDoubleQuotes = %q, should contain left curly double quote", got)
	}
	if !strings.ContainsRune(got, RightDoubleCurlyQuote) {
		t.Errorf("applyCurlyDoubleQuotes = %q, should contain right curly double quote", got)
	}
}

func TestApplyCurlyDoubleQuotes_NoDoubleQuotes(t *testing.T) {
	t.Parallel()
	got := applyCurlyDoubleQuotes("hello world")
	if got != "hello world" {
		t.Errorf("applyCurlyDoubleQuotes = %q, want unchanged", got)
	}
}

func TestApplyCurlySingleQuotes_Contraction(t *testing.T) {
	t.Parallel()
	got := applyCurlySingleQuotes("don't")
	// Apostrophe between two letters → RightSingleCurlyQuote
	if !strings.ContainsRune(got, RightSingleCurlyQuote) {
		t.Errorf("applyCurlySingleQuotes = %q, should contain right single curly quote", got)
	}
	if strings.ContainsRune(got, '\'') {
		t.Errorf("applyCurlySingleQuotes = %q, should not contain straight quote", got)
	}
}

func TestApplyCurlySingleQuotes_Opening(t *testing.T) {
	t.Parallel()
	got := applyCurlySingleQuotes("'hello'")
	// First ' is opening (at start), second ' is closing
	if !strings.ContainsRune(got, LeftSingleCurlyQuote) {
		t.Errorf("applyCurlySingleQuotes = %q, should contain left single curly quote", got)
	}
	if !strings.ContainsRune(got, RightSingleCurlyQuote) {
		t.Errorf("applyCurlySingleQuotes = %q, should contain right single curly quote", got)
	}
}

func TestApplyCurlySingleQuotes_NoSingleQuotes(t *testing.T) {
	t.Parallel()
	got := applyCurlySingleQuotes("hello world")
	if got != "hello world" {
		t.Errorf("applyCurlySingleQuotes = %q, want unchanged", got)
	}
}

func TestApplyCurlySingleQuotes_ApostropheAtEnd(t *testing.T) {
	t.Parallel()
	// Single quote at end of string preceded by letter → closing context, not contraction
	got := applyCurlySingleQuotes("it's")
	// ' is between 't' and 's' — both letters → contraction → RightSingleCurlyQuote
	if !strings.ContainsRune(got, RightSingleCurlyQuote) {
		t.Errorf("applyCurlySingleQuotes = %q, should use right curly for contraction", got)
	}
}

func TestApplyCurlySingleQuotes_LeadingQuote(t *testing.T) {
	t.Parallel()
	got := applyCurlySingleQuotes("'hello")
	// ' at position 0 → opening context
	if !strings.ContainsRune(got, LeftSingleCurlyQuote) {
		t.Errorf("applyCurlySingleQuotes = %q, should use left curly at start", got)
	}
}

func TestApplyCurlySingleQuotes_TrailingQuote(t *testing.T) {
	t.Parallel()
	got := applyCurlySingleQuotes("hello'")
	// ' at end, preceded by 'o' (letter), no next char → not contraction, closing context
	if !strings.ContainsRune(got, RightSingleCurlyQuote) {
		t.Errorf("applyCurlySingleQuotes = %q, should use right curly at end", got)
	}
}

func TestApplyEditToFile_SimpleReplace(t *testing.T) {
	t.Parallel()
	got := ApplyEditToFile("hello world", "hello", "goodbye", false)
	if got != "goodbye world" {
		t.Errorf("ApplyEditToFile = %q, want %q", got, "goodbye world")
	}
}

func TestApplyEditToFile_ReplaceAll(t *testing.T) {
	t.Parallel()
	got := ApplyEditToFile("foo bar foo baz foo", "foo", "qux", true)
	if got != "qux bar qux baz qux" {
		t.Errorf("ApplyEditToFile = %q, want %q", got, "qux bar qux baz qux")
	}
}

func TestApplyEditToFile_DeleteWithTrailingNewline(t *testing.T) {
	t.Parallel()
	// oldString doesn't end with \n, file has oldString+"\n" → strip trailing newline
	got := ApplyEditToFile("line1\nline2\nline3\n", "line2", "", false)
	if got != "line1\nline3\n" {
		t.Errorf("ApplyEditToFile = %q, want %q", got, "line1\nline3\n")
	}
}

func TestApplyEditToFile_DeleteWithoutTrailingNewline(t *testing.T) {
	t.Parallel()
	// oldString doesn't end with \n, file does NOT have oldString+"\n" → just delete
	got := ApplyEditToFile("hello world", "world", "", false)
	if got != "hello " {
		t.Errorf("ApplyEditToFile = %q, want %q", got, "hello ")
	}
}

func TestApplyEditToFile_DeleteOldStringEndsWithNewline(t *testing.T) {
	t.Parallel()
	// oldString ends with \n → no trailing newline stripping
	got := ApplyEditToFile("line1\nline2\nline3\n", "line2\n", "", false)
	if got != "line1\nline3\n" {
		t.Errorf("ApplyEditToFile = %q, want %q", got, "line1\nline3\n")
	}
}

func TestApplyEditToFile_ReplaceAllDelete(t *testing.T) {
	t.Parallel()
	// ReplaceAll + empty newString: strips "foo\n" → "" everywhere
	got := ApplyEditToFile("a foo\nb foo\nc foo\n", "foo", "", true)
	if got != "a b c " {
		t.Errorf("ApplyEditToFile = %q, want %q", got, "a b c ")
	}
}

func TestCurlyQuoteConstants(t *testing.T) {
	if LeftSingleCurlyQuote != '\u2018' {
		t.Errorf("LeftSingleCurlyQuote = %U, want U+2018", LeftSingleCurlyQuote)
	}
	if RightSingleCurlyQuote != '\u2019' {
		t.Errorf("RightSingleCurlyQuote = %U, want U+2019", RightSingleCurlyQuote)
	}
	if LeftDoubleCurlyQuote != '\u201C' {
		t.Errorf("LeftDoubleCurlyQuote = %U, want U+201C", LeftDoubleCurlyQuote)
	}
	if RightDoubleCurlyQuote != '\u201D' {
		t.Errorf("RightDoubleCurlyQuote = %U, want U+201D", RightDoubleCurlyQuote)
	}
}
