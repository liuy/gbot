//go:build linux

package computer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jezek/xgb/xproto"
)

// TestKeysymForChar verifies the keysym table and the single-char fallback.
func TestKeysymForChar(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want xproto.Keysym
		ok   bool
	}{
		{"return", "return", 0xff0d, true},
		{"enter alias", "enter", 0xff0d, true},
		{"lowercase a", "a", 0x61, true},
		{"uppercase A", "A", 0x41, true},
		{"f5", "f5", 0xffc2, true},
		{"space named", "space", 0x20, true},
		{"space literal", " ", 0x20, true},
		{"single char fallback", "!", 0x21, true},
		{"unknown multiword", "foo", 0, false},
		{"empty", "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := keysymForChar(tc.in)
			if ok != tc.ok {
				t.Errorf("keysymForChar(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("keysymForChar(%q) = 0x%x, want 0x%x", tc.in, got, tc.want)
			}
		})
	}
}

// TestX11Modifier verifies the modifier canonicalization (C8).
func TestX11Modifier(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"option", "alt"},
		{"cmd", "super"},
		{"win", "super"},
		{"windows", "super"},
		{"meta", "super"},
		{"super", "super"},
		{"ctrl", "ctrl"},
		{"shift", "shift"},
		{"alt", "alt"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := x11Modifier(tc.in); got != tc.want {
				t.Errorf("x11Modifier(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestBuildKeymap verifies the pure keymap-build loop resolves a known keysym
// to the right (keycode, level), and rejects an invalid keysyms_per_keycode.
func TestBuildKeymap(t *testing.T) {
	setup := &xproto.SetupInfo{MinKeycode: 10, MaxKeycode: 20}
	reply := &xproto.GetKeyboardMappingReply{
		KeysymsPerKeycode: 2,
		Keysyms: []xproto.Keysym{
			0x61, 0x41, // kc=10: 'a' level0, 'A' level1
			0xff0d, 0x0, // kc=11: Return level0
		},
	}
	m, err := buildKeymap(setup, reply)
	if err != nil {
		t.Fatalf("buildKeymap: %v", err)
	}
	// 'a' at index 0 → keycode 10, level 0.
	kl, ok := m[0x61]
	if !ok {
		t.Fatal("'a' keysym missing from keymap")
	}
	if kl.keycode != 10 {
		t.Errorf("'a' keycode = %d, want 10", kl.keycode)
	}
	if kl.level != 0 {
		t.Errorf("'a' level = %d, want 0", kl.level)
	}
	// 'A' at index 1 → same keycode 10, level 1.
	klA := m[0x41]
	if klA.keycode != 10 || klA.level != 1 {
		t.Errorf("'A' = (kc %d, level %d), want (10, 1)", klA.keycode, klA.level)
	}
}

// TestBuildKeymapInvalidKPK verifies keysyms_per_keycode <= 0 errors.
func TestBuildKeymapInvalidKPK(t *testing.T) {
	setup := &xproto.SetupInfo{MinKeycode: 10, MaxKeycode: 20}
	reply := &xproto.GetKeyboardMappingReply{KeysymsPerKeycode: 0}
	_, err := buildKeymap(setup, reply)
	if err == nil {
		t.Fatal("buildKeymap with kpk=0: expected error, got nil")
	}
}

// TestKeycodeAndLevelFound verifies the keymap lookup returns the cached
// keycode/level, and errors for an unknown keysym.
func TestKeycodeAndLevel(t *testing.T) {
	b := &X11Backend{
		keymap: map[xproto.Keysym]keycodeLevel{
			0x61: {keycode: 38, level: 0},
		},
		keymapOK: true,
	}
	kc, level, err := b.keycodeAndLevel(0x61)
	if err != nil {
		t.Fatalf("keycodeAndLevel(0x61): %v", err)
	}
	if kc != 38 || level != 0 {
		t.Errorf("keycodeAndLevel(0x61) = (%d,%d), want (38,0)", kc, level)
	}
	if _, _, err := b.keycodeAndLevel(0x9999); err == nil {
		t.Error("keycodeAndLevel(unknown): expected error, got nil")
	}
}

// TestFakeX11ConnClickRecording drives click through a fake conn and asserts
// the recorded FakeInputChecked sequence is [MotionNotify(warp), ButtonPress,
// ButtonRelease] with the translated root coordinates.
func TestFakeX11ConnClickRecording(t *testing.T) {
	fc := &fakeX11Conn{}
	b := newFakeX11Backend(t, fc)
	win := 42
	fc.trans = map[xproto.Window]*xproto.TranslateCoordinatesReply{
		42: {DstX: 5, DstY: 7},
	}
	in := Input{Action: ActionClick, Window: &win, Coordinate: json.RawMessage(`[100,200]`)}
	res, err := b.click(context.Background(), in)
	if err != nil {
		t.Fatalf("click: %v", err)
	}
	if !res.OK {
		t.Fatalf("click not OK: %s", res.Message)
	}
	if len(fc.calls) != 3 {
		t.Fatalf("FakeInput calls = %d, want 3: %+v", len(fc.calls), fc.calls)
	}
	if fc.calls[0].eventType != xproto.MotionNotify {
		t.Errorf("call[0] eventType = %d, want MotionNotify", fc.calls[0].eventType)
	}
	if fc.calls[0].x != 5 || fc.calls[0].y != 7 {
		t.Errorf("call[0] (warp) = (%d,%d), want (5,7)", fc.calls[0].x, fc.calls[0].y)
	}
	if fc.calls[1].eventType != xproto.ButtonPress || fc.calls[1].detail != 1 {
		t.Errorf("call[1] = (type %d, detail %d), want (ButtonPress, 1)", fc.calls[1].eventType, fc.calls[1].detail)
	}
	if fc.calls[2].eventType != xproto.ButtonRelease || fc.calls[2].detail != 1 {
		t.Errorf("call[2] = (type %d, detail %d), want (ButtonRelease, 1)", fc.calls[2].eventType, fc.calls[2].detail)
	}
}

// TestFakeX11ConnClickMissingCoordinate verifies click without a coordinate
// returns an actionErr (not a Go error) and emits no FakeInput events.
func TestFakeX11ConnClickMissingCoordinate(t *testing.T) {
	fc := &fakeX11Conn{}
	b := newFakeX11Backend(t, fc)
	win := 42
	res, err := b.click(context.Background(), Input{Action: ActionClick, Window: &win})
	if err != nil {
		t.Fatalf("click: %v", err)
	}
	if res.OK {
		t.Error("click without coordinate: OK=true, want false")
	}
	if len(fc.calls) != 0 {
		t.Errorf("FakeInput calls = %d, want 0", len(fc.calls))
	}
}

// TestFakeX11ConnTypeASCIIRecording verifies typeText on pure ASCII emits
// exactly one KeyPress + KeyRelease per rune and no Shift events (lowercase
// letters are level 0).
func TestFakeX11ConnTypeASCIIRecording(t *testing.T) {
	fc := &fakeX11Conn{}
	b := newFakeX11Backend(t, fc)
	win := 42
	res, err := b.typeText(context.Background(), Input{Action: ActionType, Window: &win, Text: "ab"})
	if err != nil {
		t.Fatalf("typeText: %v", err)
	}
	if !res.OK {
		t.Fatalf("typeText not OK: %s", res.Message)
	}
	// 2 runes, level 0 → 2 * (KeyPress + KeyRelease) = 4 events. Plus no Shift.
	if len(fc.calls) != 4 {
		t.Fatalf("FakeInput calls = %d, want 4: %+v", len(fc.calls), fc.calls)
	}
	for i, c := range fc.calls {
		switch i % 2 {
		case 0:
			if c.eventType != xproto.KeyPress {
				t.Errorf("call[%d] type = %d, want KeyPress", i, c.eventType)
			}
		case 1:
			if c.eventType != xproto.KeyRelease {
				t.Errorf("call[%d] type = %d, want KeyRelease", i, c.eventType)
			}
		}
	}
}

// TestTypeTextNonASCIIError verifies a non-ASCII rune (é, U+00E9) that is not
// in the keymap returns a descriptive error and aborts MID-STRING rather than
// silently skipping the unsupported rune. The 3 valid leading runes (c,a,f)
// are typed before the function hits é and bails. (C7)
func TestTypeTextNonASCIIError(t *testing.T) {
	fc := &fakeX11Conn{}
	b := newFakeX11Backend(t, fc)
	win := 42
	_, err := b.typeText(context.Background(), Input{Action: ActionType, Window: &win, Text: "café"})
	if err == nil {
		t.Fatal("typeText(café): expected error for non-ASCII é, got nil")
	}
	if !strings.Contains(err.Error(), "no keycode") {
		t.Errorf("error %q missing 'no keycode'", err)
	}
	if !strings.Contains(err.Error(), "U+00E9") && !strings.Contains(err.Error(), "U+00e9") {
		t.Errorf("error %q missing rune U+00E9", err)
	}
	// 3 typed runes (c,a,f) × (press+release) = 6 calls. The 4th rune (é)
	// must NOT have been injected — so call count is exactly 6, not 8.
	if len(fc.calls) != 6 {
		t.Errorf("FakeInput calls = %d, want 6 (3 valid runes typed before é bailed): %+v", len(fc.calls), fc.calls)
	}
}

// TestTypeTextShiftNotInKeymap verifies that when the server keymap lacks the
// Shift keysym, typeText surfaces a descriptive error instead of silently
// injecting keycode-0 events for every shifted rune. Uses a custom keymap
// containing only level-0 'a' and level-1 'A' (no Shift), then attempts to
// type "A" which would require Shift. Without the upfront check the old code
// resolved Shift to keycode 0 and injected KeyPress/KeyRelease for keycode 0.
func TestTypeTextShiftNotInKeymap(t *testing.T) {
	fc := &fakeX11Conn{}
	b := newFakeX11Backend(t, fc)
	// Overwrite the keymap: 'a' (0x61) at level 0, 'A' (0x41) at level 1, but
	// NO shift keysym (0xffe1). 'A' forces needShift=true.
	b.keymap = map[xproto.Keysym]keycodeLevel{
		0x61: {keycode: 38, level: 0}, // a
		0x41: {keycode: 38, level: 1}, // A (shifted)
	}
	win := 42
	_, err := b.typeText(context.Background(), Input{Action: ActionType, Window: &win, Text: "A"})
	if err == nil {
		t.Fatal("typeText(A) with no Shift in keymap: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "shift") {
		t.Errorf("error %q missing 'shift'", err)
	}
	if !strings.Contains(err.Error(), "keymap") {
		t.Errorf("error %q missing 'keymap'", err)
	}
	// No FakeInput events should be injected — the check fails before the loop.
	if len(fc.calls) != 0 {
		t.Errorf("FakeInput calls = %d, want 0 (shift check fails before typing): %+v", len(fc.calls), fc.calls)
	}
}

// TestTypeTextStartedRequired verifies typeText on an un-started backend
// surfaces an actionable error from ensureStarted (detectDisplay fails
// against an empty glob + unset DISPLAY).
func TestTypeTextStartedRequired(t *testing.T) {
	orig := x11UnixGlob
	x11UnixGlob = "/nonexistent-detect-display-test/X*"
	t.Cleanup(func() { x11UnixGlob = orig })
	t.Setenv("DISPLAY", "")
	b := &X11Backend{}
	win := 42
	_, err := b.typeText(context.Background(), Input{Action: ActionType, Window: &win, Text: "x"})
	if err == nil {
		t.Fatal("typeText on un-started backend: expected error, got nil")
	}
}

// TestScrollRecordsButtonEvents verifies scroll emits amount×(press+release)
// on the right button for each direction.
func TestScrollRecordsButtonEvents(t *testing.T) {
	cases := []struct {
		dir       string
		amount    int
		wantBtn   byte
		wantCalls int
	}{
		{DirectionDown, 3, 5, 6},
		{DirectionUp, 2, 4, 4},
		{DirectionLeft, 1, 6, 2},
		{DirectionRight, 1, 7, 2},
	}
	for _, tc := range cases {
		t.Run(tc.dir, func(t *testing.T) {
			fc := &fakeX11Conn{}
			b := newFakeX11Backend(t, fc)
			win := 42
			amt := tc.amount
			res, err := b.scroll(context.Background(), Input{Action: ActionScroll, Window: &win, Direction: tc.dir, Amount: &amt})
			if err != nil {
				t.Fatalf("scroll: %v", err)
			}
			if !res.OK {
				t.Fatalf("scroll not OK: %s", res.Message)
			}
			if len(fc.calls) != tc.wantCalls {
				t.Fatalf("FakeInput calls = %d, want %d", len(fc.calls), tc.wantCalls)
			}
			for i, c := range fc.calls {
				if c.detail != tc.wantBtn {
					t.Errorf("call[%d].detail = %d, want %d", i, c.detail, tc.wantBtn)
				}
			}
		})
	}
}

// TestScrollAmountClamp verifies amount is clamped to [1,50].
func TestScrollAmountClamp(t *testing.T) {
	fc := &fakeX11Conn{}
	b := newFakeX11Backend(t, fc)
	win := 42
	huge := 9999
	res, err := b.scroll(context.Background(), Input{Action: ActionScroll, Window: &win, Amount: &huge})
	if err != nil {
		t.Fatalf("scroll: %v", err)
	}
	if !res.OK {
		t.Fatalf("scroll not OK: %s", res.Message)
	}
	// 50 * (press + release) = 100.
	if len(fc.calls) != 100 {
		t.Errorf("clamped scroll calls = %d, want 100 (amount clamped to 50)", len(fc.calls))
	}
}

// TestScrollUnknownDirection verifies an unknown direction returns an actionErr.
func TestScrollUnknownDirection(t *testing.T) {
	fc := &fakeX11Conn{}
	b := newFakeX11Backend(t, fc)
	win := 42
	res, err := b.scroll(context.Background(), Input{Action: ActionScroll, Window: &win, Direction: "sideways"})
	if err != nil {
		t.Fatalf("scroll: %v", err)
	}
	if res.OK {
		t.Error("scroll with bad direction: OK=true, want false")
	}
}

// TestDragRecording verifies drag emits warp, ButtonPress, ≥1 MotionNotify,
// warp, ButtonRelease.
func TestDragRecording(t *testing.T) {
	fc := &fakeX11Conn{}
	b := newFakeX11Backend(t, fc)
	win := 42
	in := Input{
		Action:         ActionDrag,
		Window:         &win,
		FromCoordinate: json.RawMessage(`[0,0]`),
		ToCoordinate:   json.RawMessage(`[100,100]`),
	}
	res, err := b.drag(context.Background(), in)
	if err != nil {
		t.Fatalf("drag: %v", err)
	}
	if !res.OK {
		t.Fatalf("drag not OK: %s", res.Message)
	}
	// Expect: warp(from) + press + N motion + warp(to) + release.
	if len(fc.calls) < 4 {
		t.Fatalf("drag calls = %d, want >= 4", len(fc.calls))
	}
	first := fc.calls[0]
	if first.eventType != xproto.MotionNotify {
		t.Errorf("call[0] = type %d, want MotionNotify (warp to source)", first.eventType)
	}
	last := fc.calls[len(fc.calls)-1]
	if last.eventType != xproto.ButtonRelease || last.detail != 1 {
		t.Errorf("last call = (type %d, detail %d), want (ButtonRelease, 1)", last.eventType, last.detail)
	}
}

// TestDragMissingCoords verifies drag without both coordinates returns an
// actionErr and emits nothing.
func TestDragMissingCoords(t *testing.T) {
	fc := &fakeX11Conn{}
	b := newFakeX11Backend(t, fc)
	win := 42
	res, err := b.drag(context.Background(), Input{Action: ActionDrag, Window: &win, FromCoordinate: json.RawMessage(`[0,0]`)})
	if err != nil {
		t.Fatalf("drag: %v", err)
	}
	if res.OK {
		t.Error("drag missing to_coordinate: OK=true, want false")
	}
	if len(fc.calls) != 0 {
		t.Errorf("calls = %d, want 0", len(fc.calls))
	}
}

// TestKeyComboRecording verifies key("ctrl+r") emits shift-free ctrl-down,
// r-down, r-up, ctrl-up.
func TestKeyComboRecording(t *testing.T) {
	fc := &fakeX11Conn{}
	b := newFakeX11Backend(t, fc)
	win := 42
	res, err := b.key(context.Background(), Input{Action: ActionKey, Window: &win, Keys: "ctrl+r"})
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	if !res.OK {
		t.Fatalf("key not OK: %s", res.Message)
	}
	// ctrl-down, r-down, r-up, ctrl-up = 4 events.
	if len(fc.calls) != 4 {
		t.Fatalf("key calls = %d, want 4: %+v", len(fc.calls), fc.calls)
	}
	if fc.calls[0].eventType != xproto.KeyPress {
		t.Errorf("call[0] = type %d, want KeyPress (ctrl down)", fc.calls[0].eventType)
	}
	if fc.calls[3].eventType != xproto.KeyRelease {
		t.Errorf("call[3] = type %d, want KeyRelease (ctrl up)", fc.calls[3].eventType)
	}
}

// TestKeyUnparseable verifies a combo with no main key returns an actionErr.
func TestKeyUnparseable(t *testing.T) {
	fc := &fakeX11Conn{}
	b := newFakeX11Backend(t, fc)
	win := 42
	res, err := b.key(context.Background(), Input{Action: ActionKey, Window: &win, Keys: "ctrl+"})
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	if res.OK {
		t.Error("key with empty main: OK=true, want false")
	}
}

// TestX11BackendEnsureStartedErr verifies ensureStarted surfaces a detectDisplay
// failure as an actionable error (no panic, non-empty message).
func TestX11BackendEnsureStartedErr(t *testing.T) {
	b := &X11Backend{}
	// No DISPLAY and no sockets → detectDisplay errors. Use a temp glob that
	// matches nothing so this is deterministic.
	orig := x11UnixGlob
	x11UnixGlob = "/nonexistent-path-for-test/X*"
	t.Cleanup(func() { x11UnixGlob = orig })
	t.Setenv("DISPLAY", "")
	err := b.ensureStarted(context.Background())
	if err == nil {
		t.Fatal("ensureStarted: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "display") {
		t.Errorf("error %q does not mention display", err)
	}
}

// errConn is a fakeX11Conn variant whose FakeInputChecked returns an injected
// error, used to verify action methods propagate injection errors.
type errConn struct {
	*fakeX11Conn
	injErr error
}

func (e *errConn) FakeInputChecked(_, _ byte, _ uint32, _ xproto.Window, _, _ int16, _ byte) error {
	return e.injErr
}

// TestClickPropagatesInjectError verifies a FakeInput error during click is
// returned (not swallowed).
func TestClickPropagatesInjectError(t *testing.T) {
	fc := &fakeX11Conn{}
	b := newFakeX11Backend(t, fc)
	b.conn = &errConn{fakeX11Conn: fc, injErr: errors.New("injected")}
	win := 42
	_, err := b.click(context.Background(), Input{Action: ActionClick, Window: &win, Coordinate: json.RawMessage(`[1,1]`)})
	if err == nil {
		t.Fatal("click with injected error: expected error, got nil")
	}
}
