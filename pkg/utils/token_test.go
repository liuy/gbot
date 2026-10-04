package utils

import "testing"

// Tests calibrated against real tokenizer APIs.
// Default provider (unknown) uses CJK=0.65. Provider-specific tests use
// EstimateTokensForProvider with the correct ratio.

func TestEstimateTokens_Empty(t *testing.T) {
	t.Parallel()
	// Empty input has zero runes, so the estimate is exactly 0.
	want := 0
	if got := EstimateTokens(""); got != want {
		t.Errorf("EstimateTokens(\"\") = %d, want %d", got, want)
	}
}

func TestEstimateTokens_CJK_GLM(t *testing.T) {
	t.Parallel()
	// GLM: 你好世界 = 4 CJK * 0.85 = 3.4 → 3
	got := EstimateTokensForProvider("你好世界", "zhipu")
	if got != 3 {
		t.Errorf("EstimateTokensForProvider(\"你好世界\", zhipu) = %d, want 3", got)
	}
}

func TestEstimateTokens_CJK_DeepSeek(t *testing.T) {
	t.Parallel()
	// DeepSeek: 你好世界 = 4 CJK * 0.50 = 2.0 → 2
	got := EstimateTokensForProvider("你好世界", "deepseek")
	if got != 2 {
		t.Errorf("EstimateTokensForProvider(\"你好世界\", deepseek) = %d, want 2", got)
	}
}

func TestEstimateTokens_English(t *testing.T) {
	t.Parallel()
	// English is provider-independent: 11 non-CJK * 0.20 = 2.2 → 2
	got := EstimateTokens("hello world")
	if got != 2 {
		t.Errorf("EstimateTokens(\"hello world\") = %d, want 2", got)
	}
}

func TestEstimateTokens_LongEnglish(t *testing.T) {
	t.Parallel()
	const text = "You are a creature hosted inside gbot. This is your body, treat it that way. You help your human with software engineering tasks."
	got := EstimateTokens(text)
	if got != 25 {
		t.Errorf("EstimateTokens(long English) = %d, want 25", got)
	}
}

func TestEstimateTokens_Code(t *testing.T) {
	t.Parallel()
	const text = `func (e *Engine) setTaskDirForSession(sessionID string) error`
	cjk := 0
	for _, r := range text {
		if isCJKRune(r) {
			cjk++
		}
	}
	want := int(float64(len([]rune(text))-cjk)*defaultNonCJKTokensPerChar + float64(cjk)*defaultCJKTokensPerChar)
	got := EstimateTokens(text)
	if got != want {
		t.Errorf("EstimateTokens(code) = %d, want %d (cjk=%d, len=%d)", got, want, cjk, len([]rune(text)))
	}
}

func TestEstimateTokens_JSON(t *testing.T) {
	t.Parallel()
	// 40 non-CJK chars * 0.20 = 8
	got := EstimateTokens(`{"name":"Bash","input":{"command":"ls -la"}}`)
	if got != 8 {
		t.Errorf("EstimateTokens(json) = %d, want 8", got)
	}
}

func TestCJKTokensPerChar(t *testing.T) {
	t.Parallel()
	cases := []struct {
		provider string
		want     float64
	}{
		{"zhipu", 0.85},
		{"deepseek", 0.50},
		{"xiaomi", 0.50},
		{"unknown", 0.65},
		{"", 0.65},
	}
	for _, c := range cases {
		got := CJKTokensPerChar(c.provider)
		if got != c.want {
			t.Errorf("CJKTokensPerChar(%q) = %v, want %v", c.provider, got, c.want)
		}
	}
}

func TestFormatTokenCount_Under1000(t *testing.T) {
	t.Parallel()
	got := FormatTokenCount(42)
	if got != "42" {
		t.Errorf("FormatTokenCount(42) = %q, want %q", got, "42")
	}
}

func TestFormatTokenCount_Zero(t *testing.T) {
	t.Parallel()
	got := FormatTokenCount(0)
	if got != "0" {
		t.Errorf("FormatTokenCount(0) = %q, want %q", got, "0")
	}
}

func TestFormatTokenCount_Exactly1024(t *testing.T) {
	got := FormatTokenCount(1024)
	if got != "1.0k" {
		t.Errorf("FormatTokenCount(1024) = %q, want %q", got, "1.0k")
	}
}

func TestFormatTokenCount_Over1K(t *testing.T) {
	got := FormatTokenCount(1500)
	if got != "1.5k" {
		t.Errorf("FormatTokenCount(1500) = %q, want %q", got, "1.5k")
	}
}

func TestFormatTokenCount_Megabytes(t *testing.T) {
	got := FormatTokenCount(150000)
	if got != "146.5k" {
		t.Errorf("FormatTokenCount(150000) = %q, want %q", got, "146.5k")
	}
}

func TestFormatTokenCount_1M(t *testing.T) {
	got := FormatTokenCount(1048576)
	if got != "1.0M" {
		t.Errorf("FormatTokenCount(1048576) = %q, want %q", got, "1.0M")
	}
}

func TestFormatTokenCount_1G(t *testing.T) {
	got := FormatTokenCount(1024 * 1024 * 1024)
	if got != "1.0G" {
		t.Errorf("FormatTokenCount(1G) = %q, want %q", got, "1.0G")
	}
}

