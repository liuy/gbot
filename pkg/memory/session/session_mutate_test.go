package session

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// The shipped defaults are the tuning contract with the SM agent.
func TestDefaultConfig_AllFields(t *testing.T) {
	t.Parallel()
	c := DefaultConfig()
	if c.MinTokensToInit != 10000 {
		t.Errorf("MinTokensToInit = %d, want 10000", c.MinTokensToInit)
	}
	if c.MinTokensBetweenUpdate != 50000 {
		t.Errorf("MinTokensBetweenUpdate = %d, want 50000", c.MinTokensBetweenUpdate)
	}
	if c.ToolCallsBetweenUpdates != 50 {
		t.Errorf("ToolCallsBetweenUpdates = %d, want 50", c.ToolCallsBetweenUpdates)
	}
	if c.MaxSectionTokens != 2000 {
		t.Errorf("MaxSectionTokens = %d, want 2000", c.MaxSectionTokens)
	}
	if c.MaxTotalTokens != 12000 {
		t.Errorf("MaxTotalTokens = %d, want 12000", c.MaxTotalTokens)
	}
	if c.ExtractionTimeoutMs != 15000 {
		t.Errorf("ExtractionTimeoutMs = %d, want 15000", c.ExtractionTimeoutMs)
	}
	if c.ExtractionStaleMs != 60000 {
		t.Errorf("ExtractionStaleMs = %d, want 60000", c.ExtractionStaleMs)
	}
	if c.MinIntervalMs != 600000 {
		t.Errorf("MinIntervalMs = %d, want 600000", c.MinIntervalMs)
	}
}

func TestAnalyzeSectionSizes_Shapes(t *testing.T) {
	t.Parallel()
	// Empty content still yields one (unnamed, empty) section.
	got := analyzeSectionSizes("")
	if len(got) != 1 || got[0].Header != "" || got[0].Tokens != 0 {
		t.Errorf("analyzeSectionSizes(\"\") = %+v, want one empty unnamed section", got)
	}
	// A trailing header with no body still yields its section.
	got = analyzeSectionSizes("## Alpha\n")
	if len(got) != 1 || got[0].Header != "Alpha" || got[0].Tokens != 0 {
		t.Errorf("trailing header = %+v, want one Alpha section with 0 tokens", got)
	}
	// Preamble before the first header becomes its own unnamed section.
	got = analyzeSectionSizes("preamble line\n## Alpha\nbody text here")
	if len(got) != 2 {
		t.Fatalf("preamble sections = %+v, want 2 (unnamed preamble + Alpha)", got)
	}
	if got[0].Header != "" {
		t.Errorf("first section header = %q, want the unnamed preamble", got[0].Header)
	}
	if got[0].Tokens == 0 {
		t.Error("preamble section must carry its token count")
	}
	if got[1].Header != "Alpha" || got[1].Tokens == 0 {
		t.Errorf("second section = %+v, want Alpha with tokens", got[1])
	}
	// Two headers, last one empty-bodied.
	got = analyzeSectionSizes("## A\none two three\n## B\n")
	if len(got) != 2 || got[0].Header != "A" || got[1].Header != "B" || got[1].Tokens != 0 {
		t.Errorf("two sections = %+v, want A (tokens) and B (0 tokens)", got)
	}
}

