package computer

// display_darwin.go — DISPLAY is irrelevant on macOS (cua-driver uses native
// APIs), so detectDisplay is a no-op.

func detectDisplay() (string, error) { return "", nil }
