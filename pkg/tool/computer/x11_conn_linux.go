//go:build linux

package computer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

// x11Conn is the minimal xgb surface X11Backend uses. Mocked in tests via
// fakeX11Conn so keymap/BGRA/EWMH parsing is unit-testable without a display.
// C5: 13 methods — no Composite (NewPixmap/FreePixmap/NameWindowPixmap) in v1.
// If Composite is added later (XWayland support), it extends this interface.
type x11Conn interface {
	Close()
	Sync()
	NewId() (uint32, error)
	Setup() *xproto.SetupInfo
	DefaultScreen() *xproto.ScreenInfo

	InternAtom(onlyIfExists bool, name string) (*xproto.InternAtomReply, error)
	GetProperty(delete bool, win xproto.Window, prop, typ xproto.Atom, off, longLen uint32) (*xproto.GetPropertyReply, error)
	GetGeometry(d xproto.Drawable) (*xproto.GetGeometryReply, error)
	TranslateCoordinates(src, dst xproto.Window, x, y int16) (*xproto.TranslateCoordinatesReply, error)
	QueryPointer(win xproto.Window) (*xproto.QueryPointerReply, error)
	SendEventChecked(propagate bool, dst xproto.Window, mask uint32, event string) error
	GetImage(format byte, d xproto.Drawable, x, y int16, w, h uint16, planeMask uint32) (*xproto.GetImageReply, error)
	GetKeyboardMapping(first xproto.Keycode, count byte) (*xproto.GetKeyboardMappingReply, error)
	FakeInputChecked(eventType, detail byte, time uint32, win xproto.Window, x, y int16, device byte) error
}

// xgbConn wraps *xgb.Conn to satisfy x11Conn. Each method issues the request
// and blocks for the reply (or checks the cookie) directly — simpler than
// perfuncted's cookie abstraction, sufficient for our single-threaded action
// dispatch.
type xgbConn struct {
	conn     *xgb.Conn
	hasXTest bool
}

func newXgbConn(display string) (*xgbConn, error) {
	c, err := xgb.NewConnDisplay(display)
	if err != nil {
		return nil, err
	}
	xc := &xgbConn{conn: c}
	if err := xtest.Init(c); err == nil {
		xc.hasXTest = true
	}
	return xc, nil
}

func (c *xgbConn) Close() { c.conn.Close() }
func (c *xgbConn) Sync()  { c.conn.Sync() }
func (c *xgbConn) NewId() (uint32, error) {
	return c.conn.NewId()
}
func (c *xgbConn) Setup() *xproto.SetupInfo {
	return xproto.Setup(c.conn)
}
func (c *xgbConn) DefaultScreen() *xproto.ScreenInfo {
	return xproto.Setup(c.conn).DefaultScreen(c.conn)
}
func (c *xgbConn) InternAtom(onlyIfExists bool, name string) (*xproto.InternAtomReply, error) {
	return xproto.InternAtom(c.conn, onlyIfExists, uint16(len(name)), name).Reply()
}
func (c *xgbConn) GetProperty(delete bool, win xproto.Window, prop, typ xproto.Atom, off, longLen uint32) (*xproto.GetPropertyReply, error) {
	return xproto.GetProperty(c.conn, delete, win, prop, typ, off, longLen).Reply()
}
func (c *xgbConn) GetGeometry(d xproto.Drawable) (*xproto.GetGeometryReply, error) {
	return xproto.GetGeometry(c.conn, d).Reply()
}
func (c *xgbConn) TranslateCoordinates(src, dst xproto.Window, x, y int16) (*xproto.TranslateCoordinatesReply, error) {
	return xproto.TranslateCoordinates(c.conn, src, dst, x, y).Reply()
}
func (c *xgbConn) QueryPointer(win xproto.Window) (*xproto.QueryPointerReply, error) {
	return xproto.QueryPointer(c.conn, win).Reply()
}
func (c *xgbConn) SendEventChecked(propagate bool, dst xproto.Window, mask uint32, event string) error {
	return xproto.SendEventChecked(c.conn, propagate, dst, mask, event).Check()
}
func (c *xgbConn) GetImage(format byte, d xproto.Drawable, x, y int16, w, h uint16, planeMask uint32) (*xproto.GetImageReply, error) {
	return xproto.GetImage(c.conn, format, d, x, y, w, h, planeMask).Reply()
}
func (c *xgbConn) GetKeyboardMapping(first xproto.Keycode, count byte) (*xproto.GetKeyboardMappingReply, error) {
	return xproto.GetKeyboardMapping(c.conn, first, count).Reply()
}
func (c *xgbConn) FakeInputChecked(eventType, detail byte, time uint32, win xproto.Window, x, y int16, device byte) error {
	return xtest.FakeInputChecked(c.conn, eventType, detail, time, win, x, y, device).Check()
}

