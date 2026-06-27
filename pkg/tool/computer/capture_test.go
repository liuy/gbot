package computer

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"
)

// makeMinimalPNG returns a base64-encoded PNG of the requested dimensions,
// for use in tests that need a real image header.
func makeMinimalPNG(w, h int) string {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// TestImageDimensionsFromBytesPNG verifies the PNG header parse returns the
// exact dimensions encoded.
func TestImageDimensionsFromBytesPNG(t *testing.T) {
	cases := []struct {
		w, h int
	}{
		{1, 1},
		{8, 8},
		{100, 200},
		{1920, 1080},
	}
	for _, tc := range cases {
		b64 := makeMinimalPNG(tc.w, tc.h)
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		w, h, ok := imageDimensionsFromBytes(raw)
		if !ok {
			t.Errorf("%dx%d: ok=false, want true", tc.w, tc.h)
			continue
		}
		if w != tc.w || h != tc.h {
			t.Errorf("%dx%d: got %dx%d, want exact", tc.w, tc.h, w, h)
		}
	}
}

// TestImageDimensionsFromBytesInvalid verifies malformed bytes return ok=false.
func TestImageDimensionsFromBytesInvalid(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		[]byte("not an image"),
		[]byte{0x89, 'P', 'N', 'G', 0xD, 0xA}, // truncated PNG signature
	}
	for _, raw := range cases {
		if _, _, ok := imageDimensionsFromBytes(raw); ok {
			t.Errorf("imageDimensionsFromBytes(%v): ok=true, want false", raw)
		}
	}
}

// TestImageDimensionsFromBytesJPEG verifies a minimal JPEG header parses.
// Constructed by hand: SOI + SOF0 marker with explicit dimensions.
func TestImageDimensionsFromBytesJPEG(t *testing.T) {
	// FF D8 (SOI)
	// FF C0 00 11 08 (SOF0: marker, length=17, precision=8)
	// 00 20 (height=32)
	// 00 40 (width=64)
	// 03 01 22 00 02 11 01 03 11 01 (3 components — irrelevant to parse)
	// FF D9 (EOI)
	raw := []byte{
		0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x11, 0x08,
		0x00, 0x20, // height
		0x00, 0x40, // width
		0x03, 0x01, 0x22, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01,
		0xFF, 0xD9,
	}
	w, h, ok := imageDimensionsFromBytes(raw)
	if !ok {
		t.Fatal("ok=false for valid JPEG header, want true")
	}
	if w != 64 {
		t.Errorf("width = %d, want 64", w)
	}
	if h != 32 {
		t.Errorf("height = %d, want 32", h)
	}
}

// TestParseElementsFromStructured verifies the structured elements array
// parser preserves index/role/label/bounds/token, and skips malformed entries.
func TestParseElementsFromStructured(t *testing.T) {
	raw := []any{
		map[string]any{
			"element_index": float64(1),
			"role":          "AXButton",
			"label":         "OK",
			"frame":         map[string]any{"x": float64(10), "y": float64(20), "w": float64(100), "h": float64(40)},
			"element_token": "sabc:1",
		},
		map[string]any{
			"element_index": float64(2),
			"role":          "AXTextField",
			"label":         "Email",
		},
		// Malformed: no element_index.
		map[string]any{"role": "AXImage"},
		// Malformed: not a map.
		"garbage",
	}
	elements := parseElementsFromStructured(raw)
	if len(elements) != 2 {
		t.Fatalf("elements count = %d, want 2 (malformed skipped)", len(elements))
	}
	if elements[0].Index != 1 || elements[0].Role != "AXButton" || elements[0].Label != "OK" {
		t.Errorf("element[0] = %+v, want index=1 role=AXButton label=OK", elements[0])
	}
	wantBounds := [4]int{10, 20, 100, 40}
	if elements[0].Bounds != wantBounds {
		t.Errorf("element[0].Bounds = %v, want %v", elements[0].Bounds, wantBounds)
	}
	if elements[0].ElementToken != "sabc:1" {
		t.Errorf("element[0].ElementToken = %q, want sabc:1", elements[0].ElementToken)
	}
	// Missing frame => zero bounds.
	if elements[1].Bounds != ([4]int{}) {
		t.Errorf("element[1].Bounds = %v, want zero", elements[1].Bounds)
	}
}

