package session

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/memory/long"
	"github.com/liuy/gbot/pkg/types"
)

func setTempHome(t *testing.T) {
	t.Helper()
	homeDir := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(homeDir, 0755); err != nil {
		t.Fatalf("create temp home: %v", err)
	}
	t.Setenv("HOME", homeDir)
}

// --- ShouldExtract ---

func TestShouldExtract_BelowInitThreshold(t *testing.T) {
	setTempHome(t)
	sm := New(DefaultConfig(), t.TempDir(), nil, slog.Default())
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}
	if sm.ShouldExtract(100, msgs) {
		t.Error("should return false below MinTokensToInit")
	}
}

func TestShouldExtract_InitAtThreshold_NaturalBreak(t *testing.T) {
	setTempHome(t)
	sm := New(DefaultConfig(), t.TempDir(), nil, slog.Default())
	// No tool calls in last assistant turn → natural break
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("response")}},
	}
	// At MinTokensToInit + tokenGrowth >= MinTokensBetweenUpdate + natural break → true
	if !sm.ShouldExtract(10000, msgs) {
		t.Error("should return true at init threshold with natural break")
	}
}

func TestShouldExtract_InitAtThreshold_HasToolCalls_NoToolCallThreshold(t *testing.T) {
	setTempHome(t)
	sm := New(DefaultConfig(), t.TempDir(), nil, slog.Default())
	// Last assistant has tool_use → NOT a natural break
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("using tool"),
			{Type: types.ContentTypeToolUse, ID: "call_1", Name: "Read"},
		}},
	}
	// Init gate passes, token growth passes, but tool call threshold not met + has tool calls → false
	if sm.ShouldExtract(10000, msgs) {
		t.Error("should return false: init passes but tool calls present and threshold not met")
	}
}

func TestShouldExtract_InitAtThreshold_ToolCallThresholdMet(t *testing.T) {
	setTempHome(t)
	sm := New(DefaultConfig(), t.TempDir(), nil, slog.Default())
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("using tool"),
			{Type: types.ContentTypeToolUse, ID: "call_1", Name: "Read"},
		}},
	}
	// Manually set tool calls to meet threshold
	sm.toolCallsSinceUpdate = 3
	if !sm.ShouldExtract(10000, msgs) {
		t.Error("should return true: tool call threshold met")
	}
}

func TestShouldExtract_AfterInit_TokenGrowthInsufficient(t *testing.T) {
	setTempHome(t)
	sm := New(DefaultConfig(), t.TempDir(), nil, slog.Default())
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}

	// Initialize
	sm.ShouldExtract(10000, msgs)
	// Simulate post-extraction state reset
	sm.mu.Lock()
	sm.lastTokenCount = 10000
	sm.mu.Unlock()

	// Token growth only 2000 < MinTokensBetweenUpdate(5000) → false
	if sm.ShouldExtract(12000, msgs) {
		t.Error("should return false: insufficient token growth")
	}
}

func TestShouldExtract_AfterInit_TokenGrowthSufficient_NaturalBreak(t *testing.T) {
	setTempHome(t)
	sm := New(DefaultConfig(), t.TempDir(), nil, slog.Default())
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("response")}},
	}

	// Initialize and set post-extraction state
	sm.ShouldExtract(10000, msgs)
	sm.mu.Lock()
	sm.lastTokenCount = 10000
	sm.toolCallsSinceUpdate = 0
	sm.mu.Unlock()

	// Token growth 5001 >= 5000 + natural break → true
	if !sm.ShouldExtract(15001, msgs) {
		t.Error("should return true: token growth + natural break")
	}
}

func TestShouldExtract_AfterInit_TokenGrowthSufficient_HasToolCalls_NoThreshold(t *testing.T) {
	setTempHome(t)
	sm := New(DefaultConfig(), t.TempDir(), nil, slog.Default())
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "c1", Name: "Read"},
		}},
	}

	sm.ShouldExtract(10000, msgs)
	sm.mu.Lock()
	sm.lastTokenCount = 10000
	sm.toolCallsSinceUpdate = 0
	sm.mu.Unlock()

	// Token growth OK but has tool calls + tool call threshold not met → false
	if sm.ShouldExtract(15001, msgs) {
		t.Error("should return false: tool calls present, threshold not met")
	}
}

// --- Extract ---

