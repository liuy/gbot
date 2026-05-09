// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
//
// This file: Config hot-reload — watches .mcp.json for changes and reloads servers.
// Source: TS fs.watchFile (polling) for global config freshness.
//
// gbot uses polling with stat mtime comparison.
// Atomic writes (write temp → rename) produce new mtimes naturally,
// no need to watch parent directories.
package mcp

import (
	"log/slog"
	"os"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// ConfigWatcher — polling-based config change detection
// Source: TS fs.watchFile polling + React effect re-run
// ---------------------------------------------------------------------------

// ConfigWatcher monitors config files and triggers a reload callback on changes.
// Uses polling with stat mtime comparison.
//
// Advantages:
//   - Zero noise: only checks the specific file, not the entire directory
//   - Handles atomic writes naturally (new file = new mtime)
//   - Cross-platform: no inotify/FSEvents/Kqueue differences
//   - Simple: one stat syscall per interval (microsecond-scale)
type ConfigWatcher struct {
	paths    []string
	mtimes   map[string]time.Time
	onReload func()
	interval time.Duration
	done     chan struct{}
	once     sync.Once

	mu sync.Mutex
}

// ConfigWatcherOpt configures a ConfigWatcher.
type ConfigWatcherOpt func(*ConfigWatcher)

// WithInterval sets the polling interval. Default is 2 seconds.
func WithInterval(d time.Duration) ConfigWatcherOpt {
	return func(cw *ConfigWatcher) { cw.interval = d }
}

// NewConfigWatcher creates a config file watcher.
// onReload is called when a watched file's mtime changes.
func NewConfigWatcher(onReload func(), opts ...ConfigWatcherOpt) (*ConfigWatcher, error) {
	cw := &ConfigWatcher{
		onReload: onReload,
		interval: 2 * time.Second,
		done:     make(chan struct{}),
		mtimes:   make(map[string]time.Time),
	}

	for _, opt := range opts {
		opt(cw)
	}

	return cw, nil
}

// AddPath adds a file path to watch. Captures current mtime as baseline.
// If the file doesn't exist, the baseline is empty — file creation will be
// detected on the next poll.
func (cw *ConfigWatcher) AddPath(path string) error {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	cw.paths = append(cw.paths, path)
	if info, err := os.Stat(path); err == nil {
		cw.mtimes[path] = info.ModTime()
	}
	return nil
}

// Start begins polling for file changes.
// Blocks until Stop is called. Should be run in a goroutine.
func (cw *ConfigWatcher) Start() {
	ticker := time.NewTicker(cw.interval)
	defer ticker.Stop()

	for {
		select {
		case <-cw.done:
			return
		case <-ticker.C:
			if cw.checkChanged() {
				slog.Info("mcp: config file changed, reloading")
				cw.onReload()
			}
		}
	}
}

// checkChanged compares current mtimes against saved baselines.
// Returns true if any watched file changed (created, modified, or deleted).
func (cw *ConfigWatcher) checkChanged() bool {
	cw.mu.Lock()
	defer cw.mu.Unlock()

	for _, path := range cw.paths {
		info, err := os.Stat(path)
		if err != nil {
			// File gone — if we had a previous mtime, it changed
			_, had := cw.mtimes[path]
			if had {
				delete(cw.mtimes, path)
				return true
			}
			// File was never there, still not there → no change
			continue
		}
		newMtime := info.ModTime()
		oldMtime, had := cw.mtimes[path]
		cw.mtimes[path] = newMtime
		if !had || newMtime.After(oldMtime) {
			return true
		}
	}
	return false
}

// Stop stops the watcher.
func (cw *ConfigWatcher) Stop() {
	cw.once.Do(func() {
		close(cw.done)
	})
}
