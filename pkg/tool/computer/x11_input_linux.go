//go:build linux

package computer

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jezek/xgb/xproto"
)

// click/scroll pacing constants. cua-driver uses a 35ms click delay between
// press and release; we match it so double/triple clicks register reliably.
const (
	clickDelay = 35 * time.Millisecond
	keyDelay   = 10 * time.Millisecond
)

// keysymForName maps a key name to an X11 keysym value. Ported from
// perfuncted/input/xtest.go. Covers a-z, A-Z, 0-9, space, return/enter, tab,
// escape/esc, arrows, ctrl/shift/alt/super, f1-f12. For single printable ASCII
// not in the map, keysymForChar uses the character code directly.
var keysymForName = map[string]xproto.Keysym{
	"a": 0x61, "b": 0x62, "c": 0x63, "d": 0x64, "e": 0x65,
	"f": 0x66, "g": 0x67, "h": 0x68, "i": 0x69, "j": 0x6a,
	"k": 0x6b, "l": 0x6c, "m": 0x6d, "n": 0x6e, "o": 0x6f,
	"p": 0x70, "q": 0x71, "r": 0x72, "s": 0x73, "t": 0x74,
	"u": 0x75, "v": 0x76, "w": 0x77, "x": 0x78, "y": 0x79, "z": 0x7a,
	"A": 0x41, "B": 0x42, "C": 0x43, "D": 0x44, "E": 0x45,
	"F": 0x46, "G": 0x47, "H": 0x48, "I": 0x49, "J": 0x4a,
	"K": 0x4b, "L": 0x4c, "M": 0x4d, "N": 0x4e, "O": 0x4f,
	"P": 0x50, "Q": 0x51, "R": 0x52, "S": 0x53, "T": 0x54,
	"U": 0x55, "V": 0x56, "W": 0x57, "X": 0x58, "Y": 0x59, "Z": 0x5a,
	"0": 0x30, "1": 0x31, "2": 0x32, "3": 0x33, "4": 0x34,
	"5": 0x35, "6": 0x36, "7": 0x37, "8": 0x38, "9": 0x39,
	" ": 0x20, "space": 0x20,
	"return": 0xff0d, "enter": 0xff0d,
	"tab":    0xff09,
	"escape": 0xff1b, "esc": 0xff1b,
	"up": 0xff52, "down": 0xff54, "left": 0xff51, "right": 0xff53,
	"ctrl": 0xffe3, "shift": 0xffe1, "alt": 0xffe9, "super": 0xffeb,
	"f1": 0xffbe, "f2": 0xffbf, "f3": 0xffc0, "f4": 0xffc1,
	"f5": 0xffc2, "f6": 0xffc3, "f7": 0xffc4, "f8": 0xffc5,
	"f9": 0xffc6, "f10": 0xffc7, "f11": 0xffc8, "f12": 0xffc9,
}

// keysymForChar resolves a single-character key name (or any printable ASCII
// char) to its keysym, using the byte value directly when the named table has
// no entry. Returns ok=false for non-ASCII or control chars.
func keysymForChar(name string) (xproto.Keysym, bool) {
	if sym, ok := keysymForName[name]; ok {
		return sym, true
	}
	if len(name) == 1 {
		c := name[0]
		if c >= 0x20 && c < 0x7f {
			return xproto.Keysym(c), true
		}
	}
	return 0, false
}

// x11Modifier collapses the model-facing modifier tokens onto the X11 set.
// parseKeyCombo returns cmd/option/ctrl/shift plus win/super/meta/windows;
// X11 only knows ctrl/shift/alt/super, so option→alt and the win-family all
// collapse to super. C8: previously win/super/meta/windows were dropped
// entirely by parseKeyCombo's modifierNames gap.
func x11Modifier(name string) string {
	switch strings.ToLower(name) {
	case "option":
		return "alt"
	case "cmd", "win", "windows", "meta", "super":
		return "super"
	default:
		return strings.ToLower(name)
	}
}

