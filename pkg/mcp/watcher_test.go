package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// ConfigWatcher tests — polling with stat mtime
// ---------------------------------------------------------------------------

func TestConfigWatcher_FileModified(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".mcp.json")
	if err := os.WriteFile(configPath, []byte(`{"mcpServers":{}}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	reloadDone := make(chan struct{}, 1)
	watcher, err := NewConfigWatcher(func() {
		select {
		case reloadDone <- struct{}{}:
		default:
		}
	}, WithInterval(50*time.Millisecond))
	if err != nil {
		t.Fatalf("NewConfigWatcher: %v", err)
	}
	if err := watcher.AddPath(configPath); err != nil {
		t.Fatalf("AddPath: %v", err)
	}

	go watcher.Start()
	defer watcher.Stop()

	// Wait for first poll to pass (baseline established)
	time.Sleep(100 * time.Millisecond) // REAL-TIME: polling test needs to wait for ticker

	// Modify the file
	if err := os.WriteFile(configPath, []byte(`{"mcpServers":{"new":{}}}`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case <-reloadDone:
		// Good — reload detected
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reload after file modification")
	}
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
	}, WithInterval(50*time.Millisecond))
	if err != nil {
		t.Fatalf("NewConfigWatcher: %v", err)
	}
	if err := watcher.AddPath(configPath); err != nil {
		t.Fatalf("AddPath: %v", err)
	}

	go watcher.Start()
	defer watcher.Stop()

	// Wait for first poll to pass
	time.Sleep(100 * time.Millisecond) // REAL-TIME: polling test needs to wait for ticker

	// Delete the file — should detect via missing mtime
	if err := os.Remove(configPath); err != nil {
		t.Fatalf("remove: %v", err)
	}

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
	}, WithInterval(50*time.Millisecond))
	if err != nil {
		t.Fatalf("NewConfigWatcher: %v", err)
	}
	configPath := filepath.Join(tmpDir, ".mcp.json")
	if err := watcher.AddPath(configPath); err != nil {
		t.Fatalf("AddPath: %v", err)
	}

	go watcher.Start()
	defer watcher.Stop()

	// Now create the config file — should detect via new mtime
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

func TestConfigWatcher_RapidWrites(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".mcp.json")
	if err := os.WriteFile(configPath, []byte(`{}`), 0644); err != nil {
		t.Fatalf("write: %v", err)
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
	}, WithInterval(100*time.Millisecond))
	if err != nil {
		t.Fatalf("NewConfigWatcher: %v", err)
	}
	if err := watcher.AddPath(configPath); err != nil {
		t.Fatalf("AddPath: %v", err)
	}

	go watcher.Start()
	defer watcher.Stop()

	// Wait for first poll to pass (baseline established)
	time.Sleep(150 * time.Millisecond) // REAL-TIME: polling test needs to wait for ticker

	// Write 5 times rapidly — should coalesce into 1 reload
	for i := range 5 {
		if err := os.WriteFile(configPath, fmt.Appendf(nil, `{"v":%d}`, i), 0644); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	// Wait for reload
	select {
	case <-reloadDone:
		// Good
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reload")
	}

	// Wait another interval to verify no additional reloads
	time.Sleep(200 * time.Millisecond) // REAL-TIME: polling test needs to wait for ticker

	count := reloadCount.Load()
	if count != 1 {
		t.Errorf("expected 1 reload after rapid writes, got %d", count)
	}
}

func TestConfigWatcher_NoChange(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".mcp.json")
	if err := os.WriteFile(configPath, []byte(`{}`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var reloadCount atomic.Int32
	watcher, err := NewConfigWatcher(func() {
		reloadCount.Add(1)
	}, WithInterval(50*time.Millisecond))
	if err != nil {
		t.Fatalf("NewConfigWatcher: %v", err)
	}
	if err := watcher.AddPath(configPath); err != nil {
		t.Fatalf("AddPath: %v", err)
	}

	go watcher.Start()
	defer watcher.Stop()

	// Wait for several polls with no change
	time.Sleep(250 * time.Millisecond) // REAL-TIME: polling test needs to wait for ticker

	count := reloadCount.Load()
	if count != 0 {
		t.Errorf("expected 0 reloads with no changes, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// checkChanged unit tests (direct, no ticker)
// ---------------------------------------------------------------------------

func TestCheckChanged_FileModified(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test.json")
	if err := os.WriteFile(configPath, []byte(`v1`), 0644); err != nil {
		t.Fatal(err)
	}

	watcher, _ := NewConfigWatcher(func() {}, WithInterval(time.Hour))
	if err := watcher.AddPath(configPath); err != nil {
		t.Fatal(err)
	}

	// No change yet
	if watcher.checkChanged() {
		t.Error("should not detect change with no modification")
	}

	// Modify file
	time.Sleep(10 * time.Millisecond) // REAL-TIME: ensure mtime differs on filesystem
	if err := os.WriteFile(configPath, []byte(`v2`), 0644); err != nil {
		t.Fatal(err)
	}

	if !watcher.checkChanged() {
		t.Error("should detect modification")
	}

	// Second check should not trigger (mtime already updated)
	if watcher.checkChanged() {
		t.Error("should not detect change on re-check")
	}
}

func TestCheckChanged_FileCreated(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test.json")

	watcher, _ := NewConfigWatcher(func() {}, WithInterval(time.Hour))
	if err := watcher.AddPath(configPath); err != nil {
		t.Fatal(err)
	}

	// File doesn't exist yet — no change
	if watcher.checkChanged() {
		t.Error("should not detect change for still-missing file")
	}

	// Create the file
	if err := os.WriteFile(configPath, []byte(`v1`), 0644); err != nil {
		t.Fatal(err)
	}

	if !watcher.checkChanged() {
		t.Error("should detect file creation")
	}
}

func TestCheckChanged_FileDeleted(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test.json")
	if err := os.WriteFile(configPath, []byte(`v1`), 0644); err != nil {
		t.Fatal(err)
	}

	watcher, _ := NewConfigWatcher(func() {}, WithInterval(time.Hour))
	if err := watcher.AddPath(configPath); err != nil {
		t.Fatal(err)
	}

	// Baseline exists — no change yet
	if watcher.checkChanged() {
		t.Error("should not detect change with no modification")
	}

	// Delete file
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}

	if !watcher.checkChanged() {
		t.Error("should detect file deletion")
	}

	// Second check — file still gone, no new trigger
	if watcher.checkChanged() {
		t.Error("should not trigger again for still-missing file")
	}
}