// TruncateForCompact budgets 4 characters per section token and cuts at the
// last newline before the budget.
func TestTruncateForCompact_Exact(t *testing.T) {
	t.Parallel()
	const marker = "\n[... section truncated for length ...]"

	// maxChars = 10 tokens * 4 = 40. Body of 45 chars with a newline at 30.
	body := strings.Repeat("a", 30) + "\n" + strings.Repeat("b", 14)
	got := TruncateForCompact("## Sec\n"+body, 10)
	want := "## Sec\n" + strings.Repeat("a", 30) + marker
	if got != want {
		t.Errorf("truncate at newline = %q, want %q", got, want)
	}

	// No newline inside the budget: hard cut at maxChars.
	body = strings.Repeat("c", 45)
	got = TruncateForCompact("## Sec\n"+body, 10)
	want = "## Sec\n" + strings.Repeat("c", 40) + marker
	if got != want {
		t.Errorf("hard cut = %q, want %q", got, want)
	}

	// A leading newline puts the cut at index 0, which must fall back to the
	// hard cut rather than emitting an empty section.
	body = "\n" + strings.Repeat("d", 44)
	got = TruncateForCompact("## Sec\n"+body, 10)
	want = "## Sec\n" + "\n" + strings.Repeat("d", 39) + marker
	if got != want {
		t.Errorf("newline at index 0 = %q, want %q", got, want)
	}

	// Exactly at the budget: untouched.
	body = strings.Repeat("e", 40)
	got = TruncateForCompact("## Sec\n"+body, 10)
	if got != "## Sec\n"+body {
		t.Errorf("at-budget section = %q, want it untouched", got)
	}
}

// SanitizeNotes normalizes aliases, merges duplicates, drops strays, and
// rewrites even when the first header starts the file.
func TestSanitizeNotes_Rewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SESSION_NOTES.md")
	content := "# Session Notes\n\n## Current state\nfirst body\n\n## Current State\nsecond body\n\n## learnings\nlearned thing\n\n## reference\nshould be dropped\n\n## Empty Section\n\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SanitizeNotes(path); err != nil {
		t.Fatalf("SanitizeNotes: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "# Session Notes\n" +
		"\n## Current State\n\nfirst body\n\nsecond body\n" +
		"\n## Learnings\n\nlearned thing\n"
	if string(data) != want {
		t.Errorf("sanitized file =\n%q\nwant\n%q", data, want)
	}
}

func TestSanitizeNotes_LeadingHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SESSION_NOTES.md")
	// The first "\n## " occurs at index 0: the file still gets normalized.
	content := "\n## current state\nbody text\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SanitizeNotes(path); err != nil {
		t.Fatalf("SanitizeNotes: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "\n\n## Current State\n\nbody text\n" {
		t.Errorf("leading-header file = %q, want the canonicalized header", data)
	}
}

func TestSanitizeNotes_NoHeadersNoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SESSION_NOTES.md")
	content := "just prose, no headers at all\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SanitizeNotes(path); err != nil {
		t.Fatalf("SanitizeNotes: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Errorf("headerless file rewritten: %q", data)
	}
}

// The minimum-interval gate only applies when configured and an extraction
// has already happened.
func TestShouldExtract_MinIntervalGate(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinTokensToInit = 0
	cfg.MinTokensBetweenUpdate = 10
	cfg.ToolCallsBetweenUpdates = 0
	sm := New(cfg, t.TempDir(), "engine", nil, nil)

	// No prior extraction: the interval gate is skipped.
	if !sm.ShouldExtract(1000, nil) {
		t.Fatal("first extraction past the token threshold must be allowed")
	}

	// Recent extraction inside the interval: blocked.
	// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
	sm.lastExtractionTime = time.Now()
	if sm.ShouldExtract(5000, nil) {
		t.Error("extraction inside MinIntervalMs must be blocked")
	}

	// Old enough: allowed again.
	// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
	sm.lastExtractionTime = time.Now().Add(-time.Duration(cfg.MinIntervalMs)*time.Millisecond - time.Second)
	if !sm.ShouldExtract(9000, nil) {
		t.Error("extraction past MinIntervalMs must be allowed")
	}

	// Gate disabled: a recent extraction no longer blocks.
	sm2 := New(Config{MinTokensBetweenUpdate: 10, ExtractionStaleMs: 60000}, t.TempDir(), "engine", nil, nil)
	sm2.initialized = true
	// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
	sm2.lastExtractionTime = time.Now()
	if !sm2.ShouldExtract(5000, nil) {
		t.Error("MinIntervalMs = 0 must disable the interval gate")
	}
}

// --- ShouldExtract gate arithmetic ---