// buildKeymap is the pure keymap-build loop extracted from ensureKeymap so it
// is unit-testable without a live connection. It maps each keysym to the
// (keycode, level) it first appears at, where level = i % keysymsPerKeycode.
// level>=1 means Shift (or another group) is required.
func buildKeymap(setup *xproto.SetupInfo, reply *xproto.GetKeyboardMappingReply) (map[xproto.Keysym]keycodeLevel, error) {
	kpk := int(reply.KeysymsPerKeycode)
	if kpk <= 0 {
		return nil, fmt.Errorf("x11: invalid keyboard mapping: keysyms_per_keycode=%d", kpk)
	}
	min := int(setup.MinKeycode)
	m := make(map[xproto.Keysym]keycodeLevel, len(reply.Keysyms))
	for i, s := range reply.Keysyms {
		if s == 0 {
			continue
		}
		if _, exists := m[s]; exists {
			continue
		}
		m[s] = keycodeLevel{
			keycode: xproto.Keycode(min + i/kpk),
			level:   i % kpk,
		}
	}
	return m, nil
}

// ensureKeymap lazily fetches the server keymap once and caches it.
func (b *X11Backend) ensureKeymap() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.keymapOK {
		return nil
	}
	setup := b.conn.Setup()
	first := xproto.Keycode(setup.MinKeycode)
	count := byte(setup.MaxKeycode - setup.MinKeycode + 1)
	reply, err := b.conn.GetKeyboardMapping(first, count)
	if err != nil {
		return fmt.Errorf("x11: GetKeyboardMapping: %w", err)
	}
	m, err := buildKeymap(setup, reply)
	if err != nil {
		return err
	}
	b.keymap = m
	b.keymapOK = true
	return nil
}

// keycodeAndLevel looks up the (keycode, level) for a keysym. C7: returns a
// descriptive error when the keysym is absent (e.g. non-ASCII runes like é),
// so typeText surfaces the unsupported input instead of silently skipping.
func (b *X11Backend) keycodeAndLevel(sym xproto.Keysym) (xproto.Keycode, int, error) {
	kl, ok := b.keymap[sym]
	if !ok {
		return 0, 0, fmt.Errorf("x11: keysym 0x%x not found in keymap", sym)
	}
	return kl.keycode, kl.level, nil
}

// activateWindow sends a _NET_ACTIVE_WINDOW ClientMessage so the WM focuses
// the target. Focus-steal is acceptable on the headless/xrdp target
// environment (one agent session per display); on a shared desktop it would
// be disruptive.
func (b *X11Backend) activateWindow(win xproto.Window) error {
	data := []uint32{1, uint32(xproto.TimeCurrentTime), 0, 0, 0}
	ev := xproto.ClientMessageEvent{
		Format: 32,
		Window: win,
		Type:   b.atoms.netActiveWindow,
		Data:   xproto.ClientMessageDataUnionData32New(data),
	}
	return b.conn.SendEventChecked(false, b.root,
		xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify,
		string(ev.Bytes()))
}

// toRoot translates window-relative (x,y) to root-absolute coordinates.
func (b *X11Backend) toRoot(win xproto.Window, x, y int) (int16, int16, error) {
	trans, err := b.conn.TranslateCoordinates(win, b.root, int16(x), int16(y))
	if err != nil {
		return 0, 0, fmt.Errorf("x11: TranslateCoordinates: %w", err)
	}
	return trans.DstX, trans.DstY, nil
}

// warpPointer moves the pointer to root-absolute (x,y) via XTEST.
func (b *X11Backend) warpPointer(rootX, rootY int16) error {
	return b.conn.FakeInputChecked(xproto.MotionNotify, 0, xproto.TimeCurrentTime, b.root, rootX, rootY, 0)
}

// buttonPress / buttonRelease inject a button event via XTEST. X11 button
// numbers: 1=left, 2=middle, 3=right, 4-7=scroll.
func (b *X11Backend) buttonPress(btn byte) error {
	return b.conn.FakeInputChecked(xproto.ButtonPress, btn, xproto.TimeCurrentTime, b.root, 0, 0, 0)
}
func (b *X11Backend) buttonRelease(btn byte) error {
	return b.conn.FakeInputChecked(xproto.ButtonRelease, btn, xproto.TimeCurrentTime, b.root, 0, 0, 0)
}

