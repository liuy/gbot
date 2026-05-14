package types

import "testing"

func TestEstimateTokens_Empty(t *testing.T) {
	t.Parallel()
	if got := EstimateTokens(""); got != 0 {
		t.Errorf("EstimateTokens(%q) = %d, want 0", "", got)
	}
}

func TestEstimateTokens_ASCII(t *testing.T) {
	t.Parallel()
	// "abcd" = 4 chars, non-CJK = 1 token per 4 chars = 1 token
	got := EstimateTokens("abcd")
	if got != 1 {
		t.Errorf("EstimateTokens(%q) = %d, want 1", "abcd", got)
	}
}

func TestEstimateTokens_CJK(t *testing.T) {
	t.Parallel()
	// "中文" = 2 CJK chars, 2 * 1.5 = 3 tokens
	got := EstimateTokens("中文")
	if got != 3 {
		t.Errorf("EstimateTokens(%q) = %d, want 3", "中文", got)
	}
}

func TestEstimateTokens_Mixed(t *testing.T) {
	t.Parallel()
	// "hi中" = 2 non-CJK + 1 CJK = 0 + 1 = 1 token
	got := EstimateTokens("hi中")
	if got != 1 {
		t.Errorf("EstimateTokens(%q) = %d, want 1", "hi中", got)
	}
}

func TestEstimateTokens_Long(t *testing.T) {
	t.Parallel()
	// 32 Latin chars = 8 tokens; 10 CJK chars = 15 tokens; total = 23
	got := EstimateTokens("aaaaaaaaaabbbbbbbbbbccccccccccdd" + "中中中中中中中中中中")
	if got != 23 {
		t.Errorf("EstimateTokens(long mixed) = %d, want 22", got)
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