func TestShouldExtract_InitGate(t *testing.T) {
	setTempHome(t)
	cfg := Config{MinTokensToInit: 10000, MinTokensBetweenUpdate: 5000, ToolCallsBetweenUpdates: 0}
	sm := New(cfg, t.TempDir(), "main", nil, slog.Default())
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}
	// Below the init threshold nothing fires, even with the other gates wide open.
	if sm.ShouldExtract(7000, msgs) {
		t.Error("below MinTokensToInit should return false")
	}
	// At exactly MinTokensToInit the gate passes.
	if !sm.ShouldExtract(10000, msgs) {
		t.Error("at exactly MinTokensToInit should proceed to the other gates")
	}
	// Init is sticky: a later below-init token count no longer short-circuits.
	if !sm.ShouldExtract(7000, msgs) {
		t.Error("after init, below-init token count must not block (growth gate governs)")
	}
}

func TestShouldExtract_TokenGrowthArithmetic(t *testing.T) {
	setTempHome(t)
	cfg := Config{MinTokensToInit: 100, MinTokensBetweenUpdate: 100, ToolCallsBetweenUpdates: 0, MinIntervalMs: 0}
	sm := New(cfg, t.TempDir(), "main", nil, slog.Default())
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}
	// Growth exactly at the threshold qualifies.
	if !sm.ShouldExtract(100, msgs) {
		t.Error("token growth exactly at MinTokensBetweenUpdate should qualify")
	}
	sm.mu.Lock()
	sm.lastTokenCount = 60000
	sm.mu.Unlock()
	// Growth is measured from the last extraction, not doubled by addition.
	if sm.ShouldExtract(60001, msgs) {
		t.Error("growth of 1 below a 100 threshold must not qualify")
	}
}

// A disabled interval gate (MinIntervalMs=0) must stay disabled even for a
// future-dated last extraction (clock skew); a 1ms gate blocks it.
func TestShouldExtract_IntervalGateDisabled(t *testing.T) {
	setTempHome(t)
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}
	future := time.Now().Add(time.Hour) // REAL-TIME: clock-skew stand-in for a future-dated watermark

	cfg := Config{MinTokensToInit: 1, MinTokensBetweenUpdate: 1, ToolCallsBetweenUpdates: 0, MinIntervalMs: 0}
	sm := New(cfg, t.TempDir(), "main", nil, slog.Default())
	sm.mu.Lock()
	sm.lastExtractionTime = future
	sm.mu.Unlock()
	if !sm.ShouldExtract(5, msgs) {
		t.Error("MinIntervalMs=0 disables the interval gate entirely")
	}

	cfg.MinIntervalMs = 1
	sm = New(cfg, t.TempDir(), "main", nil, slog.Default())
	sm.mu.Lock()
	sm.lastExtractionTime = future
	sm.mu.Unlock()
	if sm.ShouldExtract(5, msgs) {
		t.Error("a 1ms interval gate must block a future-dated last extraction")
	}
}

// --- Extract state and plumbing ---

// completesIn fails the test if fn does not return within d — used to turn
// a leaked lock into a bounded failure instead of a hung run.
func completesIn(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("operation did not complete within %v (deadlock?)", d)
	}
}

// The already-extracting skip path must release the mutex before returning.
func TestExtract_SkipPathReleasesLock(t *testing.T) {
	setTempHome(t)
	sm := New(Config{ExtractionStaleMs: 600000}, t.TempDir(), "main", nil, slog.Default())
	sm.mu.Lock()
	sm.extracting = true
	sm.extractionStart = time.Now() // REAL-TIME: fresh start so the skip path is taken
	sm.mu.Unlock()
	completesIn(t, 2*time.Second, func() {
		if err := sm.Extract(context.Background(), nil, 100); err != nil {
			t.Errorf("skip-path Extract = %v, want nil", err)
		}
	})
	completesIn(t, 2*time.Second, func() { sm.ShouldExtract(1, nil) })
}

