package utils

import (
	"strings"
	"testing"
)

// Expectations hand-computed from the 6-feature model:
// CJK*0.72 + Struct*1.38 + Digit*1.34 + Indent*(-0.04) + Words*0.48 + Other*0.11,
// truncated toward zero and floored at 0. The pre-2026-10-04 2-feature
// heuristic (CJK*0.65 + nonCJK*0.20) encoded outdated calibration data; its
// values are noted where a case replaced one.

func TestEstimateTokens_Empty(t *testing.T) {
	t.Parallel()
	// Empty input has zero features, so the estimate is exactly 0.
	want := 0
	if got := EstimateTokens(""); got != want {
		t.Errorf("EstimateTokens(\"\") = %d, want %d", got, want)
	}
}

func TestEstimateTokens_English(t *testing.T) {
	t.Parallel()
	// "hello world": Other 11 (incl. the space), Words 2 →
	// 11*0.11 + 2*0.48 = 1.21 + 0.96 = 2.17 → 2. (Old heuristic: 2.)
	got := EstimateTokens("hello world")
	if got != 2 {
		t.Errorf("EstimateTokens(\"hello world\") = %d, want 2", got)
	}
}

func TestEstimateTokens_LongEnglish(t *testing.T) {
	t.Parallel()
	// 129 chars, all Other ('.' and ',' are not struct runes), Words 23 →
	// 129*0.11 + 23*0.48 = 14.19 + 11.04 = 25.23 → 25.
	// (Old heuristic: 129*0.20 = 25.8 → 25 — same answer by coincidence.)
	const text = "You are a creature hosted inside gbot. This is your body, treat it that way. You help your human with software engineering tasks."
	got := EstimateTokens(text)
	if got != 25 {
		t.Errorf("EstimateTokens(long English) = %d, want 25", got)
	}
}

func TestEstimateTokens_Code(t *testing.T) {
	t.Parallel()
	// 61 chars: Struct 4 (two '(' + two ')'; '*' is NOT a struct rune),
	// Other 57, Words 6 → 4*1.38 + 57*0.11 + 6*0.48 = 5.52 + 6.27 + 2.88 = 14.67 → 14.
	const text = `func (e *Engine) setTaskDirForSession(sessionID string) error`
	got := EstimateTokens(text)
	if got != 14 {
		t.Errorf("EstimateTokens(code) = %d, want 14", got)
	}
}

func TestEstimateTokens_JSON(t *testing.T) {
	t.Parallel()
	// 44 chars: Struct 17 (4 braces + 10 quotes + 3 colons; the comma is
	// Other), Other 27, Words 2 → 17*1.38 + 27*0.11 + 2*0.48 = 23.46 + 2.97 + 0.96 = 27.39 → 27.
	// (Old heuristic: 8 — the 6-feature model prices structure-heavy text
	// far higher, matching real tokenizers.)
	got := EstimateTokens(`{"name":"Bash","input":{"command":"ls -la"}}`)
	if got != 27 {
		t.Errorf("EstimateTokens(json) = %d, want 27", got)
	}
}

func TestEstimateTokens_CJK(t *testing.T) {
	t.Parallel()
	// 你好世界: CJK 4, Words 1 → 4*0.72 + 1*0.48 = 2.88 + 0.48 = 3.36 → 3.
	// (Old default-provider heuristic: 4*0.65 = 2.6 → 2.)
	got := EstimateTokens("你好世界")
	if got != 3 {
		t.Errorf("EstimateTokens(\"你好世界\") = %d, want 3", got)
	}
}

func TestEstimateTokens_Digits(t *testing.T) {
	t.Parallel()
	// "12345": Digit 5, Words 1 → 5*1.34 + 0.48 = 6.70 + 0.48 = 7.18 → 7.
	// (Old heuristic: 5*0.20 = 1.)
	got := EstimateTokens("12345")
	if got != 7 {
		t.Errorf("EstimateTokens(\"12345\") = %d, want 7", got)
	}
}

func TestEstimateTokens_FlooredAtZero(t *testing.T) {
	t.Parallel()
	// 100 tabs: Indent 100, everything else 0 → 100*(-0.04) = -4 → floored 0.
	want := 0
	got := EstimateTokens(strings.Repeat("\t", 100))
	if got != want {
		t.Errorf("EstimateTokens(100 tabs) = %d, want %d (negative model output floored)", got, want)
	}
}

func TestSeedTokenRatios_Application(t *testing.T) {
	t.Parallel()
	// Direct application check: EstimateTokens must be exactly the seed
	// ratios applied to the counted features, not some other table.
	// 你好世界 → CJK 4, Words 1.
	want := int(float64(4)*SeedTokenRatios[0] + float64(1)*SeedTokenRatios[4])
	if got := EstimateTokens("你好世界"); got != want {
		t.Errorf("EstimateTokens(CJK) = %d, want %d (features × SeedTokenRatios)", got, want)
	}
	// "12345" → Digit 5, Words 1.
	want = int(float64(5)*SeedTokenRatios[2] + float64(1)*SeedTokenRatios[4])
	if got := EstimateTokens("12345"); got != want {
		t.Errorf("EstimateTokens(digits) = %d, want %d (features × SeedTokenRatios)", got, want)
	}
	// The seed table itself is the 2026-10-04 measurement.
	if SeedTokenRatios != [6]float64{0.72, 1.38, 1.34, -0.04, 0.48, 0.11} {
		t.Errorf("SeedTokenRatios = %v, want the 2026-10-04 measured {0.72 1.38 1.34 -0.04 0.48 0.11}", SeedTokenRatios)
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