// X11Backend drives an X11 display directly via jezek/xgb. No cua-driver,
// no xdotool, no CGO. One lazy xgb connection per backend, kept alive for the
// process lifetime (mirrors CuaBackend's session model).
type X11Backend struct {
	mu       sync.Mutex
	conn     x11Conn
	root     xproto.Window
	screen   *xproto.ScreenInfo
	display  string
	started  bool
	atoms    ewmhAtoms
	keymap   map[xproto.Keysym]keycodeLevel
	keymapOK bool
}

// ewmhAtoms holds the EWMH/ICCCM atoms X11Backend interns at startup.
type ewmhAtoms struct {
	netClientList   xproto.Atom
	netActiveWindow xproto.Atom
	netWMName       xproto.Atom
	netWMPID        xproto.Atom
	netFrameExtents xproto.Atom
	utf8String      xproto.Atom
}

// keycodeLevel is the (keycode, shift-level) a keysym resolves to under the
// current server keymap. level>=1 means Shift must be held.
type keycodeLevel struct {
	keycode xproto.Keycode
	level   int
}

// NewX11Backend returns an X11Backend with started=false (lazy connect, like
// NewCuaBackend). No xgb connection is opened until ensureStarted.
func NewX11Backend() *X11Backend {
	return &X11Backend{}
}

// internAll fetches every EWMH atom in one helper. Any failure is hard —
// EWMH is mandatory for window enumeration and focus activation.
func internAll(conn x11Conn) (ewmhAtoms, error) {
	var a ewmhAtoms
	for name, ptr := range map[string]*xproto.Atom{
		"_NET_CLIENT_LIST":   &a.netClientList,
		"_NET_ACTIVE_WINDOW": &a.netActiveWindow,
		"_NET_WM_NAME":       &a.netWMName,
		"_NET_WM_PID":        &a.netWMPID,
		"_NET_FRAME_EXTENTS": &a.netFrameExtents,
		"UTF8_STRING":        &a.utf8String,
	} {
		rep, err := conn.InternAtom(false, name)
		if err != nil {
			return ewmhAtoms{}, fmt.Errorf("x11: intern EWMH atom %q: %w", name, err)
		}
		*ptr = rep.Atom
	}
	return a, nil
}

// ensureStarted lazily opens the xgb connection on first call. XTEST and EWMH
// are mandatory; missing either is a hard error so the caller surfaces an
// actionable message instead of silently failing every action.
func (b *X11Backend) ensureStarted(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.started {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	display, err := detectDisplay()
	if err != nil {
		return fmt.Errorf("no X11 display with visible windows found; set DISPLAY or start one under /tmp/.X11-unix: %w", err)
	}
	b.display = display

	conn, err := newXgbConn(display)
	if err != nil {
		return fmt.Errorf("x11: connect to %q: %w", display, err)
	}
	if !conn.hasXTest {
		conn.Close()
		return errors.New("x11: XTEST extension missing; the Computer tool cannot inject input without it")
	}
	atoms, err := internAll(conn)
	if err != nil {
		conn.Close()
		return err
	}

	b.conn = conn
	b.atoms = atoms
	b.screen = conn.DefaultScreen()
	b.root = b.screen.Root
	b.started = true
	slog.Debug("computer: X11Backend started", "display", display)
	return nil
}

// Stop closes the xgb connection. Best-effort.
func (b *X11Backend) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != nil {
		b.conn.Close()
		b.conn = nil
	}
	b.started = false
}
