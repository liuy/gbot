//go:build darwin

package computer

// activeWindowIDForDisplay is a no-op on macOS — cua-driver fills real
// z_index values via kCGWindowLayer, so the uniform-z_index fallback never
// triggers.
func activeWindowIDForDisplay(_ string) int { return 0 }
