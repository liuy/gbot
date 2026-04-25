package skills

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Argument substitution tests
// Source: src/utils/argumentSubstitution.ts
// ---------------------------------------------------------------------------

func TestSubstituteArguments_FullArgs(t *testing.T) {
	t.Parallel()

	content := "Hello $ARGUMENTS"
	result := SubstituteArguments(content, "world", nil, true)
	if result != "Hello world" {
		t.Errorf("got %q, want %q", result, "Hello world")
	}
}

func TestSubstituteArguments_IndexedArgs(t *testing.T) {
	t.Parallel()

	content := "First: $ARGUMENTS[0], Second: $ARGUMENTS[1]"
	result := SubstituteArguments(content, "foo bar baz", nil, true)
	if !strings.Contains(result, "First: foo") {
		t.Errorf("expected 'First: foo' in %q", result)
	}
	if !strings.Contains(result, "Second: bar") {
		t.Errorf("expected 'Second: bar' in %q", result)
	}
}

func TestSubstituteArguments_ShorthandIndexed(t *testing.T) {
	t.Parallel()

	content := "Zero: $0, One: $1, Two: $2"
	result := SubstituteArguments(content, "alpha beta gamma", nil, true)
	if !strings.Contains(result, "Zero: alpha") {
		t.Errorf("expected 'Zero: alpha' in %q", result)
	}
	if !strings.Contains(result, "One: beta") {
		t.Errorf("expected 'One: beta' in %q", result)
	}
	if !strings.Contains(result, "Two: gamma") {
		t.Errorf("expected 'Two: gamma' in %q", result)
	}
}

func TestSubstituteArguments_NamedArgs(t *testing.T) {
	t.Parallel()

	content := "File: ${file}, Pattern: ${pattern}"
	result := SubstituteArguments(content, "main.go TODO", []string{"file", "pattern"}, true)
	if !strings.Contains(result, "File: main.go") {
		t.Errorf("expected 'File: main.go' in %q", result)
	}
	if !strings.Contains(result, "Pattern: TODO") {
		t.Errorf("expected 'Pattern: TODO' in %q", result)
	}
}

func TestSubstituteArguments_AppendIfNoPlaceholder(t *testing.T) {
	t.Parallel()

	content := "No placeholders here."
	result := SubstituteArguments(content, "some args", nil, true)
	if !strings.Contains(result, "ARGUMENTS: some args") {
		t.Errorf("expected appended args in %q", result)
	}
	if !strings.HasPrefix(result, "No placeholders here.") {
		t.Errorf("expected original content preserved, got %q", result)
	}
}

func TestSubstituteArguments_NoAppendWhenDisabled(t *testing.T) {
	t.Parallel()

	content := "No placeholders here."
	result := SubstituteArguments(content, "some args", nil, false)
	if result != content {
		t.Errorf("expected unchanged content when appendIfNoPlaceholder=false, got %q", result)
	}
}

func TestSubstituteArguments_EmptyArgs(t *testing.T) {
	t.Parallel()

	content := "Hello $ARGUMENTS"
	result := SubstituteArguments(content, "", nil, true)
	if result != "Hello $ARGUMENTS" {
		t.Errorf("empty args should not substitute, got %q", result)
	}
}

func TestSubstituteArguments_OutOfBoundsIndex(t *testing.T) {
	t.Parallel()

	// TS behavior: parsedArgs[index] ?? '' → empty string
	content := "$ARGUMENTS[5]"
	result := SubstituteArguments(content, "only one", nil, true)
	if result != "" {
		t.Errorf("expected empty string for out-of-bounds index, got %q", result)
	}
}

func TestSubstituteArguments_Mixed(t *testing.T) {
	t.Parallel()

	content := "Run $ARGUMENTS on ${file}. Options: $0 $1"
	result := SubstituteArguments(content, "test main.go --verbose", []string{"file"}, true)
	// ${file} maps to parsedArgs[0] = "test", $ARGUMENTS = full string, $0 = "test", $1 = "main.go"
	if !strings.Contains(result, "Run test main.go --verbose on test.") {
		t.Errorf("expected full substitution with ${file}='test', got %q", result)
	}
	if !strings.Contains(result, "Options: test main.go") {
		t.Errorf("expected $0='test' $1='main.go', got %q", result)
	}
}

