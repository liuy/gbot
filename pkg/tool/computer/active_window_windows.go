//go:build windows

package computer

// activeWindowIDForDisplay is a no-op on Windows — cua-driver fills real
// z_index values from EnumWindows ordering, so the uniform-z_index fallback
// never triggers.
func activeWindowIDForDisplay(_ string) int { return 0 }
