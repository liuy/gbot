// Package utils provides shared utilities used across packages.
package utils

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	"image/color/palette"
	"image/jpeg"
	"image/png"
	"strings"

	xdraw "golang.org/x/image/draw"

	"github.com/liuy/gbot/pkg/tool/toolresult"
	"github.com/liuy/gbot/pkg/types"
)

// Port of claude-code-source-code/src/utils/imageResizer.ts
// (maybeResizeAndDownsampleImageBuffer + maybeResizeAndDownsampleImageBlock +
// detectImageFormatFrom*). Lines referenced in comments refer to that file.
//
// Analytics (classifyImageError/hashString/logEvent) and the compressImage*
// family are intentionally NOT ported (plan Assumptions 6–7).

// API_IMAGE_MAX_BASE64_SIZE mirrors apiLimits.ts:API_IMAGE_MAX_BASE64_SIZE.
const API_IMAGE_MAX_BASE64_SIZE = 5 * 1024 * 1024

// IMAGE_TARGET_RAW_SIZE mirrors apiLimits.ts:IMAGE_TARGET_RAW_SIZE
// (raw bytes whose base64 encoding fits under API_IMAGE_MAX_BASE64_SIZE).
const IMAGE_TARGET_RAW_SIZE = (API_IMAGE_MAX_BASE64_SIZE * 3) / 4

// ImageMaxWidth / ImageMaxHeight mirror apiLimits.ts:IMAGE_MAX_WIDTH/HEIGHT.
const (
	ImageMaxWidth  = 2000
	ImageMaxHeight = 2000
)

// jpegQualities is the [80,60,40,20] loop constant from TS lines 256/327.
var jpegQualities = [4]int{80, 60, 40, 20}

// ImageResizeError mirrors TS class ImageResizeError. Returned when an image
// exceeds the API size/dimension limit and every compression strategy failed.
type ImageResizeError struct{ Msg string }

func (e *ImageResizeError) Error() string { return e.Msg }

// NewImageResizeError constructs an ImageResizeError.
func NewImageResizeError(msg string) *ImageResizeError { return &ImageResizeError{Msg: msg} }

// ImageDimensions mirrors TS ImageDimensions.
type ImageDimensions struct {
	OriginalWidth  int
	OriginalHeight int
	DisplayWidth   int
	DisplayHeight  int
}

// ResizeResult mirrors TS ResizeResult. MediaType is normalized to one of
// {png, jpeg, gif, webp} (no "image/" prefix — same convention as TS).
// Dimensions is nil when the TS function would emit `undefined` (the
// catch-path pass-through sub-case).
type ResizeResult struct {
	Buffer     []byte
	MediaType  string
	Dimensions *ImageDimensions
}

