package types

import "testing"

// Cases ported from tokenx test/index.test.ts (johannschopplich/tokenx).
// These MUST produce the same numbers as the Node.js reference implementation
// to guarantee algorithm equivalence.

func TestEstimateTokens_Empty(t *testing.T) {
	t.Parallel()
	if got := EstimateTokens(""); got != 0 {
		t.Errorf("EstimateTokens(%q) = %d, want 0", "", got)
	}
}

func TestEstimateTokens_EnglishShort(t *testing.T) {
	t.Parallel()
	// tokenx test: "Hello, world! This is a short sentence." → 11
	const input = "Hello, world! This is a short sentence."
	if got := EstimateTokens(input); got != 11 {
		t.Errorf("EstimateTokens(short English) = %d, want 11 (tokenx ref)", got)
	}
}

func TestEstimateTokens_GermanUmlauts(t *testing.T) {
	t.Parallel()
	// tokenx test: German text with umlauts → 49
	const input = "Die pünktlich gewünschte Trüffelfüllung im übergestülpten Würzkümmel-Würfel ist kümmerlich und dürfte fürderhin zu Rüffeln in Hülle und Fülle führen"
	if got := EstimateTokens(input); got != 49 {
		t.Errorf("EstimateTokens(German umlauts) = %d, want 49 (tokenx ref)", got)
	}
}

func TestEstimateTokens_URLNotCharByChar(t *testing.T) {
	t.Parallel()
	// tokenx regression #4: URL must not count as 1 token/char
	const url = "https://example.com/path/to/resource"
	got := EstimateTokens(url)
	if got > len(url)/2 {
		t.Errorf("EstimateTokens(%q) = %d, must be ≤ len/2 = %d (tokenx regression #4)", url, got, len(url)/2)
	}
}

func TestEstimateTokens_CJK(t *testing.T) {
	t.Parallel()
	// tokenx: CJK → 1 token/char
	if got := EstimateTokens("中文"); got != 2 {
		t.Errorf("EstimateTokens(%q) = %d, want 2", "中文", got)
	}
}

func TestEstimateTokens_Numeric(t *testing.T) {
	t.Parallel()
	// tokenx: numeric sequences → 1 token regardless of length
	if got := EstimateTokens("12345"); got != 1 {
		t.Errorf("EstimateTokens(%q) = %d, want 1", "12345", got)
	}
}

func TestEstimateTokens_Punctuation(t *testing.T) {
	t.Parallel()
	// tokenx order: short check (<=3) comes before punctuation.
	// "!!!" len=3 ≤ threshold → 1 token (not ceil(3/2)=2).
	if got := EstimateTokens("!!!"); got != 1 {
		t.Errorf("EstimateTokens(%q) = %d, want 1", "!!!", got)
	}
	// "!!!!!" len=5 > threshold → punctuation path: ceil(5/2) = 3
	if got := EstimateTokens("!!!!!"); got != 3 {
		t.Errorf("EstimateTokens(%q) = %d, want 3", "!!!!!", got)
	}
}

func TestEstimateTokens_Alphanumeric(t *testing.T) {
	t.Parallel()
	// "hello" len=5, defaultCharsPerToken=6 → ceil(5/6) = 1
	if got := EstimateTokens("hello"); got != 1 {
		t.Errorf("EstimateTokens(%q) = %d, want 1", "hello", got)
	}
	// "internationalization" len=20 → ceil(20/6) = 4
	if got := EstimateTokens("internationalization"); got != 4 {
		t.Errorf("EstimateTokens(%q) = %d, want 4", "internationalization", got)
	}
}

func TestEstimateTokens_ShortWord(t *testing.T) {
	t.Parallel()
	// Words ≤3 chars → 1 token
	if got := EstimateTokens("abc"); got != 1 {
		t.Errorf("EstimateTokens(%q) = %d, want 1", "abc", got)
	}
}

func TestEstimateTokens_Whitespace(t *testing.T) {
	t.Parallel()
	// Whitespace-only → 0 tokens
	if got := EstimateTokens("    "); got != 0 {
		t.Errorf("EstimateTokens(%q) = %d, want 0", "    ", got)
	}
	if got := EstimateTokens("\t\n"); got != 0 {
		t.Errorf("EstimateTokens(%q) = %d, want 0", "\\t\\n", got)
	}
}

