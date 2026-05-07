// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
//
// This file: Config hot-reload — watches .mcp.json for changes and reloads servers.
// Source: chokidar in TS + React effect re-run pattern.
//
// gbot uses fsnotify with 500ms debounce for CLI-friendly config watching.
package mcp

import (
	"log/slog"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ---------------------------------------------------------------------------
// ConfigWatcher — file-based config change detection
// Source: chokidar watch + /reload-plugins + React effect
// ---------------------------------------------------------------------------

// ConfigWatcher monitors config files and triggers a reload callback on changes.
// Uses fsnotify with debounce to coalesce rapid writes (e.g., git checkout).
type ConfigWatcher struct {
	watcher  *fsnotify.Watcher
	onReload func()
	debounce time.Duration
	done     chan struct{}
	once     sync.Once

	// mu protects the pending timer
	mu    sync.Mutex
	timer *time.Timer
}

// ConfigWatcherOpt configures a ConfigWatcher.
type ConfigWatcherOpt func(*ConfigWatcher)

// WithDebounce sets the debounce duration. Default is 500ms.
func WithDebounce(d time.Duration) ConfigWatcherOpt {
	return func(cw *ConfigWatcher) { cw.debounce = d }
}

// NewConfigWatcher creates a config file watcher.
// onReload is called when a watched file changes (after debounce).
func NewConfigWatcher(onReload func(), opts ...ConfigWatcherOpt) (*ConfigWatcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	cw := &ConfigWatcher{
		watcher:  fw,
		onReload: onReload,
		debounce: 500 * time.Millisecond,
		done:     make(chan struct{}),
	}

	for _, opt := range opts {
		opt(cw)
	}

	return cw, nil
}

// AddPath adds a file or directory to watch.
func (cw *ConfigWatcher) AddPath(path string) error {
	return cw.watcher.Add(path)
}

// Start begins watching for file changes.
// Blocks until Stop is called. Should be run in a goroutine.
func (cw *ConfigWatcher) Start() {
	cw.runEventLoop(cw.watcher.Events, cw.watcher.Errors)
}

// runEventLoop processes file events and errors from the given channels.
// Extracted from Start for testability — tests can inject mock channels.
func (cw *ConfigWatcher) runEventLoop(events <-chan fsnotify.Event, errors <-chan error) {
	for {
		select {
		case <-cw.done:
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			// Only trigger on Write, Create, Rename, Remove events
			if event.Has(fsnotify.Write | fsnotify.Create | fsnotify.Rename | fsnotify.Remove) {
				cw.scheduleReload()
			}
		case err, ok := <-errors:
			if !ok {
				return
			}
			slog.Warn("mcp: config watcher error", "error", err)
		}
	}
}

// scheduleReload debounces rapid file events into a single reload.
func (cw *ConfigWatcher) scheduleReload() {
	cw.mu.Lock()
	defer cw.mu.Unlock()

	if cw.timer != nil {
		cw.timer.Stop()
	}
	cw.timer = time.AfterFunc(cw.debounce, func() {
		slog.Info("mcp: config file changed, reloading")
		cw.onReload()
	})
}

// Stop stops the watcher and waits for the event loop to exit.
func (cw *ConfigWatcher) Stop() {
	cw.once.Do(func() {
		// Stop pending debounce timer
		cw.mu.Lock()
		if cw.timer != nil {
			cw.timer.Stop()
		}
		cw.mu.Unlock()

		close(cw.done)
		_ = cw.watcher.Close()
	})
}
