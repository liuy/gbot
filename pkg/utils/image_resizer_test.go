package utils

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand"
	"strings"
	"sync"
	"testing"
)

// --- fixture builders ---

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

func encodeJPEG(t *testing.T, img image.Image, q int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
		t.Fatalf("jpeg.Encode q%d: %v", q, err)
	}
	return buf.Bytes()
}

func encodeGIF(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, &gif.Options{NumColors: 256}); err != nil {
		t.Fatalf("gif.Encode: %v", err)
	}
	return buf.Bytes()
}

func solidRGBA(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// noiseRGBA generates a coarse noise image (one random color per 16x16
// block) instead of per-pixel noise. This keeps JPEG/PNG encode fast under
// the race detector while still defeating pure-color optimizations that
// would prevent us from reaching the "over-cap" branches.
func noiseRGBA(w, h int, seed int64) *image.RGBA {
	r := rand.New(rand.NewSource(seed))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	const block = 16
	for by := 0; by < h; by += block {
		for bx := 0; bx < w; bx += block {
			c := color.RGBA{
				R: uint8(r.Intn(256)),
				G: uint8(r.Intn(256)),
				B: uint8(r.Intn(256)),
				A: 255,
			}
			yEnd := min(by+block, h)
			xEnd := min(bx+block, w)
			for y := by; y < yEnd; y++ {
				for x := bx; x < xEnd; x++ {
					img.SetRGBA(x, y, c)
				}
			}
		}
	}
	return img
}

// asImageResizeError returns the *ImageResizeError if present.
func asImageResizeError(t *testing.T, err error) *ImageResizeError {
	t.Helper()
	var target *ImageResizeError
	if !errors.As(err, &target) {
		t.Fatalf("err = %v, want *ImageResizeError", err)
	}
	return target
}

// reDecode decodes buf and fails the test if invalid. Returns dimensions.
func reDecode(t *testing.T, buf []byte, label string) (int, int) {
	t.Helper()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("%s: re-decode failed: %v", label, err)
	}
	return cfg.Width, cfg.Height
}

// --- 1. EmptyBuffer ---

func TestMaybeResize_EmptyBuffer(t *testing.T) {
	t.Parallel()
	_, err := MaybeResizeAndDownsampleImageBuffer(nil, 0, "png")
	re := asImageResizeError(t, err)
	if !strings.Contains(re.Msg, "empty") {
		t.Errorf("err msg = %q, want substring 'empty'", re.Msg)
	}
	// Also cover zero-length non-nil slice.
	_, err = MaybeResizeAndDownsampleImageBuffer([]byte{}, 0, "png")
	re = asImageResizeError(t, err)
	if !strings.Contains(re.Msg, "empty") {
		t.Errorf("err msg (zero-len) = %q, want substring 'empty'", re.Msg)
	}
}

// --- 2. OriginalWithinLimits (PNG) ---

