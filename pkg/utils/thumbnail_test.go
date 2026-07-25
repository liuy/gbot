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