// TestParseElementsFromStructuredEmpty verifies an empty input returns an
// empty (non-nil) slice.
func TestParseElementsFromStructuredEmpty(t *testing.T) {
	elements := parseElementsFromStructured(nil)
	if len(elements) != 0 {
		t.Errorf("count = %d, want 0", len(elements))
	}
}

// TestParseElementsFromTree verifies the regex fallback parses both the
// classic "label"-quoted and the newer id=Label formats.
func TestParseElementsFromTree(t *testing.T) {
	tree := `AppName — 2 elements
- [1] AXButton "Save"
- [2] AXTextField id=EmailField`
	elements := parseElementsFromTree(tree)
	if len(elements) != 2 {
		t.Fatalf("elements count = %d, want 2", len(elements))
	}
	if elements[0].Index != 1 || elements[0].Role != "AXButton" || elements[0].Label != "Save" {
		t.Errorf("element[0] = %+v", elements[0])
	}
	if elements[1].Index != 2 || elements[1].Role != "AXTextField" || elements[1].Label != "EmailField" {
		t.Errorf("element[1] = %+v", elements[1])
	}
	// Tree path always returns zero bounds (cua_backend.py:242 caveat).
	if elements[0].Bounds != ([4]int{}) {
		t.Errorf("element[0].Bounds = %v, want zero (tree has no bounds)", elements[0].Bounds)
	}
}

// TestFormatElements verifies the 40-line cap and the "+N more" trailer.
func TestFormatElements(t *testing.T) {
	t.Run("under cap", func(t *testing.T) {
		elements := []UIElement{
			{Index: 1, Role: "AXButton", Label: "OK"},
			{Index: 2, Role: "AXButton", Label: "Cancel"},
		}
		lines := formatElements(elements)
		if len(lines) != 2 {
			t.Errorf("lines count = %d, want 2", len(lines))
		}
		if !strings.Contains(lines[0], "#1 AXButton") || !strings.Contains(lines[0], `"OK"`) {
			t.Errorf("line[0] = %q missing element index/label", lines[0])
		}
	})
	t.Run("over cap", func(t *testing.T) {
		elements := make([]UIElement, 50)
		for i := range elements {
			elements[i] = UIElement{Index: i + 1, Role: "AXButton"}
		}
		lines := formatElements(elements)
		// 40 element lines + 1 trailer.
		if len(lines) != 41 {
			t.Errorf("lines count = %d, want 41 (40 + trailer)", len(lines))
		}
		if !strings.Contains(lines[40], "+10 more") {
			t.Errorf("trailer %q missing '+10 more'", lines[40])
		}
	})
	t.Run("label truncate", func(t *testing.T) {
		long := strings.Repeat("x", 100)
		elements := []UIElement{{Index: 1, Role: "AXButton", Label: long}}
		lines := formatElements(elements)
		// The label should be truncated to 60 chars.
		if !strings.Contains(lines[0], strings.Repeat("x", 60)) {
			t.Errorf("line[0] = %q missing 60-char label", lines[0])
		}
		if strings.Contains(lines[0], strings.Repeat("x", 61)) {
			t.Errorf("line[0] = %q has un-truncated label", lines[0])
		}
	})
}

// TestFormatElementsEmpty verifies an empty input returns no lines.
func TestFormatElementsEmpty(t *testing.T) {
	lines := formatElements(nil)
	if len(lines) != 0 {
		t.Errorf("lines count = %d, want 0", len(lines))
	}
}

