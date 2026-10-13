package agent

import (
	"fmt"
	"sync"
	"testing"
)

func TestIDMinter_Sequential(t *testing.T) {
	t.Parallel()
	m := NewIDMinter()
	for i, want := range []string{"agent-1", "agent-2", "agent-3"} {
		if got := m.Mint(); got != want {
			t.Errorf("mint %d = %q, want %q", i+1, got, want)
		}
	}
}

func TestIDMinter_InitClampsForward(t *testing.T) {
	t.Parallel()
	m := NewIDMinter()
	m.Init(5)
	if got := m.Mint(); got != "agent-6" {
		t.Errorf("after Init(5), Mint = %q, want agent-6", got)
	}
	// Init never moves the counter backwards.
	m.Init(2)
	if got := m.Mint(); got != "agent-7" {
		t.Errorf("after Init(2) post-agent-6, Mint = %q, want agent-7", got)
	}
	// Init(0) is a no-op.
	m.Init(0)
	if got := m.Mint(); got != "agent-8" {
		t.Errorf("after Init(0), Mint = %q, want agent-8", got)
	}
}

func TestIDMinter_ConcurrentUnique(t *testing.T) {
	t.Parallel()
	const goroutines = 50
	const perGoroutine = 20
	m := NewIDMinter()

	var mu sync.Mutex
	seen := make(map[string]struct{}, goroutines*perGoroutine)
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range perGoroutine {
				id := m.Mint()
				mu.Lock()
				seen[id] = struct{}{}
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	if len(seen) != goroutines*perGoroutine {
		t.Fatalf("distinct ids = %d, want %d (duplicates minted)", len(seen), goroutines*perGoroutine)
	}
	for i := range goroutines * perGoroutine {
		want := fmt.Sprintf("agent-%d", i+1)
		if _, ok := seen[want]; !ok {
			t.Errorf("id %q missing from concurrent mint set", want)
		}
	}
}

func TestIsReservedAgentID(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{
		"agent-1":   true,
		"agent-42":  true,
		"agent-0":   true,
		"agent-":    false,
		"agent":     false,
		"agent-x":   false,
		"agent-1-x": false,
		"AGENT-3":   false,
		"agents-3":  false,
		"chat-2":    false,
		"e2":        false,
		"main":      false,
		"":          false,
	} {
		if got := IsReservedAgentID(name); got != want {
			t.Errorf("IsReservedAgentID(%q) = %v, want %v", name, got, want)
		}
	}
}
