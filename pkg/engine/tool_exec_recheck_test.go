package engine

// Runs under -race too: the interleaving below (one caller's fast-path cache
// read while another caller's AllowAlways write lands under askMu) is the
// interleaving that used to be an unsynchronized map read/write pair; since
// the sync.Map fix the accesses are internally synchronized, so -race stays
// quiet here while the behavioral contract below stays asserted — the fast
// path deliberately stays off askMu (see TestToolExec_CachedAllowDoesNotTakeAskMutex).

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/types"
)

// The in-lock recheck is the second half of that double-checked cache read: a
// caller that missed the fast path can sit queued on askMu while another
// caller's "always allow" answer lands, and must then consume the cached
// answer instead of prompting the user a second time.
func TestToolExec_InLockRecheckConsumesAnswerLandedWhileQueued(t *testing.T) {
	t.Parallel()
	rec := &ctxRecordingTool{name: "ask_tool"}
	disp := newRecordingDispatcher()
	var mu sync.Mutex
	asks := 0
	// Any ask after the first is answered inline so a re-asking implementation
	// still terminates and the assertion below names the bug.
	disp.onAsk = func(evt types.QueryEvent) {
		mu.Lock()
		asks++
		n := asks
		mu.Unlock()
		if n > 1 {
			evt.Ask.ResponseCh <- types.AskResponse{Decision: types.DecisionAllow}
		}
	}
	eng := newToolExecEngine(rec, &fixedDecisionChecker{decision: permission.Decision{
		Action: permission.ActionAsk,
	}}, disp)

	var sessionAllowed sync.Map
	var askMu sync.Mutex
	done := make(chan error, 2)
	run := func() {
		_, e := eng.ExecuteTool(context.Background(), "ask_tool", json.RawMessage(`{}`), &sessionAllowed, &askMu)
		done <- e
	}
	go run() // acquires askMu, parks on the ask reply
	aAsk := disp.nextAsk(t).Ask
	go run() // misses the fast path (cache still empty), queues on askMu
	// The mutex park is invisible to synctest (Wait cannot see through a Mutex
	// wait), so wall-clock slack is the only ordering tool for "B has passed
	// the fast path".
	// REAL-TIME: sleep orders B past the fast path before the answer writes the cache
	time.Sleep(100 * time.Millisecond)
	aAsk.ResponseCh <- types.AskResponse{Decision: types.DecisionAllowAlways}

	for range 2 {
		select {
		case e := <-done:
			if e != nil {
				t.Fatalf("ExecuteTool: %v", e)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("ExecuteTool did not return after the ask was answered")
		}
	}
	if n := len(disp.asked()); n != 1 {
		t.Errorf("ask events = %d, want 1 — the caller queued on askMu prompted again instead of consuming the cached allow", n)
	}
	if rec.captured() == nil {
		t.Error("tool did not run for the queued caller")
	}
}
