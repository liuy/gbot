package utils

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// TestApplyFinalFallback_SquareInput exercises the unreachable-from-public-API
// 1000px+q20 final fallback (TS imageResizer.ts:348-371). A 2000x2000 noise
// RGBA image is the canonical input shape — the function is only reachable
// from MaybeResizeAndDownsampleImageBuffer when a 2000x2000 image's post-resize
// bytes still exceed IMAGE_TARGET_RAW_SIZE after the [80,60,40,20] JPEG loop,
// which cannot happen for real images. We call the unexported helper directly.
func TestApplyFinalFallback_SquareInput(t *testing.T) {
	t.Parallel()
	src := noiseRGBA(500, 500, 1)
	res := applyFinalFallback(src, 500, 500, 500, 500)
	if res.MediaType != "jpeg" {
		t.Errorf("MediaType = %q, want jpeg", res.MediaType)
	}
	if res.Dimensions == nil {
		t.Fatal("Dimensions nil")
	}
	if res.Dimensions.DisplayWidth != 500 {
		t.Errorf("DisplayWidth = %d, want 500 (smaller than 1000, no shrink)", res.Dimensions.DisplayWidth)
	}
	if res.Dimensions.DisplayHeight != 500 {
		t.Errorf("DisplayHeight = %d, want 500", res.Dimensions.DisplayHeight)
	}
	if res.Dimensions.OriginalWidth != 500 || res.Dimensions.OriginalHeight != 500 {
		t.Errorf("Original = %dx%d, want 500x500",
			res.Dimensions.OriginalWidth, res.Dimensions.OriginalHeight)
	}
	if len(res.Buffer) == 0 {
		t.Fatal("Buffer empty")
	}
	if len(res.Buffer) > IMAGE_TARGET_RAW_SIZE {
		t.Errorf("Buffer = %d bytes, want <= %d", len(res.Buffer), IMAGE_TARGET_RAW_SIZE)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(res.Buffer))
	if err != nil {
		t.Fatalf("re-decode failed: %v", err)
	}
	if cfg.Width != 500 || cfg.Height != 500 {
		t.Errorf("decoded = %dx%d, want 500x500", cfg.Width, cfg.Height)
	}
}

// TestApplyFinalFallback_AspectRatio uses a non-square input (1500x750) so
// the (height * smallerWidth / max(width,1)) computation produces a non-trivial
// result: smallerHeight = round(750 * 1000 / 1500) = 500.
func TestApplyFinalFallback_AspectRatio(t *testing.T) {
	t.Parallel()
	src := noiseRGBA(1500, 750, 2)
	res := applyFinalFallback(src, 1500, 750, 1500, 750)
	if res.Dimensions.DisplayWidth != 1000 {
		t.Errorf("DisplayWidth = %d, want 1000", res.Dimensions.DisplayWidth)
	}
	if res.Dimensions.DisplayHeight != 500 {
		t.Errorf("DisplayHeight = %d, want 500 (aspect-ratio)", res.Dimensions.DisplayHeight)
	}
}

// TestApplyFinalFallback_Quality is a smoke check on JPEG quality: the q20
// output for a 1200x1200 noise image should be substantially smaller than a
// q90 encoding of the same image. Catches regressions where the constant is
// accidentally changed.
func TestApplyFinalFallback_Quality(t *testing.T) {
	t.Parallel()
	src := noiseRGBA(1200, 1200, 3)
	res := applyFinalFallback(src, 1200, 1200, 1200, 1200)
	scaled := scaleImage(src, 1000, 1000)
	var hi bytes.Buffer
	if err := jpeg.Encode(&hi, scaled, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("jpeg.Encode q90: %v", err)
	}
	if len(res.Buffer) >= len(hi.Bytes()) {
		t.Errorf("q20 len = %d, q90 len = %d — q20 should be smaller",
			len(res.Buffer), len(hi.Bytes()))
	}
}

