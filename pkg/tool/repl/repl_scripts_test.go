package repl

import (
	"sync"
	"testing"
)

// The reload path makes SetReplScripts concurrent with the session-creation
// read of the same global; the RWMutex guard is what keeps that race-free.
// Run under -race this test fails without the lock.
func TestSetReplScripts_ConcurrentWithRead(t *testing.T) {
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 300 {
				SetReplScripts([]ReplScript{{Name: "a.js", Source: "x"}, {Name: "b.js", Source: "y"}})
			}
		}()
		go func() {
			defer wg.Done()
			for range 300 {
				scripts := currentReplScripts()
				if len(scripts) > 2 {
					t.Errorf("currentReplScripts returned %d scripts, want at most 2", len(scripts))
					return
				}
			}
		}()
	}
	wg.Wait()

	SetReplScripts([]ReplScript{{Name: "final.js", Source: "z"}})
	got := currentReplScripts()
	if len(got) != 1 || got[0].Name != "final.js" {
		t.Fatalf("after final write: got %+v, want exactly [final.js]", got)
	}
}
