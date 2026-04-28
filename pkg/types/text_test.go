package types

import "testing"

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

func TestFormatTokenCount_Exactly1000(t *testing.T) {
	t.Parallel()
	got := FormatTokenCount(1000)
	if got != "1.0k" {
		t.Errorf("FormatTokenCount(1000) = %q, want %q", got, "1.0k")
	}
}

func TestFormatTokenCount_Over1000(t *testing.T) {
	t.Parallel()
	got := FormatTokenCount(1500)
	if got != "1.5k" {
		t.Errorf("FormatTokenCount(1500) = %q, want %q", got, "1.5k")
	}
}

func TestFormatTokenCount_Large(t *testing.T) {
	t.Parallel()
	got := FormatTokenCount(150000)
	if got != "150.0k" {
		t.Errorf("FormatTokenCount(150000) = %q, want %q", got, "150.0k")
	}
}
