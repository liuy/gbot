package computer

// detectDisplay resolves the X11 DISPLAY on Linux; on macOS/Windows the build
// is dispatched to a no-op via per-platform files (display_darwin.go,
// display_windows.go). The resolved display is set in the cua-driver child
// env at connect time (ensureStarted in backend.go).
//
// The Linux implementation is in display_linux.go with build tag `//go:build linux`.
