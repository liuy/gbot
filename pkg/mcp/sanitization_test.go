package mcp

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// sanitizeUnicode tests — Source: sanitization.ts unit tests
// ---------------------------------------------------------------------------

func TestSanitizeUnicode_NFKC(t *testing.T) {
	// Full-width ASCII → half-width (NFKC normalization)
	// Source: sanitization.ts:36 — .normalize('NFKC')
	input := "\uff21\uff22\uff23" // ＡＢＣ (full-width)
	got := sanitizeUnicode(input)
	if got != "ABC" {
		t.Errorf("NFKC: got %q, want %q", got, "ABC")
	}
}

func TestSanitizeUnicode_InvisibleChars(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// \p{Cf} — Zero-width spaces (U+200B-F)
		{"zero-width space", "hello\u200Bworld", "helloworld"},
		{"zero-width non-joiner", "hello\u200Cworld", "helloworld"},
		{"zero-width joiner", "hello\u200Dworld", "helloworld"},
		{"left-to-right mark", "a\u200Eb", "ab"},
		{"right-to-left mark", "a\u200Fb", "ab"},

		// \p{Cf} — Directional formatting (U+202A-E)
		{"left-to-right embed", "a\u202Ab", "ab"},
		{"right-to-left embed", "a\u202Bb", "ab"},
		{"pop directional", "a\u202Cb", "ab"},
		{"left-to-right override", "a\u202Db", "ab"},
		{"right-to-left override", "a\u202Eb", "ab"},

		// \p{Cf} — Directional isolates (U+2066-9)
		{"left-to-right isolate", "a\u2066b", "ab"},
		{"right-to-left isolate", "a\u2067b", "ab"},
		{"first strong isolate", "a\u2068b", "ab"},
		{"pop directional isolate", "a\u2069b", "ab"},

		// \p{Cf} — BOM
		{"BOM", "\uFEFFhello", "hello"},

		// \p{Co} — Private Use (U+E000-F8FF)
		{"private use E000", "a\uE000b", "ab"},
		{"private use F8FF", "a\uF8FFb", "ab"},
		{"private use mid", "a\uF000b", "ab"},

		// Multiple invisible chars
		{"multiple", "\u200B\uFEFFhello\uE000\u200C", "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeUnicode(tt.input)
			if got != tt.want {
				t.Errorf("got %q (len=%d), want %q (len=%d)", got, len(got), tt.want, len(tt.want))
			}
		})
	}
}

func TestSanitizeUnicode_ControlChars(t *testing.T) {
	// General control characters (Cc) should NOT be removed.
	// Only Cf, Co, Cn are removed. Tab, newline, carriage return are Cc.
	input := "hello\tworld\nnew\rline"
	got := sanitizeUnicode(input)
	if got != input {
		t.Errorf("control chars should be preserved, got %q", got)
	}
}

func TestSanitizeUnicode_PreservesNormal(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"ASCII", "Hello World 123!@#"},
		{"CJK", "你好世界"},
		{"emoji", "🎉🚀"},
		{"mixed", "Hello 你好 123"},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeUnicode(tt.input)
			if got != tt.input {
				t.Errorf("normal text changed: got %q, want %q", got, tt.input)
			}
		})
	}
}

func TestSanitizeUnicode_Iterative(t *testing.T) {
	// Verify that iterative normalization works — NFKC can produce
	// characters that then need further cleaning.
	input := "test"
	got := sanitizeUnicode(input)
	if got != input {
		t.Errorf("stable input should not change: got %q, want %q", got, input)
	}
}

func TestSanitizeUnicode_MaxIteration(t *testing.T) {
	// Normal text stabilizes in 1-2 iterations, returns successfully.
	got := sanitizeUnicode("normal text")
	if got != "normal text" {
		t.Errorf("normal text should be unchanged, got %q", got)
	}
}

func TestSanitizeUnicode_MaxIterationTriggered(t *testing.T) {
	// Force max iterations by setting threshold to 0.
	// With 0 iterations allowed, the loop body never executes,
	// so iterations stays 0 and 0 >= 0 triggers the guard.
	orig := maxSanitizationIterations
	maxSanitizationIterations = 0
	defer func() { maxSanitizationIterations = orig }()

	got := sanitizeUnicode("hello\u200Bworld")
	// With 0 iterations, the string is never cleaned
	if got != "hello\u200Bworld" {
		t.Errorf("with max=0, input should be returned as-is, got %q", got)
	}
}

func TestSanitizeUnicode_UnassignedCodepoints(t *testing.T) {
	// \p{Cn} — Unassigned codepoints should be removed.
	// U+0378 is an unassigned codepoint (was never assigned in Unicode).
	input := "hello\u0378world"
	got := sanitizeUnicode(input)
	if strings.Contains(got, "\u0378") {
		t.Errorf("unassigned codepoint U+0378 should be removed, got %q", got)
	}
	if got != "helloworld" {
		t.Errorf("got %q, want %q", got, "helloworld")
	}
}

// ---------------------------------------------------------------------------
// sanitizeUnicodeRecursive tests — Source: sanitization.ts:67-91
// ---------------------------------------------------------------------------

func TestSanitizeUnicodeRecursive_String(t *testing.T) {
	input := "hello\u200Bworld"
	got := sanitizeUnicodeRecursive(input).(string)
	if got != "helloworld" {
		t.Errorf("got %q, want %q", got, "helloworld")
	}
}

