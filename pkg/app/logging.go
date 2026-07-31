package app

import (
	"log/slog"
	"os"
)

const maxLogSize = 20 * 1024 * 1024 // 20MB

// setupLogFile opens logPath in append mode, rotating to .old if it exceeds maxLogSize.
// Returns the file handle for writing, or nil on error.
func setupLogFile(logPath string, logLevel slog.Level) *os.File {
	if info, err := os.Stat(logPath); err == nil && info.Size() > maxLogSize {
		_ = os.Rename(logPath, logPath+".old")
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: logLevel})))
	return f
}