func TestEstimateTokens_UnicodeWhitespace(t *testing.T) {
	t.Parallel()
	// JS \s matches \p{Z} (Unicode separators) + ASCII control whitespace.
	// Go \s only covers ASCII. Without explicit \p{Z} + \t\n\v\f\r +
	// BOM in our split pattern, the JS/Go port diverges on long texts
	// containing NBSP / U+3000 / BOM — locked here as a regression test.
	cases := []struct {
		name string
		text string
		want int
	}{
		{"NBSP splits letters", "K\u00a0U\u00a0R\u00a0T", 4}, // 4 short segments
		{"ideographic space splits CJK", "你好\u3000世界", 4},
		{"BOM only", "\ufeff", 0},
		{"BOM + text", "\ufeffhello world", 2},
		{"CR/LF as whitespace", "a\r\nb", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EstimateTokens(c.text); got != c.want {
				t.Errorf("EstimateTokens(%q) = %d, want %d", c.text, got, c.want)
			}
		})
	}
}

func TestEstimateTokens_Mixed(t *testing.T) {
	t.Parallel()
	// Tokenx CJK pattern is a substring test, so "hi中" matches (contains
	// CJK rune "中"). Falls into CJK path → rune count = 3.
	if got := EstimateTokens("hi中"); got != 3 {
		t.Errorf("EstimateTokens(%q) = %d, want 3", "hi中", got)
	}
}

// TestEstimateTokens_GoldenData verifies our Go port produces the same
// token counts as the Node.js tokenx reference (johannschopplich/tokenx).
// All expected values were verified by running the upstream implementation.
func TestEstimateTokens_GoldenData(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		text string
		want int
	}{
		// From tokenx README benchmarks (actual tiktoken count vs tokenx estimate)
		{"short english", "Hello, world! This is a short sentence.", 11},
		{"german umlauts", "Die pünktlich gewünschte Trüffelfüllung im übergestülpten Würzkümmel-Würfel ist kümmerlich und dürfte fürderhin zu Rüffeln in Hülle und Fülle führen", 49},
		{"empty string", "", 0},

		// CJK — 1 token/char per tokenx rule
		{"chinese 2 chars", "中文", 2},
		{"chinese 你好世界", "你好世界", 4},
		{"japanese hiragana", "あいうえお", 1},  // hiragana NOT in tokenx CJK range → ceil(5/6)=1
		{"japanese katakana", "アイウエオ", 5},
		{"korean hangul", "안녕하세요", 5},

		// Numeric — numeric regex matches pure-digit segments only.
		// "3.14" gets split by "." into ["3", ".", "14"] → 1+1+1=3.
		// "1,000,000" gets split by "," → 5 segments.
		{"short number", "42", 1},
		{"long number", "1234567890", 1},
		{"decimal (split by .)", "3.14", 3},
		{"comma number (split by ,)", "1,000,000", 5},

		// Punctuation
		{"single punct", "!", 1},
		{"three punct (short path)", "!!!", 1},
		{"five punct (punct path)", "!!!!!", 3},

		// Alphanumeric — ceil(len/cpt), cpt=6 default
		{"short word", "hello", 1},                 // ceil(5/6)=1
		{"medium word", "internationalization", 4}, // ceil(20/6)=4
		{"camelCase code", "getUserName", 2},       // "getUserName" not pure alpha (camel ok) → ceil(11/6)=2

		// URL — : is a punct splitter (tokenx line 12 has <>:). Splits
		// into https, :// short, example, ., com, /, path, /, to, /, resource.
		{"url", "https://example.com/path/to/resource", 13},

		// Mixed English sentence
		{"two sentences", "Hello world. Nice day.", 6},

		// Chinese poem (well-known): 道德经 opening
		{"dao de jing opening", "道可道非常道名可名非常名", 12},

		// Code snippets — split by punctuation, not 1:1 char
		{"go func", "func main() {", 4}, // "func"(1) + " "(0) + "main"(1) + "()"(1) + " "(0) + "{"(1) = 4
		{"go var decl", "var x = 42", 4},

		// Punctuation char-set (tokenx upstream parity). Underscore is
		// a splitter, not a word char; angle brackets split comparisons.
		{"snake_case", "foo_bar", 3},                  // 3 alphanumeric short segments
		{"comparison operators", "a<b && c>d", 7},    // "a" "<" "b" "&&" "c" ">" "d"
		{"url with port", "http://x:8080/api", 7},  // : splits → http, ://, x, :, 8080, /, api

		// Mixed CJK segment: tokenx CJK pattern is a substring test, so
		// "hi中" doesn't match CJK (mixed); falls through to short path.
		// JS estimates 3, Go (anchored) currently returns 1.
		{"mixed cjk short", "hi中", 3},
		{"mixed cjk 2 runes", "a短", 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := EstimateTokens(tc.text)
			if got != tc.want {
				t.Errorf("EstimateTokens(%q) = %d, want %d (tokenx ref)", tc.text, got, tc.want)
			}
		})
	}
}

