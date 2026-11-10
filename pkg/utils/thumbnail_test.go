package utils

import (
	"bytes"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"strings"
	"testing"

	xdraw "golang.org/x/image/draw"
)

func TestResizeForThumbnail_LargeImage_Downscaled(t *testing.T) {
	t.Parallel()
	img := solidRGBA(1024, 1024, color.RGBA{R: 0xFF, A: 0xFF})
	png := encodePNG(t, img)

	out, mt, err := ResizeForThumbnail(png, 512)
	if err != nil {
		t.Fatalf("ResizeForThumbnail: %v", err)
	}
	// Output is re-encoded as JPEG q80; the returned media type reflects the
	// actual encoding so frontend <img> tags and Content-Type headers agree.
	if mt != "image/jpeg" {
		t.Errorf("media type = %q, want image/jpeg", mt)
	}
	cfg, _, derr := image.DecodeConfig(bytes.NewReader(out))
	if derr != nil {
		t.Fatalf("decode resized: %v", derr)
	}
	if cfg.Width != 512 || cfg.Height != 512 {
		t.Errorf("decoded dims = %dx%d, want 512x512", cfg.Width, cfg.Height)
	}
}

func TestResizeForThumbnail_SmallImage_PassedThrough(t *testing.T) {
	t.Parallel()
	img := solidRGBA(100, 100, color.RGBA{R: 0xFF, A: 0xFF})
	png := encodePNG(t, img)

	out, mt, err := ResizeForThumbnail(png, 512)
	if err != nil {
		t.Fatalf("ResizeForThumbnail: %v", err)
	}
	if mt != "image/png" {
		t.Errorf("media type = %q, want image/png (no re-encode on pass-through)", mt)
	}
	if !bytes.Equal(out, png) {
		t.Errorf("expected pass-through: got %d bytes, want %d (identical)", len(out), len(png))
	}
}

func TestResizeForThumbnail_EmptyInput_ReturnsError(t *testing.T) {
	t.Parallel()
	_, _, err := ResizeForThumbnail(nil, 512)
	if err == nil {
		t.Fatal("err = nil, want error")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("err msg = %q, want substring 'empty'", err.Error())
	}
}

func TestResizeForThumbnail_CorruptInput_ReturnsError(t *testing.T) {
	t.Parallel()
	corrupt := []byte{0xFF, 0xD8, 0xFF, 0x00} // JPEG-looking header but truncated
	_, _, err := ResizeForThumbnail(corrupt, 512)
	if err == nil {
		t.Fatal("err = nil, want decode error")
	}
}

func TestResizeForThumbnail_WideImage_PreservesAspectRatio(t *testing.T) {
	t.Parallel()
	img := solidRGBA(2000, 500, color.RGBA{G: 0xFF, A: 0xFF})
	png := encodePNG(t, img)

	out, mt, err := ResizeForThumbnail(png, 512)
	if err != nil {
		t.Fatalf("ResizeForThumbnail: %v", err)
	}
	if mt != "image/jpeg" {
		t.Errorf("media type = %q, want image/jpeg", mt)
	}
	cfg, _, derr := image.DecodeConfig(bytes.NewReader(out))
	if derr != nil {
		t.Fatalf("decode resized: %v", derr)
	}
	if cfg.Width != 512 || cfg.Height != 128 {
		t.Errorf("decoded dims = %dx%d, want 512x128", cfg.Width, cfg.Height)
	}
}

func TestResizeForThumbnail_TallImage_PreservesAspectRatio(t *testing.T) {
	t.Parallel()
	img := solidRGBA(500, 2000, color.RGBA{B: 0xFF, A: 0xFF})
	png := encodePNG(t, img)

	out, mt, err := ResizeForThumbnail(png, 512)
	if err != nil {
		t.Fatalf("ResizeForThumbnail: %v", err)
	}
	if mt != "image/jpeg" {
		t.Errorf("media type = %q, want image/jpeg", mt)
	}
	cfg, _, derr := image.DecodeConfig(bytes.NewReader(out))
	if derr != nil {
		t.Fatalf("decode resized: %v", derr)
	}
	if cfg.Width != 128 || cfg.Height != 512 {
		t.Errorf("decoded dims = %dx%d, want 128x512", cfg.Width, cfg.Height)
	}
}

