package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	ctxbuild "github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/llm"
)

// appStyleRefresher mirrors how pkg/app injects the callback: both halves are
// rebuilt from the construction-time dir, never from a live cwd.
func appStyleRefresher(dir string) func() (string, map[string]string) {
	return appStyleRefresherWithMem(dir, "")
}

// appStyleRefresherWithMem is the same for an engine whose prompt is scoped to
// its own memory dir, which is how the WeChat daemon engines are wired.
func appStyleRefresherWithMem(dir, memDir string) func() (string, map[string]string) {
	return func() (string, map[string]string) {
		return ctxbuild.BuildSystemPrompt(dir, "", nil, "", memDir),
			ctxbuild.LoadContextFiles(dir)
	}
}

// requestText flattens the message half of a request.
func requestText(req *llm.Request) string {
	var b strings.Builder
	for _, m := range req.Messages {
		for _, cb := range m.Content {
			b.WriteString(cb.Text)
		}
	}
	return b.String()
}

// requestSystem flattens the system-prompt half of a request.
func requestSystem(req *llm.Request) string {
	var b strings.Builder
	for _, sb := range req.SystemBlocks {
		b.WriteString(sb.Text)
	}
	return b.String()
}

// TestContextRefreshAfterCompact_ReachesRequest pins the whole point of the
// reload: an edit to CLAUDE.md or to a memory .md file mid-session reaches the
// model on the request right after a compaction. Both prefix halves are
// asserted on the wire — the user-context half because callLLM re-reads the
// map, the system-prompt half because it reaches callLLM as runTurns'
// parameter and has to be re-synced. The control half (no refresher) proves the
// new sentinels arrive because of the refresh and not because of anything else.
func TestContextRefreshAfterCompact_ReachesRequest(t *testing.T) {
	t.Parallel()
	const (
		sentinelA, sentinelB       = "CTX-RELOAD-A-4f1a", "CTX-RELOAD-B-8b3c"
		memSentinelA, memSentinelB = "CTX-MEMRELOAD-A-c20d", "CTX-MEMRELOAD-B-e95f"
		frozenPrompt               = "CTX-FROZEN-PROMPT-0a71"
	)

	type wire struct{ msgs, system string }

	run := func(t *testing.T, wireRefresher bool) (w wire, compacted int) {
		t.Helper()
		dir := t.TempDir()
		claudeMd := filepath.Join(dir, "CLAUDE.md")
		if err := os.WriteFile(claudeMd, []byte("# A\n"+sentinelA), 0o644); err != nil {
			t.Fatal(err)
		}
		// The memory .md half of the system prompt.
		memDir := filepath.Join(dir, "memory")
		if err := os.MkdirAll(memDir, 0o755); err != nil {
			t.Fatal(err)
		}
		memoryMd := filepath.Join(memDir, "MEMORY.md")
		if err := os.WriteFile(memoryMd, []byte("# A\n"+memSentinelA), 0o644); err != nil {
			t.Fatal(err)
		}
		var reqs []wire
		mp := &testProvider{onStream: func(req *llm.Request) {
			reqs = append(reqs, wire{msgs: requestText(req), system: requestSystem(req)})
		}}
		mp.responses = []testResponse{{events: textStreamEvents("test", "after compact")}}
		mc := &mockCompactor{}
		eng := New(&Params{
			Provider:    mp,
			Model:       "test",
			WorkingDir:  dir,
			Compactor:   mc,
			AutoCompact: AutoCompactConfig{ContextWindow: 1000},
		})
		t.Cleanup(func() { eng.Close() })
		if wireRefresher {
			eng.SetContextRefresher(appStyleRefresherWithMem(dir, memDir))
		}
		eng.SetMessages(makeLargeMessages(10, 100))
		// The edits the model can only learn about by re-reading.
		if err := os.WriteFile(claudeMd, []byte("# B\n"+sentinelB), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(memoryMd, []byte("# B\n"+memSentinelB), 0o644); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// A non-empty prompt: runTurns hands that parameter to callLLM, so this
		// is what the system-prompt half of the request would stay as if the
		// refresh failed to re-sync it.
		if r := eng.QuerySync(ctx, "continue", frozenPrompt); r.Error != nil {
			t.Fatalf("QuerySync: %v", r.Error)
		}
		if len(reqs) != 1 {
			t.Fatalf("requests sent = %d, want 1", len(reqs))
		}
		return reqs[0], mc.CallCount()
	}

	t.Run("refresher wired", func(t *testing.T) {
		got, compacted := run(t, true)
		if compacted != 1 {
			t.Fatalf("compactor calls = %d, want 1 (premise: the turn must compact)", compacted)
		}
		if n := strings.Count(got.msgs, sentinelB); n != 1 {
			t.Errorf("edited CLAUDE.md sentinel count in request = %d, want 1 — refresh did not reach the wire:\n%s", n, got.msgs)
		}
		if n := strings.Count(got.msgs, sentinelA); n != 0 {
			t.Errorf("stale CLAUDE.md sentinel count in request = %d, want 0:\n%s", n, got.msgs)
		}
		if n := strings.Count(got.system, memSentinelB); n != 1 {
			t.Errorf("edited memory sentinel count in request system prompt = %d, want 1 — the refreshed prompt never replaced the caller's:\n%s", n, got.system)
		}
		if n := strings.Count(got.system, memSentinelA); n != 0 {
			t.Errorf("stale memory sentinel count in request system prompt = %d, want 0:\n%s", n, got.system)
		}
		if n := strings.Count(got.system, frozenPrompt); n != 0 {
			t.Errorf("caller's system prompt survived the refresh (count %d, want 0):\n%s", n, got.system)
		}
	})

	t.Run("no refresher keeps the frozen copy", func(t *testing.T) {
		got, compacted := run(t, false)
		if compacted != 1 {
			t.Fatalf("compactor calls = %d, want 1 (premise: the turn must compact)", compacted)
		}
		if n := strings.Count(got.msgs, sentinelA); n != 1 {
			t.Errorf("frozen CLAUDE.md sentinel count in request = %d, want 1", n)
		}
		if n := strings.Count(got.msgs, sentinelB); n != 0 {
			t.Errorf("edited CLAUDE.md sentinel count in request = %d, want 0 without a refresher", n)
		}
		if n := strings.Count(got.system, frozenPrompt); n != 1 {
			t.Errorf("caller's system prompt count without a refresher = %d, want 1", n)
		}
		if n := strings.Count(got.system, memSentinelA) + strings.Count(got.system, memSentinelB); n != 0 {
			t.Errorf("memory sentinel count in system prompt without a refresher = %d, want 0", n)
		}
	})
}

// TestContextRefreshAfterManualCompact_ReachesNextRequest pins the /compact
// trigger, the most user-visible one. ManualCompact is not inside a turn loop,
// so the refresh cannot show up as "the rest of this query" — the proof is the
// next request. Auto-compact is set far above the message size so nothing else
// can be credited with the refresh.
func TestContextRefreshAfterManualCompact_ReachesNextRequest(t *testing.T) {
	t.Parallel()
	const sentinelA, sentinelB = "CTX-MANUAL-A-6c93", "CTX-MANUAL-B-1d47"

	dir := t.TempDir()
	claudeMd := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(claudeMd, []byte("# A\n"+sentinelA), 0o644); err != nil {
		t.Fatal(err)
	}
	var reqTexts []string
	mp := &testProvider{onStream: func(req *llm.Request) {
		reqTexts = append(reqTexts, requestText(req))
	}}
	mp.responses = []testResponse{{events: textStreamEvents("test", "after manual compact")}}
	mc := &mockCompactor{}
	eng := New(&Params{
		Provider:   mp,
		Model:      "test",
		WorkingDir: dir,
		Compactor:  mc,
		AutoCompact: AutoCompactConfig{
			ContextWindow: 1_000_000,
		},
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetContextRefresher(appStyleRefresher(dir))
	eng.SetMessages(makeLargeMessages(10, 100))
	if err := os.WriteFile(claudeMd, []byte("# B\n"+sentinelB), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.ManualCompact(context.Background(), compactUserMsg(), ""); err != nil {
		t.Fatalf("ManualCompact: %v", err)
	}
	if got := mc.CallCount(); got != 1 {
		t.Fatalf("compactor calls = %d, want 1 (premise: the manual compaction must run)", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if r := eng.QuerySync(ctx, "continue", ""); r.Error != nil {
		t.Fatalf("QuerySync: %v", r.Error)
	}
	if len(reqTexts) != 1 {
		t.Fatalf("requests sent = %d, want 1", len(reqTexts))
	}
	if got := strings.Count(reqTexts[0], sentinelB); got != 1 {
		t.Errorf("edited CLAUDE.md sentinel count in the request after /compact = %d, want 1:\n%s", got, reqTexts[0])
	}
	if got := strings.Count(reqTexts[0], sentinelA); got != 0 {
		t.Errorf("stale CLAUDE.md sentinel count in the request after /compact = %d, want 0:\n%s", got, reqTexts[0])
	}
}

// TestContextRefreshMidQuery_ReachesNextRequest covers the two compactions that
// happen inside the turn loop and continue it: the reactive one after a
// prompt_too_long API error, and the recovery one after a
// context_window_exceeded stop reason. Both must hand the next request the
// refreshed prompt in both halves — the system prompt reaches callLLM as
// runTurns' parameter, so a refresh that only swaps the field leaves the rest of
// the query sending new CLAUDE.md over the old prompt.
func TestContextRefreshMidQuery_ReachesNextRequest(t *testing.T) {
	t.Parallel()
	const (
		sentinelA, sentinelB       = "CTX-MIDQ-A-71b8", "CTX-MIDQ-B-a3e2"
		memSentinelA, memSentinelB = "CTX-MIDQMEM-A-5c14", "CTX-MIDQMEM-B-9f70"
		frozenPrompt               = "CTX-MIDQ-FROZEN-2d8b"
	)

	type wire struct{ msgs, system string }

	run := func(t *testing.T, script func(*testProvider)) {
		t.Helper()
		dir := t.TempDir()
		claudeMd := filepath.Join(dir, "CLAUDE.md")
		if err := os.WriteFile(claudeMd, []byte("# A\n"+sentinelA), 0o644); err != nil {
			t.Fatal(err)
		}
		memDir := filepath.Join(dir, "memory")
		if err := os.MkdirAll(memDir, 0o755); err != nil {
			t.Fatal(err)
		}
		memoryMd := filepath.Join(memDir, "MEMORY.md")
		if err := os.WriteFile(memoryMd, []byte("# A\n"+memSentinelA), 0o644); err != nil {
			t.Fatal(err)
		}
		var reqs []wire
		mp := &testProvider{onStream: func(req *llm.Request) {
			reqs = append(reqs, wire{msgs: requestText(req), system: requestSystem(req)})
		}}
		script(mp)
		mc := &mockCompactor{}
		eng := New(&Params{
			Provider:   mp,
			Model:      "test",
			WorkingDir: dir,
			Compactor:  mc,
			// High enough that the pre-turn proactive compact never fires, so the
			// trigger under test is the only compaction and the only refresh.
			AutoCompact: AutoCompactConfig{ContextWindow: 1_000_000},
		})
		t.Cleanup(func() { eng.Close() })
		eng.SetContextRefresher(appStyleRefresherWithMem(dir, memDir))
		eng.SetMessages(makeLargeMessages(10, 100))
		if err := os.WriteFile(claudeMd, []byte("# B\n"+sentinelB), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(memoryMd, []byte("# B\n"+memSentinelB), 0o644); err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if r := eng.QuerySync(ctx, "continue", frozenPrompt); r.Error != nil {
			t.Fatalf("QuerySync: %v", r.Error)
		}
		if got := mc.CallCount(); got != 1 {
			t.Fatalf("compactor calls = %d, want 1 (premise: exactly one compaction, from the trigger under test)", got)
		}
		if len(reqs) != 2 {
			t.Fatalf("requests sent = %d, want 2 (premise: the query must continue after compacting)", len(reqs))
		}
		// The pre-compaction request is the control: it shows the sentinels in
		// the next request arrived because of the refresh.
		if n := strings.Count(reqs[0].msgs, sentinelA); n != 1 {
			t.Errorf("pre-compaction request CLAUDE.md sentinel count = %d, want 1:\n%s", n, reqs[0].msgs)
		}
		next := reqs[1]
		if n := strings.Count(next.msgs, sentinelB); n != 1 {
			t.Errorf("post-compaction request CLAUDE.md sentinel count = %d, want 1 — refresh did not reach the continued query:\n%s", n, next.msgs)
		}
		if n := strings.Count(next.msgs, sentinelA); n != 0 {
			t.Errorf("post-compaction request still carries the stale CLAUDE.md (count %d, want 0):\n%s", n, next.msgs)
		}
		if n := strings.Count(next.system, memSentinelB); n != 1 {
			t.Errorf("post-compaction request system prompt memory sentinel count = %d, want 1 — the loop kept the caller's prompt:\n%s", n, next.system)
		}
		if n := strings.Count(next.system, frozenPrompt); n != 0 {
			t.Errorf("post-compaction request kept the caller's system prompt (count %d, want 0):\n%s", n, next.system)
		}
	}

	t.Run("reactive compact after context overflow", func(t *testing.T) {
		run(t, func(mp *testProvider) {
			mp.responses = []testResponse{
				{err: &llm.APIError{Status: 400, ErrorCode: "prompt_too_long", Message: "input length exceeds context limit"}},
				{events: textStreamEvents("test", "after reactive compact")},
			}
		})
	})

	t.Run("recovery compact after context window stop reason", func(t *testing.T) {
		run(t, func(mp *testProvider) {
			mp.responses = []testResponse{
				{events: textEventsWithStopReason("test", "truncated response...", stopReasonContextWindowExceeded)},
				{events: textStreamEvents("test", "after context window compact")},
			}
		})
	})
}

// TestContextRefresh_IsByteIdenticalWhenUnchanged is the guard for the
// "reload unconditionally" decision: prompt caching keys on content, so a
// rebuild from unchanged inputs must be deterministic. Real-world rebuilds can
// still differ for reasons outside this test's inputs — repo-root re-detection
// and the LSP list, which moves while servers start up — and those misses ride
// on the compaction that triggered the rebuild. A timestamp or any other
// non-deterministic content added to either half turns this red.
func TestContextRefresh_IsByteIdenticalWhenUnchanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const sentinel = "CTX-STABLE-2e91"
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# S\n"+sentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	eng := New(&Params{Model: "test", WorkingDir: dir})
	t.Cleanup(func() { eng.Close() })
	eng.SetContextRefresher(appStyleRefresher(dir))

	eng.refreshContext()
	sys1 := eng.SystemPrompt()
	ctx1 := ctxbuild.BuildPrependUserContext(eng.userContext())

	// Precondition: both halves really were rebuilt, so equality below cannot be
	// two copies of the same fallback/empty string.
	if !strings.Contains(sys1, "workspace="+dir) {
		t.Fatalf("system prompt was not rebuilt from %q:\n%s", dir, sys1)
	}
	if !strings.Contains(ctx1, sentinel) {
		t.Fatalf("user context rendering does not contain the CLAUDE.md sentinel:\n%s", ctx1)
	}

	eng.refreshContext()
	sys2 := eng.SystemPrompt()
	ctx2 := ctxbuild.BuildPrependUserContext(eng.userContext())

	if sys1 != sys2 {
		t.Errorf("system prompt is not byte-identical across two refreshes with nothing edited\nfirst:\n%s\nsecond:\n%s", sys1, sys2)
	}
	if ctx1 != ctx2 {
		t.Errorf("user context rendering is not byte-identical across two refreshes with nothing edited\nfirst:\n%s\nsecond:\n%s", ctx1, ctx2)
	}
}

// TestContextRefresh_ConcurrentReaders pins the overlap that the mu guard
// exists for: /context's DumpAPIRequest runs on the UI goroutine while
// refreshContext swaps both prefix halves from the query goroutine. The
// assertion is the race detector; the equality check below only proves the
// swap actually landed.
func TestContextRefresh_ConcurrentReaders(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const sentinel = "CTX-RACE-5b18"
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# R\n"+sentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	eng := New(&Params{Model: "test", WorkingDir: dir})
	t.Cleanup(func() { eng.Close() })
	eng.SetContextRefresher(appStyleRefresher(dir))

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				dump := eng.DumpAPIRequest()
				if dump.Model != "test" {
					t.Errorf("dump model = %q, want %q", dump.Model, "test")
				}
			}
		})
	}
	wg.Go(func() {
		for range 20 {
			eng.refreshContext()
		}
	})
	wg.Wait()

	if got := ctxbuild.BuildPrependUserContext(eng.userContext()); !strings.Contains(got, sentinel) {
		t.Errorf("user context after concurrent refreshes does not contain the CLAUDE.md sentinel:\n%s", got)
	}
}

// TestContextRefresh_NilRefresherIsNoOp pins the nil contract: with no callback
// injected, refreshContext must neither panic nor touch either field, and must
// report false so a turn loop keeps the prompt it was handed.
func TestContextRefresh_NilRefresherIsNoOp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const sentinel = "CTX-NILREF-3d07"
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# N\n"+sentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	eng := New(&Params{Model: "test", WorkingDir: dir})
	t.Cleanup(func() { eng.Close() })
	eng.SetSystemPrompt("frozen prompt")
	before := ctxbuild.BuildPrependUserContext(eng.userContext())
	// Precondition: the map really was loaded, so the equality below cannot be
	// two copies of the same empty string.
	if !strings.Contains(before, sentinel) {
		t.Fatalf("user context was not loaded from %q:\n%s", dir, before)
	}

	if eng.refreshContext() {
		t.Error("refreshContext with a nil refresher returned true, want false")
	}

	if got := eng.SystemPrompt(); got != "frozen prompt" {
		t.Errorf("system prompt after refresh with nil refresher = %q, want %q", got, "frozen prompt")
	}
	if got := ctxbuild.BuildPrependUserContext(eng.userContext()); got != before {
		t.Errorf("user context after refresh with nil refresher = %q, want unchanged %q", got, before)
	}
}

// TestSubEngineKeepsFrozenContextAfterParentRefresh pins that a running
// sub-agent's instructions cannot change mid-task: NewSubEngine copies the
// parent's map and gets no refresher of its own.
func TestSubEngineKeepsFrozenContextAfterParentRefresh(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const sentinelA, sentinelB = "CTX-SUBKEEP-A-9a52", "CTX-SUBKEEP-B-70cf"
	claudeMd := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(claudeMd, []byte("# A\n"+sentinelA), 0o644); err != nil {
		t.Fatal(err)
	}
	eng := New(&Params{Model: "test", WorkingDir: dir})
	t.Cleanup(func() { eng.Close() })
	eng.SetContextRefresher(appStyleRefresher(dir))

	sub := eng.NewSubEngine(SubEngineOptions{})
	if err := os.WriteFile(claudeMd, []byte("# B\n"+sentinelB), 0o644); err != nil {
		t.Fatal(err)
	}

	eng.refreshContext()
	sub.refreshContext()

	if got := strings.Count(ctxbuild.BuildPrependUserContext(sub.userContext()), sentinelA); got != 1 {
		t.Errorf("sub-engine's CLAUDE.md sentinel count = %d, want 1 — a running sub-agent must keep the instructions it started with", got)
	}
	if got := strings.Count(ctxbuild.BuildPrependUserContext(sub.userContext()), sentinelB); got != 0 {
		t.Errorf("sub-engine picked up the parent's refreshed CLAUDE.md (sentinel count %d, want 0)", got)
	}
	if got := strings.Count(ctxbuild.BuildPrependUserContext(eng.userContext()), sentinelB); got != 1 {
		t.Errorf("parent's refreshed CLAUDE.md sentinel count = %d, want 1", got)
	}
}
