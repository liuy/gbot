package computer

// display_windows.go — DISPLAY is irrelevant on Windows (cua-driver uses
// Win32 APIs), so detectDisplay is a no-op.

func detectDisplay() (string, error) { return "", nil }
