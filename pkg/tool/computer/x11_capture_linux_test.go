//go:build linux

package computer

import (
	"testing"
)

// TestDecodeBGRA verifies the BGRA→RGBA byte swap and A=0xff forcing on a
// known 2x2 buffer. Asserts against img.Pix directly (not RGBA() which is
// premultiplied) so the raw stored bytes are checked exactly.
func TestDecodeBGRA(t *testing.T) {
	// 2x2, stride=8 (w*4), tightly packed.
	data := []byte{
		0x10, 0x20, 0x30, 0xff, // px(0,0): B=0x10 G=0x20 R=0x30
		0x40, 0x50, 0x60, 0xff, // px(1,0)
		0x70, 0x80, 0x90, 0xff, // px(0,1)
		0xa0, 0xb0, 0xc0, 0xff, // px(1,1)
	}
	img := decodeBGRA(data, 2, 2, 8)
	if got := img.Bounds().Dx(); got != 2 {
		t.Fatalf("width = %d, want 2", got)
	}
	if got := img.Bounds().Dy(); got != 2 {
		t.Fatalf("height = %d, want 2", got)
	}
	// img.Pix for an *image.RGBA at (x,y): idx = y*Stride + x*4.
	wantPix := []byte{
		0x30, 0x20, 0x10, 0xff, // (0,0): R<-B, G, B<-R, A=ff
		0x60, 0x50, 0x40, 0xff, // (1,0)
		0x90, 0x80, 0x70, 0xff, // (0,1)
		0xc0, 0xb0, 0xa0, 0xff, // (1,1)
	}
	if img.Stride != 8 {
		t.Fatalf("Stride = %d, want 8", img.Stride)
	}
	for i, want := range wantPix {
		if img.Pix[i] != want {
			t.Errorf("Pix[%d] = %02x, want %02x (full Pix=%v)", i, img.Pix[i], want, img.Pix)
			break
		}
	}
}

// TestDecodeBGRAStridePadding verifies decodeBGRA handles stride > w*4
// (row padding). The X server may pad rows to a wider stride; the decoder must
// copy only w*4 pixel bytes per row and skip the trailing padding. Here
// stride=16 for w=2 (8 pixel bytes + 8 padding bytes per row).
func TestDecodeBGRAStridePadding(t *testing.T) {
	// Row 0: 2 pixels (8 bytes) + 8 padding bytes (0xee). Row 1 likewise.
	// Padding bytes are distinct from pixel data so a stride bug that reads
	// past the row would pick them up.
	data := []byte{
		0x10, 0x20, 0x30, 0xff, // px(0,0): B=0x10 G=0x20 R=0x30
		0x40, 0x50, 0x60, 0xff, // px(1,0)
		0xee, 0xee, 0xee, 0xee, 0xee, 0xee, 0xee, 0xee, // row0 padding
		0x70, 0x80, 0x90, 0xff, // px(0,1)
		0xa0, 0xb0, 0xc0, 0xff, // px(1,1)
		0xee, 0xee, 0xee, 0xee, 0xee, 0xee, 0xee, 0xee, // row1 padding
	}
	img := decodeBGRA(data, 2, 2, 16)
	if img.Bounds().Dx() != 2 || img.Bounds().Dy() != 2 {
		t.Fatalf("bounds = %v, want 2x2", img.Bounds())
	}
	// Decoded image Stride is w*4=8 regardless of source stride; padding is
	// dropped on decode, so it must not leak into Pix.
	wantPix := []byte{
		0x30, 0x20, 0x10, 0xff, // (0,0)
		0x60, 0x50, 0x40, 0xff, // (1,0)
		0x90, 0x80, 0x70, 0xff, // (0,1)
		0xc0, 0xb0, 0xa0, 0xff, // (1,1)
	}
	if img.Stride != 8 {
		t.Fatalf("decoded Stride = %d, want 8", img.Stride)
	}
	if len(img.Pix) != len(wantPix) {
		t.Fatalf("len(Pix) = %d, want %d", len(img.Pix), len(wantPix))
	}
	for i, want := range wantPix {
		if img.Pix[i] != want {
			t.Errorf("Pix[%d] = %02x, want %02x (full Pix=%v)", i, img.Pix[i], want, img.Pix)
			break
		}
	}
}

// TestDecodeBGRAEmpty verifies zero/empty inputs return a 0x0 image with no
// panic. stride=0 is NOT treated as empty (per the perfuncted port: only
// stride<0 aborts), so it is excluded here.
func TestDecodeBGRAEmpty(t *testing.T) {
	cases := []struct {
		name         string
		data         []byte
		w, h, stride int
	}{
		{"nil data", nil, 2, 2, 8},
		{"zero width", data8(), 0, 2, 8},
		{"zero height", data8(), 2, 0, 8},
		{"negative stride", data8(), 2, 2, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img := decodeBGRA(tc.data, tc.w, tc.h, tc.stride)
			if img.Bounds().Dx() != 0 || img.Bounds().Dy() != 0 {
				t.Errorf("bounds = %v, want 0x0", img.Bounds())
			}
		})
	}
}

// data8 returns 8 bytes of filler.
func data8() []byte { return []byte{1, 2, 3, 4, 5, 6, 7, 8} }

// TestDecodeBGRAShortBuffer verifies a buffer too short for all rows does not
// panic and decodes the rows that fit (last row stays zero — no source bytes).
func TestDecodeBGRAShortBuffer(t *testing.T) {
	// Claim 2x2 (16 bytes needed) but supply 8 (one full row only).
	data := []byte{0x10, 0x20, 0x30, 0xff, 0x40, 0x50, 0x60, 0xff}
	img := decodeBGRA(data, 2, 2, 8)
	if img.Bounds().Dx() != 2 || img.Bounds().Dy() != 2 {
		t.Fatalf("bounds = %v, want 2x2", img.Bounds())
	}
	// Row 0 (bytes 0..7): swapped; Row 1 (bytes 8..15): absent → zero.
	wantRow0 := []byte{0x30, 0x20, 0x10, 0xff, 0x60, 0x50, 0x40, 0xff}
	for i, want := range wantRow0 {
		if img.Pix[i] != want {
			t.Errorf("row0 Pix[%d] = %02x, want %02x", i, img.Pix[i], want)
		}
	}
	row1Start := img.Stride
	for i := row1Start; i < row1Start+8; i++ {
		if img.Pix[i] != 0 {
			t.Errorf("row1 Pix[%d] = %02x, want 0 (no source bytes)", i, img.Pix[i])
		}
	}
}
