package utils

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"strings"
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
	res := compressAfterResize(src, 200, 200, 200, 200, true, IMAGE_TARGET_RAW_SIZE)
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
	res := compressAfterResize(src, 800, 800, 800, 800, false, IMAGE_TARGET_RAW_SIZE)
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

// TestCompressAfterResize_LoopKeepsDims pins that a JPEG-loop acceptance
// returns the input dimensions: a mutant inverting the size comparison
// (`ok && len > cap`) skips every loop step and lands on the 1000px final
// fallback, whose DisplayWidth (1000) differs from the loop's (1200).
func TestCompressAfterResize_LoopKeepsDims(t *testing.T) {
	t.Parallel()
	src := noiseRGBA(1200, 1200, 5)
	res := compressAfterResize(src, 1200, 1200, 1200, 1200, false, IMAGE_TARGET_RAW_SIZE)
	if res.MediaType != "jpeg" {
		t.Errorf("MediaType = %q, want jpeg", res.MediaType)
	}
	if res.Dimensions == nil {
		t.Fatal("Dimensions nil")
	}
	if res.Dimensions.DisplayWidth != 1200 || res.Dimensions.DisplayHeight != 1200 {
		t.Errorf("Display = %dx%d, want 1200x1200 (loop acceptance, not the 1000px fallback)",
			res.Dimensions.DisplayWidth, res.Dimensions.DisplayHeight)
	}
	reDecode(t, res.Buffer, "CompressAfterResize_LoopKeepsDims")
}

// refPalettizedPNG / refJpegBytes / refPngDefault re-encode through the stdlib
// directly, so a production change to an encoder option (compression level,
// JPEG quality) shows up as a byte difference instead of silently tracking it.
func refPalettizedPNG(t *testing.T, src image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, quantizeToPlan9(src)); err != nil {
		t.Fatalf("reference palettized encode: %v", err)
	}
	return buf.Bytes()
}

func refJpegBytes(t *testing.T, src image.Image, quality int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("reference jpeg encode: %v", err)
	}
	return buf.Bytes()
}

func refPngDefault(t *testing.T, src image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("reference png encode: %v", err)
	}
	return buf.Bytes()
}

// entropyImage is incompressible, so its encoded size exceeds the palettized /
// lower-quality re-encodes — the precondition the compression branch needs.
func entropyImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(3))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(rng.Intn(256)), G: uint8(rng.Intn(256)), B: uint8(rng.Intn(256)), A: 255})
		}
	}
	return img
}

func entropyPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, entropyImage(w, h)); err != nil {
		t.Fatalf("entropy png encode: %v", err)
	}
	return buf.Bytes()
}

func entropyJPEG(t *testing.T, w, h, quality int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, entropyImage(w, h), &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("entropy jpeg encode: %v", err)
	}
	return buf.Bytes()
}

func refDecode(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode reference image: %v", err)
	}
	return img
}

// The magic-byte gates must test every byte they claim: a JPEG whose fourth
// byte happens to be the PNG terminator is still a JPEG, and the length gate
// must reject buffers shorter than four bytes without indexing them.
func TestDetectImageFormatFromBuffer_MagicPrecedenceAndShortBuffers(t *testing.T) {
	t.Parallel()
	if got := DetectImageFormatFromBuffer([]byte{0xff, 0xd8, 0xff, 0x47, 0x00, 0x00}); got != "image/jpeg" {
		t.Errorf("DetectImageFormatFromBuffer(FF D8 FF 47) = %q, want image/jpeg (the PNG gate must not match on a subset of its four bytes)", got)
	}
	for _, buf := range [][]byte{nil, {0x89}, {0x89, 0x50}, {0x89, 0x50, 0x4e}} {
		if got := DetectImageFormatFromBuffer(buf); got != "image/png" {
			t.Errorf("DetectImageFormatFromBuffer(len %d) = %q, want the image/png default", len(buf), got)
		}
	}
}