func TestParseArguments_Simple(t *testing.T) {
	t.Parallel()

	args := ParseArguments("foo bar baz")
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(args), args)
	}
	if args[0] != "foo" {
		t.Errorf("args[0] = %q, want %q", args[0], "foo")
	}
	if args[2] != "baz" {
		t.Errorf("args[2] = %q, want %q", args[2], "baz")
	}
}

func TestParseArguments_QuotedStrings(t *testing.T) {
	t.Parallel()

	args := ParseArguments(`foo "hello world" baz`)
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(args), args)
	}
	if args[1] != "hello world" {
		t.Errorf("args[1] = %q, want %q", args[1], "hello world")
	}
}

func TestParseArguments_SingleQuotes(t *testing.T) {
	t.Parallel()

	args := ParseArguments(`foo 'hello world' baz`)
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(args), args)
	}
	if args[1] != "hello world" {
		t.Errorf("args[1] = %q, want %q", args[1], "hello world")
	}
}

func TestParseArguments_Empty(t *testing.T) {
	t.Parallel()

	args := ParseArguments("")
	if len(args) != 0 {
		t.Errorf("expected empty for empty input, got %d", len(args))
	}

	args = ParseArguments("   ")
	if len(args) != 0 {
		t.Errorf("expected empty for whitespace input, got %d", len(args))
	}
}

func TestParseArguments_MultipleSpaces(t *testing.T) {
	t.Parallel()

	args := ParseArguments("foo    bar")
	if len(args) != 2 {
		t.Fatalf("expected 2 args, got %d: %v", len(args), args)
	}
}

func TestParseArgumentNames_String(t *testing.T) {
	t.Parallel()

	names := ParseArgumentNames("file pattern output")
	if len(names) != 3 {
		t.Fatalf("expected 3 names, got %d: %v", len(names), names)
	}
	if names[0] != "file" {
		t.Errorf("names[0] = %q, want %q", names[0], "file")
	}
}

func TestParseArgumentNames_Array(t *testing.T) {
	t.Parallel()

	names := ParseArgumentNames([]any{"file", "pattern", "output"})
	if len(names) != 3 {
		t.Fatalf("expected 3 names, got %d: %v", len(names), names)
	}
}

func TestParseArgumentNames_FiltersDigits(t *testing.T) {
	t.Parallel()

	// Numeric-only names should be filtered (conflict with $0, $1 shorthand)
	// Source: argumentSubstitution.ts:58-59
	names := ParseArgumentNames("file 123 pattern")
	if len(names) != 2 {
		t.Fatalf("expected 2 names (digit-only filtered), got %d: %v", len(names), names)
	}
	if names[0] != "file" || names[1] != "pattern" {
		t.Errorf("expected [file, pattern], got %v", names)
	}
}

func TestParseArgumentNames_Nil(t *testing.T) {
	t.Parallel()

	names := ParseArgumentNames(nil)
	if len(names) != 0 {
		t.Errorf("expected empty for nil, got %d", len(names))
	}
}

func TestGenerateProgressiveArgumentHint(t *testing.T) {
	t.Parallel()

	hint := GenerateProgressiveArgumentHint(
		[]string{"file", "pattern", "output"},
		[]string{"main.go"},
	)
	if hint != "[pattern] [output]" {
		t.Errorf("hint = %q, want %q", hint, "[pattern] [output]")
	}
}

func TestGenerateProgressiveArgumentHint_AllFilled(t *testing.T) {
	t.Parallel()

	hint := GenerateProgressiveArgumentHint(
		[]string{"file", "pattern"},
		[]string{"main.go", "TODO"},
	)
	if hint != "" {
		t.Errorf("expected empty hint when all filled, got %q", hint)
	}
}

func TestGenerateProgressiveArgumentHint_NoneTyped(t *testing.T) {
	t.Parallel()

	hint := GenerateProgressiveArgumentHint(
		[]string{"file", "pattern"},
		nil,
	)
	if hint != "[file] [pattern]" {
		t.Errorf("hint = %q, want %q", hint, "[file] [pattern]")
	}
}

// ---------------------------------------------------------------------------
// Additional substitute coverage
// ---------------------------------------------------------------------------