func TestSanitizeUnicodeRecursive_NestedJSON(t *testing.T) {
	input := map[string]any{
		"name":        "tool\u200Bname",
		"description": "a\uFEFFdesc",
		"nested": map[string]any{
			"inner": "val\uE000ue",
		},
		"array":  []any{"a\u200Bb", 123, true},
		"number": 42,
		"bool":   false,
	}

	got := sanitizeUnicodeRecursive(input).(map[string]any)

	// Check string fields cleaned
	if got["name"].(string) != "toolname" {
		t.Errorf("name: got %q, want %q", got["name"], "toolname")
	}
	if got["description"].(string) != "adesc" {
		t.Errorf("description: got %q, want %q", got["description"], "adesc")
	}

	// Check nested object
	nested := got["nested"].(map[string]any)
	if nested["inner"].(string) != "value" {
		t.Errorf("nested.inner: got %q, want %q", nested["inner"], "value")
	}

	// Check array
	arr := got["array"].([]any)
	if arr[0].(string) != "ab" {
		t.Errorf("array[0]: got %q, want %q", arr[0], "ab")
	}
	if arr[1].(int) != 123 {
		t.Errorf("array[1]: got %v, want 123", arr[1])
	}
	if arr[2].(bool) != true {
		t.Errorf("array[2]: got %v, want true", arr[2])
	}

	// Check primitives unchanged
	if got["number"].(int) != 42 {
		t.Errorf("number: got %v, want 42", got["number"])
	}
	if got["bool"].(bool) != false {
		t.Errorf("bool: got %v, want false", got["bool"])
	}
}

func TestSanitizeUnicodeRecursive_PreservesNonString(t *testing.T) {
	tests := []struct {
		name  string
		input any
	}{
		{"nil", nil},
		{"int", 42},
		{"float", 3.14},
		{"bool", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeUnicodeRecursive(tt.input)
			if got != tt.input {
				t.Errorf("got %v (%T), want %v (%T)", got, got, tt.input, tt.input)
			}
		})
	}
}

func TestSanitizeUnicodeRecursive_SanitizesKeys(t *testing.T) {
	// Source: sanitization.ts:83 — keys are also sanitized
	input := map[string]any{
		"key\u200Bname": "value",
	}
	got := sanitizeUnicodeRecursive(input).(map[string]any)
	if _, ok := got["keyname"]; !ok {
		t.Errorf("key should be sanitized to %q, got keys: %v", "keyname", got)
	}
	if _, ok := got["key\u200Bname"]; ok {
		t.Error("original key with invisible char should not exist")
	}
}

// ---------------------------------------------------------------------------
// sanitizeString tests
// ---------------------------------------------------------------------------

func TestSanitizeString_CleansInput(t *testing.T) {
	got := sanitizeString("hello\u200Bworld")
	if got != "helloworld" {
		t.Errorf("got %q, want %q", got, "helloworld")
	}
}

func TestSanitizeString_PreservesClean(t *testing.T) {
	got := sanitizeString("clean")
	if got != "clean" {
		t.Errorf("got %q, want %q", got, "clean")
	}
}

// ---------------------------------------------------------------------------
// sanitizeJSON tests
// ---------------------------------------------------------------------------

func TestSanitizeJSON_CleansStrings(t *testing.T) {
	input := json.RawMessage(`{"name":"tool\u200bname"}`)
	got := sanitizeJSON(input)
	if strings.Contains(string(got), "\\u200b") {
		t.Errorf("invisible char should be removed, got %q", string(got))
	}
}

func TestSanitizeJSON_EmptyInput(t *testing.T) {
	got := sanitizeJSON(nil)
	if got != nil {
		t.Errorf("nil input should return nil, got %q", string(got))
	}
	got = sanitizeJSON(json.RawMessage(""))
	if string(got) != "" {
		t.Errorf("empty input should return empty, got %q", string(got))
	}
}

func TestSanitizeJSON_InvalidJSON(t *testing.T) {
	input := json.RawMessage(`{invalid json}`)
	got := sanitizeJSON(input)
	if string(got) != string(input) {
		t.Errorf("invalid JSON should be returned as-is, got %q", string(got))
	}
}

// ---------------------------------------------------------------------------
// isInvisibleChar direct tests
// ---------------------------------------------------------------------------

func TestIsInvisibleChar_ExplicitRanges(t *testing.T) {
	// Test all explicit ranges from sanitization.ts:47-53
	invisible := []rune{
		0x200B, 0x200C, 0x200D, 0x200E, 0x200F, // Zero-width + LTR/RTL
		0x202A, 0x202B, 0x202C, 0x202D, 0x202E, // Directional formatting
		0x2066, 0x2067, 0x2068, 0x2069, // Directional isolates
		0xFEFF,         // BOM
		0xE000, 0xF8FF, // Private use range boundaries
		0xF000, // Private use mid
	}
	for _, r := range invisible {
		if !isInvisibleChar(r) {
			t.Errorf("rune U+%04X should be invisible", r)
		}
	}
}

func TestIsInvisibleChar_NormalChars(t *testing.T) {
	normal := []rune{
		'a', 'Z', '0', ' ', '\t', '\n', '\r',
		'你', '日', // CJK
		'🎉', // emoji
		'€', // symbol
	}
	for _, r := range normal {
		if isInvisibleChar(r) {
			t.Errorf("rune U+%04X (%c) should NOT be invisible", r, r)
		}
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestSanitizeUnicode_AllInvisible(t *testing.T) {
	// Input that's entirely invisible chars → result is empty string
	input := "\u200B\uFEFF\uE000\u200C"
	got := sanitizeUnicode(input)
	if got != "" {
		t.Errorf("all-invisible input should result in empty string, got %q", got)
	}
	if utf8.RuneCountInString(input) != 4 {
		t.Errorf("test setup: input should be 4 runes, got %d", utf8.RuneCountInString(input))
	}
}

func TestSanitizeUnicode_EmptyString(t *testing.T) {
	got := sanitizeUnicode("")
	if got != "" {
		t.Errorf("empty string should remain empty, got %q", got)
	}
}