// pngCatchHeader builds a buffer that reaches handleResizeError's IHDR gate:
// PNG magic at 0-3, IHDR width at 16-19, height at 20-23, total length total.
func pngCatchHeader(magic [4]byte, w, h uint32, total int) []byte {
	buf := make([]byte, total)
	copy(buf, magic[:])
	if total >= 8 {
		copy(buf[4:8], []byte{0x0d, 0x0a, 0x1a, 0x0a})
	}
	if total >= 16 {
		binary.BigEndian.PutUint32(buf[8:12], 13)
		copy(buf[12:16], []byte("IHDR"))
	}
	if total >= 20 {
		binary.BigEndian.PutUint32(buf[16:20], w)
	}
	if total >= 24 {
		binary.BigEndian.PutUint32(buf[20:24], h)
	}
	return buf
}

// The catch-path over-dimension gate must be exact in buffer length, in each of
// the four PNG signature bytes, in each IHDR offset and in the strict > against
// the 2000x2000 cap: anything looser turns a passthrough into the
// "dimensions exceed" error (or panics on a short slice).
func TestHandleResizeError_PNGHeaderGateExact(t *testing.T) {
	t.Parallel()
	pngSig := [4]byte{0x89, 0x50, 0x4e, 0x47}
	cases := []struct {
		name    string
		buf     []byte
		wantErr bool
	}{
		{"width-over", pngCatchHeader(pngSig, 3000, 1000, 24), true},
		{"height-over", pngCatchHeader(pngSig, 1000, 3000, 24), true},
		{"both-under", pngCatchHeader(pngSig, 1000, 1000, 24), false},
		{"width-exactly-cap", pngCatchHeader(pngSig, 2000, 1000, 24), false},
		{"height-exactly-cap", pngCatchHeader(pngSig, 1000, 2000, 24), false},
		{"sig-byte0-broken", pngCatchHeader([4]byte{0x00, 0x50, 0x4e, 0x47}, 3000, 3000, 24), false},
		{"sig-byte1-broken", pngCatchHeader([4]byte{0x89, 0x00, 0x4e, 0x47}, 3000, 3000, 24), false},
		{"sig-byte2-broken", pngCatchHeader([4]byte{0x89, 0x50, 0x00, 0x47}, 3000, 3000, 24), false},
		{"sig-byte3-broken", pngCatchHeader([4]byte{0x89, 0x50, 0x4e, 0x00}, 3000, 3000, 24), false},
		{"short-23-width-over", pngCatchHeader(pngSig, 3000, 1000, 23), false},
		{"short-23-width-under", pngCatchHeader(pngSig, 1000, 1000, 23), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := MaybeResizeAndDownsampleImageBufferWithBudget(tc.buf, 5000, "png", 100)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("err = %v, want the passthrough result for a %d-byte buffer", err, len(tc.buf))
				}
				if !bytes.Equal(res.Buffer, tc.buf) {
					t.Errorf("Buffer = %d bytes, want the original %d bytes passed through", len(res.Buffer), len(tc.buf))
				}
				if res.MediaType != "png" || res.Dimensions != nil {
					t.Errorf("passthrough = (%q,%v), want (\"png\",nil dimensions)", res.MediaType, res.Dimensions)
				}
				return
			}
			var re *ImageResizeError
			if !errors.As(err, &re) {
				t.Fatalf("err = %v, want *ImageResizeError", err)
			}
			if !strings.Contains(re.Msg, "dimensions exceed the 2000x2000px limit") {
				t.Errorf("Msg = %q, want the over-dimension envelope", re.Msg)
			}
		})
	}
}

// scaleImage must actually scale: a solid-color source stays solid, and the
// rounding term in the aspect-ratio clamp is the one TS used (height/2).
func TestScaleImage_ActuallyScales(t *testing.T) {
	t.Parallel()
	src := solidRGBA(4, 4, color.RGBA{R: 255, A: 255})
	dst := scaleImage(src, 2, 2)
	for y := range 2 {
		for x := range 2 {
			r, g, b, a := dst.At(x, y).RGBA()
			if r != 0xffff || g != 0 || b != 0 || a != 0xffff {
				t.Fatalf("scaled pixel (%d,%d) = (%#x,%#x,%#x,%#x), want solid red (the scaler call is load-bearing)", x, y, r, g, b, a)
			}
		}
	}
}

