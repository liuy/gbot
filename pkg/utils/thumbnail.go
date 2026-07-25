// Package utils provides shared utilities used across packages.
package utils

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"

	// Register decoders for every format ResizeForThumbnail accepts. Without
	// these blank imports image.Decode returns "unknown format" for any
	// format not pulled in transitively by the rest of the binary — gif and
	// webp are not otherwise reachable from the WUI connector's call graph
	// (gif lives in pkg/tool/fileread, webp only in pkg/mcp), so a production
	// build that links just the WUI path would silently break GIF/WebP
	// history thumbnails. Self-contained here so this file does not regress
	// when an unrelated package drops its import.
	_ "image/gif"

	_ "golang.org/x/image/webp"

	xdraw "golang.org/x/image/draw"
)

// ResizeForThumbnail decodes data, scales it so the longest edge <= maxEdge
// (preserving aspect ratio, never enlarging), and re-encodes as JPEG q80.
// Returns the resized bytes and a normalized media type ("image/jpeg").
// Decode failure or empty input returns an error — callers MUST degrade to a
// placeholder rather than render a broken image.
//
// Distinct from MaybeResizeAndDownsampleImageBuffer, which targets LLM API
// limits (2000px/5MB). This helper targets history-replay thumbnails where
// the only goal is small JSON payloads, so the long edge is bounded by the
// caller (e.g. 512px) and JPEG quality is fixed at q80 for size.
func ResizeForThumbnail(data []byte, maxEdge int) ([]byte, string, error) {
	if len(data) == 0 {
		return nil, "", errors.New("thumbnail: empty input")
	}
	src, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()

	// Never enlarge — already-small images pass through verbatim with their
	// detected media type so quality is not degraded by a needless JPEG round-trip.
	if w <= maxEdge && h <= maxEdge {
		return data, "image/" + format, nil
	}

	// var not := because both targetW and targetH are unconditionally
	// overwritten in the if/else below — initializing them to (w,h) would
	// trip ineffassign.
	var targetW, targetH int
	if w >= h {
		targetW = maxEdge
		targetH = (h*maxEdge + w/2) / w
	} else {
		targetH = maxEdge
		targetW = (w*maxEdge + h/2) / h
	}
	dst := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 80}); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "image/jpeg", nil
}
