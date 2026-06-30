// Package computer implements the Computer tool: a platform-agnostic
// perception/action surface (Backend) plus an Android backend that drives a
// GBot app over WebSocket JSON-RPC.
package computer

import "context"

// ElementRef is one addressable element returned by Screen, with its ref
// number and the device-reported bounds. Refs are 1-based and assigned by
// Screen in tree order.
//
// JSON tags preserve the GBot app's Kotlin property names verbatim — Gson
// serializes a Kotlin data class using its property names, and the `is`-prefixed
// booleans must be tagged explicitly because Go's default lower-casing would
// turn `Clickable` into "clickable" (the Kotlin field is "isClickable").
type ElementRef struct {
	Ref                int    `json:"-"`
	ClassName          string `json:"className"`
	Text               string `json:"text"`
	ContentDescription string `json:"contentDescription"`
	ViewID             string `json:"viewId"`
	PackageName        string `json:"packageName"`
	Clickable          bool   `json:"isClickable"`
	Scrollable         bool   `json:"isScrollable"`
	Editable           bool   `json:"isEditable"`
	Enabled            bool   `json:"isEnabled"`
	Checked            bool   `json:"isChecked"`
	Focused            bool   `json:"isFocused"`
	Selected           bool   `json:"isSelected"`
	Bounds             Bounds `json:"bounds"`
}

// Bounds is an absolute screen rectangle in pixels. left<right, top<bottom.
// The JSON tags mirror the GBot app's nested bounds object
// {"left":...,"top":...,"right":...,"bottom":...}.
type Bounds struct {
	Left   int `json:"left"`
	Top    int `json:"top"`
	Right  int `json:"right"`
	Bottom int `json:"bottom"`
}

// Center returns the geometric center of the rectangle. Used by ref-resolved
// click/open_menu to translate an element ref into tap coordinates.
func (b Bounds) Center() (int, int) { return (b.Left + b.Right) / 2, (b.Top + b.Bottom) / 2 }

// Width returns Right - Left.
func (b Bounds) Width() int { return b.Right - b.Left }

// Height returns Bottom - Top.
func (b Bounds) Height() int { return b.Bottom - b.Top }

// ScreenResult is the Screen() return: a flat, ref-numbered element list +
// the raw tree (for token-cheap hierarchical rendering) + screen size.
//
// Width/Height are 0 when the underlying source does not report screen size
// (get_ui_tree does not — only get_device_info does). renderScreenResult
// treats 0 as "unknown", never as "0 pixels".
type ScreenResult struct {
	Width, Height int
	Elements      []ElementRef
	Tree          *UINode
}

// UINode mirrors the GBot app's get_ui_tree node (app/android/.../model/Models.kt:UINode).
// the tree render only; refs live in ScreenResult.Elements.
type UINode struct {
	ClassName          string   `json:"className"`
	Text               string   `json:"text"`
	ContentDescription string   `json:"contentDescription"`
	ViewID             string   `json:"viewId"`
	PackageName        string   `json:"packageName"`
	Clickable          bool     `json:"isClickable"`
	Scrollable         bool     `json:"isScrollable"`
	Editable           bool     `json:"isEditable"`
	Enabled            bool     `json:"isEnabled"`
	Checked            bool     `json:"isChecked"`
	Focused            bool     `json:"isFocused"`
	Selected           bool     `json:"isSelected"`
	Bounds             Bounds   `json:"bounds"`
	Children           []UINode `json:"children"`
}

// Screenshot is the Screenshot() return.
type Screenshot struct {
	MIMEType string // "image/jpeg"
	DataB64  string // base64-encoded JPEG bytes
	Width    int
	Height   int
}

// DeviceInfo is the DeviceInfo() return (mirrors the GBot app's get_device_info).
type DeviceInfo struct {
	Manufacturer string
	Model        string
	SDK          int
	Release      string
	ScreenWidth  int
	ScreenHeight int
	Density      float64
	DensityDPI   int
}

// Backend is the platform-agnostic computer-use surface. Every method maps
// 1:1 to a tool action. Implementations own connection lifecycle themselves;
// the interface deliberately does not expose connect/disconnect so it can be
// implemented without forcing callers to wire a device.
type Backend interface {
	Screen(ctx context.Context, maxDepth int) (*ScreenResult, error)
	Screenshot(ctx context.Context) (*Screenshot, error)
	Click(ctx context.Context, x, y int) error
	ClickElement(ctx context.Context, ref int) error
	OpenMenu(ctx context.Context, x, y int) error
	OpenMenuElement(ctx context.Context, ref int) error
	Type(ctx context.Context, text, mode string) error
	SendKey(ctx context.Context, key string) error
	Scroll(ctx context.Context, direction string) error
	Zoom(ctx context.Context, x, y int, scale float64) error
	DeviceInfo(ctx context.Context) (*DeviceInfo, error)
	OpenApp(ctx context.Context, packageName string) error
	SendFile(ctx context.Context, path string) error
}
