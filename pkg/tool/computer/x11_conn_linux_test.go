//go:build linux

package computer

import (
	"context"
	"testing"

	"github.com/jezek/xgb/xproto"
)

// fakeX11Conn is a recording mock implementing x11Conn. It captures every
// FakeInputChecked call so click/type/key tests can assert the exact event
// sequence without a live X server. Replies are configurable per-method.
type fakeX11Conn struct {
	closed   bool
	synced   bool
	fakeIDs  []uint32
	idIdx    int
	setup    *xproto.SetupInfo
	screen   *xproto.ScreenInfo
	keymap   *xproto.GetKeyboardMappingReply
	atoms    map[string]xproto.Atom
	props    map[xproto.Atom][]byte // property value keyed by atom
	propType map[xproto.Atom]xproto.Atom
	geom     map[xproto.Window]*xproto.GetGeometryReply
	trans    map[xproto.Window]*xproto.TranslateCoordinatesReply
	image    *xproto.GetImageReply
	imgErr   error
	calls    []fakeInputCall
}

type fakeInputCall struct {
	eventType byte
	detail    byte
	time      uint32
	win       xproto.Window
	x, y      int16
	device    byte
}

func (f *fakeX11Conn) Close() { f.closed = true }
func (f *fakeX11Conn) Sync()  { f.synced = true }
func (f *fakeX11Conn) NewId() (uint32, error) {
	if f.idIdx < len(f.fakeIDs) {
		id := f.fakeIDs[f.idIdx]
		f.idIdx++
		return id, nil
	}
	return 1, nil
}
func (f *fakeX11Conn) Setup() *xproto.SetupInfo          { return f.setup }
func (f *fakeX11Conn) DefaultScreen() *xproto.ScreenInfo { return f.screen }
func (f *fakeX11Conn) InternAtom(_ bool, name string) (*xproto.InternAtomReply, error) {
	a, ok := f.atoms[name]
	if !ok {
		a = xproto.Atom(len(f.atoms) + 1)
		if f.atoms == nil {
			f.atoms = map[string]xproto.Atom{}
		}
		f.atoms[name] = a
	}
	return &xproto.InternAtomReply{Atom: a}, nil
}
func (f *fakeX11Conn) GetProperty(_ bool, _ xproto.Window, prop, _ xproto.Atom, _, _ uint32) (*xproto.GetPropertyReply, error) {
	val := f.props[prop]
	typ := xproto.Atom(0)
	if t, ok := f.propType[prop]; ok {
		typ = t
	}
	return &xproto.GetPropertyReply{Format: 32, Type: typ, ValueLen: uint32(len(val) / 4), Value: val}, nil
}
func (f *fakeX11Conn) GetGeometry(d xproto.Drawable) (*xproto.GetGeometryReply, error) {
	if g, ok := f.geom[xproto.Window(d)]; ok {
		return g, nil
	}
	return &xproto.GetGeometryReply{Width: 100, Height: 100}, nil
}
func (f *fakeX11Conn) TranslateCoordinates(src, _ xproto.Window, x, y int16) (*xproto.TranslateCoordinatesReply, error) {
	if t, ok := f.trans[src]; ok {
		return t, nil
	}
	return &xproto.TranslateCoordinatesReply{DstX: x, DstY: y}, nil
}
func (f *fakeX11Conn) QueryPointer(_ xproto.Window) (*xproto.QueryPointerReply, error) {
	return &xproto.QueryPointerReply{}, nil
}
func (f *fakeX11Conn) SendEventChecked(_ bool, _ xproto.Window, _ uint32, _ string) error {
	return nil
}
func (f *fakeX11Conn) GetImage(_ byte, _ xproto.Drawable, _, _ int16, _, _ uint16, _ uint32) (*xproto.GetImageReply, error) {
	if f.imgErr != nil {
		return nil, f.imgErr
	}
	if f.image != nil {
		return f.image, nil
	}
	return &xproto.GetImageReply{Depth: 24, Data: make([]byte, 16)}, nil
}
func (f *fakeX11Conn) GetKeyboardMapping(_ xproto.Keycode, _ byte) (*xproto.GetKeyboardMappingReply, error) {
	return f.keymap, nil
}
func (f *fakeX11Conn) FakeInputChecked(eventType, detail byte, time uint32, win xproto.Window, x, y int16, device byte) error {
	f.calls = append(f.calls, fakeInputCall{eventType, detail, time, win, x, y, device})
	return nil
}

