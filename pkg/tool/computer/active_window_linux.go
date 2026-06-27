//go:build linux

package computer

import (
	"os"

	xgb "github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// activeWindowIDForDisplay returns the frontmost app window's X11 id, or 0 if
// it can't be determined.
//
// Two-level strategy:
//  1. _NET_ACTIVE_WINDOW — the WM's authoritative "focused window" property.
//     Most reliable when the WM sets it.
//  2. _NET_CLIENT_LIST_STACKING + _NET_WM_WINDOW_TYPE — walk the stacking list
//     top-to-bottom, return the first type=NORMAL window. This is the fallback
//     for WMs (like xfwm4 under xrdp) that declare _NET_ACTIVE_WINDOW support
//     but leave the property unset.
//
// The window id matches what cua-driver returns in list_windows' window_id
// field (both are X11 window ids), so resolveTargetWindow can match them
// directly.
func activeWindowIDForDisplay(display string) int {
	if display == "" {
		return 0
	}

	// xgb.NewConn reads DISPLAY from the process env. Set it temporarily so
	// the connection targets the right X server even when gbot itself was
	// launched without DISPLAY (e.g. over SSH — the display is resolved by
	// detectDisplay and only injected into the cua-driver child env).
	prev := os.Getenv("DISPLAY")
	_ = os.Setenv("DISPLAY", display)
	X, err := xgb.NewConn()
	_ = os.Setenv("DISPLAY", prev)
	if err != nil {
		return 0
	}
	defer X.Close()

	root := xproto.Setup(X).DefaultScreen(X).Root

	if id := readNetActiveWindow(X, root); id != 0 {
		return id
	}
	return frontmostNormalByStacking(X, root)
}

// readNetActiveWindow reads the _NET_ACTIVE_WINDOW root property. Returns 0
// when the WM hasn't set it.
func readNetActiveWindow(X *xgb.Conn, root xproto.Window) int {
	atom, err := internAtom(X, atomNetActiveWindow)
	if err != nil {
		return 0
	}
	prop, err := xproto.GetProperty(X, false, root, atom,
		xproto.GetPropertyTypeAny, 0, 1).Reply()
	if err != nil || len(prop.Value) < 4 {
		return 0
	}
	return decodeCard32(prop.Value)
}

// frontmostNormalByStacking reads _NET_CLIENT_LIST_STACKING (bottom-to-top
// window order), then walks it top-to-bottom returning the first window whose
// _NET_WM_WINDOW_TYPE contains NORMAL. Skips Desktop and Dock/panel windows.
func frontmostNormalByStacking(X *xgb.Conn, root xproto.Window) int {
	stackAtom, err := internAtom(X, atomNetClientListStacking)
	if err != nil {
		return 0
	}
	prop, err := xproto.GetProperty(X, false, root, stackAtom,
		xproto.GetPropertyTypeAny, 0, 64).Reply()
	if err != nil || len(prop.Value) < 4 {
		return 0
	}

	// Each window is a CARD32. Walk top-to-bottom (reverse of bottom-to-top).
	numWindows := len(prop.Value) / 4
	typeAtom, err := internAtom(X, atomNetWmWindowType)
	if err != nil {
		return 0
	}
	normalAtom, err := internAtom(X, atomNetWmWindowTypeNormal)
	if err != nil {
		return 0
	}
	for i := numWindows - 1; i >= 0; i-- {
		wid := xproto.Window(decodeCard32(prop.Value[i*4:]))
		if wid == 0 {
			continue
		}
		if windowHasType(X, wid, typeAtom, normalAtom) {
			return int(wid)
		}
	}
	return 0
}

// windowHasType reports whether the window's _NET_WM_WINDOW_TYPE property
// contains the target atom. A window can have multiple types; the spec says
// the first recognized type wins, but we check membership because some apps
// list NORMAL alongside others.
func windowHasType(X *xgb.Conn, wid xproto.Window, typeAtom, target xproto.Atom) bool {
	prop, err := xproto.GetProperty(X, false, wid, typeAtom,
		xproto.GetPropertyTypeAny, 0, 16).Reply()
	if err != nil || len(prop.Value) == 0 {
		return false
	}
	for i := 0; i+4 <= len(prop.Value); i += 4 {
		if xproto.Atom(decodeCard32(prop.Value[i:])) == target {
			return true
		}
	}
	return false
}

// internAtom resolves an atom name to its id.
func internAtom(X *xgb.Conn, name string) (xproto.Atom, error) {
	reply, err := xproto.InternAtom(X, true, uint16(len(name)), name).Reply()
	if err != nil {
		return 0, err
	}
	return reply.Atom, nil
}

// decodeCard32 reads a little-endian 32-bit unsigned int from b (must be >=4
// bytes). X11 wire format is always LE regardless of host byte order.
func decodeCard32(b []byte) int {
	return int(uint32(b[0]) | uint32(b[1])<<8 |
		uint32(b[2])<<16 | uint32(b[3])<<24)
}

// EWMH atom names.
const (
	atomNetActiveWindow       = "_NET_ACTIVE_WINDOW"
	atomNetClientListStacking = "_NET_CLIENT_LIST_STACKING"
	atomNetWmWindowType       = "_NET_WM_WINDOW_TYPE"
	atomNetWmWindowTypeNormal = "_NET_WM_WINDOW_TYPE_NORMAL"
)