func TestExtract_CreatesFileAndCallsExtractFn(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	var called bool
	extractFn := func(ctx context.Context, prompt string, notesPath string, _ []types.Message, _ string) error {
		called = true
		// Verify the file was created by ensureFile
		if _, err := os.Stat(notesPath); os.IsNotExist(err) {
			t.Errorf("notes file should exist at %s", notesPath)
		}
		return nil
	}

	sm := New(DefaultConfig(), tmpDir, extractFn, slog.Default())
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}

	err := sm.Extract(context.Background(), msgs, 10000)
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if !called {
		t.Error("extractFn should have been called")
	}

	// Verify state was updated
	sm.mu.Lock()
	lastTokens := sm.lastTokenCount
	toolCalls := sm.toolCallsSinceUpdate
	sm.mu.Unlock()

	if lastTokens != 10000 {
		t.Errorf("lastTokenCount = %d, want 10000", lastTokens)
	}
	if toolCalls != 0 {
		t.Errorf("toolCallsSinceUpdate = %d, want 0 (reset after extraction)", toolCalls)
	}
}

func TestExtract_ExtractFnFailure(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	extractFn := func(ctx context.Context, prompt string, notesPath string, _ []types.Message, _ string) error {
		return os.ErrPermission
	}

	sm := New(DefaultConfig(), tmpDir, extractFn, slog.Default())
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}

	err := sm.Extract(context.Background(), msgs, 10000)
	if err == nil {
		t.Fatal("expected error from failed extraction")
	}
	// Error is wrapped by Extract: "extraction failed: <cause>"
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error = %v, want permission error", err)
	}
}

func TestExtract_ContextCancellation(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	extractFn := func(ctx context.Context, prompt string, notesPath string, _ []types.Message, _ string) error {
		return context.Canceled
	}

	sm := New(Config{
		MinTokensToInit:        100,
		MinTokensBetweenUpdate: 50,
		ExtractionTimeoutMs:    5000,
		ExtractionStaleMs:      60000,
	}, tmpDir, extractFn, slog.Default())

	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}

	err := sm.Extract(context.Background(), msgs, 100)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("error = %v, want context canceled", err)
	}
}

func TestExtract_SkipsWhenAlreadyExtracting(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	blockCh := make(chan struct{})
	callCount := 0
	var mu sync.Mutex

	extractFn := func(ctx context.Context, prompt string, notesPath string, _ []types.Message, _ string) error {
		mu.Lock()
		callCount++
		mu.Unlock()
		<-blockCh // block until test releases
		return nil
	}

	sm := New(Config{
		MinTokensToInit:        100,
		MinTokensBetweenUpdate: 50,
		ExtractionTimeoutMs:    5000,
		ExtractionStaleMs:      60000,
	}, tmpDir, extractFn, slog.Default())

	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}

	// First call blocks
	go func() { _ = sm.Extract(context.Background(), msgs, 100) }()
	time.Sleep(50 * time.Millisecond) // REAL-TIME: let goroutine start

	// Second call should be skipped (extraction in progress)
	err := sm.Extract(context.Background(), msgs, 200)
	if err != nil {
		t.Fatalf("second Extract should return nil (skip), got: %v", err)
	}

	close(blockCh)                    // release first call
	time.Sleep(50 * time.Millisecond) // REAL-TIME: wait for cleanup

	mu.Lock()
	count := callCount
	mu.Unlock()
	if count != 1 {
		t.Errorf("extractFn should be called once, got %d", count)
	}
}

// --- WaitForExtraction ---

func TestWaitForExtraction_NotExtracting(t *testing.T) {
	setTempHome(t)
	sm := New(DefaultConfig(), t.TempDir(), nil, slog.Default())
	// Not extracting → returns immediately
	if err := sm.WaitForExtraction(); err != nil {
		t.Errorf("expected nil when not extracting, got: %v", err)
	}
}