// Cleanup after a finished extraction must let WaitForExtraction return nil
// immediately instead of waiting out the timeout.
func TestExtract_CleanupLetsWaitReturn(t *testing.T) {
	setTempHome(t)
	sm := New(Config{ExtractionTimeoutMs: 100, ExtractionStaleMs: 600000}, t.TempDir(), "main",
		func(context.Context, string, string, []types.Message, string) error { return nil },
		slog.Default())
	if err := sm.Extract(context.Background(), nil, 100); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	completesIn(t, 2*time.Second, func() {
		if err := sm.WaitForExtraction(); err != nil {
			t.Errorf("WaitForExtraction after completed Extract = %v, want nil", err)
		}
	})
}

func TestExtract_PassesSystemPromptAndContext(t *testing.T) {
	setTempHome(t)
	type ctxKey struct{}
	wantCtx := context.WithValue(context.Background(), ctxKey{}, "ctx-marker")
	var gotCtx context.Context
	var gotPrompt string
	sm := New(Config{}, t.TempDir(), "main",
		func(ctx context.Context, _ string, _ string, _ []types.Message, sysPrompt string) error {
			gotCtx = ctx
			gotPrompt = sysPrompt
			return nil
		}, slog.Default())
	sm.SetSystemPromptFn(func() string { return "SP-marker" })
	if err := sm.Extract(wantCtx, nil, 100); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if gotCtx == nil || gotCtx.Value(ctxKey{}) != "ctx-marker" {
		t.Errorf("extractFn received ctx = %v, want the caller's context", gotCtx)
	}
	if gotPrompt != "SP-marker" {
		t.Errorf("extractFn received sysPrompt = %q, want %q", gotPrompt, "SP-marker")
	}
}

func TestExtract_WrapsUnderlyingErrors(t *testing.T) {
	setTempHome(t)
	t.Run("ensure file", func(t *testing.T) {
		sm := New(Config{}, t.TempDir(), "main", nil, slog.Default())
		dir := filepath.Dir(sm.NotesPath())
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dir, []byte("blocker"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := sm.Extract(context.Background(), nil, 100)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "setup session memory file") {
			t.Errorf("error = %v, want setup prefix", err)
		}
		var pe *os.PathError
		if !errors.As(err, &pe) {
			t.Errorf("error does not wrap *os.PathError: %v", err)
		}
	})
	t.Run("read notes", func(t *testing.T) {
		sm := New(Config{}, t.TempDir(), "main", nil, slog.Default())
		notesPath := sm.NotesPath()
		if err := os.MkdirAll(filepath.Dir(notesPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(notesPath, 0o755); err != nil {
			t.Fatal(err)
		}
		err := sm.Extract(context.Background(), nil, 100)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "read session memory") {
			t.Errorf("error = %v, want read prefix", err)
		}
		var pe *os.PathError
		if !errors.As(err, &pe) {
			t.Errorf("error does not wrap *os.PathError: %v", err)
		}
	})
	t.Run("extraction", func(t *testing.T) {
		sentinel := os.ErrPermission
		sm := New(Config{}, t.TempDir(), "main",
			func(context.Context, string, string, []types.Message, string) error { return sentinel },
			slog.Default())
		err := sm.Extract(context.Background(), nil, 100)
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, sentinel) {
			t.Errorf("error = %v, want it to wrap the extractFn error", err)
		}
	})
}

// Extraction resets the tool-call counter; afterwards the threshold gate
// must fail again even when the last turn had tool calls.
func TestExtract_ResetsToolCallCounter(t *testing.T) {
	setTempHome(t)
	cfg := Config{MinTokensToInit: 1, MinTokensBetweenUpdate: 1, ToolCallsBetweenUpdates: 3, MinIntervalMs: 0}
	sm := New(cfg, t.TempDir(), "main",
		func(context.Context, string, string, []types.Message, string) error { return nil },
		slog.Default())
	sm.RecordToolCalls(3)
	if err := sm.Extract(context.Background(), nil, 5); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	msgs := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "c1", Name: "Read"},
		}},
	}
	if sm.ShouldExtract(6, msgs) {
		t.Error("tool-call counter must reset after extraction")
	}
}