// keyDown / keyUp inject a key event for a raw keycode via XTEST.
func (b *X11Backend) keyDown(kc xproto.Keycode) error {
	return b.conn.FakeInputChecked(xproto.KeyPress, byte(kc), xproto.TimeCurrentTime, b.root, 0, 0, 0)
}
func (b *X11Backend) keyUp(kc xproto.Keycode) error {
	return b.conn.FakeInputChecked(xproto.KeyRelease, byte(kc), xproto.TimeCurrentTime, b.root, 0, 0, 0)
}

// buttonNumber maps the model-facing button name to the X11 button number.
func buttonNumber(button string) (byte, bool) {
	switch strings.ToLower(button) {
	case "", ButtonLeft:
		return 1, true
	case ButtonMiddle:
		return 2, true
	case ButtonRight:
		return 3, true
	}
	return 0, false
}

// click activates the window, warps the pointer to the window-relative
// coordinate translated to root-absolute, then performs count press/release
// cycles with clickDelay pacing.
func (b *X11Backend) click(ctx context.Context, in Input) (*ActionResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	if err := b.ensureKeymap(); err != nil {
		return nil, err
	}
	if in.Window == nil {
		return actionErr("click", "click requires coordinate=[x,y]"), nil
	}
	x, y, ok := parseCoordinate(in.Coordinate)
	if !ok {
		return actionErr("click", "click requires coordinate=[x,y]"), nil
	}
	btn, ok := buttonNumber(in.Button)
	if !ok {
		return actionErr("click", fmt.Sprintf("unknown button %q — expected left, right, middle.", in.Button)), nil
	}
	count := 1
	if in.Count != nil {
		count = *in.Count
	}
	if count < 1 {
		count = 1
	}
	if count > 3 {
		count = 3
	}

	win := xproto.Window(*in.Window)
	if err := b.activateWindow(win); err != nil {
		return nil, fmt.Errorf("x11: activate window: %w", err)
	}
	rootX, rootY, err := b.toRoot(win, x, y)
	if err != nil {
		return nil, err
	}
	if err := b.warpPointer(rootX, rootY); err != nil {
		return nil, fmt.Errorf("x11: warp pointer: %w", err)
	}
	for i := 0; i < count; i++ {
		if err := b.buttonPress(btn); err != nil {
			return nil, err
		}
		time.Sleep(clickDelay)
		if err := b.buttonRelease(btn); err != nil {
			return nil, err
		}
		if i < count-1 {
			time.Sleep(80 * time.Millisecond)
		}
	}
	b.conn.Sync()
	return &ActionResult{OK: true, Action: "click", Message: fmt.Sprintf("Clicked at (%d,%d) x%d", x, y, count)}, nil
}