func TestIsCJK_CJKUnifiedIdeographs(t *testing.T) {
	t.Parallel()
	if !IsCJK('中') {
		t.Error("expected CJK Unified Ideograph to be CJK")
	}
	if !IsCJK('字') {
		t.Error("expected CJK Unified Ideograph to be CJK")
	}
}

func TestIsCJK_HiraganaKatakana(t *testing.T) {
	t.Parallel()
	if !IsCJK('あ') {
		t.Error("expected Hiragana to be CJK")
	}
	if !IsCJK('ア') {
		t.Error("expected Katakana to be CJK")
	}
}

func TestIsCJK_Hangul(t *testing.T) {
	t.Parallel()
	if !IsCJK('한') {
		t.Error("expected Hangul Syllable to be CJK")
	}
	if !IsCJK('ㄱ') {
		t.Error("expected Hangul Jamo to be CJK")
	}
}

func TestIsCJK_ASCIINotCJK(t *testing.T) {
	t.Parallel()
	if IsCJK('A') {
		t.Error("expected ASCII to not be CJK")
	}
	if IsCJK(' ') {
		t.Error("expected space to not be CJK")
	}
	if IsCJK('0') {
		t.Error("expected digit to not be CJK")
	}
}

func TestIsCJK_ExtensionRanges(t *testing.T) {
	t.Parallel()
	// CJK Extension A (0x3400-0x4DBF)
	if !IsCJK(0x3500) {
		t.Error("expected CJK Extension A to be CJK")
	}
	// CJK Extension B (0x20000-0x2A6DF)
	if !IsCJK(0x20000) {
		t.Error("expected CJK Extension B to be CJK")
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
	// 1024 / 1024 = 1.0k
	got := FormatTokenCount(1024)
	if got != "1.0k" {
		t.Errorf("FormatTokenCount(1024) = %q, want %q", got, "1.0k")
	}
}

func TestFormatTokenCount_Over1K(t *testing.T) {
	// 1500 / 1024 ≈ 1.465 → "1.5k"
	got := FormatTokenCount(1500)
	if got != "1.5k" {
		t.Errorf("FormatTokenCount(1500) = %q, want %q", got, "1.5k")
	}
}

func TestFormatTokenCount_Megabytes(t *testing.T) {
	// 150000 / 1024 ≈ 146.5k
	got := FormatTokenCount(150000)
	if got != "146.5k" {
		t.Errorf("FormatTokenCount(150000) = %q, want %q", got, "146.5k")
	}
}

func TestFormatTokenCount_1M(t *testing.T) {
	// 1048576 / 1024² = 1.0M
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

func TestPermissionResultMarkers(t *testing.T) {
	// Verify marker methods exist and satisfy the interface
	var _ PermissionResult = PermissionAllowDecision{}
	var _ PermissionResult = PermissionAskDecision{}
	var _ PermissionResult = PermissionDenyDecision{}

	// Explicitly call marker methods for coverage
	allow2 := PermissionAllowDecision{}
	ask2 := PermissionAskDecision{}
	deny2 := PermissionDenyDecision{}
	allow2.permissionResultMarker()
	ask2.permissionResultMarker()
	deny2.permissionResultMarker()

	allow := PermissionAllowDecision{}
	if allow.Behavior() != BehaviorAllow {
		t.Error("AllowDecision.Behavior should be BehaviorAllow")
	}
	ask := PermissionAskDecision{}
	if ask.Behavior() != BehaviorAsk {
		t.Error("AskDecision.Behavior should be BehaviorAsk")
	}
	deny := PermissionDenyDecision{}
	if deny.Behavior() != BehaviorDeny {
		t.Error("DenyDecision.Behavior should be BehaviorDeny")
	}
}