// quantizeToPlan9 must actually draw: Plan9 index 0 is black, so an undrawn
// paletted image is indistinguishable from a black one.
func TestQuantizeToPlan9_ActuallyDraws(t *testing.T) {
	t.Parallel()
	pm := quantizeToPlan9(solidRGBA(4, 4, color.RGBA{R: 255, A: 255}))
	r, g, b, _ := pm.Palette[pm.ColorIndexAt(0, 0)].RGBA()
	if r != 0xffff || g != 0 || b != 0 {
		t.Errorf("quantized color = (%#x,%#x,%#x), want pure red (FloydSteinberg.Draw is load-bearing)", r, g, b)
	}
}

// Every encoder option in the ladder is pinned to a stdlib reference encode:
// dropping CompressionLevel or Quality, or nudging 80/20, changes the bytes.
func TestEncoderOptionsMatchReferenceBytes(t *testing.T) {
	t.Parallel()
	src := noiseRGBA(64, 48, 5)
	if got, ok := encodePngPalettizedImg(src); !ok || !bytes.Equal(got, refPalettizedPNG(t, src)) {
		t.Errorf("encodePngPalettizedImg = %d bytes, want the BestCompression reference", len(got))
	}
	if got, ok := encodeJpegImg(src, 55); !ok || !bytes.Equal(got, refJpegBytes(t, src, 55)) {
		t.Errorf("encodeJpegImg(q55) = %d bytes, want the q55 reference", len(got))
	}
	pngBytes := pinPNG(t, 64, 48)
	if got, ok := tryEncodePngPalettizedFromBytes(pngBytes); !ok || !bytes.Equal(got, refPalettizedPNG(t, refDecode(t, pngBytes))) {
		t.Errorf("tryEncodePngPalettizedFromBytes = %d bytes, want the BestCompression reference", len(got))
	}
	jpegBytes := pinJPEG(t, 64, 48)
	if got, ok := tryEncodeJpegFromBytes(jpegBytes, 33); !ok || !bytes.Equal(got, refJpegBytes(t, refDecode(t, jpegBytes), 33)) {
		t.Errorf("tryEncodeJpegFromBytes(q33) = %d bytes, want the q33 reference", len(got))
	}
	if got, err := encodeInSourceFormat(src, "jpeg"); err != nil || !bytes.Equal(got, refJpegBytes(t, src, 80)) {
		t.Errorf("encodeInSourceFormat(jpeg) = %d bytes, want the sharp-default q80 reference", len(got))
	}
	if got, err := encodeInSourceFormat(src, "png"); err != nil || !bytes.Equal(got, refPngDefault(t, src)) {
		t.Errorf("encodeInSourceFormat(png) = %d bytes, want the default png reference", len(got))
	}
	res := applyFinalFallback(src, 1200, 900, 1200, 900)
	want := refJpegBytes(t, scaleImage(src, 1000, 750), 20)
	if !bytes.Equal(res.Buffer, want) {
		t.Errorf("applyFinalFallback buffer = %d bytes, want the q20 reference %d bytes", len(res.Buffer), len(want))
	}
	if res.Dimensions.DisplayWidth != 1000 || res.Dimensions.DisplayHeight != 750 {
		t.Errorf("applyFinalFallback display = %dx%d, want 1000x750", res.Dimensions.DisplayWidth, res.Dimensions.DisplayHeight)
	}
}

// applyFinalFallback's denominator guard and rounding term are observable only
// through the returned dimensions: max(width,1) keeps width=1 exact, and
// denom/2 rounds 8x5 to 2 rows rather than 1.
func TestApplyFinalFallback_Rounding(t *testing.T) {
	t.Parallel()
	src := solidRGBA(8, 8, color.RGBA{G: 200, A: 255})
	res := applyFinalFallback(src, 1, 100, 1, 100)
	if res.Dimensions.DisplayWidth != 1 || res.Dimensions.DisplayHeight != 100 {
		t.Errorf("width=1: display = %dx%d, want 1x100 (max(width,1) must not widen the denominator)", res.Dimensions.DisplayWidth, res.Dimensions.DisplayHeight)
	}
	res = applyFinalFallback(src, 1600, 1, 1600, 1)
	if res.Dimensions.DisplayWidth != 1000 || res.Dimensions.DisplayHeight != 1 {
		t.Errorf("width=1600,height=1: display = %dx%d, want 1000x1 (the rounding term is denom/2)", res.Dimensions.DisplayWidth, res.Dimensions.DisplayHeight)
	}
}