func TestResizeForThumbnail_OutputIsJPEGEncoded(t *testing.T) {
	t.Parallel()
	img := solidRGBA(2048, 1024, color.RGBA{R: 0xAB, G: 0xCD, B: 0xEF, A: 0xFF})
	png := encodePNG(t, img)
	out, _, err := ResizeForThumbnail(png, 256)
	if err != nil {
		t.Fatalf("ResizeForThumbnail: %v", err)
	}
	if _, derr := jpeg.Decode(bytes.NewReader(out)); derr != nil {
		t.Errorf("jpeg.Decode: %v", derr)
	}
}

// TestResizeForThumbnail_GIF_DecodesInProduction exercises the GIF path
// end-to-end. The production decoder is registered by the blank import in
// thumbnail.go; this test would not by itself catch a removal of that import
// (the test file imports image/gif too), but it documents the intent and
// guards ResizeForThumbnail's contract for GIF input.
func TestResizeForThumbnail_GIF_DecodesInProduction(t *testing.T) {
	t.Parallel()
	img := solidRGBA(4, 4, color.RGBA{R: 0xFF, A: 0xFF})
	gifBytes := encodeGIF(t, img)
	// Sanity: confirm the bytes decode as a GIF before exercising resize.
	cfg, format, derr := image.DecodeConfig(bytes.NewReader(gifBytes))
	if derr != nil {
		t.Fatalf("gif does not decode in test binary: %v (image/gif not registered?)", derr)
	}
	if format != "gif" {
		t.Fatalf("decoded format = %q, want gif", format)
	}
	if cfg.Width != 4 || cfg.Height != 4 {
		t.Fatalf("gif dims = %dx%d, want 4x4", cfg.Width, cfg.Height)
	}

	// GIF passes through (both edges 4 ≤ 512) — but the returned bytes are
	// the original GIF bytes with media type image/gif. The frontend renders
	// <img src="data:image/gif;base64,..."> from this, so a wrong media type
	// would break the data URL.
	out, mt, err := ResizeForThumbnail(gifBytes, 512)
	if err != nil {
		t.Fatalf("ResizeForThumbnail(gif): %v", err)
	}
	if mt != "image/gif" {
		t.Errorf("media type = %q, want image/gif (no re-encode on pass-through)", mt)
	}
	// A 4x4 image is never enlarged; bytes come back verbatim so the data
	// URL the frontend builds matches what the backend stored.
	if !bytes.Equal(out, gifBytes) {
		t.Errorf("expected pass-through: got %d bytes, want %d (identical)", len(out), len(gifBytes))
	}
}

// TestResizeForThumbnail_EdgeExactlyMaxEdge_PassesThrough pins the pass-through
// comparison to <=: an image whose long edge equals maxEdge needs no scaling,
// and a strict < would send it through a needless JPEG round-trip (quality loss
// plus a media-type change) at exactly the boundary the caller chose.
func TestResizeForThumbnail_EdgeExactlyMaxEdge_PassesThrough(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		w, h int
	}{
		{"width at the edge", 512, 256},
		{"height at the edge", 256, 512},
	} {
		pngBytes := encodePNG(t, solidRGBA(tc.w, tc.h, color.RGBA{R: 0x11, G: 0x22, B: 0x33, A: 0xFF}))
		out, mt, err := ResizeForThumbnail(pngBytes, 512)
		if err != nil {
			t.Fatalf("%s: ResizeForThumbnail: %v", tc.name, err)
		}
		if mt != "image/png" {
			t.Errorf("%s: media type = %q, want image/png (edge is not over the limit)", tc.name, mt)
		}
		if !bytes.Equal(out, pngBytes) {
			t.Errorf("%s: got %d re-encoded bytes, want the %d input bytes verbatim", tc.name, len(out), len(pngBytes))
		}
	}
}

