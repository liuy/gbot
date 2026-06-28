package computer

import (
	"fmt"
	"strings"
)

// maxRenderElements caps the rendered ref list so a huge screen does not blow
// the token budget. Matches the old formatElements cap.
const maxRenderElements = 60

// renderScreenResult formats the numbered element list for the model.
//
// Header screen-size segment: when both Width and Height are non-zero render
// `screen {W}x{H}, N element(s):`; when either is 0 — the get_ui_tree path,
// which does not report screen size — render `screen size unknown, N
// element(s):`. Never render "screen 0x0": a 0 dimension means unknown, not
// zero pixels.
func renderScreenResult(s *ScreenResult) string {
	if s == nil {
		return "screen: no result"
	}
	var b strings.Builder
	if s.Width > 0 && s.Height > 0 {
		fmt.Fprintf(&b, "screen %dx%d, %d element(s):", s.Width, s.Height, len(s.Elements))
	} else {
		fmt.Fprintf(&b, "screen size unknown, %d element(s):", len(s.Elements))
	}
	b.WriteByte('\n')

	shown := s.Elements
	truncated := 0
	if len(shown) > maxRenderElements {
		truncated = len(shown) - maxRenderElements
		shown = shown[:maxRenderElements]
	}
	for _, el := range shown {
		b.WriteString("  ")
		b.WriteString(renderElement(el))
		b.WriteByte('\n')
	}
	if truncated > 0 {
		fmt.Fprintf(&b, "... +%d more\n", truncated)
	}
	return b.String()
}

// renderElement formats one element line:
// `#<ref> <short-classname> "<label>" @ [bounds] <flags>`.
func renderElement(el ElementRef) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s", el.Ref, shortClassName(el.ClassName))
	if label := elementLabel(el); label != "" {
		fmt.Fprintf(&b, " %q", label)
	}
	fmt.Fprintf(&b, " @ [%d,%d,%d,%d]", el.Bounds.Left, el.Bounds.Top, el.Bounds.Right, el.Bounds.Bottom)
	for _, flag := range elementFlags(el) {
		b.WriteByte(' ')
		b.WriteByte('[')
		b.WriteString(flag)
		b.WriteByte(']')
	}
	return b.String()
}

// elementLabel returns the Text if present, else ContentDescription, else "".
func elementLabel(el ElementRef) string {
	if el.Text != "" {
		return el.Text
	}
	return el.ContentDescription
}

// shortClassName returns the last `.`-segment of the Android class name
// (e.g. "android.widget.Button" → "Button"). Empty input yields "".
func shortClassName(cls string) string {
	if cls == "" {
		return ""
	}
	if i := strings.LastIndex(cls, "."); i >= 0 {
		return cls[i+1:]
	}
	return cls
}

// elementFlags lists the rendered boolean flags in display order.
func elementFlags(el ElementRef) []string {
	var flags []string
	if el.Clickable {
		flags = append(flags, "clickable")
	}
	if el.Editable {
		flags = append(flags, "editable")
	}
	if el.Scrollable {
		flags = append(flags, "scrollable")
	}
	if el.Checked {
		flags = append(flags, "checked")
	}
	if el.Selected {
		flags = append(flags, "selected")
	}
	return flags
}

// renderDeviceInfo formats a one-line device summary.
func renderDeviceInfo(d *DeviceInfo) string {
	if d == nil {
		return "device: no info"
	}
	return fmt.Sprintf("%s %s (Android %s, sdk=%d) %dx%d @ %g",
		d.Manufacturer, d.Model, d.Release, d.SDK,
		d.ScreenWidth, d.ScreenHeight, d.Density)
}
