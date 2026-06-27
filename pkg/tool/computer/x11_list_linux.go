//go:build linux

package computer

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/jezek/xgb/xproto"
)

// list enumerates on-screen top-level windows via the _NET_CLIENT_LIST root
// property. For each window it reads geometry (translated to root-absolute so
// the reported bounds include the frame decoration), title (_NET_WM_NAME with
// WM_NAME fallback), and PID (_NET_WM_PID, may be 0). Output format matches
// CuaBackend's list so dispatch_test and callers reading Meta["windows"] are
// unchanged.
func (b *X11Backend) list(ctx context.Context) (*ActionResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	rep, err := b.conn.GetProperty(false, b.root, b.atoms.netClientList, xproto.AtomWindow, 0, 1024)
	if err != nil {
		return nil, fmt.Errorf("x11: get _NET_CLIENT_LIST: %w", err)
	}
	if rep.Format == 0 {
		return &ActionResult{OK: true, Action: "list", Message: "no windows found", Meta: map[string]any{"windows": []windowInfo{}, "count": 0}}, nil
	}
	if rep.Format != 32 {
		return nil, fmt.Errorf("x11: unexpected _NET_CLIENT_LIST format %d", rep.Format)
	}
	ids := make([]xproto.Window, len(rep.Value)/4)
	for i := range ids {
		ids[i] = xproto.Window(binary.LittleEndian.Uint32(rep.Value[i*4 : i*4+4]))
	}

	windows := make([]windowInfo, 0, len(ids))
	lines := make([]string, 0, len(ids))
	for _, id := range ids {
		w := b.windowInfoFor(id)
		windows = append(windows, w)
		lines = append(lines, formatWindowLine(w))
	}
	summary := "no windows found"
	if len(lines) > 0 {
		summary = strings.Join(lines, "\n")
	}
	return &ActionResult{
		OK:      true,
		Action:  "list",
		Message: summary,
		Meta: map[string]any{
			"windows": windows,
			"count":   len(windows),
		},
	}, nil
}

// windowInfoFor collects geometry+title+pid for a single window id.
func (b *X11Backend) windowInfoFor(id xproto.Window) windowInfo {
	w := windowInfo{WindowID: int(id)}

	if geo, err := b.conn.GetGeometry(xproto.Drawable(id)); err == nil {
		w.Width = int(geo.Width)
		w.Height = int(geo.Height)
	}
	// Root-absolute position via TranslateCoordinates, then expand to include
	// the frame extents so window-relative screenshot coords line up.
	if trans, err := b.conn.TranslateCoordinates(id, b.root, 0, 0); err == nil {
		w.X = int(trans.DstX)
		w.Y = int(trans.DstY)
		if fr, ferr := b.conn.GetProperty(false, id, b.atoms.netFrameExtents, xproto.GetPropertyTypeAny, 0, 4); ferr == nil && len(fr.Value) == 16 {
			left := int(binary.LittleEndian.Uint32(fr.Value[0:4]))
			right := int(binary.LittleEndian.Uint32(fr.Value[4:8]))
			top := int(binary.LittleEndian.Uint32(fr.Value[8:12]))
			bottom := int(binary.LittleEndian.Uint32(fr.Value[12:16]))
			w.X -= left
			w.Y -= top
			w.Width += left + right
			w.Height += top + bottom
		}
	}

	w.Title = b.windowTitle(id)
	w.PID = b.windowPID(id)
	return w
}

// windowTitle returns _NET_WM_NAME (UTF-8) with a WM_NAME (Latin-1) fallback.
func (b *X11Backend) windowTitle(id xproto.Window) string {
	if rep, err := b.conn.GetProperty(false, id, b.atoms.netWMName, b.atoms.utf8String, 0, 1024); err == nil && len(rep.Value) > 0 {
		return string(rep.Value)
	}
	if rep, err := b.conn.GetProperty(false, id, xproto.AtomWmName, xproto.AtomString, 0, 1024); err == nil && len(rep.Value) > 0 {
		return string(rep.Value)
	}
	return ""
}

// windowPID returns the _NET_WM_PID value, or 0 if unavailable.
func (b *X11Backend) windowPID(id xproto.Window) int {
	rep, err := b.conn.GetProperty(false, id, b.atoms.netWMPID, xproto.GetPropertyTypeAny, 0, 1)
	if err != nil || len(rep.Value) < 4 {
		return 0
	}
	return int(binary.LittleEndian.Uint32(rep.Value[0:4]))
}
