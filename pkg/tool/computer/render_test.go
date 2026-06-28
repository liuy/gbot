package computer

import "testing"

func TestRenderScreenResult_KnownSize(t *testing.T) {
	t.Parallel()
	s := &ScreenResult{
		Width:  1080,
		Height: 2400,
		Elements: []ElementRef{
			{Ref: 1, ClassName: "android.widget.Button", Text: "Save", Bounds: Bounds{Left: 100, Top: 200, Right: 300, Bottom: 260}, Clickable: true},
		},
	}
	got := renderScreenResult(s)
	// Header must report the known size, never "screen 0x0" or "unknown".
	wantHeader := "screen 1080x2400, 1 element(s):"
	if !startsWith(got, wantHeader) {
		t.Errorf("header = %q, want it to start with %q", firstLine(got), wantHeader)
	}
	if !contains(got, `#1 Button "Save"`) {
		t.Errorf("rendered = %q, want element line with #1 Button \"Save\"", got)
	}
	if !contains(got, "@ [100,200,300,260]") {
		t.Errorf("rendered = %q, want bounds [100,200,300,260]", got)
	}
	if !contains(got, "[clickable]") {
		t.Errorf("rendered = %q, want [clickable] flag", got)
	}
}

func TestRenderScreenResult_UnknownSize(t *testing.T) {
	t.Parallel()
	// get_ui_tree path: Width=Height=0 → "screen size unknown".
	s := &ScreenResult{
		Width:  0,
		Height: 0,
		Elements: []ElementRef{
			{Ref: 1, ClassName: "android.widget.TextView", Text: "Hi"},
		},
	}
	got := renderScreenResult(s)
	wantHeader := "screen size unknown, 1 element(s):"
	if !startsWith(got, wantHeader) {
		t.Errorf("header = %q, want it to start with %q", firstLine(got), wantHeader)
	}
	// Must NEVER render "screen 0x0".
	if contains(got, "screen 0x0") {
		t.Errorf("rendered = %q, must not contain 'screen 0x0'", got)
	}
}

func TestRenderScreenResult_UsesContentDescription(t *testing.T) {
	t.Parallel()
	// When Text is empty, the label falls back to ContentDescription.
	s := &ScreenResult{
		Width: 1080, Height: 2400,
		Elements: []ElementRef{
			{Ref: 2, ClassName: "android.widget.Image", ContentDescription: "icon"},
		},
	}
	got := renderScreenResult(s)
	if !contains(got, `"icon"`) {
		t.Errorf("rendered = %q, want label 'icon' from ContentDescription", got)
	}
}

func TestRenderScreenResult_NoLabelWhenEmpty(t *testing.T) {
	t.Parallel()
	s := &ScreenResult{
		Width: 1080, Height: 2400,
		Elements: []ElementRef{
			{Ref: 3, ClassName: "android.widget.View", Editable: true},
		},
	}
	got := renderScreenResult(s)
	// The line must contain the ref and class but no quoted label.
	line := elementLines(got)
	if len(line) != 1 {
		t.Fatalf("element lines = %d, want 1", len(line))
	}
	if !contains(line[0], "#3 View") {
		t.Errorf("line = %q, want #3 View", line[0])
	}
	if contains(line[0], `""`) {
		t.Errorf("line = %q, must not have empty quoted label", line[0])
	}
	if !contains(line[0], "[editable]") {
		t.Errorf("line = %q, want [editable] flag", line[0])
	}
}

func TestRenderScreenResult_TruncatesAt60(t *testing.T) {
	t.Parallel()
	els := make([]ElementRef, 75)
	for i := range els {
		els[i] = ElementRef{Ref: i + 1, ClassName: "android.widget.Button", Text: "B"}
	}
	s := &ScreenResult{Width: 1080, Height: 2400, Elements: els}
	got := renderScreenResult(s)
	if !contains(got, "... +15 more") {
		t.Errorf("rendered = %q, want '+15 more' truncation marker", got)
	}
}

func TestRenderScreenResult_NilSafe(t *testing.T) {
	t.Parallel()
	got := renderScreenResult(nil)
	if got != "screen: no result" {
		t.Errorf("renderScreenResult(nil) = %q, want 'screen: no result'", got)
	}
}

func TestRenderDeviceInfo(t *testing.T) {
	t.Parallel()
	d := &DeviceInfo{
		Manufacturer: "Google",
		Model:        "Pixel 8",
		SDK:          34,
		Release:      "14",
		ScreenWidth:  1080,
		ScreenHeight: 2400,
		Density:      2.625,
	}
	got := renderDeviceInfo(d)
	want := "Google Pixel 8 (Android 14, sdk=34) 1080x2400 @ 2.625"
	if got != want {
		t.Errorf("renderDeviceInfo = %q, want %q", got, want)
	}
}

func TestRenderDeviceInfo_NilSafe(t *testing.T) {
	t.Parallel()
	got := renderDeviceInfo(nil)
	if got != "device: no info" {
		t.Errorf("renderDeviceInfo(nil) = %q, want 'device: no info'", got)
	}
}

func TestShortClassName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"android.widget.Button", "Button"},
		{"android.widget.FrameLayout", "FrameLayout"},
		{"Button", "Button"},
		{"", ""},
		{"a.b.c.MyView", "MyView"},
	}
	for _, c := range cases {
		if got := shortClassName(c.in); got != c.want {
			t.Errorf("shortClassName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestElementFlags_Order(t *testing.T) {
	t.Parallel()
	el := ElementRef{Clickable: true, Editable: true, Scrollable: true, Checked: true, Selected: true}
	flags := elementFlags(el)
	want := []string{"clickable", "editable", "scrollable", "checked", "selected"}
	if len(flags) != len(want) {
		t.Fatalf("flags len = %d, want %d", len(flags), len(want))
	}
	for i, f := range flags {
		if f != want[i] {
			t.Errorf("flags[%d] = %q, want %q", i, f, want[i])
		}
	}
}

func TestElementLabel_PrefersText(t *testing.T) {
	t.Parallel()
	el := ElementRef{Text: "real", ContentDescription: "fallback"}
	if got := elementLabel(el); got != "real" {
		t.Errorf("elementLabel = %q, want 'real' (Text preferred)", got)
	}
	el2 := ElementRef{ContentDescription: "fallback"}
	if got := elementLabel(el2); got != "fallback" {
		t.Errorf("elementLabel = %q, want 'fallback' (ContentDescription when no Text)", got)
	}
}

// --- helpers ---

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}

// elementLines extracts the indented element lines (skipping the header and
// any truncation marker).
func elementLines(rendered string) []string {
	var out []string
	for _, line := range splitLines(rendered) {
		if len(line) >= 2 && line[0] == ' ' && line[1] == ' ' && !contains(line, "...") {
			out = append(out, line)
		}
	}
	return out
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start <= len(s) {
		out = append(out, s[start:])
	}
	return out
}
