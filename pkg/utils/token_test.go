package utils

import "testing"

// Tests calibrated against real tokenizer APIs.
// Default provider (unknown) uses CJK=0.65. Provider-specific tests use
// EstimateTokensForProvider with the correct ratio.

func TestEstimateTokens_Empty(t *testing.T) {
	t.Parallel()
	if got := EstimateTokens(""); got != 0 {
		t.Errorf("EstimateTokens(\"\") = %d, want 0", got)
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