func TestWaitForExtraction_WaitsForCompletion(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	releaseCh := make(chan struct{})

	extractFn := func(ctx context.Context, prompt string, notesPath string, _ []types.Message, _ string) error {
		<-releaseCh
		return nil
	}

	sm := New(Config{
		MinTokensToInit:        100,
		MinTokensBetweenUpdate: 50,
		ExtractionTimeoutMs:    5000,
		ExtractionStaleMs:      60000,
	}, tmpDir, extractFn, slog.Default())

	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}

	// Start extraction in background
	go func() { _ = sm.Extract(context.Background(), msgs, 100) }()

	// Wait for extraction to actually start before calling WaitForExtraction
	deadline := time.Now().Add(2 * time.Second) // REAL-TIME: polling deadline
	for time.Now().Before(deadline) {           // REAL-TIME: polling loop
		sm.mu.Lock()
		if sm.extracting {
			sm.mu.Unlock()
			break
		}
		sm.mu.Unlock()
		time.Sleep(10 * time.Millisecond) // REAL-TIME: polling interval
	}

	// Release after short delay
	go func() {
		time.Sleep(100 * time.Millisecond) // REAL-TIME: short delay then release
		close(releaseCh)
	}()

	// WaitForExtraction should complete after extraction finishes
	if err := sm.WaitForExtraction(); err != nil {
		t.Errorf("WaitForExtraction failed: %v", err)
	}
}

func TestWaitForExtraction_StaleRecovery(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	sm := New(Config{
		MinTokensToInit:        100,
		MinTokensBetweenUpdate: 50,
		ExtractionTimeoutMs:    5000,
		ExtractionStaleMs:      100, // 100ms stale threshold
	}, tmpDir, nil, nil)

	// Simulate stale extraction state
	sm.mu.Lock()
	sm.extracting = true
	sm.extractionStart = time.Now().Add(-1 * time.Second) // REAL-TIME: simulate stale extraction
	sm.mu.Unlock()

	// Should recover from stale and return nil
	if err := sm.WaitForExtraction(); err != nil {
		t.Errorf("should recover from stale extraction, got: %v", err)
	}
}

func TestWaitForExtraction_Timeout(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	blockCh := make(chan struct{})

	extractFn := func(ctx context.Context, prompt string, notesPath string, _ []types.Message, _ string) error {
		<-blockCh
		return nil
	}

	sm := New(Config{
		MinTokensToInit:        100,
		MinTokensBetweenUpdate: 50,
		ExtractionTimeoutMs:    200, // 200ms timeout
		ExtractionStaleMs:      60000,
	}, tmpDir, extractFn, slog.Default())

	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}

	// Start extraction that blocks forever
	go func() { _ = sm.Extract(context.Background(), msgs, 100) }()

	// Wait for extraction to actually start (sm.extracting = true)
	deadline := time.Now().Add(2 * time.Second) // REAL-TIME: polling deadline
	for time.Now().Before(deadline) {           // REAL-TIME: polling loop
		sm.mu.Lock()
		extracting := sm.extracting
		sm.mu.Unlock()
		if extracting {
			break
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME: polling interval
	}

	// WaitForExtraction should timeout
	err := sm.WaitForExtraction()
	if err == nil {
		t.Error("expected timeout error")
	}
	close(blockCh) // cleanup
}

// --- IsEmpty ---

func TestIsEmpty_NoFile(t *testing.T) {
	setTempHome(t)
	sm := New(DefaultConfig(), t.TempDir(), nil, slog.Default())
	if !sm.IsEmpty() {
		t.Error("should be empty when file doesn't exist")
	}
}

func TestIsEmpty_TemplateOnly(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}
	notesPath := filepath.Join(memDir, SessionMemoryFileName)
	if err := os.WriteFile(notesPath, []byte(DefaultTemplate), 0644); err != nil {
		t.Fatal(err)
	}

	sm := New(DefaultConfig(), tmpDir, nil, nil)
	if !sm.IsEmpty() {
		t.Error("template-only content should be empty")
	}
}

func TestIsEmpty_RealContent(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}
	notesPath := filepath.Join(memDir, SessionMemoryFileName)
	content := "# Session Notes\n\n## Session Title\nReal work happened\n"
	if err := os.WriteFile(notesPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	sm := New(DefaultConfig(), tmpDir, nil, nil)
	if sm.IsEmpty() {
		t.Error("real content should NOT be empty")
	}
}

// --- RecordToolCalls ---

func TestRecordToolCalls(t *testing.T) {
	setTempHome(t)
	sm := New(DefaultConfig(), t.TempDir(), nil, slog.Default())

	sm.RecordToolCalls(3)
	sm.RecordToolCalls(2)

	sm.mu.Lock()
	count := sm.toolCallsSinceUpdate
	sm.mu.Unlock()

	if count != 5 {
		t.Errorf("toolCallsSinceUpdate = %d, want 5", count)
	}
}

