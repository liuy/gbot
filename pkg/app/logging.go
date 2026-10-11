package app

import (
	"log/slog"
	"os"
)

const maxLogSize = 20 * 1024 * 1024 // 20MB

// setupLogFile opens logPath in append mode, rotating to .old if it exceeds maxLogSize.
// Returns the file handle for writing, or nil on error.
// SetDefault lives in installLogHandler, not here: a test that exercises
// rotation must not hijack the process-wide slog default — concurrent tests'
// log output then cannot land in (and size-pollute) the freshly rotated file.
func setupLogFile(logPath string) *os.File {
	if info, err := os.Stat(logPath); err == nil && info.Size() > maxLogSize {
		_ = os.Rename(logPath, logPath+".old")
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil
	}
	return f
}

// installLogHandler points the global slog default at f. Production entry only.
func installLogHandler(f *os.File, logLevel slog.Level) {
	slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: logLevel})))
}
