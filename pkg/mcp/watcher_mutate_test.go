package mcp

// Mutation-killing tests for watcher.go (batch mcp-small8).
// Verdicts: /home/yliu/mutate-mcp1/ledger-A.txt.

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Kills watcher.go:55 numbers/decrementer (2→1) and :55 numbers/incrementer
// (2→3): the documented default polling interval is exactly 2 seconds.
func TestNewConfigWatcher_DefaultInterval2s(t *testing.T) {
	cw := NewConfigWatcher(func() {})
	if cw.interval != 2*time.Second {
		t.Errorf("default interval = %v, want 2s", cw.interval)
	}

	custom := NewConfigWatcher(func() {}, WithInterval(250*time.Millisecond))
	if custom.interval != 250*time.Millisecond {
		t.Errorf("custom interval = %v, want 250ms", custom.interval)
	}
}

// Kills watcher.go:115 loop/break (continue → break): a watched path that
// never existed must not stop the scan — changes on later paths must still be
// detected in the same pass.
func TestCheckChanged_MissingPathDoesNotStopScan(t *testing.T) {
	tmpDir := t.TempDir()
	missing := filepath.Join(tmpDir, "never-existed.json")
	existing := filepath.Join(tmpDir, "present.json")
	if err := os.WriteFile(existing, []byte(`v1`), 0644); err != nil {
		t.Fatal(err)
	}

	cw := NewConfigWatcher(func() {}, WithInterval(time.Hour))
	cw.AddPath(missing)
	cw.AddPath(existing)

	time.Sleep(10 * time.Millisecond) // REAL-TIME: ensure mtime differs on filesystem
	if err := os.WriteFile(existing, []byte(`v2`), 0644); err != nil {
		t.Fatal(err)
	}

	if !cw.checkChanged() {
		t.Error("change on the second path must be detected even when the first path is missing")
	}
}

// Kills watcher.go:72 statement/defer-remove (AddPath defer Unlock → immediate
// Unlock) and :103 statement/defer-remove (checkChanged defer Unlock →
// immediate Unlock): with the mutex actually held, concurrent AddPath calls
// lose no paths and concurrent map writes cannot interleave. The exact-count
// assertion is deterministic on the unmutated (serialized) code.
func TestConfigWatcher_ConcurrentAddAndCheck(t *testing.T) {
	tmpDir := t.TempDir()
	const n = 50
	paths := make([]string, n)
	for i := range n {
		paths[i] = filepath.Join(tmpDir, fmt.Sprintf("f%d.json", i))
		if err := os.WriteFile(paths[i], []byte(`{}`), 0644); err != nil {
			t.Fatal(err)
		}
	}

	cw := NewConfigWatcher(func() {}, WithInterval(time.Hour))

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			cw.AddPath(paths[i])
		}(i)
	}
	for range 10 {
		wg.Go(func() {
			<-start
			cw.checkChanged()
		})
	}
	close(start)
	wg.Wait()

	if got := len(cw.paths); got != n {
		t.Errorf("paths len = %d, want %d (concurrent AddPath lost updates — lock not held)", got, n)
	}
}