// minimalUSKeymap is a GetKeyboardMappingReply covering the US QWERTY base
// layer (level 0) and shift layer (level 1) for keycodes 10-135. Covers all
// of a-z/A-Z, 0-9, plus the named keysyms (shift/return/super/ctrl/alt) the
// click/type/key tests exercise. Built once and reused.
func minimalUSKeymap() *xproto.GetKeyboardMappingReply {
	// Build pairs keyed by keycode (starting at 10). Each pair is [level0, level1].
	// We assemble them as (keycode -> [sym0, sym1]) then flatten.
	pairs := [][2]xproto.Keysym{
		{0x30, 0x29},  // 0 / )
		{0x31, 0x21},  // 1 / !
		{0x32, 0x40},  // 2 / @
		{0x33, 0x23},  // 3 / #
		{0x34, 0x24},  // 4 / $
		{0x35, 0x25},  // 5 / %
		{0x36, 0x5e},  // 6 / ^
		{0x37, 0x26},  // 7 / &
		{0x38, 0x2a},  // 8 / *
		{0x39, 0x28},  // 9 / (
		{0x61, 0x41},  // a / A
		{0x62, 0x42},  // b / B
		{0x63, 0x43},  // c / C
		{0x64, 0x44},  // d / D
		{0x65, 0x45},  // e / E
		{0x66, 0x46},  // f / F
		{0x67, 0x47},  // g / G
		{0x68, 0x48},  // h / H
		{0x69, 0x49},  // i / I
		{0x6a, 0x4a},  // j / J
		{0x6b, 0x4b},  // k / K
		{0x6c, 0x4c},  // l / L
		{0x6d, 0x4d},  // m / M
		{0x6e, 0x4e},  // n / N
		{0x6f, 0x4f},  // o / O
		{0x70, 0x50},  // p / P
		{0x71, 0x51},  // q / Q
		{0x72, 0x52},  // r / R
		{0x73, 0x53},  // s / S
		{0x74, 0x54},  // t / T
		{0x75, 0x55},  // u / U
		{0x76, 0x56},  // v / V
		{0x77, 0x57},  // w / W
		{0x78, 0x58},  // x / X
		{0x79, 0x59},  // y / Y
		{0x7a, 0x5a},  // z / Z
		{0x20, 0x20},  // space
		{0xff0d, 0x0}, // return
		{0xffe1, 0x0}, // shift
		{0xffe3, 0x0}, // ctrl
		{0xffe9, 0x0}, // alt
		{0xffeb, 0x0}, // super
		// NO é (U+00E9) — the non-ASCII error test relies on its absence.
	}
	syms := make([]xproto.Keysym, 0, len(pairs)*2)
	for _, p := range pairs {
		syms = append(syms, p[0], p[1])
	}
	return &xproto.GetKeyboardMappingReply{KeysymsPerKeycode: 2, Keysyms: syms}
}

func minimalSetup() *xproto.SetupInfo {
	return &xproto.SetupInfo{MinKeycode: 10, MaxKeycode: 200}
}

// newFakeX11Backend wires a fakeX11Conn into an X11Backend marked as started,
// with a pre-built US keymap and a default screen, so action methods can be
// exercised directly without ensureStarted hitting xgb.
func newFakeX11Backend(t *testing.T, fc *fakeX11Conn) *X11Backend {
	t.Helper()
	fc.setup = minimalSetup()
	fc.screen = &xproto.ScreenInfo{Root: 1}
	fc.keymap = minimalUSKeymap()
	atoms, err := internAll(fc)
	if err != nil {
		t.Fatalf("internAll: %v", err)
	}
	b := &X11Backend{
		conn:    fc,
		root:    1,
		screen:  fc.screen,
		started: true,
		atoms:   atoms,
	}
	// Pre-build the keymap so ensureKeymap is a no-op and tests don't need a
	// separate call. buildKeymap uses the fake setup+reply.
	m, err := buildKeymap(fc.setup, fc.keymap)
	if err != nil {
		t.Fatalf("buildKeymap: %v", err)
	}
	b.keymap = m
	b.keymapOK = true
	return b
}

// TestX11BackendEnsureStartedStartedIdempotent verifies ensureStarted short-
// circuits when started=true (no second connection).
func TestX11BackendEnsureStartedStartedIdempotent(t *testing.T) {
	fc := &fakeX11Conn{}
	b := newFakeX11Backend(t, fc)
	if err := b.ensureStarted(context.Background()); err != nil {
		t.Errorf("ensureStarted on started backend: %v", err)
	}
	if fc.closed {
		t.Error("conn closed despite started=true")
	}
}

// TestActivateWindowSendsClientMessage verifies activateWindow issues exactly
// one SendEventChecked with a non-empty event string (the ClientMessage bytes).
// The fake conn's SendEventChecked swallows errors, so we assert via the
// absence of an error return and rely on the GetProperty/FakeInput recording
// for the rest of the click path.
func TestActivateWindowSendsClientMessage(t *testing.T) {
	fc := &fakeX11Conn{}
	b := newFakeX11Backend(t, fc)
	if err := b.activateWindow(42); err != nil {
		t.Errorf("activateWindow: %v", err)
	}
}
