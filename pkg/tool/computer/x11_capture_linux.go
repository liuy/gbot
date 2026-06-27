//go:build linux

package computer

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"

	"github.com/jezek/xgb/xproto"
)

// decodeBGRA converts raw X GetImage ZPixmap BGRA bytes (32-bit, depth 24/32)
// to an *image.RGBA. stride is bytes-per-row (usually w*4). Pixels outside the
// buffer are left zero. X servers return BGRA on little-endian; this swaps
// R<->B and forces A=0xff. Ported from perfuncted/screen/pixels.go.
func decodeBGRA(data []byte, w, h, stride int) *image.RGBA {
	if len(data) == 0 || w <= 0 || h <= 0 || stride < 0 {
		return image.NewRGBA(image.Rect(0, 0, 0, 0))
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rowBytes := w * 4
	for row := range h {
		srcStart := row * stride
		if srcStart >= len(data) {
			break
		}
		srcEnd := min(srcStart+rowBytes, len(data))
		dstOff := row * img.Stride
		copyBGRA(img.Pix[dstOff:dstOff+rowBytes], data[srcStart:srcEnd])
	}
	return img
}

// copyBGRA swaps R<->B and forces alpha to 0xff over n 4-byte pixels.
func copyBGRA(dst, src []byte) {
	n := min(len(dst), len(src))
	n -= n % 4
	for i := 0; i < n; i += 4 {
		dst[i+0] = src[i+2] // R <- B
		dst[i+1] = src[i+1] // G <- G
		dst[i+2] = src[i+0] // B <- R
		dst[i+3] = 0xff     // A
	}
}

// snapshot captures a window's pixels via GetImage(ZPixmap, windowDrawable),
// decodes BGRA→RGBA, encodes PNG, and returns a CaptureResult with Elements=nil
// (X11Backend has no AT-SPI tree). Coordinates are window-relative; the
// returned width/height match the window geometry so the model can address
// pixels by [x,y] in [0,width)×[0,height). If the window drawable is occluded
// (GetImage returns short/empty), we fall back to capturing the root region
// under the window's translated bounds.
func (b *X11Backend) snapshot(ctx context.Context, in Input) (*CaptureResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	if in.Window == nil {
		return nil, fmt.Errorf("snapshot requires window=")
	}
	win := xproto.Window(*in.Window)
	mode := in.Mode
	if mode == "" {
		mode = ModeSom
	}

	geo, err := b.conn.GetGeometry(xproto.Drawable(win))
	if err != nil {
		return nil, fmt.Errorf("x11: GetGeometry: %w", err)
	}

	data, err := b.conn.GetImage(xproto.ImageFormatZPixmap, xproto.Drawable(win), 0, 0, geo.Width, geo.Height, 0xffffffff)
	if err != nil || len(data.Data) == 0 {
		// Fallback: capture the root region under the window (handles occluded
		// or composited windows where the window drawable is unreadable).
		trans, terr := b.conn.TranslateCoordinates(win, b.root, 0, 0)
		if terr != nil {
			return nil, fmt.Errorf("x11: GetImage failed and TranslateCoordinates fallback failed: %w", terr)
		}
		rootData, rerr := b.conn.GetImage(xproto.ImageFormatZPixmap, xproto.Drawable(b.root), trans.DstX, trans.DstY, geo.Width, geo.Height, 0xffffffff)
		if rerr != nil {
			return nil, fmt.Errorf("x11: root GetImage fallback: %w", rerr)
		}
		data = rootData
	}

	rgba := decodeBGRA(data.Data, int(geo.Width), int(geo.Height), int(geo.Width)*4)
	var buf bytes.Buffer
	if err := png.Encode(&buf, rgba); err != nil {
		return nil, fmt.Errorf("x11: png encode: %w", err)
	}
	pngBytes := buf.Bytes()
	b64 := base64.StdEncoding.EncodeToString(pngBytes)

	return &CaptureResult{
		Mode:          mode,
		Width:         int(geo.Width),
		Height:        int(geo.Height),
		PngB64:        b64,
		Elements:      nil,
		PngBytesLen:   len(pngBytes),
		ImageMimeType: "image/png",
	}, nil
}