// compressAfterResize returns the strategy's bytes and the caller's dimensions
// verbatim; the budget comparison is <=, so a budget equal to the encoded size
// still takes that strategy.
func TestCompressAfterResize_ExactBudgetAndFields(t *testing.T) {
	t.Parallel()
	src := noiseRGBA(120, 90, 11)
	palet := refPalettizedPNG(t, src)
	res := compressAfterResize(src, 120, 90, 900, 700, true, len(palet))
	if !bytes.Equal(res.Buffer, palet) || res.MediaType != "png" {
		t.Errorf("palettized path = (%q,%d bytes), want the palettized %d bytes", res.MediaType, len(res.Buffer), len(palet))
	}
	if res.Dimensions == nil {
		t.Fatal("Dimensions = nil, want the threaded dimensions")
	}
	if *res.Dimensions != (ImageDimensions{OriginalWidth: 900, OriginalHeight: 700, DisplayWidth: 120, DisplayHeight: 90}) {
		t.Errorf("Dimensions = %+v, want Original 900x700 / Display 120x90", *res.Dimensions)
	}
	j80 := refJpegBytes(t, src, 80)
	res = compressAfterResize(src, 120, 90, 900, 700, false, len(j80))
	if !bytes.Equal(res.Buffer, j80) || res.MediaType != "jpeg" {
		t.Errorf("jpeg loop = (%q,%d bytes), want the q80 reference %d bytes", res.MediaType, len(res.Buffer), len(j80))
	}
}

// The pre-resize compression branch returns the strategy bytes and the
// unresized dimensions; budgets set exactly at the encoded size must still fit.
func TestResizeBuffer_CompressionBranchFields(t *testing.T) {
	t.Parallel()
	pngBytes := entropyPNG(t, 300, 200)
	img := refDecode(t, pngBytes)
	palet := refPalettizedPNG(t, img)
	res, err := MaybeResizeAndDownsampleImageBufferWithBudget(pngBytes, len(pngBytes), "png", len(palet))
	if err != nil {
		t.Fatalf("palettized branch: %v", err)
	}
	if !bytes.Equal(res.Buffer, palet) || res.MediaType != "png" {
		t.Errorf("palettized branch = (%q,%d bytes), want the palettized %d bytes", res.MediaType, len(res.Buffer), len(palet))
	}
	if *res.Dimensions != (ImageDimensions{OriginalWidth: 300, OriginalHeight: 200, DisplayWidth: 300, DisplayHeight: 200}) {
		t.Errorf("Dimensions = %+v, want 300x200 unresized in both pairs", *res.Dimensions)
	}
	jpegBytes := entropyJPEG(t, 300, 200, 90)
	jimg := refDecode(t, jpegBytes)
	j80 := refJpegBytes(t, jimg, 80)
	res, err = MaybeResizeAndDownsampleImageBufferWithBudget(jpegBytes, len(jpegBytes), "jpg", len(j80))
	if err != nil {
		t.Fatalf("jpeg branch: %v", err)
	}
	if !bytes.Equal(res.Buffer, j80) || res.MediaType != "jpeg" {
		t.Errorf("jpeg branch = (%q,%d bytes), want the q80 reference %d bytes", res.MediaType, len(res.Buffer), len(j80))
	}
	if *res.Dimensions != (ImageDimensions{OriginalWidth: 300, OriginalHeight: 200, DisplayWidth: 300, DisplayHeight: 200}) {
		t.Errorf("Dimensions = %+v, want 300x200 unresized in both pairs", *res.Dimensions)
	}
}