func TestMaybeResize_OriginalPngWithinLimits(t *testing.T) {
	t.Parallel()
	img := solidRGBA(100, 100, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	buf := encodePNG(t, img)
	res, err := MaybeResizeAndDownsampleImageBuffer(buf, len(buf), "png")
	if err != nil {
		t.Fatalf("MaybeResize: %v", err)
	}
	if res.MediaType != "png" {
		t.Errorf("MediaType = %q, want png", res.MediaType)
	}
	if !bytes.Equal(res.Buffer, buf) {
		t.Errorf("Buffer mutated: got %d bytes, want %d (identical)", len(res.Buffer), len(buf))
	}
	if res.Dimensions == nil {
		t.Fatal("Dimensions nil")
	}
	d := *res.Dimensions
	if d.OriginalWidth != 100 || d.OriginalHeight != 100 || d.DisplayWidth != 100 || d.DisplayHeight != 100 {
		t.Errorf("dims = %+v, want all 100", d)
	}
}

// --- 3. OriginalJpegWithinLimits ---

func TestMaybeResize_OriginalJpegWithinLimits(t *testing.T) {
	t.Parallel()
	img := solidRGBA(100, 100, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	buf := encodeJPEG(t, img, 90)
	res, err := MaybeResizeAndDownsampleImageBuffer(buf, len(buf), "jpeg")
	if err != nil {
		t.Fatalf("MaybeResize: %v", err)
	}
	if res.MediaType != "jpeg" {
		t.Errorf("MediaType = %q, want jpeg", res.MediaType)
	}
	if !bytes.Equal(res.Buffer, buf) {
		t.Errorf("Buffer mutated: got %d bytes, want %d (identical)", len(res.Buffer), len(buf))
	}
	if res.Dimensions.OriginalWidth != 100 || res.Dimensions.DisplayWidth != 100 {
		t.Errorf("dims = %+v, want w=100", *res.Dimensions)
	}
}

// --- 4. JpgNormalizedToJpeg ---

func TestMaybeResize_JpgNormalizedToJpeg(t *testing.T) {
	t.Parallel()
	img := solidRGBA(100, 100, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	buf := encodeJPEG(t, img, 90)
	res, err := MaybeResizeAndDownsampleImageBuffer(buf, len(buf), "jpg")
	if err != nil {
		t.Fatalf("MaybeResize: %v", err)
	}
	// image.DecodeConfig reports "jpeg" for real JPEGs regardless of ext;
	// the explicit jpg→jpeg normalization only fires when format=="".
	// In both cases the returned MediaType must be "jpeg".
	if res.MediaType != "jpeg" {
		t.Errorf("MediaType = %q, want jpeg", res.MediaType)
	}
}

// --- 5. NoMetadataCatchPathPassthrough ---
//
// Go's image.DecodeConfig returns an error for unknown formats, so the TS
// "no metadata + small" branch is reached in Go via the catch path's
// passthrough sub-case (base64 size fits API limit).

func TestMaybeResize_NoMetadataCatchPassthrough(t *testing.T) {
	t.Parallel()
	// 4 non-magic bytes + zero padding to make a buffer whose length is
	// well under IMAGE_TARGET_RAW_SIZE. detectImageFormatFromBuffer returns
	// "image/png" (default) for unknown magic.
	buf := make([]byte, 256)
	buf[0], buf[1], buf[2], buf[3] = 0x01, 0x02, 0x03, 0x04
	res, err := MaybeResizeAndDownsampleImageBuffer(buf, len(buf), "png")
	if err != nil {
		t.Fatalf("MaybeResize: %v", err)
	}
	if !bytes.Equal(res.Buffer, buf) {
		t.Errorf("Buffer mutated: got %d bytes, want %d (passthrough)", len(res.Buffer), len(buf))
	}
	wantMT := strings.TrimPrefix(DetectImageFormatFromBuffer(buf), "image/")
	if res.MediaType != wantMT {
		t.Errorf("MediaType = %q, want %q", res.MediaType, wantMT)
	}
	if res.Dimensions != nil {
		t.Errorf("Dimensions = %+v, want nil (catch-path passthrough)", *res.Dimensions)
	}
}

// --- 6. NoMetadataCatchPathOversizeError ---

func TestMaybeResize_NoMetadataCatchOversizeError(t *testing.T) {
	t.Parallel()
	// Non-decodable, non-PNG-magic buffer whose base64 size exceeds
	// API_IMAGE_MAX_BASE64_SIZE. Size > IMAGE_TARGET_RAW_SIZE suffices.
	size := IMAGE_TARGET_RAW_SIZE + 1
	buf := make([]byte, size)
	buf[0], buf[1], buf[2], buf[3] = 0x01, 0x02, 0x03, 0x04
	_, err := MaybeResizeAndDownsampleImageBuffer(buf, len(buf), "png")
	re := asImageResizeError(t, err)
	if !strings.Contains(re.Msg, "5MB API limit") {
		t.Errorf("err msg = %q, want substring '5MB API limit'", re.Msg)
	}
}

// --- 7. DimensionOKSizeOverPngPalette ---
//
// A PNG whose dimensions are within the cap but whose raw bytes exceed
// IMAGE_TARGET_RAW_SIZE forces the PNG palette / JPEG loop. We pad a real
// PNG with ancillary bytes to push size over cap without changing the
// decoded dimensions.

func TestMaybeResize_DimensionOKSizeOverPng(t *testing.T) {
	t.Parallel()
	// Small (100x100) PNG so palette quantization + PNG encode is fast
	// even under -race. Pad with ancillary bytes after IEND to push the
	// raw size over cap without changing the decoded dimensions.
	img := solidRGBA(100, 100, color.RGBA{R: 100, G: 100, B: 100, A: 255})
	buf := encodePNG(t, img)
	padded := append([]byte{}, buf...)
	padded = append(padded, make([]byte, IMAGE_TARGET_RAW_SIZE+1)...)
	if len(padded) <= IMAGE_TARGET_RAW_SIZE {
		t.Skipf("padded size %d not over cap %d", len(padded), IMAGE_TARGET_RAW_SIZE)
	}
	res, err := MaybeResizeAndDownsampleImageBuffer(padded, len(padded), "png")
	if err != nil {
		t.Fatalf("MaybeResize: %v", err)
	}
	// Palette quantization may succeed (png) or fall through to jpeg — both
	// are valid per TS branching.
	switch res.MediaType {
	case "png", "jpeg":
	default:
		t.Errorf("MediaType = %q, want png or jpeg", res.MediaType)
	}
	reDecode(t, res.Buffer, "DimensionOKSizeOverPng")
	if res.Dimensions.OriginalWidth != 100 || res.Dimensions.OriginalHeight != 100 {
		t.Errorf("Original = %dx%d, want 100x100",
			res.Dimensions.OriginalWidth, res.Dimensions.OriginalHeight)
	}
}

// --- 8. DimensionOKSizeOverJpegFallback ---
//
// JPEG input (isPng=false so PNG branch is skipped) padded over cap forces
// the [80,60,40,20] JPEG loop directly.

func TestMaybeResize_DimensionOKSizeOverJpegFallback(t *testing.T) {
	t.Parallel()
	img := noiseRGBA(800, 800, 42)
	buf := encodeJPEG(t, img, 95)
	padded := append([]byte{}, buf...)
	padded = append(padded, make([]byte, IMAGE_TARGET_RAW_SIZE+1)...)
	res, err := MaybeResizeAndDownsampleImageBuffer(padded, len(padded), "jpeg")
	if err != nil {
		t.Fatalf("MaybeResize: %v", err)
	}
	if res.MediaType != "jpeg" {
		t.Errorf("MediaType = %q, want jpeg", res.MediaType)
	}
	reDecode(t, res.Buffer, "DimensionOKSizeOverJpeg")
	if len(res.Buffer) > IMAGE_TARGET_RAW_SIZE {
		t.Errorf("buffer = %d bytes, want <= %d", len(res.Buffer), IMAGE_TARGET_RAW_SIZE)
	}
}

// --- 9. DimensionOverNoCompressionNeeded ---
//
// Dimensions over cap (2500x2500) but bytes under cap (solid-color PNG is
// tiny): resize path produces a 2000x2000 PNG that fits without further
// compression.

func TestMaybeResize_DimensionOverNoCompressionNeeded(t *testing.T) {
	t.Parallel()
	img := solidRGBA(2500, 2500, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	buf := encodePNG(t, img)
	res, err := MaybeResizeAndDownsampleImageBuffer(buf, len(buf), "png")
	if err != nil {
		t.Fatalf("MaybeResize: %v", err)
	}
	if res.MediaType != "png" {
		t.Errorf("MediaType = %q, want png", res.MediaType)
	}
	w, h := reDecode(t, res.Buffer, "DimensionOverNoCompressionNeeded")
	if w != 2000 || h != 2000 {
		t.Errorf("decoded dims = %dx%d, want 2000x2000", w, h)
	}
	if res.Dimensions.OriginalWidth != 2500 || res.Dimensions.OriginalHeight != 2500 {
		t.Errorf("Original = %dx%d, want 2500x2500",
			res.Dimensions.OriginalWidth, res.Dimensions.OriginalHeight)
	}
	if res.Dimensions.DisplayWidth != 2000 || res.Dimensions.DisplayHeight != 2000 {
		t.Errorf("Display = %dx%d, want 2000x2000",
			res.Dimensions.DisplayWidth, res.Dimensions.DisplayHeight)
	}
}

// --- 10. DimensionOverPngNeedsPaletteAfterResize ---
//
// Dimensions over cap AND post-resize bytes still over cap. Noise PNG
// forces the post-resize palette/JPEG fallback branches.

func TestMaybeResize_DimensionOverPngPostResize(t *testing.T) {
	t.Parallel()
	img := noiseRGBA(2500, 2500, 7)
	buf := encodePNG(t, img)
	res, err := MaybeResizeAndDownsampleImageBuffer(buf, len(buf), "png")
	if err != nil {
		t.Fatalf("MaybeResize: %v", err)
	}
	switch res.MediaType {
	case "png", "jpeg":
	default:
		t.Errorf("MediaType = %q, want png or jpeg", res.MediaType)
	}
	w, h := reDecode(t, res.Buffer, "DimensionOverPngPostResize")
	if w > ImageMaxWidth || h > ImageMaxHeight {
		t.Errorf("decoded dims = %dx%d, must be <= %dx%d", w, h, ImageMaxWidth, ImageMaxHeight)
	}
	if res.Dimensions.OriginalWidth != 2500 {
		t.Errorf("Original width = %d, want 2500", res.Dimensions.OriginalWidth)
	}
}

// TestMaybeResize_DimensionOverPostResizeExceedsCap is intentionally omitted.
// Exercising the dispatch into compressAfterResize (image_resizer.go:189)
// requires a ≥2100x2100 image whose downscaled 2000x2000 PNG re-encode
// exceeds IMAGE_TARGET_RAW_SIZE (3.75MB). Only full per-pixel noise achieves
// this, and encoding such an image under -race takes ~10s — beyond the
// package's 30s budget. The branch is covered indirectly via
// TestCompressAfterResize_JpegLoop / TestCompressAfterResize_PngPaletteSucceeds
// in image_resizer_internal_test.go, which call compressAfterResize directly.

// --- 11. FinalFallback1000px lives in image_resizer_internal_test.go ---

// --- 12. CatchOversizeDimensionPNGHeader ---

func TestMaybeResize_CatchOversizeDimensionPNGHeader(t *testing.T) {
	t.Parallel()
	// Fake PNG header with IHDR width=4000 (over cap) at bytes 16-19,
	// height=4000 at bytes 20-23. Garbage body so image.Decode fails →
	// catch path's overDim arm fires.
	buf := make([]byte, 64)
	buf[0], buf[1], buf[2], buf[3] = 0x89, 0x50, 0x4e, 0x47
	buf[16], buf[17], buf[18], buf[19] = 0x00, 0x00, 0x0F, 0xA0 // 4000
	buf[20], buf[21], buf[22], buf[23] = 0x00, 0x00, 0x0F, 0xA0 // 4000
	_, err := MaybeResizeAndDownsampleImageBuffer(buf, len(buf), "png")
	re := asImageResizeError(t, err)
	if !strings.Contains(re.Msg, "dimensions exceed") {
		t.Errorf("err msg = %q, want substring 'dimensions exceed'", re.Msg)
	}
	if !strings.Contains(re.Msg, "2000x2000") {
		t.Errorf("err msg = %q, want substring '2000x2000'", re.Msg)
	}
}

// --- 13. CatchOversizeByBase64 ---

func TestMaybeResize_CatchOversizeByBase64(t *testing.T) {
	t.Parallel()
	size := IMAGE_TARGET_RAW_SIZE + 1
	buf := make([]byte, size)
	buf[0] = 0x00 // non-PNG magic
	_, err := MaybeResizeAndDownsampleImageBuffer(buf, len(buf), "png")
	re := asImageResizeError(t, err)
	if !strings.Contains(re.Msg, "5MB API limit") {
		t.Errorf("err msg = %q, want substring '5MB API limit'", re.Msg)
	}
}

// --- 14. CatchFallsBackToOriginalUnder5MB ---

func TestMaybeResize_CatchFallbackToOriginalUnder5MB(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 100)
	buf[0], buf[1], buf[2], buf[3] = 0xDE, 0xAD, 0xBE, 0xEF
	res, err := MaybeResizeAndDownsampleImageBuffer(buf, len(buf), "png")
	if err != nil {
		t.Fatalf("MaybeResize: %v", err)
	}
	if !bytes.Equal(res.Buffer, buf) {
		t.Errorf("Buffer mutated: got %d bytes, want %d", len(res.Buffer), len(buf))
	}
	wantMT := strings.TrimPrefix(DetectImageFormatFromBuffer(buf), "image/")
	if res.MediaType != wantMT {
		t.Errorf("MediaType = %q, want %q", res.MediaType, wantMT)
	}
}

// --- 17. DetectImageFormatFromBuffer table test ---

func TestDetectImageFormatFromBuffer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		buf  []byte
		want string
	}{
		{"png", []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a}, "image/png"},
		{"jpeg", []byte{0xff, 0xd8, 0xff, 0xe0}, "image/jpeg"},
		{"gif", []byte{0x47, 0x49, 0x46, 0x38, 0x39, 0x61}, "image/gif"},
		{"webp", append([]byte{0x52, 0x49, 0x46, 0x46, 0x00, 0x00, 0x00, 0x00}, []byte{0x57, 0x45, 0x42, 0x50}...), "image/webp"},
		{"riff-but-not-webp", []byte{0x52, 0x49, 0x46, 0x46, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, "image/png"},
		{"short-3-bytes", []byte{0x01, 0x02, 0x03}, "image/png"},
		{"short-0-bytes", []byte{}, "image/png"},
		{"unknown-4-bytes", []byte{0x01, 0x02, 0x03, 0x04}, "image/png"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := DetectImageFormatFromBuffer(tc.buf)
			if got != tc.want {
				t.Errorf("DetectImageFormatFromBuffer = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- 18. DetectImageFormatFromBase64 ---

func TestDetectImageFormatFromBase64(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  []byte
		want string
	}{
		{"png", []byte{0x89, 0x50, 0x4e, 0x47}, "image/png"},
		// JPEG/GIF magic must be ≥4 bytes — TS detectImageFormatFromBuffer
		// guards on `buffer.length < 4` before any magic check.
		{"jpeg", []byte{0xff, 0xd8, 0xff, 0xe0}, "image/jpeg"},
		{"gif", []byte{0x47, 0x49, 0x46, 0x38}, "image/gif"},
		{"webp", append([]byte{0x52, 0x49, 0x46, 0x46, 0, 0, 0, 0}, []byte{0x57, 0x45, 0x42, 0x50}...), "image/webp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b64 := base64.StdEncoding.EncodeToString(tc.raw)
			got := DetectImageFormatFromBase64(b64)
			if got != tc.want {
				t.Errorf("DetectImageFormatFromBase64 = %q, want %q", got, tc.want)
			}
		})
	}
	t.Run("invalid-base64", func(t *testing.T) {
		t.Parallel()
		if got := DetectImageFormatFromBase64("!!!not-base64!!!"); got != "image/png" {
			t.Errorf("invalid base64 = %q, want image/png", got)
		}
	})
}

// --- Edge case: dimensions exactly at cap (boundary) ---

func TestMaybeResize_DimensionsExactlyAtCap(t *testing.T) {
	t.Parallel()
	img := solidRGBA(ImageMaxWidth, ImageMaxHeight, color.RGBA{R: 50, G: 60, B: 70, A: 255})
	buf := encodePNG(t, img)
	res, err := MaybeResizeAndDownsampleImageBuffer(buf, len(buf), "png")
	if err != nil {
		t.Fatalf("MaybeResize: %v", err)
	}
	// At-cap → no resize; bytes returned unchanged.
	if !bytes.Equal(res.Buffer, buf) {
		t.Errorf("Buffer mutated at-cap; got %d want %d", len(res.Buffer), len(buf))
	}
	if res.Dimensions.DisplayWidth != ImageMaxWidth || res.Dimensions.DisplayHeight != ImageMaxHeight {
		t.Errorf("Display = %dx%d, want %dx%d",
			res.Dimensions.DisplayWidth, res.Dimensions.DisplayHeight, ImageMaxWidth, ImageMaxHeight)
	}
}

// --- Edge case: dimensions one pixel over cap (triggers resize) ---

func TestMaybeResize_DimensionsOnePxOverCap(t *testing.T) {
	t.Parallel()
	img := solidRGBA(ImageMaxWidth+1, ImageMaxHeight+1, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	buf := encodePNG(t, img)
	res, err := MaybeResizeAndDownsampleImageBuffer(buf, len(buf), "png")
	if err != nil {
		t.Fatalf("MaybeResize: %v", err)
	}
	w, h := reDecode(t, res.Buffer, "DimensionsOnePxOverCap")
	if w != ImageMaxWidth || h != ImageMaxHeight {
		t.Errorf("decoded = %dx%d, want %dx%d", w, h, ImageMaxWidth, ImageMaxHeight)
	}
}

// --- Edge case: originalSize exactly at IMAGE_TARGET_RAW_SIZE ---

func TestMaybeResize_SizeExactlyAtCap(t *testing.T) {
	t.Parallel()
	img := solidRGBA(50, 50, color.RGBA{R: 9, G: 9, B: 9, A: 255})
	buf := encodePNG(t, img)
	// originalSize = cap exactly → ≤ comparison passes, no compression.
	res, err := MaybeResizeAndDownsampleImageBuffer(buf, IMAGE_TARGET_RAW_SIZE, "png")
	if err != nil {
		t.Fatalf("MaybeResize: %v", err)
	}
	if !bytes.Equal(res.Buffer, buf) {
		t.Errorf("Buffer mutated at-cap; got %d want %d (passthrough)", len(res.Buffer), len(buf))
	}
}

// --- Concurrency: function must be pure w.r.t. inputs ---

func TestMaybeResize_Concurrent(t *testing.T) {
	t.Parallel()
	img1 := solidRGBA(800, 800, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	img2 := noiseRGBA(2500, 2500, 99)
	buf1 := encodePNG(t, img1)
	buf2 := encodePNG(t, img2)
	var wg sync.WaitGroup
	var err1, err2 error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err1 = MaybeResizeAndDownsampleImageBuffer(buf1, len(buf1), "png")
	}()
	go func() {
		defer wg.Done()
		_, err2 = MaybeResizeAndDownsampleImageBuffer(buf2, len(buf2), "png")
	}()
	wg.Wait()
	if err1 != nil {
		t.Errorf("goroutine 1: %v", err1)
	}
	if err2 != nil {
		t.Errorf("goroutine 2: %v", err2)
	}
}

// --- 3b. GIF passthrough preserves image/gif MIME ---

func TestMaybeResize_GifPassthrough(t *testing.T) {
	t.Parallel()
	img := solidRGBA(50, 50, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	buf := encodeGIF(t, img)
	res, err := MaybeResizeAndDownsampleImageBuffer(buf, len(buf), "gif")
	if err != nil {
		t.Fatalf("MaybeResize: %v", err)
	}
	// GIF decodes successfully, dims under cap → return byte-identical.
	if !bytes.Equal(res.Buffer, buf) {
		t.Errorf("Buffer mutated: got %d bytes, want %d", len(res.Buffer), len(buf))
	}
	if res.MediaType != "gif" {
		t.Errorf("MediaType = %q, want gif", res.MediaType)
	}
	if res.Dimensions.OriginalWidth != 50 || res.Dimensions.OriginalHeight != 50 {
		t.Errorf("Original = %dx%d, want 50x50",
			res.Dimensions.OriginalWidth, res.Dimensions.OriginalHeight)
	}
}
