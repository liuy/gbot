package computer

// computerPrompt returns the system-prompt text teaching the model the
// connect-first lifecycle and ref-based interaction model.
func computerPrompt() string {
	return `# Computer tool — Android device control via DroidPilot

You drive a remote Android device through the Computer tool, which talks to the DroidPilot app over a WebSocket.

## Lifecycle

Your FIRST computer action is always ` + "`connect`" + `:
{"action":"connect","host":"192.168.1.100","port":8765}
Port defaults to 8765; supply ` + "`password`" + ` if the device requires a bearer token. Until connect succeeds, every other action returns "not connected; call connect first".

Right after connect, call ` + "`device_info`" + ` to confirm which device you are driving (manufacturer, model, screen size).

Call ` + "`disconnect`" + ` when done, or to switch devices — connecting a new device auto-disconnects the previous one.

## Perceiving the screen

- ` + "`screen`" + ` returns a token-cheap numbered list of on-screen elements (refs). Call it FIRST to learn what is addressable.
- ` + "`screenshot`" + ` returns a JPEG; use it only when you need visual context (icons, layouts).
- If the connection drops mid-session, the next action returns "not connected; call connect first" — call connect again to recover.

## Acting

- Prefer ` + "`click_element`" + ` / ` + "`open_menu_element`" + ` (which take a ` + "`ref`" + ` from the most recent screen) over raw ` + "`click`" + ` / ` + "`open_menu`" + ` coordinate taps — refs are robust to layout shifts, coordinates are not.
- ` + "`type`" + ` REPLACES the focused field's text (set_text semantics). Tap the field first, then type the full intended content, not a delta.
- ` + "`send_key`" + ` accepts ONLY: back, home, recents, notifications, quick_settings, power_dialog, split_screen, lock_screen, take_screenshot.
- ` + "`scroll`" + ` takes direction up|down|left|right.
- ` + "`zoom`" + ` pinches at a coordinate with a scale factor (>1 zoom in, <1 zoom out).
- ` + "`open_app`" + ` launches an installed app by package name (e.g. ` + "`com.android.chrome`" + `).

## Workflow

connect → device_info → screen → click_element (or type/send_key/scroll/zoom/open_app) → screen (re-read after a change) → ... → disconnect. Re-call screen after any action that changes the UI, because refs from a stale screen are invalid.`
}