// needsDimensionResize is an OR over the two caps with strict >, the
// compression branch is gated on it, and the post-resize comparison is strict >.
func TestResizeBuffer_DimensionGateExact(t *testing.T) {
	t.Parallel()
	// Only the width is over cap: the resize path must clamp it to 2000.
	wide := entropyPNG(t, 3000, 100)
	wideImg := refDecode(t, wide)
	res, err := MaybeResizeAndDownsampleImageBufferWithBudget(wide, len(wide)+1, "png", len(refPalettizedPNG(t, wideImg)))
	if err != nil {
		t.Fatalf("width-over: %v", err)
	}
	if res.Dimensions.DisplayWidth != 2000 || res.Dimensions.DisplayHeight != 67 {
		t.Errorf("width-over display = %dx%d, want 2000x67 (needsDimensionResize is an OR)", res.Dimensions.DisplayWidth, res.Dimensions.DisplayHeight)
	}
	// Only the height is over cap.
	tall := entropyPNG(t, 100, 3000)
	tallImg := refDecode(t, tall)
	res, err = MaybeResizeAndDownsampleImageBufferWithBudget(tall, len(tall)+1, "png", len(refPalettizedPNG(t, tallImg)))
	if err != nil {
		t.Fatalf("height-over: %v", err)
	}
	if res.Dimensions.DisplayWidth != 67 || res.Dimensions.DisplayHeight != 2000 {
		t.Errorf("height-over display = %dx%d, want 67x2000 (needsDimensionResize is an OR)", res.Dimensions.DisplayWidth, res.Dimensions.DisplayHeight)
	}
	// width/height exactly at the cap are EQUIV mutants: the clamp at 172/176 is
	// strict > too, so the "resize" path runs the same ladder on the same pixels.
	// 1000x2001: the clamp's rounding term (height/2) keeps width at 1000.
	tall2001 := pinPNG(t, 1000, 2001)
	res, err = MaybeResizeAndDownsampleImageBufferWithBudget(tall2001, len(tall2001)+1, "png", 10<<20)
	if err != nil {
		t.Fatalf("2001-tall: %v", err)
	}
	if res.Dimensions.DisplayWidth != 1000 || res.Dimensions.DisplayHeight != 2000 {
		t.Errorf("2001-tall display = %dx%d, want 1000x2000 (the rounding term is height/2)", res.Dimensions.DisplayWidth, res.Dimensions.DisplayHeight)
	}
	// Budget equal to the post-resize encode: the strict > keeps that buffer.
	scaled := scaleImage(refDecode(t, tall2001), 1000, 2000)
	resizedPNG := refPngDefault(t, scaled)
	res, err = MaybeResizeAndDownsampleImageBufferWithBudget(tall2001, len(tall2001)+1, "png", len(resizedPNG))
	if err != nil {
		t.Fatalf("exact post-resize budget: %v", err)
	}
	if !bytes.Equal(res.Buffer, resizedPNG) {
		t.Errorf("exact post-resize budget buffer = %d bytes, want the resized png %d bytes", len(res.Buffer), len(resizedPNG))
	}
}

// Pin tests for image_resizer.go mutation sweep; assertions are exact.

func pinPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 0x80, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func pinJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: 0x40, G: uint8((x + y) % 256), B: 0xc0, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPinResizer_Constants(t *testing.T) {
	if API_IMAGE_MAX_BASE64_SIZE != 5*1024*1024 {
		t.Errorf("API_IMAGE_MAX_BASE64_SIZE = %d", API_IMAGE_MAX_BASE64_SIZE)
	}
	if IMAGE_TARGET_RAW_SIZE != (5*1024*1024*3)/4 {
		t.Errorf("IMAGE_TARGET_RAW_SIZE = %d", IMAGE_TARGET_RAW_SIZE)
	}
	if ImageMaxWidth != 2000 || ImageMaxHeight != 2000 {
		t.Errorf("limits = %dx%d, want 2000x2000", ImageMaxWidth, ImageMaxHeight)
	}
	if jpegQualities != [4]int{80, 60, 40, 20} {
		t.Errorf("jpegQualities = %v", jpegQualities)
	}
}

func TestPinResizer_EmptyBuffer(t *testing.T) {
	_, err := MaybeResizeAndDownsampleImageBuffer(nil, 0, "png")
	if err == nil || !strings.Contains(err.Error(), "Image file is empty") {
		t.Errorf("err = %v, want the empty-buffer error", err)
	}
}

