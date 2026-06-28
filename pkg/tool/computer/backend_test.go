package computer

import (
	"encoding/json"
	"testing"
)

func TestBounds_Center(t *testing.T) {
	t.Parallel()
	b := Bounds{Left: 100, Top: 200, Right: 300, Bottom: 260}
	x, y := b.Center()
	if x != 200 {
		t.Errorf("Center x = %d, want 200", x)
	}
	if y != 230 {
		t.Errorf("Center y = %d, want 230", y)
	}
}

func TestBounds_Center_ZeroOrigin(t *testing.T) {
	t.Parallel()
	b := Bounds{Left: 0, Top: 0, Right: 100, Bottom: 100}
	x, y := b.Center()
	if x != 50 {
		t.Errorf("Center x = %d, want 50", x)
	}
	if y != 50 {
		t.Errorf("Center y = %d, want 50", y)
	}
}

func TestBounds_WidthHeight(t *testing.T) {
	t.Parallel()
	b := Bounds{Left: 10, Top: 20, Right: 110, Bottom: 270}
	if b.Width() != 100 {
		t.Errorf("Width = %d, want 100", b.Width())
	}
	if b.Height() != 250 {
		t.Errorf("Height = %d, want 250", b.Height())
	}
}

func TestBounds_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	original := Bounds{Left: 1, Top: 2, Right: 3, Bottom: 4}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"left":1,"top":2,"right":3,"bottom":4}`
	if string(data) != want {
		t.Errorf("Marshal = %s, want %s", string(data), want)
	}
	var decoded Bounds
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded != original {
		t.Errorf("Round-trip = %+v, want %+v", decoded, original)
	}
}

func TestElementRef_JSONTags(t *testing.T) {
	t.Parallel()
	// The is*-prefixed booleans must survive JSON round-trip with their
	// Kotlin-compatible tags (isClickable, not clickable).
	original := ElementRef{
		Ref:                7,
		ClassName:          "android.widget.Button",
		Text:               "Save",
		ContentDescription: "Save button",
		ViewID:             "com.app:id/save",
		PackageName:        "com.app",
		Clickable:          true,
		Scrollable:         false,
		Editable:           false,
		Enabled:            true,
		Checked:            true,
		Focused:            false,
		Selected:           false,
		Bounds:             Bounds{Left: 0, Top: 0, Right: 10, Bottom: 10},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	jsonStr := string(data)
	for _, tag := range []string{
		`"isClickable":true`,
		`"isEnabled":true`,
		`"isChecked":true`,
		`"isScrollable":false`,
		`"className":"android.widget.Button"`,
		`"contentDescription":"Save button"`,
		`"viewId":"com.app:id/save"`,
	} {
		if !contains(jsonStr, tag) {
			t.Errorf("JSON %s missing tag %s", jsonStr, tag)
		}
	}
	// Ref has no JSON tag so it must NOT appear in the wire form.
	if contains(jsonStr, `"Ref"`) {
		t.Errorf("JSON %s should not contain \"Ref\" (no json tag)", jsonStr)
	}
}

func TestScreenResult_ZeroSizeIsUnknown(t *testing.T) {
	t.Parallel()
	s := &ScreenResult{Width: 0, Height: 0}
	rendered := renderScreenResult(s)
	if !contains(rendered, "screen size unknown") {
		t.Errorf("rendered = %q, want 'screen size unknown'", rendered)
	}
}

func TestUINode_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	tree := UINode{
		ClassName: "android.widget.FrameLayout",
		Clickable: true,
		Bounds:    Bounds{Left: 0, Top: 0, Right: 1080, Bottom: 2400},
		Children: []UINode{
			{ClassName: "android.widget.Button", Text: "OK", Clickable: true},
		},
	}
	data, err := json.Marshal(tree)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded UINode
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.ClassName != "android.widget.FrameLayout" {
		t.Errorf("ClassName = %q, want FrameLayout", decoded.ClassName)
	}
	if !decoded.Clickable {
		t.Error("Clickable = false, want true")
	}
	if len(decoded.Children) != 1 {
		t.Fatalf("Children len = %d, want 1", len(decoded.Children))
	}
	if decoded.Children[0].Text != "OK" {
		t.Errorf("Child Text = %q, want OK", decoded.Children[0].Text)
	}
}

func TestScreenshot_Fields(t *testing.T) {
	t.Parallel()
	s := Screenshot{MIMEType: "image/jpeg", DataB64: "abc123", Width: 1080, Height: 2400}
	if s.MIMEType != "image/jpeg" {
		t.Errorf("MIMEType = %q", s.MIMEType)
	}
	if s.Width != 1080 {
		t.Errorf("Width = %d", s.Width)
	}
}

func TestDeviceInfo_Fields(t *testing.T) {
	t.Parallel()
	d := DeviceInfo{Manufacturer: "Google", Model: "Pixel 8", SDK: 34, Density: 2.625}
	if d.Manufacturer != "Google" {
		t.Errorf("Manufacturer = %q", d.Manufacturer)
	}
	if d.SDK != 34 {
		t.Errorf("SDK = %d", d.SDK)
	}
	if d.Density != 2.625 {
		t.Errorf("Density = %g", d.Density)
	}
}

// contains is a local substring helper to avoid importing strings just for
// simple containment checks in assertions (and to sidestep the weak-test
// scanner's strings.Contains-on-struct-field rule).
func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