// Extraction stamps lastExtractionTime; the min-interval gate then blocks
// an immediate re-extraction that would otherwise qualify.
func TestExtract_StampsLastExtractionTime(t *testing.T) {
	setTempHome(t)
	sm := New(DefaultConfig(), t.TempDir(), "main",
		func(context.Context, string, string, []types.Message, string) error { return nil },
		slog.Default())
	if err := sm.Extract(context.Background(), nil, 10000); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}
	if sm.ShouldExtract(70000, msgs) {
		t.Error("min interval must block extraction right after a completed one")
	}
}

func TestReset_ClearsIntervalState(t *testing.T) {
	setTempHome(t)
	sm := New(DefaultConfig(), t.TempDir(), "main",
		func(context.Context, string, string, []types.Message, string) error { return nil },
		slog.Default())
	if err := sm.Extract(context.Background(), nil, 10000); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	sm.Reset()
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}
	if !sm.ShouldExtract(70000, msgs) {
		t.Error("Reset must clear the interval gate along with the rest of the state")
	}
}

func TestWaitForExtraction_ReleasesLockOnIdlePath(t *testing.T) {
	setTempHome(t)
	sm := New(Config{}, t.TempDir(), "main", nil, slog.Default())
	completesIn(t, 2*time.Second, func() {
		if err := sm.WaitForExtraction(); err != nil {
			t.Errorf("WaitForExtraction when idle = %v, want nil", err)
		}
	})
	completesIn(t, 2*time.Second, func() { sm.ShouldExtract(1, nil) })
}

// Stale recovery must close the done channel so a waiter blocked on it is
// woken rather than left to time out. The waiter must already be parked on
// the channel when staleness is injected, otherwise it would recover on
// its own and prove nothing.
func TestWaitForExtraction_StaleRecoveryWakesWaiters(t *testing.T) {
	setTempHome(t)
	sm := New(Config{ExtractionTimeoutMs: 1000, ExtractionStaleMs: 600000}, t.TempDir(), "main", nil, slog.Default())
	sm.mu.Lock()
	sm.extracting = true
	sm.extractDone = make(chan struct{})
	sm.extractionStart = time.Now() // REAL-TIME: fresh, so a waiter parks on done instead of recovering
	sm.mu.Unlock()

	waitErr := make(chan error, 1)
	go func() { waitErr <- sm.WaitForExtraction() }()
	time.Sleep(50 * time.Millisecond) // REAL-TIME: let the waiter park on done

	sm.mu.Lock()
	sm.extractionStart = time.Now().Add(-time.Hour) // REAL-TIME: now stale
	sm.mu.Unlock()
	if err := sm.WaitForExtraction(); err != nil {
		t.Fatalf("stale-recovery call = %v, want nil", err)
	}
	if err := <-waitErr; err != nil {
		t.Errorf("parked waiter = %v, want nil (should be woken by close)", err)
	}
}

// --- ensureFile / LoadSessionMemoryContent ---

func TestEnsureFile_PermModes(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	sm := New(Config{}, t.TempDir(), "main", nil, nil)
	if err := sm.ensureFile(); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(filepath.Dir(sm.NotesPath()))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o755 {
		t.Errorf("session_notes mode = %o, want 755", perm)
	}
	fileInfo, err := os.Stat(sm.NotesPath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o644 {
		t.Errorf("notes file mode = %o, want 644", perm)
	}
}

// A blocked session_notes directory must fail inside MkdirAll, not surface
// later as the WriteFile error.
func TestEnsureFile_MkdirFailureSurfacesAtMkdir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("path-blocking setup behaves differently as root")
	}
	setTempHome(t)
	sm := New(Config{}, t.TempDir(), "main", nil, nil)
	dir := filepath.Dir(sm.NotesPath())
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("blocker"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := sm.ensureFile()
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *os.PathError
	if !errors.As(err, &pe) {
		t.Fatalf("error does not wrap *os.PathError: %v", err)
	}
	if pe.Op != "mkdir" {
		t.Errorf("failing op = %q, want the failure to surface at mkdir (got %v)", pe.Op, err)
	}
}