// MaybeResizeAndDownsampleImageBuffer ports TS maybeResizeAndDownsampleImageBuffer
// (imageResizer.ts:169–433). Every TS branch is preserved verbatim; the only
// deviations are documented in the plan (Go's image.DecodeConfig returns an
// error instead of zero dimensions for unknown formats, so the TS
// "no metadata" branch funnels into the catch path).
func MaybeResizeAndDownsampleImageBuffer(imageBuffer []byte, originalSize int, ext string) (ResizeResult, error) {
	// TS line 174.
	if len(imageBuffer) == 0 {
		return ResizeResult{}, NewImageResizeError("Image file is empty (0 bytes)")
	}

	// TS lines 184–188: image.metadata(). In Go, DecodeConfig yields w/h/format
	// or an error; any error funnels to handleResizeError (TS catch).
	config, format, err := image.DecodeConfig(bytes.NewReader(imageBuffer))
	if err != nil {
		return handleResizeError(imageBuffer, originalSize)
	}

	// TS line 187: const mediaType = metadata.format ?? ext.
	// In Go, image.DecodeConfig always returns a non-empty format for
	// registered decoders — the `?? ext` fallback is therefore unreachable
	// through the public pipeline but kept for parity.
	normalizedMediaType := normalizeMediaType(format, ext)

	// TS lines 191–201 ("no metadata" branch) is unreachable in Go: a
	// successful DecodeConfig always has non-zero w/h. The unreachable
	// sub-cases map onto the catch path (see handleResizeError + plan note
	// "No-metadata branch — Go semantics").

	originalWidth := config.Width
	originalHeight := config.Height

	width, height := originalWidth, originalHeight

	// TS lines 212–228: original fits all three caps → return as-is.
	if originalSize <= IMAGE_TARGET_RAW_SIZE &&
		width <= ImageMaxWidth && height <= ImageMaxHeight {
		return ResizeResult{
			Buffer:    imageBuffer,
			MediaType: normalizedMediaType,
			Dimensions: &ImageDimensions{
				OriginalWidth:  originalWidth,
				OriginalHeight: originalHeight,
				DisplayWidth:   width,
				DisplayHeight:  height,
			},
		}, nil
	}

	needsDimensionResize := width > ImageMaxWidth || height > ImageMaxHeight
	isPng := normalizedMediaType == "png"

	// TS lines 235–275: dimensions OK but bytes oversize → try lossless /
	// lossy compression before resizing. Each attempt re-decodes
	// imageBuffer (plan Assumption 3 — behavioral parity with sharp).
	if !needsDimensionResize && originalSize > IMAGE_TARGET_RAW_SIZE {
		if isPng {
			if buf, ok := tryEncodePngPalettizedFromBytes(imageBuffer); ok && len(buf) <= IMAGE_TARGET_RAW_SIZE {
				return ResizeResult{
					Buffer:    buf,
					MediaType: "png",
					Dimensions: &ImageDimensions{
						OriginalWidth:  originalWidth,
						OriginalHeight: originalHeight,
						DisplayWidth:   width,
						DisplayHeight:  height,
					},
				}, nil
			}
		}
		for _, q := range jpegQualities {
			if buf, ok := tryEncodeJpegFromBytes(imageBuffer, q); ok && len(buf) <= IMAGE_TARGET_RAW_SIZE {
				return ResizeResult{
					Buffer:    buf,
					MediaType: "jpeg",
					Dimensions: &ImageDimensions{
						OriginalWidth:  originalWidth,
						OriginalHeight: originalHeight,
						DisplayWidth:   width,
						DisplayHeight:  height,
					},
				}, nil
			}
		}
		// Quality reduction alone wasn't enough — fall through to resize.
	}

	// TS lines 278–286: constrain width/height maintaining aspect ratio.
	// (height*MAX_W + width/2)/width emulates Math.round for positive operands.
	if width > ImageMaxWidth {
		height = (height*ImageMaxWidth + width/2) / width
		width = ImageMaxWidth
	}
	if height > ImageMaxHeight {
		width = (width*ImageMaxHeight + height/2) / height
		height = ImageMaxHeight
	}

	// TS line 293–299: sharp(imageBuffer).resize(...).toBuffer(). Decode
	// pixels and scale. Decode failure funnels to the catch path.
	src, _, err := image.Decode(bytes.NewReader(imageBuffer))
	if err != nil {
		return handleResizeError(imageBuffer, originalSize)
	}
	resized := scaleImage(src, width, height)

	// Re-encode the resized image in the source format (sharp's default
	// toBuffer() preserves input format). This is the byte count compared
	// against IMAGE_TARGET_RAW_SIZE below. encodeInSourceFormat does not
	// return an error on valid decoded images (errors are dropped to mirror
	// TS which has no per-call encode handling inside try).
	resizedBytes, _ := encodeInSourceFormat(resized, normalizedMediaType)

	// TS lines 301–371: still too large after resize → palettize / JPEG loop /
	// final 1000px+q20 fallback. Factored into compressAfterResize so the
	// otherwise-unreachable-from-public-API branches (q-loop, 1000px
	// fallback) can be exercised directly by unit tests. Always returns a
	// valid result — the JPEG q20 fallback is a guaranteed terminator.
	if len(resizedBytes) > IMAGE_TARGET_RAW_SIZE {
		return compressAfterResize(resized, width, height, originalWidth, originalHeight, isPng), nil
	}

	// TS lines 374–382: resized image fits — return resized buffer in the
	// source format.
	return ResizeResult{
		Buffer:    resizedBytes,
		MediaType: normalizedMediaType,
		Dimensions: &ImageDimensions{
			OriginalWidth:  originalWidth,
			OriginalHeight: originalHeight,
			DisplayWidth:   width,
			DisplayHeight:  height,
		},
	}, nil
}