// TestResizeForThumbnail_RoundingUsesHalfDivisor pins the aspect-ratio
// rounding term (w/2, h/2). The chosen fixtures land the exact-half remainder
// in the window where half and third divisors round to different integers,
// which is the only observable difference.
func TestResizeForThumbnail_RoundingUsesHalfDivisor(t *testing.T) {
	t.Parallel()
	// 505*512 = 258560: (258560+500)/1000 = 259, (258560+333)/1000 = 258.
	for _, tc := range []struct {
		name         string
		w, h         int
		wantW, wantH int
	}{
		{"landscape", 1000, 505, 512, 259},
		{"portrait", 505, 1000, 259, 512},
	} {
		pngBytes := encodePNG(t, solidRGBA(tc.w, tc.h, color.RGBA{G: 0x7F, A: 0xFF}))
		out, _, err := ResizeForThumbnail(pngBytes, 512)
		if err != nil {
			t.Fatalf("%s: ResizeForThumbnail: %v", tc.name, err)
		}
		cfg, _, derr := image.DecodeConfig(bytes.NewReader(out))
		if derr != nil {
			t.Fatalf("%s: decode resized: %v", tc.name, derr)
		}
		if cfg.Width != tc.wantW || cfg.Height != tc.wantH {
			t.Errorf("%s: decoded dims = %dx%d, want %dx%d", tc.name, cfg.Width, cfg.Height, tc.wantW, tc.wantH)
		}
	}
}

// TestResizeForThumbnail_OutputIsQ80OfScaledPixels pins the resize pipeline
// end to byte level: the returned bytes must be exactly the q80 JPEG encoding
// of the CatmullRom-scaled destination. A dropped Scale leaves a blank buffer,
// a cleared/±1 Quality changes the encoding, and both are invisible to the
// dimension and decode-only checks above.
func TestResizeForThumbnail_OutputIsQ80OfScaledPixels(t *testing.T) {
	t.Parallel()
	src := noiseRGBA(600, 400, 7)
	pngBytes := encodePNG(t, src)
	out, mt, err := ResizeForThumbnail(pngBytes, 512)
	if err != nil {
		t.Fatalf("ResizeForThumbnail: %v", err)
	}
	if mt != "image/jpeg" {
		t.Errorf("media type = %q, want image/jpeg", mt)
	}
	decoded, _, derr := image.Decode(bytes.NewReader(pngBytes))
	if derr != nil {
		t.Fatalf("decode fixture: %v", derr)
	}
	targetW, targetH := 512, (400*512+300)/600
	want := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	xdraw.CatmullRom.Scale(want, want.Bounds(), decoded, decoded.Bounds(), xdraw.Over, nil)
	if wantBytes := encodeJPEG(t, want, 80); !bytes.Equal(out, wantBytes) {
		t.Errorf("resized bytes = %d, want the %d-byte q80 encoding of the %dx%d CatmullRom result",
			len(out), len(wantBytes), targetW, targetH)
	}
}

// TestResizeForThumbnail_EncodeFailurePropagates reaches the one failure the
// re-encode step can actually return: jpeg.Encode refuses a destination whose
// edge hits the 65536 limit (the destination is a bytes.Buffer, so its writes
// never fail). The helper must report the error rather than hand back the
// empty buffer as a thumbnail.
func TestResizeForThumbnail_EncodeFailurePropagates(t *testing.T) {
	t.Parallel()
	big := image.NewRGBA(image.Rect(0, 0, 65537, 1))
	out, mt, err := ResizeForThumbnail(encodePNG(t, big), 65536)
	if err == nil {
		t.Fatalf("err = nil, want jpeg's \"image is too large to encode\" for a 65536px destination")
	}
	if out != nil {
		t.Errorf("out = %d bytes, want nil alongside the error", len(out))
	}
	if mt != "" {
		t.Errorf("media type = %q, want empty alongside the error", mt)
	}
}