// Boundary passthrough: size and both dimensions at exactly the caps.
func TestPinResizer_PassthroughAtCaps(t *testing.T) {
	buf := pinPNG(t, 2000, 2000)
	res, err := MaybeResizeAndDownsampleImageBufferWithBudget(buf, 3932160, "png", 3932160)
	if err != nil {
		t.Fatalf("boundary input errored: %v", err)
	}
	if &res.Buffer[0] != &buf[0] {
		t.Error("boundary input was not passed through by reference (same buffer expected)")
	}
	if res.MediaType != "png" {
		t.Errorf("MediaType = %q", res.MediaType)
	}
	if res.Dimensions == nil ||
		res.Dimensions.OriginalWidth != 2000 || res.Dimensions.OriginalHeight != 2000 ||
		res.Dimensions.DisplayWidth != 2000 || res.Dimensions.DisplayHeight != 2000 {
		t.Errorf("Dimensions = %+v, want 2000x2000 throughout", res.Dimensions)
	}
}

// One byte over the size cap (dimensions fine) enters the compression path:
// the palettized refit at EXACTLY the budget wins (<= boundary).
func TestPinResizer_SizeOverCompresses(t *testing.T) {
	buf := pinPNG(t, 64, 64)
	pal, ok := tryEncodePngPalettizedFromBytes(buf)
	if !ok {
		t.Fatal("palettized fixture encode failed")
	}
	if len(pal) >= len(buf) {
		t.Skipf("palettized %d not smaller than original %d for this fixture", len(pal), len(buf))
	}
	res, err := MaybeResizeAndDownsampleImageBufferWithBudget(buf, len(buf)+1, "png", len(pal))
	if err != nil {
		t.Fatalf("oversize png errored: %v", err)
	}
	if res.MediaType != "png" {
		t.Errorf("MediaType = %q, want png (palettized refit)", res.MediaType)
	}
	if !bytes.Equal(res.Buffer, pal) {
		t.Error("buffer is not the palettized refit that fits the budget exactly")
	}
	if res.Dimensions == nil || res.Dimensions.DisplayWidth != 64 || res.Dimensions.DisplayHeight != 64 ||
		res.Dimensions.OriginalWidth != 64 || res.Dimensions.OriginalHeight != 64 {
		t.Errorf("Dimensions = %+v, want 64x64", res.Dimensions)
	}
}

// Budget too small for any PNG refit → the JPEG ladder takes over.
func TestPinResizer_SizeOverFallsToJpeg(t *testing.T) {
	buf := pinPNG(t, 128, 128)
	bigBudget := len(buf) + 1024 // over palettized size? use a jpeg-friendly budget
	res, err := MaybeResizeAndDownsampleImageBufferWithBudget(buf, len(buf)*10, "png", bigBudget)
	if err != nil {
		t.Fatalf("ladder errored: %v", err)
	}
	if res.MediaType != "jpeg" || len(res.Buffer) > bigBudget {
		t.Errorf("generous budget: media=%q len=%d, want the jpeg ladder result within %d bytes", res.MediaType, len(res.Buffer), bigBudget)
	}
	// Deterministic kill: force the ladder by making the palettized refit
	// too big (budget between jpeg q80 size and palettized size).
	pal, _ := tryEncodePngPalettizedFromBytes(buf)
	j80, _ := tryEncodeJpegFromBytes(buf, 80)
	if len(j80) < len(pal) {
		budget := len(j80) // jpeg fits exactly, palettized does not
		res2, err2 := MaybeResizeAndDownsampleImageBufferWithBudget(buf, len(buf)*10, "png", budget)
		if err2 != nil {
			t.Fatalf("jpeg-ladder errored: %v", err2)
		}
		if res2.MediaType != "jpeg" {
			t.Errorf("MediaType = %q, want jpeg (palettized exceeded the budget)", res2.MediaType)
		}
		if len(res2.Buffer) > budget {
			t.Errorf("result %d bytes exceeds budget %d", len(res2.Buffer), budget)
		}
	}
}