// compressAfterResize mirrors TS lines 301–371 (post-resize palettize / JPEG
// loop / 1000px+q20 fallback). Always returns a valid ResizeResult — when
// every upstream strategy fails to fit under IMAGE_TARGET_RAW_SIZE, the
// 1000px+q20 fallback (applyFinalFallback) terminates the chain.
func compressAfterResize(resized image.Image, width, height, originalWidth, originalHeight int, isPng bool) ResizeResult {
	dims := &ImageDimensions{
		OriginalWidth:  originalWidth,
		OriginalHeight: originalHeight,
		DisplayWidth:   width,
		DisplayHeight:  height,
	}
	if isPng {
		if buf, ok := encodePngPalettizedImg(resized); ok && len(buf) <= IMAGE_TARGET_RAW_SIZE {
			return ResizeResult{Buffer: buf, MediaType: "png", Dimensions: dims}
		}
	}
	for _, q := range jpegQualities {
		if buf, ok := encodeJpegImg(resized, q); ok && len(buf) <= IMAGE_TARGET_RAW_SIZE {
			return ResizeResult{Buffer: buf, MediaType: "jpeg", Dimensions: dims}
		}
	}
	// TS lines 348–371: final 1000px+q20 fallback.
	return applyFinalFallback(resized, width, height, originalWidth, originalHeight)
}

// normalizeMediaType ports TS line 187 `metadata.format ?? ext` plus the
// jpg→jpeg normalization (line 188). Factored out so the otherwise-unreachable
// `format==""` and `format=="jpg"` branches can be exercised by tests
// directly — through the public pipeline, image.DecodeConfig always returns
// a non-empty canonical format (e.g. "jpeg", not "jpg").
func normalizeMediaType(format, ext string) string {
	mt := format
	if mt == "" {
		mt = ext
	}
	if mt == "jpg" {
		return "jpeg"
	}
	return mt
}

// handleResizeError implements TS catch block (imageResizer.ts:383–432).
// Any decode/encode failure in the try block funnels here. The helper either
// passes the original buffer through (when its base64 encoding fits the API
// limit AND no PNG-header dimension overflow is detected) or returns
// ImageResizeError.
func handleResizeError(imageBuffer []byte, originalSize int) (ResizeResult, error) {
	// TS lines 386–388: detect actual format from magic bytes.
	detected := DetectImageFormatFromBuffer(imageBuffer)
	normalizedExt := strings.TrimPrefix(detected, "image/")

	// TS line 399: Math.ceil((originalSize * 4) / 3) — base64 size ceiling.
	base64Size := (originalSize*4 + 2) / 3

	// TS lines 404–412: PNG IHDR dimension check. PNG sig (8 bytes) then
	// IHDR width at offset 16, height at offset 20 (big-endian uint32).
	overDim := len(imageBuffer) >= 24 &&
		imageBuffer[0] == 0x89 &&
		imageBuffer[1] == 0x50 &&
		imageBuffer[2] == 0x4e &&
		imageBuffer[3] == 0x47 &&
		(binary.BigEndian.Uint32(imageBuffer[16:20]) > uint32(ImageMaxWidth) ||
			binary.BigEndian.Uint32(imageBuffer[20:24]) > uint32(ImageMaxHeight))

	// TS lines 414–422: passthrough when base64 fits and dims are OK.
	if base64Size <= API_IMAGE_MAX_BASE64_SIZE && !overDim {
		return ResizeResult{
			Buffer:     imageBuffer,
			MediaType:  normalizedExt,
			Dimensions: nil,
		}, nil
	}

	// TS lines 424–431: surface a user-friendly error.
	if overDim {
		return ResizeResult{}, NewImageResizeError(fmt.Sprintf(
			"Unable to resize image — dimensions exceed the %dx%dpx limit and image processing failed. "+
				"Please resize the image to reduce its pixel dimensions.",
			ImageMaxWidth, ImageMaxHeight))
	}
	return ResizeResult{}, NewImageResizeError(fmt.Sprintf(
		"Unable to resize image (%s raw, %s base64). "+
			"The image exceeds the 5MB API limit and compression failed. "+
			"Please resize the image manually or use a smaller image.",
		toolresult.FormatFileSize(originalSize), toolresult.FormatFileSize(base64Size)))
}

