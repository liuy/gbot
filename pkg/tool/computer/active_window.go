package computer

// activeWindowID returns the X11 active window id (from _NET_ACTIVE_WINDOW)
// on Linux, or 0 on macOS/Windows where z_index-based ordering works.
//
// On Linux, cua-driver's list_windows reports z_index=0 for all windows
// (PARITY.md marks Linux as OPEN), so capture() uses this to pick the true
// frontmost window. The Linux implementation is in active_window_linux.go;
// macOS/Windows stubs are in active_window_darwin.go / active_window_windows.go.
//
// display is the resolved X11 display string (e.g. ":10"); pass "" to skip.
func activeWindowID(display string) int { return activeWindowIDForDisplay(display) }
