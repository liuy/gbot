package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ---------------------------------------------------------------------------
// ConfigWatcher tests
// ---------------------------------------------------------------------------

func TestConfigWatcher_Debounce(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".mcp.json")
	if err := os.WriteFile(configPath, []byte(`{"mcpServers":{}}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var reloadCount atomic.Int32
	reloadDone := make(chan struct{})
	watcher, err := NewConfigWatcher(func() {
		if reloadCount.Add(1) >= 1 {
			select {
			case reloadDone <- struct{}{}:
			default:
			}
		}
	}, WithDebounce(100*time.Millisecond))
	if err != nil {
		t.Fatalf("NewConfigWatcher: %v", err)
	}
	if err := watcher.AddPath(tmpDir); err != nil {
		t.Fatalf("AddPath: %v", err)
	}

	go watcher.Start()
	defer watcher.Stop()

	// Write 5 times rapidly
	for i := range 5 {
		if err := os.WriteFile(configPath, []byte(`{"mcpServers":{}}`), 0644); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		// Brief pause between writes to ensure fsnotify picks them up
		<-time.After(20 * time.Millisecond)
	}

	// Wait for first reload via channel
	select {
	case <-reloadDone:
		// Good — reload happened
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reload")
	}

	// Check that debouncing worked — should be 1 reload, not 5
	count := reloadCount.Load()
	if count != 1 {
		t.Errorf("expected 1 reload after debounce, got %d", count)
	}
}

func TestConfigWatcher_Stop(t *testing.T) {
	var reloadCount atomic.Int32
	watcher, err := NewConfigWatcher(func() {
		reloadCount.Add(1)
	})
	if err != nil {
		t.Fatalf("NewConfigWatcher: %v", err)
	}

	go watcher.Start()
	watcher.Stop()

	// Stop should be idempotent
	watcher.Stop()
}

func TestConfigWatcher_FileDeleted(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".mcp.json")
	if err := os.WriteFile(configPath, []byte(`{}`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	reloadDone := make(chan struct{}, 1)
	watcher, err := NewConfigWatcher(func() {
		select {
		case reloadDone <- struct{}{}:
		default:
		}
	}, WithDebounce(50*time.Millisecond))
	if err != nil {
		t.Fatalf("NewConfigWatcher: %v", err)
	}
	if err := watcher.AddPath(tmpDir); err != nil {
		t.Fatalf("AddPath: %v", err)
	}

	go watcher.Start()
	defer watcher.Stop()

	// Delete the file — should not crash
	if err := os.Remove(configPath); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// Wait for the reload to happen (proves watcher survived)
	select {
	case <-reloadDone:
		// Good — watcher handled file deletion
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reload after file deletion")
	}
}

func TestConfigWatcher_ColdStart(t *testing.T) {
	// Start watcher in a directory with no config file
	tmpDir := t.TempDir()

	reloadDone := make(chan struct{}, 1)
	watcher, err := NewConfigWatcher(func() {
		select {
		case reloadDone <- struct{}{}:
		default:
		}
	}, WithDebounce(50*time.Millisecond))
	if err != nil {
		t.Fatalf("NewConfigWatcher: %v", err)
	}
	if err := watcher.AddPath(tmpDir); err != nil {
		t.Fatalf("AddPath: %v", err)
	}

	go watcher.Start()
	defer watcher.Stop()

	// Now create the config file — should trigger reload via CREATE event
	configPath := filepath.Join(tmpDir, ".mcp.json")
	if err := os.WriteFile(configPath, []byte(`{"mcpServers":{}}`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case <-reloadDone:
		// Good — cold start reload worked
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reload after file creation")
	}
}

func TestConfigWatcher_EventsChannelClosed(t *testing.T) {
	// Test that Start() exits cleanly when the fsnotify Events channel closes.
	reloadDone := make(chan struct{}, 1)
	watcher, err := NewConfigWatcher(func() {
		select {
		case reloadDone <- struct{}{}:
		default:
		}
	}, WithDebounce(50*time.Millisecond))
	if err != nil {
		t.Fatalf("NewConfigWatcher: %v", err)
	}

	done := make(chan struct{})
	go func() {
		watcher.Start()
		close(done)
	}()

	// Close the underlying fsnotify watcher directly — closes Events channel.
	// Start() should detect the closed channel and return.
	_ = watcher.watcher.Close()

	select {
	case <-done:
		// Good — Start() exited when Events channel closed
	case <-time.After(2 * time.Second):
		t.Fatal("Start() should exit when Events channel closes")
	}
}

func TestConfigWatcher_RunEventLoop_EventsClosed(t *testing.T) {
	reloadDone := make(chan struct{}, 1)
	watcher, _ := NewConfigWatcher(func() {
		select {
		case reloadDone <- struct{}{}:
		default:
		}
	}, WithDebounce(10*time.Millisecond))
	defer watcher.Stop()

	events := make(chan fsnotify.Event)
	errors := make(chan error)

	done := make(chan struct{})
	go func() {
		watcher.runEventLoop(events, errors)
		close(done)
	}()

	// Close events channel — should cause runEventLoop to return
	close(events)

	select {
	case <-done:
		// Good
	case <-time.After(2 * time.Second):
		t.Fatal("runEventLoop should exit when events channel closes")
	}
}

func TestConfigWatcher_RunEventLoop_ErrorsChannel(t *testing.T) {
	reloadDone := make(chan struct{}, 1)
	watcher, _ := NewConfigWatcher(func() {
		select {
		case reloadDone <- struct{}{}:
		default:
		}
	}, WithDebounce(10*time.Millisecond))
	defer watcher.Stop()

	events := make(chan fsnotify.Event)
	errors := make(chan error)

	done := make(chan struct{})
	go func() {
		watcher.runEventLoop(events, errors)
		close(done)
	}()

	// Send an error — should log warning and continue
	errors <- fmt.Errorf("test fs error")

	// Close errors channel — should cause return
	close(errors)

	select {
	case <-done:
		// Good — exited after errors channel closed
	case <-time.After(2 * time.Second):
		t.Fatal("runEventLoop should exit when errors channel closes")
	}
}

func TestConfigWatcher_RunEventLoop_IgnoresIrrelevantEvents(t *testing.T) {
	reloadDone := make(chan struct{}, 1)
	watcher, _ := NewConfigWatcher(func() {
		select {
		case reloadDone <- struct{}{}:
		default:
		}
	}, WithDebounce(10*time.Millisecond))
	defer watcher.Stop()

	events := make(chan fsnotify.Event, 1)
	errors := make(chan error)

	done := make(chan struct{})
	go func() {
		watcher.runEventLoop(events, errors)
		close(done)
	}()

	// Send a Chmod event — should NOT trigger reload (only Write/Create/Rename/Remove)
	events <- fsnotify.Event{Name: "test", Op: fsnotify.Chmod}

	// Now stop the watcher
	watcher.Stop()

	select {
	case <-done:
		// Good — exited cleanly
	case <-time.After(2 * time.Second):
		t.Fatal("runEventLoop should exit on Stop")
	}

	// No reload should have been triggered for Chmod
	select {
	case <-reloadDone:
		t.Error("Chmod event should not trigger reload")
	default:
		// Good — no reload
	}
}