// scaleImage scales src to width×height using CatmullRom (the closest
// high-quality scaler in golang.org/x/image/draw to sharp's default Lanczos3
// — plan Assumption 1). Inputs are guaranteed to be downscales by the
// callers (withoutEnlargement semantics — plan "withoutEnlargement semantics").
func scaleImage(src image.Image, width, height int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Over, nil)
	return dst
}

// quantizeToPlan9 maps src onto the 256-color Plan9 palette via
// FloydSteinberg dithering. Closest stdlib approximation of sharp's
// .png({palette: true}) (plan Assumption 2).
func quantizeToPlan9(src image.Image) *image.Paletted {
	b := src.Bounds()
	pm := image.NewPaletted(b, palette.Plan9)
	xdraw.FloydSteinberg.Draw(pm, b, src, b.Min)
	return pm
}

// tryEncodePngPalettizedFromBytes decodes imageBuffer, quantizes to Plan9,
// and PNG-encodes. Used in the dimension-OK-but-oversize branch (TS 237–255).
// Returns (bytes, false) only when image.Decode fails; png.Encode cannot
// fail on a valid image.Paletted (the encode-error path is omitted as dead
// code, mirroring TS which has no per-call encode-error handling inside try).
func tryEncodePngPalettizedFromBytes(imageBuffer []byte) ([]byte, bool) {
	src, _, err := image.Decode(bytes.NewReader(imageBuffer))
	if err != nil {
		return nil, false
	}
	pm := quantizeToPlan9(src)
	var buf bytes.Buffer
	_ = (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, pm)
	return buf.Bytes(), true
}

// tryEncodeJpegFromBytes decodes imageBuffer and JPEG-encodes at the given
// quality. Used in the dimension-OK-but-oversize branch (TS 256–275).
func tryEncodeJpegFromBytes(imageBuffer []byte, quality int) ([]byte, bool) {
	src, _, err := image.Decode(bytes.NewReader(imageBuffer))
	if err != nil {
		return nil, false
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, src, &jpeg.Options{Quality: quality})
	return buf.Bytes(), true
}

// encodePngPalettizedImg quantizes a decoded image and PNG-encodes.
// Used in the post-resize oversize branch (TS 303–324).
func encodePngPalettizedImg(src image.Image) ([]byte, bool) {
	pm := quantizeToPlan9(src)
	var buf bytes.Buffer
	_ = (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, pm)
	return buf.Bytes(), true
}

// encodeJpegImg JPEG-encodes a decoded image at the given quality.
// Used in the post-resize oversize branch (TS 326–346).
func encodeJpegImg(src image.Image, quality int) ([]byte, bool) {
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, src, &jpeg.Options{Quality: quality})
	return buf.Bytes(), true
}

// encodeInSourceFormat mirrors sharp.toBuffer() preserving the input format.
// JPEG quality 80 is sharp's default. GIF/WebP inputs fall back to PNG
// because the stdlib cannot reliably re-encode them. The encoders cannot
// fail on valid decoded images — errors are dropped to mirror TS which has
// no per-call encode-error handling inside try.
func encodeInSourceFormat(src image.Image, normalizedMediaType string) ([]byte, error) {
	var buf bytes.Buffer
	switch normalizedMediaType {
	case "jpeg":
		_ = jpeg.Encode(&buf, src, &jpeg.Options{Quality: 80})
	default:
		_ = png.Encode(&buf, src)
	}
	return buf.Bytes(), nil
}