// ---------------------------------------------------------------------------
// CountTokenFeatures
// ---------------------------------------------------------------------------

func TestCountTokenFeatures_Empty(t *testing.T) {
	t.Parallel()
	got := CountTokenFeatures("")
	want := TokenFeatures{}
	if got != want {
		t.Errorf("CountTokenFeatures(\"\") = %+v, want %+v", got, want)
	}
}

func TestCountTokenFeatures_JSONFixture(t *testing.T) {
	t.Parallel()
	// Hand-computed fixture. Every rune partitioned exactly once:
	//   {"a": 123}
	//   { " a " :  → Struct×5 ({, ", ", :, })
	//   1 2 3      → Digit×3
	//   a, space   → Other×2 (mid-line space belongs to no char class)
	//   no leading whitespace, no newline → Indent×0, CJK×0
	//   Fields: ["{\"a\":", "123}"] → Words×2
	got := CountTokenFeatures(`{"a": 123}`)
	want := TokenFeatures{CJK: 0, Struct: 5, Digit: 3, Indent: 0, Words: 2, Other: 2}
	if got != want {
		t.Errorf("CountTokenFeatures(JSON) = %+v, want %+v", got, want)
	}
}

func TestCountTokenFeatures_MultilineIndent(t *testing.T) {
	t.Parallel()
	// Hand-computed:
	//   "{\n"             → Struct×1, newline → Other×1
	//   "  \"key\": 42\n" → Indent×2, Struct×3 (" " " :), Digit×2, Other×3 (k,e,y),
	//                      mid-line space → Other×1, newline → Other×1
	//   "}\n"             → Struct×1, newline → Other×1
	// Totals: Struct 5, Digit 2, Indent 2, Other 7 (3 letters + 1 space + 3 newlines), CJK 0
	// Fields: ["{", "\"key\":", "42", "}"] → Words 4
	got := CountTokenFeatures("{\n  \"key\": 42\n}\n")
	want := TokenFeatures{CJK: 0, Struct: 5, Digit: 2, Indent: 2, Words: 4, Other: 7}
	if got != want {
		t.Errorf("CountTokenFeatures(multiline) = %+v, want %+v", got, want)
	}
}

func TestCountTokenFeatures_PartitionExhaustive(t *testing.T) {
	t.Parallel()
	// Every rune must be counted by exactly one of CJK/Struct/Digit/Indent/
	// Other: their sum equals the rune count for mixed inputs.
	inputs := []string{
		"hello 你好世界 123 {}[]()<>=;:\"'`+/&|@#$~^\\",
		"\tindented line\n    deeper indent\nno indent",
		"mixed 中English mix\twith tabs and\n  leading",
		"\"quoted string\" [array] {object: value}",
		"numbers 007 and 42 plus symbols @#$%",
	}
	for _, s := range inputs {
		f := CountTokenFeatures(s)
		sum := f.CJK + f.Struct + f.Digit + f.Indent + f.Other
		if runes := len([]rune(s)); sum != runes {
			t.Errorf("partition of %q sums to %d, want %d runes (features: %+v)", s, sum, runes, f)
		}
	}
}

func TestCountTokenFeatures_IndentExcludedFromOther(t *testing.T) {
	t.Parallel()
	// "    x" → 4 leading spaces (Indent), 1 Other char; the indent chars
	// must not also (or instead) land in Other.
	f := CountTokenFeatures("    x")
	if f.Indent != 4 {
		t.Errorf("Indent = %d, want 4", f.Indent)
	}
	if f.Other != 1 {
		t.Errorf("Other = %d, want 1 (only 'x'; indent excluded)", f.Other)
	}
	if f.Words != 1 {
		t.Errorf("Words = %d, want 1", f.Words)
	}
}

func TestCountTokenFeatures_MidlineWhitespaceIsOtherNotIndent(t *testing.T) {
	t.Parallel()
	// "a b" → 'a','b' Other, mid-line space Other (not Indent).
	f := CountTokenFeatures("a b")
	if f.Indent != 0 {
		t.Errorf("Indent = %d, want 0 (mid-line space is not indent)", f.Indent)
	}
	if f.Other != 3 {
		t.Errorf("Other = %d, want 3", f.Other)
	}
	if f.Words != 2 {
		t.Errorf("Words = %d, want 2", f.Words)
	}
}

func TestCountTokenFeatures_TabsCountAsIndent(t *testing.T) {
	t.Parallel()
	f := CountTokenFeatures("\t\tcode")
	if f.Indent != 2 {
		t.Errorf("Indent = %d, want 2 (tabs)", f.Indent)
	}
	if f.Other != 4 {
		t.Errorf("Other = %d, want 4", f.Other)
	}
}

func TestCountTokenFeatures_CJKClasses(t *testing.T) {
	t.Parallel()
	// 你好世界 all fall in the 0x4E00-0x9FFF range.
	f := CountTokenFeatures("你好世界abc")
	if f.CJK != 4 {
		t.Errorf("CJK = %d, want 4", f.CJK)
	}
	if f.Other != 3 {
		t.Errorf("Other = %d, want 3", f.Other)
	}
	if f.Words != 1 {
		t.Errorf("Words = %d, want 1", f.Words)
	}
}