// TestImageFromResult verifies image extraction from both the image content
// part array and structuredContent screenshot fields (cua_backend.py:610-650).
func TestImageFromResult(t *testing.T) {
	t.Run("from images", func(t *testing.T) {
		out := map[string]any{
			"images":           []string{"BASE64"},
			"image_mime_types": []string{"image/jpeg"},
		}
		b64, mime := imageFromResult(out)
		if b64 != "BASE64" {
			t.Errorf("b64 = %q, want BASE64", b64)
		}
		if mime != "image/jpeg" {
			t.Errorf("mime = %q, want image/jpeg", mime)
		}
	})
	t.Run("from structured screenshot_png_b64", func(t *testing.T) {
		out := map[string]any{
			"structuredContent": map[string]any{
				"screenshot_png_b64":   "STRUCTB64",
				"screenshot_mime_type": "image/png",
			},
		}
		b64, mime := imageFromResult(out)
		if b64 != "STRUCTB64" {
			t.Errorf("b64 = %q, want STRUCTB64", b64)
		}
		if mime != "image/png" {
			t.Errorf("mime = %q, want image/png", mime)
		}
	})
	t.Run("no image", func(t *testing.T) {
		out := map[string]any{"data": "text only"}
		b64, mime := imageFromResult(out)
		if b64 != "" || mime != "" {
			t.Errorf("imageFromResult(no image) = (%q, %q), want empty", b64, mime)
		}
	})
}

// TestParseKeyComboTable verifies the key-combo parser splits modifiers
// from the non-modifier key, applying aliases.
func TestParseKeyComboTable(t *testing.T) {
	cases := []struct {
		in        string
		key       string
		modifiers []string
	}{
		{"cmd+s", "s", []string{"cmd"}},
		{"ctrl+alt+t", "t", []string{"ctrl", "option"}},
		{"return", "return", nil},
		{"cmd+shift+option+ctrl+esc", "esc", []string{"cmd", "shift", "option", "ctrl"}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			key, mods := parseKeyCombo(tc.in)
			if key != tc.key {
				t.Errorf("key = %q, want %q", key, tc.key)
			}
			if len(mods) != len(tc.modifiers) {
				t.Errorf("modifiers = %v, want %v", mods, tc.modifiers)
			}
		})
	}
}

// TestParseKeyComboWinModifier covers the C8 change: win/super/meta/windows
// are recognized as modifiers (previously dropped, silently mis-parsing
// combos like win+r). Each must yield a modifier in the returned set and the
// main key 'r'.
func TestParseKeyComboWinModifier(t *testing.T) {
	for _, in := range []string{"win+r", "super+r", "meta+r", "windows+r"} {
		t.Run(in, func(t *testing.T) {
			key, mods := parseKeyCombo(in)
			if key != "r" {
				t.Errorf("key = %q, want r", key)
			}
			if len(mods) != 1 {
				t.Fatalf("modifiers = %v, want exactly 1", mods)
			}
			m := strings.ToLower(mods[0])
			if m != "win" && m != "super" && m != "meta" && m != "windows" {
				t.Errorf("modifier = %q, want one of win/super/meta/windows", mods[0])
			}
		})
	}
}

// TestExtractWindowTitle verifies the window title regex for both macOS
// (AXWindow "...") and Linux AT-SPI (frame = "...") tree formats.
func TestExtractWindowTitle(t *testing.T) {
	cases := []struct {
		tree string
		want string
	}{
		{`AXWindow "Untitled — Terminal"`, "Untitled — Terminal"},
		{`- AXWindow "Calculator"`, "Calculator"},
		{`- frame = "Terminal - yliu@host: ~/repos"`, "Terminal - yliu@host: ~/repos"},
		{`no window here`, ""},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.tree, func(t *testing.T) {
			got := extractWindowTitle(tc.tree)
			if got != tc.want {
				t.Errorf("extractWindowTitle(%q) = %q, want %q", tc.tree, got, tc.want)
			}
		})
	}
}

// TestSplitTreeText verifies the summary/tree split.
func TestSplitTreeText(t *testing.T) {
	summary, tree := splitTreeText("line one\nrest\nof tree")
	if summary != "line one" {
		t.Errorf("summary = %q, want 'line one'", summary)
	}
	if tree != "rest\nof tree" {
		t.Errorf("tree = %q, want 'rest\\nof tree'", tree)
	}
	// Single-line: tree is empty.
	summary, tree = splitTreeText("only line")
	if summary != "only line" || tree != "" {
		t.Errorf("split(single) = (%q, %q), want ('only line', '')", summary, tree)
	}
}
