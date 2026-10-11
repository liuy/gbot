package utils

import "testing"

// The seed ratios are the tuned contract shared with the engine's
// calibration layer.
func TestSeedTokenRatios(t *testing.T) {
	t.Parallel()
	want := [6]float64{0.72, 1.38, 1.34, -0.04, 0.48, 0.11}
	if SeedTokenRatios != want {
		t.Errorf("SeedTokenRatios = %v, want %v", SeedTokenRatios, want)
	}
}

// Hand-computed values for the 6-feature linear model, one feature per case.
func TestEstimateTokens_FeatureWeights(t *testing.T) {
	t.Parallel()
	cases := []struct {
		text string
		want int
	}{
		{"", 0},
		// Words 2*0.48 + Other 11*0.11 = 2.17
		{"hello world", 2},
		// CJK 2*0.72 + Words 1*0.48 = 1.92
		{"中文", 1},
		// Digit 3*1.34 + Words 1*0.48 = 4.50
		{"123", 4},
		// Struct 2*1.38 + Words 1*0.48 = 3.24
		{"{}", 3},
		// Indent 2*-0.04 + Words 0.48 + Other 0.11 = 0.51
		{"  x", 0},
	}
	for _, c := range cases {
		if got := EstimateTokens(c.text); got != c.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", c.text, got, c.want)
		}
	}
}

func TestCountTokenFeatures_Partition(t *testing.T) {
	t.Parallel()
	f := CountTokenFeatures("  a1{}\n\tb\n")
	if f.CJK != 0 {
		t.Errorf("CJK = %d, want 0", f.CJK)
	}
	if f.Digit != 1 {
		t.Errorf("Digit = %d, want 1", f.Digit)
	}
	if f.Struct != 2 {
		t.Errorf("Struct = %d, want 2", f.Struct)
	}
	if f.Indent != 3 {
		t.Errorf("Indent = %d, want 3 (two leading spaces on line 1, one tab on line 2)", f.Indent)
	}
	if f.Words != 2 {
		t.Errorf("Words = %d, want 2", f.Words)
	}
	if f.Other != 4 {
		t.Errorf("Other = %d, want 4", f.Other)
	}
}

// Magic-byte detection must check every byte position of each signature.
func TestDetectImageFormatFromBuffer_BytePositions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		buf  []byte
		want string
	}{
		{"png", []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, "image/png"},
		{"jpeg", []byte{0xff, 0xd8, 0xff, 0xe0}, "image/jpeg"},
		{"gif", []byte("GIF89a"), "image/gif"},
		{"webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), "image/webp"},
		{"riff-not-webp", []byte("RIFF\x00\x00\x00\x00WAVEfmt "), "image/png"},
		{"riff-short", []byte("RIFF\x00\x00"), "image/png"},
		{"tiny", []byte{0x89, 0x50}, "image/png"},
		{"empty", nil, "image/png"},
		{"png byte1 wrong", []byte{0x89, 0x00, 0x4e, 0x47}, "image/png"},
		{"png byte2 wrong", []byte{0x89, 0x50, 0x00, 0x47}, "image/png"},
		{"png byte3 wrong", []byte{0x89, 0x50, 0x4e, 0x00}, "image/png"},
		{"jpeg byte1 wrong", []byte{0xff, 0x00, 0xff, 0xe0}, "image/png"},
		{"jpeg byte2 wrong", []byte{0xff, 0xd8, 0x00, 0xe0}, "image/png"},
		{"gif byte1 wrong", []byte("GXF89a"), "image/png"},
		{"gif byte2 wrong", []byte("GIY89a"), "image/png"},
		{"webp byte9 wrong", []byte("RIFF\x00\x00\x00\x00W\x00BPVP8 "), "image/png"},
		{"webp byte10 wrong", []byte("RIFF\x00\x00\x00\x00WE\x00PVP8 "), "image/png"},
		{"webp byte11 wrong", []byte("RIFF\x00\x00\x00\x00WEB\x0bVP8 "), "image/png"},
	}
	for _, c := range cases {
		if got := DetectImageFormatFromBuffer(c.buf); got != c.want {
			t.Errorf("%s = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestImageConstants(t *testing.T) {
	t.Parallel()
	if ImageMaxWidth != 2000 || ImageMaxHeight != 2000 {
		t.Errorf("image dimension caps = (%d, %d), want (2000, 2000)", ImageMaxWidth, ImageMaxHeight)
	}
	want := [4]int{80, 60, 40, 20}
	if jpegQualities != want {
		t.Errorf("jpegQualities = %v, want %v", jpegQualities, want)
	}
}

// Every CJK range boundary: endpoints are CJK, the neighbours are not.
func TestIsCJKRune_RangeBoundaries(t *testing.T) {
	t.Parallel()
	ranges := [][2]rune{
		{0x4E00, 0x9FFF}, {0x3400, 0x4DBF}, {0x3000, 0x303F}, {0xFF00, 0xFFEF},
		{0x30A0, 0x30FF}, {0x2E80, 0x2EFF}, {0x31C0, 0x31EF}, {0x3200, 0x32FF},
		{0x3300, 0x33FF}, {0xAC00, 0xD7AF}, {0x1100, 0x11FF}, {0x3130, 0x318F},
		{0xA960, 0xA97F}, {0xD7B0, 0xD7FF},
	}
	for _, r := range ranges {
		for _, in := range []rune{r[0], r[1], (r[0] + r[1]) / 2} {
			if !isCJKRune(in) {
				t.Errorf("isCJKRune(%#X) = false, want true (range %#X-%#X)", in, r[0], r[1])
			}
		}
		for _, out := range []rune{r[0] - 1, r[1] + 1} {
			// Adjacent ranges touch; only neighbours outside every range
			// are guaranteed non-CJK.
			covered := false
			for _, other := range ranges {
				if out >= other[0] && out <= other[1] {
					covered = true
					break
				}
			}
			if covered {
				continue
			}
			if isCJKRune(out) {
				t.Errorf("isCJKRune(%#X) = true, want false (just outside %#X-%#X)", out, r[0], r[1])
			}
		}
	}
	for _, out := range []rune{'a', '0', ' ', '\n', 0x00E9} {
		if isCJKRune(out) {
			t.Errorf("isCJKRune(%#X) = true, want false", out)
		}
	}
}
