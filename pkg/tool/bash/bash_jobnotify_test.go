package bash

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// notifySink counts JobNotifications it receives (mutex-guarded: completion
// callbacks run on the job's goroutine).
type notifySink struct {
	mu sync.Mutex
	n  []JobNotification
}

func (s *notifySink) receive(n JobNotification) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n = append(s.n, n)
}

func (s *notifySink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.n)
}

func (s *notifySink) first() (JobNotification, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.n) == 0 {
		return JobNotification{}, false
	}
	return s.n[0], true
}

// The per-job override must beat the registry-level callback: jobs spawned
// through the tool notify the tool's hook, not the table owner.
func TestBash_SetJobNotifyFn_OverridesRegistryNotify(t *testing.T) {
	forceNonPTY(t)
	reg := NewBashRegistry()
	regSink := &notifySink{}
	reg.OnNotify = regSink.receive
	toolSink := &notifySink{}
	b := New(reg)
	b.SetJobNotifyFn(toolSink.receive)

	res, err := b.Call(context.Background(), json.RawMessage(`{"command":"sleep 0.1","run_in_background":true}`), nil)
	if err != nil {
		t.Fatalf("Bash.Call: %v", err)
	}
	if out, ok := res.Data.(*Output); !ok || !strings.Contains(out.Stdout, "Background job started with ID:") {
		t.Fatalf("Bash.Call data = %+T, want *Output carrying the bg id line", res.Data)
	}
	jobs := reg.List()
	if len(jobs) != 1 {
		t.Fatalf("registry holds %d jobs, want 1", len(jobs))
	}
	id := jobs[0].ID
	if _, err := reg.Wait(id); err != nil {
		t.Fatalf("reg.Wait(%q): %v", id, err)
	}
	// Complete closes done BEFORE sending the notification, so the sink fires
	// shortly after Wait returns — poll for it.
	deadline := time.Now().Add(2 * time.Second) // REAL-TIME: bounded poll for async notify
	for toolSink.count() == 0 {
		if time.Now().After(deadline) { // REAL-TIME: bounded poll for async notify
			t.Fatal("tool sink received no notification within 2s of Wait")
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME: poll loop waiting for async state
	}
	if got := toolSink.count(); got != 1 {
		t.Fatalf("tool sink received %d notifications, want 1", got)
	}
	first, _ := toolSink.first()
	if first.JobID != id {
		t.Errorf("tool sink notification JobID = %q, want %q", first.JobID, id)
	}
	if first.Status != "completed" {
		t.Errorf("tool sink notification Status = %q, want completed", first.Status)
	}
	if n := regSink.count(); n != 0 {
		t.Errorf("registry sink received %d notifications, want 0 (per-job override wins)", n)
	}
}

// transitionToBackground must apply the same override — it is the second
// production registration point (auto-background on timeout).
func TestTransitionToBackground_AppliesJobNotifyOverride(t *testing.T) {
	reg := NewBashRegistry()
	regSink := &notifySink{}
	reg.OnNotify = regSink.receive
	toolSink := &notifySink{}

	_, err := transitionToBackground(reg, "cmd", 0, NewStreamingOutput(nil), Input{Command: "cmd"}, t.TempDir(), func(*BackgroundJob) {}, toolSink.receive)
	if err != nil {
		t.Fatalf("transitionToBackground: %v", err)
	}
	jobs := reg.List()
	if len(jobs) != 1 {
		t.Fatalf("registry holds %d jobs, want 1", len(jobs))
	}
	jobs[0].Complete(0, false)
	if _, err := reg.Wait(jobs[0].ID); err != nil {
		t.Fatalf("reg.Wait(%q): %v", jobs[0].ID, err)
	}
	deadline := time.Now().Add(2 * time.Second) // REAL-TIME: bounded poll for async notify
	for toolSink.count() == 0 {
		if time.Now().After(deadline) { // REAL-TIME: bounded poll for async notify
			t.Fatal("tool sink received no notification within 2s of Complete")
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME: poll loop waiting for async state
	}
	if first, _ := toolSink.first(); first.JobID != jobs[0].ID || first.Status != "completed" {
		t.Errorf("tool sink notification = {JobID:%q Status:%q}, want {%q, completed}", first.JobID, first.Status, jobs[0].ID)
	}
	if n := regSink.count(); n != 0 {
		t.Errorf("registry sink received %d notifications, want 0 (per-job override wins)", n)
	}
}