// typeText types each rune by resolving its keysym in the server keymap and
// holding Shift when the keysym lives at level >= 1. C7: a rune whose keysym
// is not in the keymap (any non-ASCII char the layout cannot produce, e.g.
// é/emoji) returns a descriptive error rather than producing a garbled string.
func (b *X11Backend) typeText(ctx context.Context, in Input) (*ActionResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	if err := b.ensureKeymap(); err != nil {
		return nil, err
	}
	if in.Window == nil {
		return nil, fmt.Errorf("type requires window=")
	}
	if err := b.activateWindow(xproto.Window(*in.Window)); err != nil {
		return nil, fmt.Errorf("x11: activate window: %w", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Resolve the Shift keycode once. A server keymap without Shift is
	// unrecoverably broken; resolving up front surfaces that instead of
	// injecting keycode-0 events for every shifted rune. Also avoids the
	// per-rune repeat lookup the old code did on each shifted char.
	shiftKC, _, err := b.keycodeAndLevel(keysymForName["shift"])
	if err != nil {
		return nil, fmt.Errorf("x11: shift key not in keymap: %w", err)
	}

	for _, ch := range in.Text {
		var sym xproto.Keysym
		switch ch {
		case '\n':
			sym = 0xff0d
		case '\t':
			sym = 0xff09
		default:
			sym = xproto.Keysym(ch)
		}
		kc, level, err := b.keycodeAndLevel(sym)
		if err != nil {
			return nil, fmt.Errorf("type_text: rune %q (U+%04X) has no keycode in the current keyboard layout; X11 cannot inject it", ch, ch)
		}
		needShift := level >= 1
		if needShift {
			if err := b.keyDown(shiftKC); err != nil {
				return nil, err
			}
		}
		if err := b.keyDown(kc); err != nil {
			return nil, err
		}
		time.Sleep(keyDelay)
		if err := b.keyUp(kc); err != nil {
			return nil, err
		}
		if needShift {
			if err := b.keyUp(shiftKC); err != nil {
				return nil, err
			}
		}
	}
	b.conn.Sync()
	return &ActionResult{OK: true, Action: "type_text", Message: fmt.Sprintf("Typed %d character(s) via XTest.", utf8.RuneCountInString(in.Text))}, nil
}

// key presses a key combo: modifiers (canonicalized via x11Modifier) held
// down, then the main key tapped, then modifiers released in reverse.
func (b *X11Backend) key(ctx context.Context, in Input) (*ActionResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	if err := b.ensureKeymap(); err != nil {
		return nil, err
	}
	if in.Window == nil {
		return nil, fmt.Errorf("key requires window=")
	}
	mainName, modifiers := parseKeyCombo(in.Keys)
	if mainName == "" {
		return actionErr("key", fmt.Sprintf("Could not parse key from '%s'.", in.Keys)), nil
	}
	sym, ok := keysymForChar(mainName)
	if !ok {
		return actionErr("key", fmt.Sprintf("unknown key %q", mainName)), nil
	}
	mainKC, _, err := b.keycodeAndLevel(sym)
	if err != nil {
		return actionErr("key", fmt.Sprintf("key %q not in keymap: %v", mainName, err)), nil
	}

	// Resolve modifier keycodes up front so the press/release sequence is
	// symmetric even if a later lookup failed.
	var modKC []xproto.Keycode
	for _, m := range modifiers {
		msym, ok := keysymForName[x11Modifier(m)]
		if !ok {
			return actionErr("key", fmt.Sprintf("unknown modifier %q", m)), nil
		}
		mkc, _, err := b.keycodeAndLevel(msym)
		if err != nil {
			return actionErr("key", fmt.Sprintf("modifier %q not in keymap: %v", m, err)), nil
		}
		modKC = append(modKC, mkc)
	}

	if err := b.activateWindow(xproto.Window(*in.Window)); err != nil {
		return nil, fmt.Errorf("x11: activate window: %w", err)
	}
	time.Sleep(50 * time.Millisecond)

	for _, mkc := range modKC {
		if err := b.keyDown(mkc); err != nil {
			return nil, err
		}
	}
	if err := b.keyDown(mainKC); err != nil {
		return nil, err
	}
	time.Sleep(keyDelay)
	if err := b.keyUp(mainKC); err != nil {
		return nil, err
	}
	for i := len(modKC) - 1; i >= 0; i-- {
		if err := b.keyUp(modKC[i]); err != nil {
			return nil, err
		}
	}
	b.conn.Sync()
	return &ActionResult{OK: true, Action: "press_key", Message: fmt.Sprintf("Pressed key '%s' via XTest.", in.Keys)}, nil
}

// scrollButton maps a direction to the X11 scroll button number.
func scrollButton(direction string) (byte, bool) {
	switch strings.ToLower(direction) {
	case "", DirectionDown:
		return 5, true
	case DirectionUp:
		return 4, true
	case DirectionLeft:
		return 6, true
	case DirectionRight:
		return 7, true
	}
	return 0, false
}

// scroll warps the pointer to the (optional) coordinate first so the scroll
// lands under the cursor, then injects `amount` button press/release cycles.
// If no coordinate is given the pointer stays at the window's (0,0).
func (b *X11Backend) scroll(ctx context.Context, in Input) (*ActionResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	if in.Window == nil {
		return nil, fmt.Errorf("scroll requires window=")
	}
	direction := in.Direction
	if direction == "" {
		direction = DirectionDown
	}
	btn, ok := scrollButton(direction)
	if !ok {
		return actionErr("scroll", fmt.Sprintf("unknown direction %q", in.Direction)), nil
	}
	amount := 3
	if in.Amount != nil {
		amount = *in.Amount
	}
	if amount < 1 {
		amount = 1
	}
	if amount > 50 {
		amount = 50
	}

	win := xproto.Window(*in.Window)
	if err := b.activateWindow(win); err != nil {
		return nil, fmt.Errorf("x11: activate window: %w", err)
	}
	if x, y, ok := parseCoordinate(in.Coordinate); ok {
		rootX, rootY, err := b.toRoot(win, x, y)
		if err != nil {
			return nil, err
		}
		if err := b.warpPointer(rootX, rootY); err != nil {
			return nil, fmt.Errorf("x11: warp pointer: %w", err)
		}
	}
	for i := 0; i < amount; i++ {
		if err := b.buttonPress(btn); err != nil {
			return nil, err
		}
		if err := b.buttonRelease(btn); err != nil {
			return nil, err
		}
	}
	b.conn.Sync()
	return &ActionResult{OK: true, Action: "scroll", Message: fmt.Sprintf("Scrolled %s x%d", direction, amount)}, nil
}

// drag presses the left button at from_coordinate, interpolates MotionNotify
// events to to_coordinate, then releases. Interpolation step count scales
// with distance so short drags stay responsive and long drags stay smooth.
func (b *X11Backend) drag(ctx context.Context, in Input) (*ActionResult, error) {
	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}
	if in.Window == nil {
		return nil, fmt.Errorf("drag requires window=")
	}
	fx, fy, fromOK := parseCoordinate(in.FromCoordinate)
	tx, ty, toOK := parseCoordinate(in.ToCoordinate)
	if !fromOK || !toOK {
		return actionErr("drag", "drag requires from_coordinate and to_coordinate."), nil
	}

	win := xproto.Window(*in.Window)
	if err := b.activateWindow(win); err != nil {
		return nil, fmt.Errorf("x11: activate window: %w", err)
	}
	time.Sleep(50 * time.Millisecond)

	rootFX, rootFY, err := b.toRoot(win, fx, fy)
	if err != nil {
		return nil, err
	}
	rootTX, rootTY, err := b.toRoot(win, tx, ty)
	if err != nil {
		return nil, err
	}

	btn, _ := buttonNumber(ButtonLeft)
	if err := b.warpPointer(rootFX, rootFY); err != nil {
		return nil, fmt.Errorf("x11: warp pointer: %w", err)
	}
	if err := b.buttonPress(btn); err != nil {
		return nil, err
	}
	time.Sleep(clickDelay)

	dist := math.Hypot(float64(rootTX-rootFX), float64(rootTY-rootFY))
	steps := max(int(dist/10), 1)
	const durationMs = 200
	stepDelay := time.Duration(durationMs/steps) * time.Millisecond
	for i := 1; i <= steps; i++ {
		t := float64(i) / float64(steps)
		ix := rootFX + int16(float64(rootTX-rootFX)*t)
		iy := rootFY + int16(float64(rootTY-rootFY)*t)
		if err := b.warpPointer(ix, iy); err != nil {
			return nil, err
		}
		time.Sleep(stepDelay)
	}
	if err := b.warpPointer(rootTX, rootTY); err != nil {
		return nil, err
	}
	if err := b.buttonRelease(btn); err != nil {
		return nil, err
	}
	b.conn.Sync()
	return &ActionResult{OK: true, Action: "drag", Message: fmt.Sprintf("Dragged (%d,%d)→(%d,%d)", fx, fy, tx, ty)}, nil
}