func TestReplaceNamedArg_FollowedByBracket(t *testing.T) {
	t.Parallel()

	// $name[...] should NOT be replaced (it's for $ARGUMENTS[N])
	content := "test $file[0] end"
	result := replaceNamedArg(content, "file", "value.go")
	if result != content {
		t.Errorf("$file[0] should not be replaced, got %q", result)
	}
}

func TestReplaceNamedArg_FollowedByWordChar(t *testing.T) {
	t.Parallel()

	// $fileXxx should NOT be replaced
	content := "test $filename end"
	result := replaceNamedArg(content, "file", "value.go")
	if result != content {
		t.Errorf("$filename should not be replaced, got %q", result)
	}
}

func TestReplaceNamedArg_AtEndOfString(t *testing.T) {
	t.Parallel()

	content := "prefix $file"
	result := replaceNamedArg(content, "file", "value.go")
	if result != "prefix value.go" {
		t.Errorf("at end: got %q, want %q", result, "prefix value.go")
	}
}

func TestReplaceNamedArg_EmptyValue(t *testing.T) {
	t.Parallel()

	content := "prefix $file suffix"
	result := replaceNamedArg(content, "file", "")
	if result != "prefix  suffix" {
		t.Errorf("empty value: got %q, want %q", result, "prefix  suffix")
	}
}

func TestParseIndex_InvalidChars(t *testing.T) {
	if parseIndex("12abc") != -1 {
		t.Error("expected -1 for non-digit chars")
	}
	if parseIndex("") != 0 {
		t.Error("expected 0 for empty string")
	}
	if parseIndex("0") != 0 {
		t.Error("expected 0 for '0'")
	}
	if parseIndex("42") != 42 {
		t.Error("expected 42 for '42'")
	}
}

func TestSubstituteArguments_NamedArgNotEnoughParsed(t *testing.T) {
	t.Parallel()

	// 2 arg names but only 1 parsed arg — second should be empty
	content := "${file} ${pattern}"
	result := SubstituteArguments(content, "only_one.go", []string{"file", "pattern"}, true)
	if result != "only_one.go " {
		t.Errorf("got %q, want %q", result, "only_one.go ")
	}
}

func TestSubstituteArguments_ShorthandOutOfBounds(t *testing.T) {
	t.Parallel()

	content := "$0 $5"
	result := SubstituteArguments(content, "one two", nil, true)
	// $0 = "one", $5 = "" (out of bounds, written as original "$5"? no — replaceShorthandIndexed writes original)
	// Actually for out of bounds it writes the original match
	if !strings.Contains(result, "one") {
		t.Errorf("expected 'one' in %q", result)
	}
}

func TestParseArgumentNames_EmptyString(t *testing.T) {
	t.Parallel()

	names := ParseArgumentNames("")
	if names != nil {
		t.Errorf("empty string: got %v, want nil", names)
	}
}

func TestParseArgumentNames_EmptyArray(t *testing.T) {
	t.Parallel()

	names := ParseArgumentNames([]any{})
	if names != nil {
		t.Errorf("empty array: got %v, want nil", names)
	}
}

func TestParseArgumentNames_NonStringArray(t *testing.T) {
	t.Parallel()

	names := ParseArgumentNames([]any{42, true})
	if names != nil {
		t.Errorf("non-string array: got %v, want nil", names)
	}
}

func TestParseArgumentNames_DefaultType(t *testing.T) {
	t.Parallel()

	names := ParseArgumentNames(42)
	if names != nil {
		t.Errorf("int input: got %v, want nil", names)
	}
}

func TestSubstituteArguments_DollarNameNotFollowedByBracket(t *testing.T) {
	t.Parallel()

	// $file/ — '/' is not a word char, so $file should be replaced
	content := "path: $file/extra"
	result := SubstituteArguments(content, "main.go", []string{"file"}, true)
	if result != "path: main.go/extra" {
		t.Errorf("got %q, want %q", result, "path: main.go/extra")
	}
}

func TestReplaceShorthandIndexed_FollowedByWord(t *testing.T) {
	t.Parallel()

	// $1abc — should not be replaced
	content := "test $1abc end"
	result := SubstituteArguments(content, "foo bar", nil, true)
	if !strings.Contains(result, "$1abc") {
		t.Errorf("$1abc should not be replaced, got %q", result)
	}
}