// TestApplyFinalFallback_WidthZero exercises the denom<1 guard inside
// applyFinalFallback (width=0 is unreachable from the public pipeline, which
// always constrains width to ≥ ImageMaxWidth before calling, but the guard
// exists to mirror TS's Math.max(width, 1)).
func TestApplyFinalFallback_WidthZero(t *testing.T) {
	t.Parallel()
	src := solidRGBA(1, 1, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	res := applyFinalFallback(src, 0, 10, 0, 10)
	if res.Dimensions.DisplayWidth != 0 {
		t.Errorf("DisplayWidth = %d, want 0 (min(0,1000))", res.Dimensions.DisplayWidth)
	}
	if res.Dimensions.DisplayHeight != 0 {
		t.Errorf("DisplayHeight = %d, want 0", res.Dimensions.DisplayHeight)
	}
}

// TestEncodeHelpers exercises the unexported PNG/JPEG encode helpers directly
// so their success paths get coverage even when the public pipeline doesn't
// reach every one of them with real images.
func TestEncodeHelpers(t *testing.T) {
	t.Parallel()
	src := noiseRGBA(500, 500, 11)

	t.Run("tryEncodePngPalettizedFromBytes", func(t *testing.T) {
		t.Parallel()
		raw := encodePNG(t, src)
		out, ok := tryEncodePngPalettizedFromBytes(raw)
		if !ok {
			t.Fatal("ok = false, want true")
		}
		if len(out) == 0 {
			t.Fatal("output empty")
		}
		reDecode(t, out, "tryEncodePngPalettizedFromBytes")
		if _, ok := tryEncodePngPalettizedFromBytes([]byte{0x00, 0x01}); ok {
			t.Error("garbage input returned ok=true")
		}
	})

	t.Run("tryEncodeJpegFromBytes", func(t *testing.T) {
		t.Parallel()
		raw := encodeJPEG(t, src, 80)
		out, ok := tryEncodeJpegFromBytes(raw, 60)
		if !ok {
			t.Fatal("ok = false, want true")
		}
		if len(out) == 0 {
			t.Fatal("output empty")
		}
		reDecode(t, out, "tryEncodeJpegFromBytes")
		if _, ok := tryEncodeJpegFromBytes([]byte{0x00, 0x01}, 60); ok {
			t.Error("garbage input returned ok=true")
		}
	})

	t.Run("encodePngPalettizedImg", func(t *testing.T) {
		t.Parallel()
		out, ok := encodePngPalettizedImg(src)
		if !ok {
			t.Fatal("ok = false, want true")
		}
		if len(out) == 0 {
			t.Fatal("output empty")
		}
		reDecode(t, out, "encodePngPalettizedImg")
	})

	t.Run("encodeJpegImg", func(t *testing.T) {
		t.Parallel()
		out, ok := encodeJpegImg(src, 70)
		if !ok {
			t.Fatal("ok = false, want true")
		}
		if len(out) == 0 {
			t.Fatal("output empty")
		}
		reDecode(t, out, "encodeJpegImg")
	})

	t.Run("encodeInSourceFormat_JPEG", func(t *testing.T) {
		t.Parallel()
		out, err := encodeInSourceFormat(src, "jpeg")
		if err != nil {
			t.Fatalf("encodeInSourceFormat jpeg: %v", err)
		}
		cfg, _, derr := image.DecodeConfig(bytes.NewReader(out))
		if derr != nil {
			t.Fatalf("re-decode: %v", derr)
		}
		if cfg.Width != 500 {
			t.Errorf("decoded width = %d, want 500", cfg.Width)
		}
	})

	t.Run("encodeInSourceFormat_PNG", func(t *testing.T) {
		t.Parallel()
		out, err := encodeInSourceFormat(src, "png")
		if err != nil {
			t.Fatalf("encodeInSourceFormat png: %v", err)
		}
		cfg, _, derr := image.DecodeConfig(bytes.NewReader(out))
		if derr != nil {
			t.Fatalf("re-decode: %v", derr)
		}
		if cfg.Width != 500 {
			t.Errorf("decoded width = %d, want 500", cfg.Width)
		}
	})
}

// TestImageResizeError_ErrorMethod exercises the Error() method directly
// (covered indirectly nowhere else — only the message substring is checked
// elsewhere via errors.As).
func TestImageResizeError_ErrorMethod(t *testing.T) {
	t.Parallel()
	e := NewImageResizeError("boom")
	if got := e.Error(); got != "boom" {
		t.Errorf("Error() = %q, want \"boom\"", got)
	}
}

// TestMaybeResize_HeightOverCapOnly exercises the height>ImageMaxHeight
// resize branch (width ≤ cap, height > cap) — TestMaybeResize_DimensionsOnePxOverCap
// only hits the width path.
func TestMaybeResize_HeightOverCapOnly(t *testing.T) {
	t.Parallel()
	// 1000x2500: width under cap, height over cap.
	img := solidRGBA(1000, 2500, color.RGBA{R: 7, G: 8, B: 9, A: 255})
	buf := encodePNG(t, img)
	res, err := MaybeResizeAndDownsampleImageBuffer(buf, len(buf), "png")
	if err != nil {
		t.Fatalf("MaybeResize: %v", err)
	}
	w, h := reDecode(t, res.Buffer, "HeightOverCapOnly")
	if w != 800 || h != 2000 {
		// First width check (1000 ≤ 2000) skips; height check fires:
		//   width = round(1000 * 2000 / 2500) = 800, height = 2000.
		t.Errorf("decoded = %dx%d, want 800x2000", w, h)
	}
}

// TestMaybeResize_DecodeFailureAfterConfigSuccess covers the
// `image.Decode` failure arm inside MaybeResizeAndDownsampleImageBuffer —
// the path that fires when DecodeConfig succeeds but Decode fails AND the
// all-good early-return is bypassed (so execution reaches the resize block
// where Decode is called).
//
// Strategy: a JPEG whose header is intact (DecodeConfig OK) but body is
// truncated (Decode fails). Dimensions are 2500x2500 — over cap → all-good
// is bypassed → dimension-resize math runs → image.Decode is invoked →
// fails → handleResizeError. Since the buffer is not PNG-magic, the PNG
// IHDR overDim check is false; with size under 5MB, the catch passes
// the truncated buffer through.
func TestMaybeResize_DecodeFailureAfterConfigSuccess(t *testing.T) {
	t.Parallel()
	img := solidRGBA(2500, 2500, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var fullBuf bytes.Buffer
	if err := jpeg.Encode(&fullBuf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	full := fullBuf.Bytes()
	// Truncate the body. JPEG SOI+markers+frame header occupy roughly the
	// first 1000 bytes; cutting at 1500 leaves dimensions parseable but no
	// scan data.
	cut := 1500
	if cut >= len(full) {
		t.Skip("JPEG too small to truncate meaningfully")
	}
	truncated := full[:cut]
	cfg, _, err := image.DecodeConfig(bytes.NewReader(truncated))
	if err != nil {
		t.Skipf("DecodeConfig unexpectedly failed: %v", err)
	}
	if cfg.Width <= ImageMaxWidth {
		t.Skipf("decoded width = %d, want > %d to bypass all-good", cfg.Width, ImageMaxWidth)
	}
	if _, _, err := image.Decode(bytes.NewReader(truncated)); err == nil {
		t.Skip("Decode unexpectedly succeeded; failure path unreachable here")
	}
	res, err := MaybeResizeAndDownsampleImageBuffer(truncated, len(truncated), "jpeg")
	if err != nil {
		t.Fatalf("MaybeResize: %v", err)
	}
	// Catch path passes the truncated buffer through (non-PNG magic,
	// size well under 5MB).
	if !bytes.Equal(res.Buffer, truncated) {
		t.Errorf("Buffer mutated: got %d bytes, want %d", len(res.Buffer), len(truncated))
	}
}

// TestNormalizeMediaType covers all three branches of normalizeMediaType.
// The empty-format and "jpg" branches are unreachable through the public
// pipeline (image.DecodeConfig always returns a non-empty canonical format)
// but mirror TS `metadata.format ?? ext` + the jpg→jpeg normalization.
func TestNormalizeMediaType(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		format string
		ext    string
		want   string
	}{
		{"format-jpeg", "jpeg", "png", "jpeg"},
		{"format-png", "png", "jpg", "png"},
		{"empty-format-falls-back-to-ext", "", "gif", "gif"},
		{"empty-format-ext-jpg-normalized", "", "jpg", "jpeg"},
		{"format-jpg-normalized", "jpg", "png", "jpeg"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeMediaType(tc.format, tc.ext)
			if got != tc.want {
				t.Errorf("normalizeMediaType(%q, %q) = %q, want %q",
					tc.format, tc.ext, got, tc.want)
			}
		})
	}
}

// TestCompressAfterResize_PngPaletteSucceeds covers the PNG palette arm of
// compressAfterResize — feed a small solid-color image whose palettized PNG
// fits under cap so the first branch returns.
func TestCompressAfterResize_PngPaletteSucceeds(t *testing.T) {
	t.Parallel()
	src := solidRGBA(200, 200, color.RGBA{R: 50, G: 100, B: 150, A: 255})
	res := compressAfterResize(src, 200, 200, 200, 200, true)
	if res.MediaType != "png" {
		t.Errorf("MediaType = %q, want png (palette path)", res.MediaType)
	}
	reDecode(t, res.Buffer, "CompressAfterResize_PngPaletteSucceeds")
}

// TestCompressAfterResize_JpegLoop covers the JPEG-loop arm — feed a JPEG
// (isPng=false) so the palette path is skipped and the JPEG loop runs.
func TestCompressAfterResize_JpegLoop(t *testing.T) {
	t.Parallel()
	src := noiseRGBA(800, 800, 5)
	res := compressAfterResize(src, 800, 800, 800, 800, false)
	if res.MediaType != "jpeg" {
		t.Errorf("MediaType = %q, want jpeg (loop or fallback)", res.MediaType)
	}
	if len(res.Buffer) > IMAGE_TARGET_RAW_SIZE {
		t.Errorf("Buffer = %d, want <= %d", len(res.Buffer), IMAGE_TARGET_RAW_SIZE)
	}
	reDecode(t, res.Buffer, "CompressAfterResize_JpegLoop")
}

// TestApplyFinalFallback_DirectCall exercises the final-resort branch
// (1000px + JPEG q20) directly via applyFinalFallback. This branch is
// mathematically unreachable from the public API for real ≤2000x2000
// inputs (q80 JPEG of such images is well under cap), so we call the
// internal helper with a small image to cover its behavior without
// spending 50s on JPEG encodes of 20MP procedural noise.
func TestApplyFinalFallback_DirectCall(t *testing.T) {
	t.Parallel()
	src := newProceduralNoise(400, 400, 13)
	res := applyFinalFallback(src, 400, 400, 800, 800)
	if res.MediaType != "jpeg" {
		t.Errorf("MediaType = %q, want jpeg (final fallback)", res.MediaType)
	}
	if res.Dimensions == nil {
		t.Fatal("Dimensions nil")
	}
	if res.Dimensions.DisplayWidth != 400 {
		t.Errorf("DisplayWidth = %d, want 400 (smaller than 1000, no further shrink)", res.Dimensions.DisplayWidth)
	}
	if res.Dimensions.DisplayHeight != 400 {
		t.Errorf("DisplayHeight = %d, want 400", res.Dimensions.DisplayHeight)
	}
	if res.Dimensions.OriginalWidth != 800 {
		t.Errorf("OriginalWidth = %d, want 800", res.Dimensions.OriginalWidth)
	}
	if len(res.Buffer) > IMAGE_TARGET_RAW_SIZE {
		t.Errorf("Buffer = %d, want <= %d", len(res.Buffer), IMAGE_TARGET_RAW_SIZE)
	}
	reDecode(t, res.Buffer, "ApplyFinalFallback_DirectCall")
}

// proceduralNoise is an image.Image whose pixels are deterministic
// pseudo-noise computed on demand from (x, y, seed). Avoids allocating a
// backing buffer for very large test images.
type proceduralNoise struct {
	w, h int
	seed int64
}

func newProceduralNoise(w, h int, seed int64) *proceduralNoise {
	return &proceduralNoise{w: w, h: h, seed: seed}
}

func (p *proceduralNoise) ColorModel() color.Model { return color.RGBAModel }

func (p *proceduralNoise) Bounds() image.Rectangle {
	return image.Rect(0, 0, p.w, p.h)
}

func (p *proceduralNoise) At(x, y int) color.Color {
	// cheap deterministic hash — no per-pixel rand allocation.
	h := uint32(x)*73856093 ^ uint32(y)*19349663 ^ uint32(p.seed)*83492791
	return color.RGBA{
		R: uint8(h),
		G: uint8(h >> 8),
		B: uint8(h >> 16),
		A: 255,
	}
}