// Aspect-preserving dimension resize with the exact integer rounding.
func TestPinResizer_DimensionResize(t *testing.T) {
	cases := []struct {
		name         string
		w, h         int
		wantW, wantH int
	}{
		{"wide odd", 2003, 1000, 2000, 999},
		{"wide even", 2004, 1000, 2000, 998},
		{"tall", 1000, 2003, 999, 2000},
		{"both", 2003, 2001, 2000, 1998},
		{"height-round", 2500, 1001, 2000, 801},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := pinPNG(t, tc.w, tc.h)
			// Big budget so the resized bytes fit and no compression runs.
			res, err := MaybeResizeAndDownsampleImageBufferWithBudget(buf, 1, "png", 1<<30)
			if err != nil {
				t.Fatalf("resize errored: %v", err)
			}
			if res.MediaType != "png" {
				t.Errorf("MediaType = %q, want png", res.MediaType)
			}
			d := res.Dimensions
			if d == nil {
				t.Fatal("Dimensions nil")
			}
			if d.OriginalWidth != tc.w || d.OriginalHeight != tc.h {
				t.Errorf("original = %dx%d, want %dx%d", d.OriginalWidth, d.OriginalHeight, tc.w, tc.h)
			}
			if d.DisplayWidth != tc.wantW || d.DisplayHeight != tc.wantH {
				t.Errorf("display = %dx%d, want %dx%d", d.DisplayWidth, d.DisplayHeight, tc.wantW, tc.wantH)
			}
			cfg, _, derr := image.DecodeConfig(bytes.NewReader(res.Buffer))
			if derr != nil {
				t.Fatalf("result not decodable: %v", derr)
			}
			if cfg.Width != tc.wantW || cfg.Height != tc.wantH {
				t.Errorf("encoded = %dx%d, want %dx%d", cfg.Width, cfg.Height, tc.wantW, tc.wantH)
			}
		})
	}
}

// Post-resize compression: budget forces the final 1000px q20 fallback.
func TestPinResizer_FinalFallback(t *testing.T) {
	buf := pinPNG(t, 3000, 8)
	res, err := MaybeResizeAndDownsampleImageBufferWithBudget(buf, 1, "png", 1)
	if err != nil {
		t.Fatalf("fallback errored: %v", err)
	}
	if res.MediaType != "jpeg" {
		t.Errorf("MediaType = %q, want jpeg", res.MediaType)
	}
	d := res.Dimensions
	if d == nil || d.OriginalWidth != 3000 || d.OriginalHeight != 8 {
		t.Errorf("original dims = %+v", d)
	}
	if d.DisplayWidth != 1000 || d.DisplayHeight != 3 {
		t.Errorf("display = %dx%d, want 1000x3", d.DisplayWidth, d.DisplayHeight)
	}
	cfg, _, derr := image.DecodeConfig(bytes.NewReader(res.Buffer))
	if derr != nil || cfg.Width != 1000 || cfg.Height != 3 {
		t.Errorf("encoded = %+v err=%v, want 1000x3", cfg, derr)
	}
}

// A jpeg input above the width cap re-encodes as jpeg.
func TestPinResizer_JpegInputResize(t *testing.T) {
	buf := pinJPEG(t, 2500, 100)
	res, err := MaybeResizeAndDownsampleImageBufferWithBudget(buf, 1, "jpeg", 1<<30)
	if err != nil {
		t.Fatalf("jpeg resize errored: %v", err)
	}
	if res.MediaType != "jpeg" {
		t.Errorf("MediaType = %q, want jpeg", res.MediaType)
	}
	if res.Dimensions.DisplayWidth != 2000 || res.Dimensions.DisplayHeight != 80 {
		t.Errorf("display = %dx%d, want 2000x80", res.Dimensions.DisplayWidth, res.Dimensions.DisplayHeight)
	}
}