// --- Reset ---

func TestReset(t *testing.T) {
	setTempHome(t)
	sm := New(DefaultConfig(), t.TempDir(), nil, slog.Default())

	// Set some state
	sm.mu.Lock()
	sm.initialized = true
	sm.lastTokenCount = 50000
	sm.extracting = true
	sm.extractionStart = time.Now() // REAL-TIME: set extraction start for reset test
	sm.toolCallsSinceUpdate = 10
	sm.mu.Unlock()

	sm.Reset()

	sm.mu.Lock()
	init := sm.initialized
	lastTokens := sm.lastTokenCount
	extracting := sm.extracting
	toolCalls := sm.toolCallsSinceUpdate
	sm.mu.Unlock()

	if init {
		t.Error("initialized should be false after reset")
	}
	if lastTokens != 0 {
		t.Errorf("lastTokenCount = %d, want 0", lastTokens)
	}
	if extracting {
		t.Error("extracting should be false after reset")
	}
	if toolCalls != 0 {
		t.Errorf("toolCallsSinceUpdate = %d, want 0", toolCalls)
	}
}

// --- NotesPath ---

func TestNotesPath(t *testing.T) {
	setTempHome(t)
	sm := New(DefaultConfig(), "/tmp/workdir", nil, nil)
	path := sm.NotesPath()

	memDir := long.GetMemoryPath("/tmp/workdir")
	expected := filepath.Join(memDir, SessionMemoryFileName)
	if path != expected {
		t.Errorf("NotesPath = %q, want %q", path, expected)
	}
}

// --- LoadSessionMemoryContent ---

func TestLoadSessionMemoryContent_NoFile(t *testing.T) {
	setTempHome(t)
	content := LoadSessionMemoryContent(t.TempDir())
	if content != "" {
		t.Errorf("expected empty string for missing file, got %q", content)
	}
}

func TestLoadSessionMemoryContent_WithFile(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}
	notesPath := filepath.Join(memDir, SessionMemoryFileName)
	expected := "session title: testing"
	if err := os.WriteFile(notesPath, []byte(expected), 0644); err != nil {
		t.Fatal(err)
	}

	content := LoadSessionMemoryContent(tmpDir)
	if content != expected {
		t.Errorf("got %q, want %q", content, expected)
	}
}

// --- ensureFile ---

func TestEnsureFile_CreatesFromTemplate(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	sm := New(DefaultConfig(), tmpDir, nil, nil)

	if err := sm.ensureFile(); err != nil {
		t.Fatalf("ensureFile failed: %v", err)
	}

	data, err := os.ReadFile(sm.NotesPath())
	if err != nil {
		t.Fatalf("failed to read created file: %v", err)
	}
	if string(data) != DefaultTemplate {
		t.Error("created file should contain the default template")
	}
}

func TestEnsureFile_Idempotent(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	sm := New(DefaultConfig(), tmpDir, nil, nil)

	// Create twice — second should be a no-op
	if err := sm.ensureFile(); err != nil {
		t.Fatalf("first ensureFile failed: %v", err)
	}
	// Modify the file
	modified := "custom content"
	if err := os.WriteFile(sm.NotesPath(), []byte(modified), 0644); err != nil {
		t.Fatal(err)
	}
	// Second call should NOT overwrite
	if err := sm.ensureFile(); err != nil {
		t.Fatalf("second ensureFile failed: %v", err)
	}
	data, _ := os.ReadFile(sm.NotesPath())
	if string(data) != modified {
		t.Error("ensureFile should not overwrite existing file")
	}
}

// --- lastAssistantHasToolCalls ---

func TestLastAssistantHasToolCalls_NoMessages(t *testing.T) {
	if lastAssistantHasToolCalls(nil) {
		t.Error("empty messages should return false")
	}
}

func TestLastAssistantHasToolCalls_TextOnly(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
	}
	if lastAssistantHasToolCalls(msgs) {
		t.Error("text-only assistant should return false")
	}
}

func TestLastAssistantHasToolCalls_HasToolUse(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.NewTextBlock("using tool"),
			{Type: types.ContentTypeToolUse, ID: "c1", Name: "Read"},
		}},
	}
	if !lastAssistantHasToolCalls(msgs) {
		t.Error("assistant with tool_use should return true")
	}
}