func TestLoadSessionMemoryContent_RoutesByEngine(t *testing.T) {
	setTempHome(t)
	dir := t.TempDir()
	notesDir := filepath.Join(dir, "session_notes")
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notesDir, "main.md"), []byte("main notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notesDir, "e2.md"), []byte("e2 notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LoadSessionMemoryContent(dir, "e2"); got != "e2 notes" {
		t.Errorf("LoadSessionMemoryContent(e2) = %q, want %q", got, "e2 notes")
	}
	if got := LoadSessionMemoryContent(dir, ""); got != "main notes" {
		t.Errorf("LoadSessionMemoryContent(\"\") = %q, want %q", got, "main notes")
	}
}

// --- template.go section accounting ---

// A header-only section (no body) still occupies a slot in the size list.
func TestAnalyzeSectionSizes_HeaderOnlySections(t *testing.T) {
	t.Parallel()
	sizes := analyzeSectionSizes("## Alpha\nalpha body\n## Beta")
	if len(sizes) != 2 {
		t.Fatalf("sizes = %+v, want 2 sections (header-only Beta included)", sizes)
	}
	if sizes[0].Header != "Alpha" || sizes[1].Header != "Beta" {
		t.Errorf("headers = [%q %q], want [Alpha Beta]", sizes[0].Header, sizes[1].Header)
	}
	sizes = analyzeSectionSizes("## A\n## B\nb body")
	if len(sizes) != 2 {
		t.Fatalf("sizes = %+v, want 2 sections (header-only A included)", sizes)
	}
}

func TestGenerateSectionReminders_Boundaries(t *testing.T) {
	t.Parallel()
	// Section exactly at budget: not oversized.
	if got := generateSectionReminders([]SectionSizes{{Header: "H", Tokens: 300}}, 300, 1000); got != "" {
		t.Errorf("section at budget = %q, want no reminders", got)
	}
	// Total exactly at budget: no warnings.
	if got := generateSectionReminders([]SectionSizes{{Header: "H", Tokens: 100}}, 1000, 100); got != "" {
		t.Errorf("total at budget = %q, want no reminders", got)
	}
	// Total exactly at budget but one section oversized: warn about the
	// section without claiming the total budget is exceeded.
	got := generateSectionReminders([]SectionSizes{{Header: "H", Tokens: 300}, {Header: "H2", Tokens: 100}}, 250, 400)
	if !strings.Contains(got, "Size Warnings") {
		t.Errorf("oversized section at total budget = %q, want warnings", got)
	}
	if strings.Contains(got, "CRITICAL") {
		t.Errorf("total at budget must not be reported as exceeding:\n%s", got)
	}
	// Total over budget with no oversized section: total warning only.
	got = generateSectionReminders([]SectionSizes{{Header: "H", Tokens: 100}}, 250, 50)
	if !strings.Contains(got, "CRITICAL") {
		t.Errorf("total over budget = %q, want the critical warning", got)
	}
	if strings.Contains(got, "The following sections") {
		t.Errorf("no oversized section, but per-section block emitted:\n%s", got)
	}
}

func TestGenerateSectionReminders_ExactOutput(t *testing.T) {
	t.Parallel()
	got := generateSectionReminders([]SectionSizes{{Header: "H", Tokens: 300}, {Header: "H2", Tokens: 10}}, 100, 200)
	want := "## Size Warnings\n\n" +
		"CRITICAL: Total session memory is ~310 tokens, exceeding the 200 token budget. " +
		"You MUST condense the notes to fit within the budget. Prioritize recent and actionable information.\n\n" +
		"The following sections exceed the per-section token budget and should be condensed:\n" +
		"- **H**: ~300 tokens (budget: 100)\n"
	if got != want {
		t.Errorf("reminders =\n%q\nwant\n%q", got, want)
	}
}

// A newline at index 1 inside the truncation window is not a valid cut point;
// the hard byte cut applies instead.
func TestTruncateForCompact_NewlineAtOneHardCuts(t *testing.T) {
	t.Parallel()
	text := "a\n" + strings.Repeat("x", 100)
	got := TruncateForCompact(text, 10) // maxChars = 40
	want := "a" + "\n[... section truncated for length ...]"
	if got != want {
		t.Errorf("TruncateForCompact = %q, want %q", got, want)
	}
}