// Corrupt buffer → catch path: passthrough with nil dimensions when base64
// fits, exact errors otherwise.
func TestPinResizer_CatchPathPassthroughAndErrors(t *testing.T) {
	corrupt := []byte("definitely not an image")
	res, err := MaybeResizeAndDownsampleImageBufferWithBudget(corrupt, len(corrupt), "png", 1<<30)
	if err != nil {
		t.Fatalf("small corrupt buffer errored: %v", err)
	}
	if !bytes.Equal(res.Buffer, corrupt) {
		t.Error("catch path did not pass the original buffer through")
	}
	if res.MediaType != "png" {
		t.Errorf("MediaType = %q, want png (detection default)", res.MediaType)
	}
	if res.Dimensions != nil {
		t.Errorf("Dimensions = %+v, want nil on the catch path", res.Dimensions)
	}

	// base64 exactly at the ceiling passes; one raw byte over errors.
	atLimit := (5 * 1024 * 1024 * 3) / 4
	if _, err := MaybeResizeAndDownsampleImageBuffer(corrupt, atLimit, "png"); err != nil {
		t.Errorf("size exactly at the base64 ceiling errored: %v", err)
	}
	_, err = MaybeResizeAndDownsampleImageBuffer(corrupt, atLimit+1, "png")
	if err == nil || !strings.Contains(err.Error(), "exceeds the 5MB API limit") {
		t.Errorf("err = %v, want the 5MB limit error", err)
	}

	// A PNG header naming oversized dimensions surfaces the dimension error.
	hdr := make([]byte, 64)
	copy(hdr, []byte{0x89, 'P', 'N', 'G'})
	binaryPut32(hdr[16:20], 2001)
	binaryPut32(hdr[20:24], 100)
	_, err = MaybeResizeAndDownsampleImageBuffer(hdr, 10, "png")
	if err == nil || !strings.Contains(err.Error(), "dimensions exceed the 2000x2000px limit") {
		t.Errorf("err = %v, want the dimension limit error", err)
	}
}

func binaryPut32(b []byte, v uint32) {
	b[0] = byte(v >> 24)
	b[1] = byte(v >> 16)
	b[2] = byte(v >> 8)
	b[3] = byte(v)
}

func TestPinResizer_NormalizeMediaType(t *testing.T) {
	cases := [][3]string{
		{"", "jpg", "jpeg"},
		{"", "png", "png"},
		{"jpg", "", "jpeg"},
		{"jpeg", "", "jpeg"},
		{"png", "jpg", "png"},
	}
	for _, c := range cases {
		if got := normalizeMediaType(c[0], c[1]); got != c[2] {
			t.Errorf("normalizeMediaType(%q,%q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestPinResizer_DetectImageFormat(t *testing.T) {
	if got := DetectImageFormatFromBuffer(nil); got != "image/png" {
		t.Errorf("short buffer = %q", got)
	}
	if got := DetectImageFormatFromBuffer([]byte{0x89, 'P', 'N', 'G'}); got != "image/png" {
		t.Errorf("png magic = %q", got)
	}
	if got := DetectImageFormatFromBuffer([]byte{0xff, 0xd8, 0xff, 0xe0}); got != "image/jpeg" {
		t.Errorf("jpeg magic = %q", got)
	}
	if got := DetectImageFormatFromBuffer([]byte{'G', 'I', 'F', '8'}); got != "image/gif" {
		t.Errorf("gif magic = %q", got)
	}
	webp := make([]byte, 12)
	copy(webp, "RIFF")
	copy(webp[8:], "WEBP")
	if got := DetectImageFormatFromBuffer(webp); got != "image/webp" {
		t.Errorf("webp magic = %q", got)
	}
	if got := DetectImageFormatFromBuffer([]byte("RIFF___NOTWEBP______")); got != "image/png" {
		t.Errorf("riff-not-webp = %q", got)
	}
	if got := DetectImageFormatFromBase64("!!!not base64!!!"); got != "image/png" {
		t.Errorf("bad base64 = %q", got)
	}
	if got := DetectImageFormatFromBase64(base64.StdEncoding.EncodeToString([]byte{0xff, 0xd8, 0xff, 0xe0})); got != "image/jpeg" {
		t.Errorf("base64 jpeg = %q", got)
	}
}

func TestPinResizer_ApplyFinalFallback(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 40, 40))
	res := applyFinalFallback(src, 1500, 3000, 1500, 3000)
	if res.MediaType != "jpeg" {
		t.Errorf("MediaType = %q", res.MediaType)
	}
	d := res.Dimensions
	if d == nil || d.DisplayWidth != 1000 || d.DisplayHeight != 2000 || d.OriginalWidth != 1500 || d.OriginalHeight != 3000 {
		t.Errorf("dims = %+v, want 1000x2000 display of 1500x3000", d)
	}
	res2 := applyFinalFallback(src, 800, 400, 800, 400)
	if res2.Dimensions.DisplayWidth != 800 || res2.Dimensions.DisplayHeight != 400 {
		t.Errorf("small fallback = %+v, want 800x400 (min with 1000)", res2.Dimensions)
	}
}