func TestLastAssistantHasToolCalls_SkipsUserMessages(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("response")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("next question")}},
	}
	// Last message is user, last assistant has no tool calls
	if lastAssistantHasToolCalls(msgs) {
		t.Error("should check last assistant, not last message")
	}
}

// --- Coverage gap tests ---

func TestExtract_StaleRecovery(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	var called bool
	extractFn := func(ctx context.Context, prompt string, notesPath string, _ []types.Message, _ string) error {
		called = true
		return nil
	}

	sm := New(Config{
		MinTokensToInit:        100,
		MinTokensBetweenUpdate: 50,
		ExtractionTimeoutMs:    5000,
		ExtractionStaleMs:      100,
	}, tmpDir, extractFn, slog.Default())

	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}

	// Simulate stale extraction state
	sm.mu.Lock()
	sm.extracting = true
	sm.extractionStart = time.Now().Add(-1 * time.Second) // REAL-TIME: stale
	sm.mu.Unlock()

	err := sm.Extract(context.Background(), msgs, 100)
	if err != nil {
		t.Fatalf("Extract should recover from stale, got: %v", err)
	}
	if !called {
		t.Error("extractFn should have been called after stale recovery")
	}
}

func TestExtract_ReadFileError(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	sm := New(DefaultConfig(), tmpDir, nil, slog.Default())

	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}

	// Create file then replace with directory to cause read error
	memDir := long.GetMemoryPath(tmpDir)
	notesPath := filepath.Join(memDir, SessionMemoryFileName)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notesPath, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(notesPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(notesPath, 0755); err != nil {
		t.Fatal(err)
	}

	err := sm.Extract(context.Background(), msgs, 10000)
	if err == nil {
		t.Fatal("expected error when reading directory instead of file")
	}
	if !strings.Contains(err.Error(), "read session memory") {
		t.Errorf("error should mention read session memory, got: %v", err)
	}
}

func TestEnsureFile_MkdirAllError(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	sm := New(DefaultConfig(), tmpDir, nil, nil)

	// Compute the exact directory path that ensureFile will try to MkdirAll
	notesPath := sm.NotesPath()
	dir := filepath.Dir(notesPath) // the directory ensureFile tries to create

	// Create parent directory
	if err := os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
		t.Fatal(err)
	}
	// Place a FILE where the directory should be — MkdirAll will fail
	if err := os.WriteFile(dir, []byte("blocker"), 0644); err != nil {
		t.Fatal(err)
	}

	err := sm.ensureFile()
	if err == nil {
		t.Fatal("expected error when MkdirAll fails (file blocks directory)")
	}
	if !strings.Contains(err.Error(), "memory") {
		t.Errorf("error should reference memory path, got: %v", err)
	}
}

func TestWaitForExtraction_StaleRecovery_WithChannel(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	sm := New(Config{
		MinTokensToInit:        100,
		MinTokensBetweenUpdate: 50,
		ExtractionTimeoutMs:    5000,
		ExtractionStaleMs:      100,
	}, tmpDir, nil, nil)

	// Simulate stale extraction with an active channel
	sm.mu.Lock()
	sm.extracting = true
	sm.extractDone = make(chan struct{})
	sm.extractionStart = time.Now().Add(-1 * time.Second) // REAL-TIME: stale
	sm.mu.Unlock()

	if err := sm.WaitForExtraction(); err != nil {
		t.Errorf("should recover from stale with channel, got: %v", err)
	}

	sm.mu.Lock()
	done := sm.extractDone
	extracting := sm.extracting
	sm.mu.Unlock()
	if extracting {
		t.Error("extracting should be false after stale recovery")
	}
	if done != nil {
		t.Error("extractDone should be nil after stale recovery")
	}
}

func TestExtract_EnsureFileFailure(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	sm := New(DefaultConfig(), tmpDir, nil, slog.Default())

	// Block ensureFile by placing a file where the directory should be
	notesPath := sm.NotesPath()
	dir := filepath.Dir(notesPath)
	if err := os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("blocker"), 0644); err != nil {
		t.Fatal(err)
	}

	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	}

	err := sm.Extract(context.Background(), msgs, 10000)
	if err == nil {
		t.Fatal("expected error when ensureFile fails in Extract")
	}
	if !strings.Contains(err.Error(), "setup session memory file") {
		t.Errorf("error should mention setup session memory file, got: %v", err)
	}
}