// applyFinalFallback downscales src to min(width,1000) and JPEG-encodes at
// q20. Ports TS imageResizer.ts:348–371. Unexported so the unreachable-from-
// public-API final-fallback branch can be unit-tested in isolation.
// width/height are the current target dimensions; originalWidth/Height are
// threaded through to the returned Dimensions for caller coordinate mapping.
func applyFinalFallback(src image.Image, width, height, originalWidth, originalHeight int) ResizeResult {
	smallerWidth := min(width, 1000)
	denom := max(width, 1)
	// TS: Math.round((height * smallerWidth) / Math.max(width, 1)).
	smallerHeight := (height*smallerWidth + denom/2) / denom
	scaled := scaleImage(src, smallerWidth, smallerHeight)
	var buf bytes.Buffer
	// TS ignores the encode error (sharp.toBuffer throws, caught by try/catch).
	// In Go a failed encode yields an empty buffer; the caller's downstream
	// base64 gate would then pass it through. Match TS by best-effort encode.
	_ = jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: 20})
	return ResizeResult{
		Buffer:    buf.Bytes(),
		MediaType: "jpeg",
		Dimensions: &ImageDimensions{
			OriginalWidth:  originalWidth,
			OriginalHeight: originalHeight,
			DisplayWidth:   smallerWidth,
			DisplayHeight:  smallerHeight,
		},
	}
}

// MaybeResizeAndDownsampleImageBlock ports TS maybeResizeAndDownsampleImageBlock
// (imageResizer.ts:445–496). Non-base64 blocks pass through unchanged.
// Base64 blocks are decoded, run through MaybeResizeAndDownsampleImageBuffer,
// and re-wrapped with the post-resize media type.
func MaybeResizeAndDownsampleImageBlock(block types.ContentBlock) (types.ContentBlock, *ImageDimensions, error) {
	if block.Source == nil || block.Source.Type != "base64" {
		return block, nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(block.Source.Data)
	if err != nil {
		return block, nil, fmt.Errorf("decode base64 image block: %w", err)
	}
	// TS line 465: mediaType.split('/')[1] || 'png'.
	ext := "png"
	if mt := block.Source.MediaType; strings.HasPrefix(mt, "image/") {
		ext = mt[len("image/"):]
	}
	resized, err := MaybeResizeAndDownsampleImageBuffer(raw, len(raw), ext)
	if err != nil {
		return block, nil, err
	}
	return types.NewImageBlock(types.ImageSource{
		Type:      "base64",
		MediaType: "image/" + resized.MediaType,
		Data:      base64.StdEncoding.EncodeToString(resized.Buffer),
	}), resized.Dimensions, nil
}

// DetectImageFormatFromBuffer ports TS detectImageFormatFromBuffer
// (imageResizer.ts:769–807). Returns the canonical "image/<ext>" string.
// Exported because fileread's catch path may use it for byte-truthful MIME.
func DetectImageFormatFromBuffer(buf []byte) string {
	if len(buf) < 4 {
		return "image/png" // TS default
	}
	// PNG: 89 50 4E 47
	if buf[0] == 0x89 && buf[1] == 0x50 && buf[2] == 0x4e && buf[3] == 0x47 {
		return "image/png"
	}
	// JPEG: FF D8 FF
	if buf[0] == 0xff && buf[1] == 0xd8 && buf[2] == 0xff {
		return "image/jpeg"
	}
	// GIF: "GIF" (47 49 46)
	if buf[0] == 0x47 && buf[1] == 0x49 && buf[2] == 0x46 {
		return "image/gif"
	}
	// WebP: "RIFF" .... "WEBP"
	if buf[0] == 0x52 && buf[1] == 0x49 && buf[2] == 0x46 && buf[3] == 0x46 {
		if len(buf) >= 12 && buf[8] == 0x57 && buf[9] == 0x45 && buf[10] == 0x42 && buf[11] == 0x50 {
			return "image/webp"
		}
	}
	return "image/png"
}

// DetectImageFormatFromBase64 ports TS detectImageFormatFromBase64
// (imageResizer.ts:819–832). Any decode error returns the PNG default.
func DetectImageFormatFromBase64(b64 string) string {
	buf, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "image/png"
	}
	return DetectImageFormatFromBuffer(buf)
}
